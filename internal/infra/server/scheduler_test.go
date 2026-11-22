package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/server"
)

// mockStore is the store port used by every service test. Each operation can
// be failed independently, so a store error is checked for every method.
type mockStore struct {
	items                                            map[string]server.Schedule
	createErr, getErr, listErr, updateErr, deleteErr error
	creates, updates, deletes                        int
	lastUpdate                                       server.Schedule
}

func newMockStore() *mockStore {
	return &mockStore{items: make(map[string]server.Schedule)}
}

func (m *mockStore) Create(_ context.Context, s server.Schedule) error {
	m.creates++
	if m.createErr != nil {
		return m.createErr
	}
	if _, ok := m.items[s.ID]; ok {
		return server.ErrScheduleExists
	}
	m.items[s.ID] = s
	return nil
}

func (m *mockStore) Get(_ context.Context, id string) (server.Schedule, error) {
	if m.getErr != nil {
		return server.Schedule{}, m.getErr
	}
	s, ok := m.items[id]
	if !ok {
		return server.Schedule{}, server.ErrScheduleNotFound
	}
	return s, nil
}

func (m *mockStore) List(_ context.Context) ([]server.Schedule, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]server.Schedule, 0, len(m.items))
	for _, s := range m.items {
		out = append(out, s)
	}
	return out, nil
}

func (m *mockStore) Update(_ context.Context, s server.Schedule) error {
	m.updates++
	m.lastUpdate = s
	if m.updateErr != nil {
		return m.updateErr
	}
	if _, ok := m.items[s.ID]; !ok {
		return server.ErrScheduleNotFound
	}
	m.items[s.ID] = s
	return nil
}

func (m *mockStore) Delete(_ context.Context, id string) error {
	m.deletes++
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.items[id]; !ok {
		return server.ErrScheduleNotFound
	}
	delete(m.items, id)
	return nil
}

