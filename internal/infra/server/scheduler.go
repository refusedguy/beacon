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
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
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
func IsValidation(err error) bool {
	var v *ValidationError
	return errors.As(err, &v)
}

func invalid(format string, a ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, a...)}
}

// ScheduleService implements the three schedule operations. It never reads a
// clock to decide whether a task runs: pausing only flips Enabled through the
// store, so a paused schedule is preserved but never dispatched.
type ScheduleService struct {
	store ScheduleStore
}

// NewScheduleService builds a service over store. A nil store is refused, so a
// misconfigured composition root fails at startup rather than on first use.
func NewScheduleService(store ScheduleStore) (*ScheduleService, error) {
	if store == nil {
		return nil, errors.New("server: nil schedule store")
	}
	return &ScheduleService{store: store}, nil
}

// Schedule validates in and persists a new, enabled schedule.
func (s *ScheduleService) Schedule(ctx context.Context, in ScheduleCreate) (Schedule, error) {
	built, err := buildSchedule(in)
	if err != nil {
		return Schedule{}, err
	}
	if err := s.store.Create(ctx, built); err != nil {
		return Schedule{}, err
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

// buildSchedule validates the create input and fills defaults. Validation is a
// pure function of the input: no clock, file, or socket is read.
func buildSchedule(in ScheduleCreate) (Schedule, error) {
	in.ID = strings.TrimSpace(in.ID)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.Expr = strings.TrimSpace(in.Expr)
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.TaskID == "" {
		return Schedule{}, invalid("server: пустой taskID расписания")
	}
	if in.ID == "" {
		in.ID = fmt.Sprintf("sched-%d", time.Now().UnixNano())
	}
	if in.Timezone == "" {
		in.Timezone = "Local"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		return Schedule{}, invalid("server: неверный часовой пояс %q: %v", in.Timezone, err)
	}
	if err := validateExpr(in.Kind, in.Expr); err != nil {
		return Schedule{}, err
	}
	return Schedule{
		ID: in.ID, TaskID: in.TaskID, Kind: in.Kind, Expr: in.Expr,
		Timezone: in.Timezone, Enabled: true,
	}, nil
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
		if n := len(strings.Fields(expr)); n != 5 {
			return invalid("server: cron %q: нужно 5 полей, получено %d", expr, n)
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
