// Package server hosts Beacon's loopback internal API. The scheduler group
// exposes task schedules through a store port, so the transport owns no SQLite
// handle and no persistence schema (ADR 0189, ADR 0192, BS.23, BS.32).
//
// BS.33 supplies the durable store implementation. Until it lands, a caller
// injects any ScheduleStore: the in-memory store for tests, or the JSON file
// store used by the task CLI. No SQL leaks through the port.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// Kind selects how a schedule fires. Values mirror the pure planner in
// internal/app/scheduler without importing it: infra may not import app.
type Kind string

const (
	KindInterval Kind = "interval"
	KindCron     Kind = "cron"
	KindMark     Kind = "mark"
)

// Schedule is one persisted trigger definition. It is a transport-owned value
// built from stored fields, never a live database row handed to a caller.
type Schedule struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"taskID"`
	Kind      Kind      `json:"kind"`
	Expr      string    `json:"expr"`
	Timezone  string    `json:"timezone"`
	Enabled   bool      `json:"enabled"`
	NextRunAt time.Time `json:"nextRunAt"`
	LastRun   time.Time `json:"lastRun"`
	CreatedAt time.Time `json:"createdAt"`
}

// ScheduleCreate is the caller-supplied part of a new schedule. ID is optional;
// the service generates one when it is empty.
type ScheduleCreate struct {
	ID       string
	TaskID   string
	Kind     Kind
	Expr     string
	Timezone string
}

// ScheduleStore is the minimal persistence port the scheduler needs. Create
// refuses an existing ID; Get, Update and Delete report ErrScheduleNotFound
// for an unknown ID. BS.33 implements this port over the schedules table.
type ScheduleStore interface {
	Create(ctx context.Context, s Schedule) error
	Get(ctx context.Context, id string) (Schedule, error)
	List(ctx context.Context) ([]Schedule, error)
	Update(ctx context.Context, s Schedule) error
	Delete(ctx context.Context, id string) error
}

// IntentOutbox records one durable intention that a schedule must run. The
// composition root supplies the store, so the server owns no SQL and no file
// format. Commit reports whether the key was newly written; a repeated key is
// not an error, so a restart never queues a second intention for the same run.
type IntentOutbox interface {
	Commit(ctx context.Context, key, kind string, payload []byte) (bool, error)
}

// ScheduleIntentKind labels the outbox row written for a new schedule.
const ScheduleIntentKind = "schedule_intent"

// enabledSetter is an optional store capability that flips Enabled and returns
// the row in one locked operation. Stores that implement it let Pause and
// Resume stay atomic even when several services share the store. The service
// uses a Get/Update pair under its own mutex for stores without the capability.
type enabledSetter interface {
	SetEnabled(ctx context.Context, id string, enabled bool) (Schedule, error)
}

var (
	// ErrScheduleNotFound means no schedule carries the requested ID.
	ErrScheduleNotFound = errors.New("server: schedule not found")
	// ErrScheduleExists means Create was asked to reuse an existing ID.
	ErrScheduleExists = errors.New("server: schedule already exists")
)

// ValidationError reports bad caller input. The CLI maps it to exit code 1;
// store and configuration failures stay at the generic startup code.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// IsValidation reports whether err came from input validation.
func IsValidation(err error) bool { return domain.IsErr[*ValidationError](err) }

func invalid(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// ScheduleService implements the three schedule operations. It never reads a
// clock to decide whether a task runs: pausing only flips Enabled through the
// store, so a paused schedule is preserved but never dispatched.
type ScheduleService struct {
	store  ScheduleStore
	outbox IntentOutbox
	mu     sync.Mutex
}

// NewScheduleService builds a service over store. A nil store is refused, so a
// misconfigured composition root fails at startup rather than on first use.
// The optional outbox records the run intention; callers that do not dispatch
// schedules can omit it and keep the original behaviour.
func NewScheduleService(store ScheduleStore, outbox ...IntentOutbox) (*ScheduleService, error) {
	if store == nil {
		return nil, errors.New("server: nil schedule store")
	}
	var intent IntentOutbox
	if len(outbox) > 0 {
		intent = outbox[0]
	}
	return &ScheduleService{store: store, outbox: intent}, nil
}

// Schedule validates in and persists a new, enabled schedule. When an outbox
// is wired, it also records one intention for the schedule's first run.
func (s *ScheduleService) Schedule(ctx context.Context, in ScheduleCreate) (Schedule, error) {
	built, err := buildSchedule(in)
	if err != nil {
		return Schedule{}, err
	}
	if err := s.store.Create(ctx, built); err != nil {
		return Schedule{}, err
	}
	if s.outbox != nil {
		payload, err := json.Marshal(built)
		if err != nil {
			return Schedule{}, err
		}
		key := ScheduleIntentKind + ":" + built.ID
		if _, err := s.outbox.Commit(ctx, key, ScheduleIntentKind, payload); err != nil {
			return Schedule{}, err
		}
	}
	return built, nil
}

// List returns all schedules ordered by ID, so the wire output is stable.
func (s *ScheduleService) List(ctx context.Context) ([]Schedule, error) {
	out, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if out == nil {
		out = []Schedule{}
	}
	return out, nil
}

// Pause preserves the schedule and sets Enabled=false. It does not touch
// NextRunAt, LastRun, or any run state, and it does not dispatch a task.
func (s *ScheduleService) Pause(ctx context.Context, id string) (Schedule, error) {
	return s.setEnabled(ctx, id, false)
}

// Resume is the inverse of Pause: it sets Enabled=true without changing the
// trigger. An interval that elapsed while paused is collapsed by the planner,
// not replayed here.
func (s *ScheduleService) Resume(ctx context.Context, id string) (Schedule, error) {
	return s.setEnabled(ctx, id, true)
}

func (s *ScheduleService) setEnabled(ctx context.Context, id string, enabled bool) (Schedule, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Schedule{}, invalid("server: пустой id расписания")
	}
	if setter, ok := s.store.(enabledSetter); ok {
		return setter.SetEnabled(ctx, id, enabled)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.store.Get(ctx, id)
	if err != nil {
		return Schedule{}, err
	}
	current.Enabled = enabled
	if err := s.store.Update(ctx, current); err != nil {
		return Schedule{}, err
	}
	return current, nil
}

// Delete removes the schedule permanently. A second delete reports
// ErrScheduleNotFound, so deletion is not an idempotent no-op.
func (s *ScheduleService) Delete(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return invalid("server: пустой id расписания")
	}
	return s.store.Delete(ctx, id)
}

// scheduleIDSeq keeps generated IDs distinct inside one process even when the
// clock is fixed or many goroutines create schedules at the same instant.
var scheduleIDSeq atomic.Uint64

// newScheduleID combines a timestamp, random bytes and a process-local counter.
// The counter alone wins any same-nanosecond race inside the process; the
// random part separates IDs from other processes.
func newScheduleID() string {
	seq := scheduleIDSeq.Add(1)
	stamp := time.Now().UnixNano()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sched-%d-%d", stamp, seq)
	}
	return fmt.Sprintf("sched-%d-%s-%d", stamp, hex.EncodeToString(b[:]), seq)
}

// buildSchedule validates the create input and fills defaults. Validation is a
// pure function of the input for caller fields; only the generated ID reads a
// clock, a counter and random bytes.
func buildSchedule(in ScheduleCreate) (Schedule, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.Expr = strings.TrimSpace(in.Expr)
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.TaskID == "" {
		return Schedule{}, invalid("server: пустой taskID расписания")
	}
	if in.ID == "" {
		in.ID = newScheduleID()
	}
	if in.Timezone == "" {
		in.Timezone = "Local"
	}
	loc, err := time.LoadLocation(in.Timezone)
	if err != nil {
		return Schedule{}, invalid("server: неверный часовой пояс %q: %v", in.Timezone, err)
	}
	if err := validateExpr(in.Kind, in.Expr); err != nil {
		return Schedule{}, err
	}
	next, err := firstRunAt(in.Kind, in.Expr, time.Now(), loc)
	if err != nil {
		return Schedule{}, err
	}
	return Schedule{
		ID: in.ID, TaskID: in.TaskID, Kind: in.Kind, Expr: in.Expr,
		Timezone: in.Timezone, Enabled: true, NextRunAt: next,
	}, nil
}

// Five-field cron support. The fields are minute, hour, day of month, month and
// day of week. The parser is pure and lives here so infra needs no dependency
// on the app-layer planner (ADR 0314, ADR 0005). It supports "*", lists, ranges,
// steps and any combination of them.
type cronSpec struct {
	minute []bool
	hour   []bool
	dom    []bool
	month  []bool
	dow    []bool
	domAny bool
	dowAny bool
}

func (c cronSpec) matches(t time.Time) bool {
	day := c.dom[t.Day()] && c.dow[int(t.Weekday())]
	// Unix cron uses OR between day-of-month and day-of-week when both fields
	// are restricted, and AND when either field is "*".
	if !c.domAny && !c.dowAny {
		day = c.dom[t.Day()] || c.dow[int(t.Weekday())]
	}
	return c.minute[t.Minute()] && c.hour[t.Hour()] && c.month[int(t.Month())] && day
}

func parseCron(expr string) (cronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return cronSpec{}, fmt.Errorf("нужно 5 полей, получено %d", len(fields))
	}
	var spec cronSpec
	var err error
	if spec.minute, err = parseCronField(fields[0], 0, 59); err != nil {
		return cronSpec{}, fmt.Errorf("минуты: %w", err)
	}
	if spec.hour, err = parseCronField(fields[1], 0, 23); err != nil {
		return cronSpec{}, fmt.Errorf("часы: %w", err)
	}
	if spec.dom, err = parseCronField(fields[2], 1, 31); err != nil {
		return cronSpec{}, fmt.Errorf("день месяца: %w", err)
	}
	if spec.month, err = parseCronField(fields[3], 1, 12); err != nil {
		return cronSpec{}, fmt.Errorf("месяц: %w", err)
	}
	if spec.dow, err = parseCronField(fields[4], 0, 7); err != nil {
		return cronSpec{}, fmt.Errorf("день недели: %w", err)
	}
	// Cron treats 7 as an alias for Sunday.
	if spec.dow[7] {
		spec.dow[0] = true
	}
	spec.domAny = fields[2] == "*"
	spec.dowAny = fields[4] == "*"
	return spec, nil
}

// parseCronField parses one field into a lookup slice indexed by field value.
func parseCronField(field string, min, max int) ([]bool, error) {
	allowed := make([]bool, max+1)
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("пустое поле")
		}
		base, step := part, 1
		if i := strings.IndexByte(part, '/'); i >= 0 {
			base = part[:i]
			n, err := strconv.Atoi(part[i+1:])
			if err != nil || n <= 0 {
				return nil, fmt.Errorf("неверный шаг %q", part[i+1:])
			}
			step = n
		}
		lo, hi := min, max
		if base != "*" {
			if strings.Contains(base, "-") {
				bounds := strings.Split(base, "-")
				if len(bounds) != 2 {
					return nil, fmt.Errorf("неверный диапазон %q", base)
				}
				start, err := strconv.Atoi(bounds[0])
				if err != nil {
					return nil, fmt.Errorf("неверное значение %q", bounds[0])
				}
				end, err := strconv.Atoi(bounds[1])
				if err != nil {
					return nil, fmt.Errorf("неверное значение %q", bounds[1])
				}
				lo, hi = start, end
			} else {
				n, err := strconv.Atoi(base)
				if err != nil {
					return nil, fmt.Errorf("неверное значение %q", base)
				}
				lo, hi = n, n
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("значение %d-%d вне диапазона %d-%d", lo, hi, min, max)
		}
		for v := lo; v <= hi; v += step {
			allowed[v] = true
		}
	}
	return allowed, nil
}

// maxCronMinutes bounds the search for an impossible expression such as
// "0 0 30 2 *", which has no matching date in a Gregorian calendar.
const maxCronMinutes = 5 * 366 * 24 * 60

func nextCronAfter(spec cronSpec, loc *time.Location, after time.Time) (time.Time, bool) {
	t := after.In(loc).Truncate(time.Minute).Add(time.Minute)
	for i := 0; i < maxCronMinutes; i++ {
		if spec.matches(t) {
			return t, true
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}, false
}

// NextCronRun parses a five-field cron expression and returns the first firing
// strictly after at in timezone. It is the pure entry point for callers that
// need a fixed wall-clock time in a named zone, including the daemon adapter
// that advances a missed interval without duplicating it.
func NextCronRun(expr, timezone string, at time.Time) (time.Time, error) {
	spec, err := parseCron(expr)
	if err != nil {
		return time.Time{}, invalid("server: cron %q: %v", expr, err)
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, invalid("server: часовой пояс %q: %v", timezone, err)
	}
	next, ok := nextCronAfter(spec, loc, at)
	if !ok {
		return time.Time{}, invalid("server: cron %q: нет подходящего времени за 5 лет", expr)
	}
	return next, nil
}

// firstRunAt computes the first storage-valid run time for a schedule. Cron
// uses the parsed expression in the schedule's zone, interval arithmetic stays
// exact and mark is due immediately.
func firstRunAt(kind Kind, expr string, now time.Time, loc *time.Location) (time.Time, error) {
	switch kind {
	case KindInterval:
		d, err := time.ParseDuration(expr)
		if err != nil {
			return time.Time{}, invalid("server: интервал %q: %v", expr, err)
		}
		return now.Add(d), nil
	case KindCron:
		spec, err := parseCron(expr)
		if err != nil {
			return time.Time{}, invalid("server: cron %q: %v", expr, err)
		}
		next, ok := nextCronAfter(spec, loc, now)
		if !ok {
			return time.Time{}, invalid("server: cron %q: нет подходящего времени за 5 лет", expr)
		}
		return next, nil
	case KindMark:
		return now, nil
	default:
		return time.Time{}, invalid("server: неизвестный вид расписания %q", kind)
	}
}

func validateExpr(kind Kind, expr string) error {
	switch kind {
	case KindInterval:
		d, err := time.ParseDuration(expr)
		if err != nil {
			return invalid("server: интервал %q: %v", expr, err)
		}
		if d <= 0 {
			return invalid("server: интервал %q должен быть положительным", expr)
		}
		return nil
	case KindCron:
		if _, err := parseCron(expr); err != nil {
			return invalid("server: cron %q: %v", expr, err)
		}
		return nil
	case KindMark:
		if expr == "" {
			return invalid("server: webhook mark не может быть пустым")
		}
		return nil
	default:
		return invalid("server: неизвестный вид расписания %q", kind)
	}
}

// ---- ScheduleStore implementations ----

// MemoryScheduleStore is an in-memory ScheduleStore for tests and short-lived
// processes. It is safe for concurrent handlers.
type MemoryScheduleStore struct {
	mu    sync.RWMutex
	items map[string]Schedule
}

// NewMemoryScheduleStore builds an empty store.
func NewMemoryScheduleStore() *MemoryScheduleStore {
	return &MemoryScheduleStore{items: make(map[string]Schedule)}
}

func (m *MemoryScheduleStore) Create(_ context.Context, s Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[s.ID]; ok {
		return fmt.Errorf("%w: %s", ErrScheduleExists, s.ID)
	}
	m.items[s.ID] = s
	return nil
}

func (m *MemoryScheduleStore) Get(_ context.Context, id string) (Schedule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.items[id]
	if !ok {
		return Schedule{}, fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	return s, nil
}

// SetEnabled flips Enabled under the store lock, so a concurrent Pause and
// Resume cannot read the same row and write it back over each other.
func (m *MemoryScheduleStore) SetEnabled(_ context.Context, id string, enabled bool) (Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.items[id]
	if !ok {
		return Schedule{}, fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	s.Enabled = enabled
	m.items[id] = s
	return s, nil
}

func (m *MemoryScheduleStore) List(_ context.Context) ([]Schedule, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Schedule, 0, len(m.items))
	for _, s := range m.items {
		out = append(out, s)
	}
	return out, nil
}

func (m *MemoryScheduleStore) Update(_ context.Context, s Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[s.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrScheduleNotFound, s.ID)
	}
	m.items[s.ID] = s
	return nil
}

func (m *MemoryScheduleStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	delete(m.items, id)
	return nil
}

// FileScheduleStore persists schedules as JSON under the state directory. It
// is the stopgap port implementation until BS.33 supplies the SQLite store;
// the CLI depends only on ScheduleStore, never on this file format.
type FileScheduleStore struct {
	path  string
	mu    sync.Mutex
	items map[string]Schedule
}

// NewFileScheduleStore opens path, treating a missing file as empty state.
func NewFileScheduleStore(path string) (*FileScheduleStore, error) {
	s := &FileScheduleStore{path: path, items: make(map[string]Schedule)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("server: read schedules %s: %w", path, err)
	}
	if len(strings.TrimSpace(string(data))) != 0 {
		if err := json.Unmarshal(data, &s.items); err != nil {
			return nil, fmt.Errorf("server: decode schedules %s: %w", path, err)
		}
	}
	if s.items == nil {
		s.items = make(map[string]Schedule)
	}
	return s, nil
}

// save writes the whole map under lock. Callers must hold s.mu.
func (s *FileScheduleStore) save() error {
	data, err := json.MarshalIndent(s.items, "", "  ")
	if err != nil {
		return fmt.Errorf("server: encode schedules: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("server: mkdir schedules: %w", err)
	}
	return os.WriteFile(s.path, append(data, '\n'), 0o600)
}

func (s *FileScheduleStore) Create(_ context.Context, in Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[in.ID]; ok {
		return fmt.Errorf("%w: %s", ErrScheduleExists, in.ID)
	}
	s.items[in.ID] = in
	return s.save()
}

func (s *FileScheduleStore) Get(_ context.Context, id string) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got, ok := s.items[id]
	if !ok {
		return Schedule{}, fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	return got, nil
}

// SetEnabled flips Enabled and persists the row in one locked operation.
func (s *FileScheduleStore) SetEnabled(_ context.Context, id string, enabled bool) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.items[id]
	if !ok {
		return Schedule{}, fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	cur.Enabled = enabled
	s.items[id] = cur
	if err := s.save(); err != nil {
		return Schedule{}, err
	}
	return cur, nil
}

func (s *FileScheduleStore) List(_ context.Context) ([]Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Schedule, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	return out, nil
}

func (s *FileScheduleStore) Update(_ context.Context, in Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[in.ID]; !ok {
		return fmt.Errorf("%w: %s", ErrScheduleNotFound, in.ID)
	}
	s.items[in.ID] = in
	return s.save()
}

func (s *FileScheduleStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.items[id]; !ok {
		return fmt.Errorf("%w: %s", ErrScheduleNotFound, id)
	}
	delete(s.items, id)
	return s.save()
}

// ---- Internal HTTP API: /internal/scheduler ----

const (
	SchedulePath       = "/internal/scheduler/schedule"
	ScheduleListPath   = "/internal/scheduler/list"
	SchedulePausePath  = "/internal/scheduler/pause"
	ScheduleResumePath = "/internal/scheduler/resume"
	ScheduleDeletePath = "/internal/scheduler/delete"
)

// SchedulerHandler serves the scheduler route group on loopback under a bearer
// token (ADR 0189). The token is injected, never read from the environment here.
type SchedulerHandler struct {
	svc   *ScheduleService
	token string
}

// NewSchedulerHandler binds a service and a bearer token. An empty token is
// refused: the internal API must never be reachable without authentication.
func NewSchedulerHandler(svc *ScheduleService, token string) (*SchedulerHandler, error) {
	if svc == nil {
		return nil, errors.New("server: nil schedule service")
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, errors.New("server: empty scheduler token")
	}
	return &SchedulerHandler{svc: svc, token: token}, nil
}

// Handler returns an authenticated mux with all scheduler routes registered.
func (h *SchedulerHandler) Handler() http.Handler {
	mux := http.NewServeMux()
	h.Register(mux)
	return h.bearer(mux)
}

// Register mounts the routes on mux. The caller owns outer middleware such as
// bearer auth when the group is mounted into a larger server.
func (h *SchedulerHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST "+SchedulePath, h.handleCreate)
	mux.HandleFunc("GET "+ScheduleListPath, h.handleList)
	mux.HandleFunc("POST "+SchedulePausePath, h.handlePause)
	mux.HandleFunc("POST "+ScheduleResumePath, h.handleResume)
	mux.HandleFunc("DELETE "+ScheduleDeletePath, h.handleDelete)
}

func (h *SchedulerHandler) bearer(next http.Handler) http.Handler {
	want := "Bearer " + h.token
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
			writeScheduleError(w, http.StatusUnauthorized, "UNAUTHORIZED", "неверный bearer-токен")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type scheduleRequest struct {
	ID       string `json:"id"`
	TaskID   string `json:"taskID"`
	Kind     Kind   `json:"kind"`
	Expr     string `json:"expr"`
	Timezone string `json:"timezone"`
	Enabled  *bool  `json:"enabled"`
}

func decodeScheduleRequest(w http.ResponseWriter, r *http.Request) (scheduleRequest, bool) {
	var in scheduleRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&in); err != nil {
		writeScheduleError(w, http.StatusBadRequest, "INVALID_INPUT", "неверное тело запроса")
		return in, false
	}
	return in, true
}

func (h *SchedulerHandler) handleCreate(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeScheduleRequest(w, r)
	if !ok {
		return
	}
	created, err := h.svc.Schedule(r.Context(), ScheduleCreate{
		ID: in.ID, TaskID: in.TaskID, Kind: in.Kind, Expr: in.Expr, Timezone: in.Timezone,
	})
	if err != nil {
		writeScheduleError(w, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}
	writeScheduleJSON(w, http.StatusCreated, map[string]any{"ok": true, "schedule": created})
}

func (h *SchedulerHandler) handleList(w http.ResponseWriter, r *http.Request) {
	items, err := h.svc.List(r.Context())
	if err != nil {
		writeScheduleError(w, http.StatusInternalServerError, "STORE_FAILED", err.Error())
		return
	}
	writeScheduleJSON(w, http.StatusOK, map[string]any{"ok": true, "schedules": items})
}

func (h *SchedulerHandler) handlePause(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeScheduleRequest(w, r)
	if !ok {
		return
	}
	paused, err := h.svc.Pause(r.Context(), in.ID)
	if err != nil {
		writeScheduleError(w, http.StatusNotFound, "SCHEDULE_FAILED", err.Error())
		return
	}
	writeScheduleJSON(w, http.StatusOK, map[string]any{"ok": true, "schedule": paused})
}

func (h *SchedulerHandler) handleResume(w http.ResponseWriter, r *http.Request) {
	in, ok := decodeScheduleRequest(w, r)
	if !ok {
		return
	}
	resumed, err := h.svc.Resume(r.Context(), in.ID)
	if err != nil {
		writeScheduleError(w, http.StatusNotFound, "SCHEDULE_FAILED", err.Error())
		return
	}
	writeScheduleJSON(w, http.StatusOK, map[string]any{"ok": true, "schedule": resumed})
}

func (h *SchedulerHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	if id == "" {
		writeScheduleError(w, http.StatusBadRequest, "INVALID_INPUT", "нужен параметр id")
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		writeScheduleError(w, http.StatusNotFound, "SCHEDULE_FAILED", err.Error())
		return
	}
	writeScheduleJSON(w, http.StatusOK, map[string]any{"ok": true, "id": id, "deleted": true})
}

func writeScheduleJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeScheduleError(w http.ResponseWriter, status int, code, message string) {
	writeScheduleJSON(w, status, map[string]any{
		"ok": false, "code": code, "message": message,
	})
}