func newService(t *testing.T, store server.ScheduleStore) *server.ScheduleService {
	t.Helper()
	svc, err := server.NewScheduleService(store)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestNewScheduleServiceRejectsNilStore(t *testing.T) {
	if _, err := server.NewScheduleService(nil); err == nil {
		t.Fatal("nil store accepted")
	}
}

// Creation validates the input and persists an enabled schedule.
func TestScheduleCreate(t *testing.T) {
	store := newMockStore()
	svc := newService(t, store)
	got, err := svc.Schedule(context.Background(), server.ScheduleCreate{
		ID: "s1", TaskID: "BS.32", Kind: server.KindInterval, Expr: "15m", Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "s1" || got.TaskID != "BS.32" || got.Kind != server.KindInterval || got.Expr != "15m" {
		t.Fatalf("unexpected schedule: %+v", got)
	}
	if !got.Enabled {
		t.Fatal("new schedule must be enabled")
	}
	if got.Timezone != "UTC" {
		t.Fatalf("timezone = %q", got.Timezone)
	}
	if store.creates != 1 {
		t.Fatalf("creates = %d, want 1", store.creates)
	}
	if stored := store.items["s1"]; !stored.Enabled || stored.ID != "s1" {
		t.Fatalf("stored schedule: %+v", stored)
	}
}

func TestScheduleCreateGeneratesIDAndDefaultZone(t *testing.T) {
	store := newMockStore()
	svc := newService(t, store)
	got, err := svc.Schedule(context.Background(), server.ScheduleCreate{
		TaskID: "BS.32", Kind: server.KindMark, Expr: "deploy",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || !strings.HasPrefix(got.ID, "sched-") {
		t.Fatalf("generated id = %q", got.ID)
	}
	if got.Timezone != "Local" {
		t.Fatalf("default timezone = %q", got.Timezone)
	}
}

func TestScheduleCreateValidation(t *testing.T) {
	svc := newService(t, newMockStore())
	cases := []struct {
		name string
		in   server.ScheduleCreate
	}{
		{"пустой taskID", server.ScheduleCreate{Kind: server.KindInterval, Expr: "15m"}},
		{"неизвестный вид", server.ScheduleCreate{TaskID: "T", Kind: "bogus", Expr: "x"}},
		{"интервал не число", server.ScheduleCreate{TaskID: "T", Kind: server.KindInterval, Expr: "abc"}},
		{"отрицательный интервал", server.ScheduleCreate{TaskID: "T", Kind: server.KindInterval, Expr: "-1m"}},
		{"cron не из 5 полей", server.ScheduleCreate{TaskID: "T", Kind: server.KindCron, Expr: "* * *"}},
		{"пустой mark", server.ScheduleCreate{TaskID: "T", Kind: server.KindMark, Expr: " "}},
		{"неверный пояс", server.ScheduleCreate{TaskID: "T", Kind: server.KindMark, Expr: "m", Timezone: "Mars/Phobos"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := svc.Schedule(context.Background(), tc.in); err == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestScheduleCreateSetsNextRunAt(t *testing.T) {
	cases := []struct {
		name string
		in   server.ScheduleCreate
	}{
		{"interval", server.ScheduleCreate{TaskID: "T", Kind: server.KindInterval, Expr: "15m", Timezone: "UTC"}},
		{"cron", server.ScheduleCreate{TaskID: "T", Kind: server.KindCron, Expr: "0 9 * * *", Timezone: "UTC"}},
		{"mark", server.ScheduleCreate{TaskID: "T", Kind: server.KindMark, Expr: "deploy", Timezone: "UTC"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := newService(t, newMockStore()).Schedule(context.Background(), tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got.NextRunAt.IsZero() {
				t.Fatalf("NextRunAt is zero for %s", tc.name)
			}
		})
	}
}

// List returns every schedule ordered by ID, including paused ones.
func TestScheduleList(t *testing.T) {
	store := newMockStore()
	store.items["b"] = server.Schedule{ID: "b", TaskID: "B", Enabled: true}
	store.items["a"] = server.Schedule{ID: "a", TaskID: "A", Enabled: false}
	svc := newService(t, store)
	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("list = %+v", got)
	}
	if got[0].Enabled {
		t.Fatal("paused schedule lost its disabled state in list")
	}
}

// Pause keeps the task and its trigger, flips only Enabled, and never runs it.
func TestSchedulePausePreservesTaskAndDoesNotRun(t *testing.T) {
	store := newMockStore()
	created, err := newService(t, store).Schedule(context.Background(), server.ScheduleCreate{
		ID: "s1", TaskID: "BS.32", Kind: server.KindCron, Expr: "0 9 * * *", Timezone: "UTC",
	})
	if err != nil {
		t.Fatal(err)
	}
	store.items["s1"] = created

	createsBefore := store.creates
	paused, err := newService(t, store).Pause(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if paused.Enabled {
		t.Fatal("pause left schedule enabled")
	}
	if paused.TaskID != created.TaskID || paused.Kind != created.Kind || paused.Expr != created.Expr || paused.Timezone != created.Timezone {
		t.Fatalf("pause changed the trigger: before %+v after %+v", created, paused)
	}
	if !paused.CreatedAt.Equal(created.CreatedAt) || !paused.NextRunAt.Equal(created.NextRunAt) {
		t.Fatal("pause changed run timestamps")
	}
	if store.updates != 1 {
		t.Fatalf("updates = %d, want 1", store.updates)
	}
	if store.creates != createsBefore {
		t.Fatal("pause created a schedule instead of updating it")
	}
	if store.lastUpdate.Enabled || store.lastUpdate.ID != "s1" {
		t.Fatalf("last update: %+v", store.lastUpdate)
	}
	// The stored row is still there: pause is not delete.
	if _, err := store.Get(context.Background(), "s1"); err != nil {
		t.Fatalf("pause removed the schedule: %v", err)
	}
}

func TestScheduleResume(t *testing.T) {
	store := newMockStore()
	store.items["s1"] = server.Schedule{ID: "s1", TaskID: "T", Enabled: false}
	got, err := newService(t, store).Resume(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || got.TaskID != "T" {
		t.Fatalf("resume: %+v", got)
	}
	if _, err := newService(t, store).Resume(context.Background(), "  "); !server.IsValidation(err) {
		t.Fatalf("empty id error = %v, want ValidationError", err)
	}
}

// Delete is permanent: the schedule is gone and a repeat fails.
func TestScheduleDelete(t *testing.T) {
	store := newMockStore()
	svc := newService(t, store)
	if _, err := svc.Schedule(context.Background(), server.ScheduleCreate{
		ID: "s1", TaskID: "T", Kind: server.KindMark, Expr: "m",
	}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if store.deletes != 1 {
		t.Fatalf("deletes = %d, want 1", store.deletes)
	}
	list, err := svc.List(context.Background())
	if err != nil || len(list) != 0 {
		t.Fatalf("schedule survived delete: %+v, %v", list, err)
	}
	if err := svc.Delete(context.Background(), "s1"); !errors.Is(err, server.ErrScheduleNotFound) {
		t.Fatalf("second delete error = %v", err)
	}
}

// A store failure propagates from every operation.
func TestScheduleStoreErrorsPropagate(t *testing.T) {
	cases := []struct {
		name string
		fail func(*mockStore)
		call func(*server.ScheduleService) error
	}{
		{"create", func(m *mockStore) { m.createErr = errors.New("store down") },
			func(s *server.ScheduleService) error {
				_, err := s.Schedule(context.Background(), server.ScheduleCreate{TaskID: "T", Kind: server.KindMark, Expr: "m"})
				return err
			}},
		{"list", func(m *mockStore) { m.listErr = errors.New("store down") },
			func(s *server.ScheduleService) error {
				_, err := s.List(context.Background())
				return err
			}},
		{"pause get", func(m *mockStore) { m.getErr = errors.New("store down") },
			func(s *server.ScheduleService) error {
				_, err := s.Pause(context.Background(), "s1")
				return err
			}},
		{"pause update", func(m *mockStore) { m.updateErr = errors.New("store down") },
			func(s *server.ScheduleService) error {
				_, err := s.Pause(context.Background(), "s1")
				return err
			}},
		{"delete", func(m *mockStore) { m.deleteErr = errors.New("store down") },
			func(s *server.ScheduleService) error { return s.Delete(context.Background(), "s1") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newMockStore()
			m.items["s1"] = server.Schedule{ID: "s1", TaskID: "T", Enabled: true}
			tc.fail(m)
			if err := tc.call(newService(t, m)); err == nil || !strings.Contains(err.Error(), "store down") {
				t.Fatalf("error = %v, want store failure", err)
			}
		})
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	store := server.NewMemoryScheduleStore()
	_, err := newService(t, store).Schedule(context.Background(), server.ScheduleCreate{
		ID: "s1", TaskID: "T", Kind: server.KindInterval, Expr: "5m",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newService(t, store).Pause(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(context.Background(), "s1")
	if err != nil || got.Enabled {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
}

// The internal API refuses an unauthenticated caller and serves the three
// operations to an authenticated one.
func TestSchedulerHandler(t *testing.T) {
	store := server.NewMemoryScheduleStore()
	svc, err := server.NewScheduleService(store)
	if err != nil {
		t.Fatal(err)
	}
	h, err := server.NewSchedulerHandler(svc, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	handler := h.Handler()

	do := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			req.Header.Set("Authorization", "Bearer secret-token")
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := do(http.MethodGet, server.ScheduleListPath, "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated list status = %d", rec.Code)
	}
	createBody := `{"id":"s1","taskID":"BS.32","kind":"interval","expr":"15m","timezone":"UTC"}`
	if rec := do(http.MethodPost, server.SchedulePath, createBody, true); rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(http.MethodPost, server.SchedulePausePath, `{"id":"s1"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("pause status = %d body %s", rec.Code, rec.Body.String())
	}
	listRec := do(http.MethodGet, server.ScheduleListPath, "", true)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d", listRec.Code)
	}
	var payload struct {
		Schedules []server.Schedule `json:"schedules"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Schedules) != 1 || payload.Schedules[0].Enabled {
		t.Fatalf("list after pause = %+v", payload.Schedules)
	}
	pausedField := payload.Schedules[0]

	if rec := do(http.MethodPost, server.ScheduleResumePath, `{"id":"s1"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("resume status = %d body %s", rec.Code, rec.Body.String())
	}
	listRec = do(http.MethodGet, server.ScheduleListPath, "", true)
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Schedules) != 1 || !payload.Schedules[0].Enabled {
		t.Fatalf("list after resume = %+v", payload.Schedules)
	}
	if got := payload.Schedules[0]; got.TaskID != pausedField.TaskID || got.Kind != pausedField.Kind ||
		got.Expr != pausedField.Expr || !got.NextRunAt.Equal(pausedField.NextRunAt) {
		t.Fatalf("resume changed the trigger: before %+v after %+v", pausedField, got)
	}

	if rec := do(http.MethodDelete, server.ScheduleDeletePath+"?id=s1", "", true); rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body %s", rec.Code, rec.Body.String())
	}
	listRec = do(http.MethodGet, server.ScheduleListPath, "", true)
	if err := json.Unmarshal(listRec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Schedules) != 0 {
		t.Fatalf("schedule survived delete: %+v", payload.Schedules)
	}
}

func TestSchedulerHandlerRejectsEmptyToken(t *testing.T) {
	svc, err := server.NewScheduleService(server.NewMemoryScheduleStore())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.NewSchedulerHandler(svc, "  "); err == nil {
		t.Fatal("empty token accepted")
	}
}
