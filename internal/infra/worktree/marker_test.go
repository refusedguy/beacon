package worktree

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

var markerNow = time.Date(2026, time.November, 22, 10, 0, 0, 0, time.UTC)

// A freshly bound worktree exposes a marker that names its agent and path.
// The marker is the artifact the collector must be able to read back.
func TestWriteMarkerThenReadBack(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "agent-worktree")

	m, err := WriteMarker(store, wt, "ses_123", markerNow)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("маркер не найден после записи")
	}
	if got.Path != m.Path || got.AgentID != "ses_123" {
		t.Fatalf("маркер %+v, ожидался путь %q и агент ses_123", got, wt)
	}
	if !got.UpdatedAt.Equal(markerNow) {
		t.Fatalf("время маркера %s, ожидалось %s", got.UpdatedAt, markerNow)
	}

	info, err := os.Stat(MarkerPath(store, wt))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права маркера %o, ожидалось 600", perm)
	}
}

// An absent marker is a normal miss, not an error.
func TestReadMarkerMissing(t *testing.T) {
	store := t.TempDir()
	_, ok, err := ReadMarker(store, filepath.Join(t.TempDir(), "never-bound"))
	if err != nil {
		t.Fatalf("отсутствие маркера считается ошибкой: %v", err)
	}
	if ok {
		t.Fatal("отсутствующий маркер найден")
	}
}

// A broken marker must surface as an error so the collector cannot silently
// treat corrupt state as an unlocked worktree.
func TestReadMarkerCorruptFails(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	path := MarkerPath(store, wt)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadMarker(store, wt); err == nil {
		t.Fatal("повреждённый маркер прочитан без ошибки")
	}
}

// A fresh marker protects its worktree. Once its TTL passes, the same marker
// stops protecting it so cleanup is not blocked forever.
func TestMarkerValidFreshThenExpired(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	m := Marker{Path: wt, AgentID: "ses_123", UpdatedAt: markerNow}

	if !MarkerValid(m, wt, markerNow.Add(5*time.Minute), DefaultMarkerTTL) {
		t.Fatal("свежий маркер не защищает worktree")
	}
	if MarkerValid(m, wt, markerNow.Add(DefaultMarkerTTL), DefaultMarkerTTL) {
		t.Fatal("устаревший маркер всё ещё защищает worktree")
	}
	if !MarkerValid(m, wt, markerNow.Add(24*time.Hour), 0) {
		t.Fatal("отрицательный TTL должен отключать истечение")
	}
}

// A marker for another worktree, or one without an agent or timestamp, cannot
// protect this worktree. A timestamp in the future is not a valid lease.
func TestMarkerValidRejectsMismatch(t *testing.T) {
	wt := filepath.Join(t.TempDir(), "wt")
	other := filepath.Join(t.TempDir(), "other")
	cases := []struct {
		name string
		m    Marker
	}{
		{"чужой путь", Marker{Path: other, AgentID: "ses_123", UpdatedAt: markerNow}},
		{"нет агента", Marker{Path: wt, UpdatedAt: markerNow}},
		{"нет времени", Marker{Path: wt, AgentID: "ses_123"}},
		{"будущее время", Marker{Path: wt, AgentID: "ses_123", UpdatedAt: markerNow.Add(time.Hour)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if MarkerValid(tc.m, wt, markerNow, DefaultMarkerTTL) {
				t.Fatalf("маркер %+v защитил чужой worktree", tc.m)
			}
		})
	}
}

// Reusing a worktree refreshes its marker without changing the agent that
// owns it. This is what keeps a long-running agent from being collected.
func TestTouchMarkerRefreshesTimeAndKeepsAgent(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := WriteMarker(store, wt, "ses_123", markerNow); err != nil {
		t.Fatal(err)
	}

	later := markerNow.Add(10 * time.Minute)
	if _, err := TouchMarker(store, wt, "ses_other", later); err != nil {
		t.Fatal(err)
	}
	got, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("маркер пропал после обновления")
	}
	if got.AgentID != "ses_123" {
		t.Fatalf("агент маркера %q, ожидался ses_123", got.AgentID)
	}
	if !got.UpdatedAt.Equal(later) {
		t.Fatalf("время маркера %s, ожидалось %s", got.UpdatedAt, later)
	}
	if !MarkerValid(got, wt, later.Add(5*time.Minute), DefaultMarkerTTL) {
		t.Fatal("обновлённый маркер не защищает worktree")
	}
}

// Marker files are keyed by worktree path: the same worktree always maps to
// one file, and two worktrees never share a marker.
func TestMarkerPathIsStableAndDistinct(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	first := MarkerPath(store, wt)
	if again := MarkerPath(store, wt); again != first {
		t.Fatalf("путь маркера нестабилен: %q и %q", first, again)
	}
	if other := MarkerPath(store, filepath.Join(t.TempDir(), "other")); other == first {
		t.Fatal("два worktree получили один маркер")
	}
}

func markerFileCount(t *testing.T, store string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(store, MarkerDir))
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

// Two agents binding the same worktree at once must not both win. One takes
// ownership, the other gets a conflict, and the winner keeps the marker.
func TestWriteMarkerConcurrentBindHasSingleOwner(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")

	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for _, agentID := range []string{"ses_first", "ses_second"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			_, err := WriteMarker(store, wt, id, markerNow)
			results <- err
		}(agentID)
	}
	close(start)
	wg.Wait()
	close(results)

	var wins, refusals int
	for err := range results {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrMarkerOwnerConflict):
			refusals++
		default:
			t.Fatalf("неожиданная ошибка bind: %v", err)
		}
	}
	if wins != 1 || refusals != 1 {
		t.Fatalf("bind: успех %d, отказ %d; ожидалось 1 и 1", wins, refusals)
	}

	got, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("маркер не записан после гонки bind")
	}
	winner := got.AgentID
	if winner != "ses_first" && winner != "ses_second" {
		t.Fatalf("неизвестный владелец %q", winner)
	}
	loser := "ses_second"
	if winner == "ses_second" {
		loser = "ses_first"
	}
	if n := markerFileCount(t, store); n != 1 {
		t.Fatalf("файлов маркеров %d, ожидалось 1", n)
	}

	// The loser cannot take over later, even with a fresher timestamp.
	if _, err := WriteMarker(store, wt, loser, markerNow.Add(time.Hour)); !errors.Is(err, ErrMarkerOwnerConflict) {
		t.Fatalf("проигравший агент получил владение: %v", err)
	}
	after, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || after.AgentID != winner {
		t.Fatalf("владелец сменился: %+v, ожидался %q", after, winner)
	}
	if n := markerFileCount(t, store); n != 1 {
		t.Fatalf("файлов маркеров после повторного bind %d, ожидалось 1", n)
	}
}

// A repeated bind by the same agent keeps one marker and never rewrites it.
func TestWriteMarkerSameAgentRebindDoesNotOverwrite(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")

	first, err := WriteMarker(store, wt, "ses_123", markerNow)
	if err != nil {
		t.Fatal(err)
	}
	later := markerNow.Add(10 * time.Minute)
	again, err := WriteMarker(store, wt, "ses_123", later)
	if err != nil {
		t.Fatal(err)
	}
	if !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("повторный bind перезаписал время: %s, ожидалось %s", again.UpdatedAt, first.UpdatedAt)
	}
	got, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.AgentID != "ses_123" || !got.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("маркер после повторного bind: %+v, ok=%v", got, ok)
	}
	if n := markerFileCount(t, store); n != 1 {
		t.Fatalf("файлов маркеров %d, ожидалось 1", n)
	}
}

// A different agent cannot replace an existing owner, and the conflict error
// names both the owner and the rejected caller.
func TestWriteMarkerDifferentAgentConflict(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	if _, err := WriteMarker(store, wt, "ses_owner", markerNow); err != nil {
		t.Fatal(err)
	}

	_, err := WriteMarker(store, wt, "ses_other", markerNow.Add(time.Minute))
	if !errors.Is(err, ErrMarkerOwnerConflict) {
		t.Fatalf("чужой агент не получил отказ: %v", err)
	}
	var conflict *OwnerConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("ошибка не несёт владельца: %T", err)
	}
	if conflict.Owner != "ses_owner" || conflict.Requested != "ses_other" {
		t.Fatalf("конфликт: %+v", conflict)
	}
	got, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.AgentID != "ses_owner" || !got.UpdatedAt.Equal(markerNow) {
		t.Fatalf("маркер после чужого bind: %+v, ok=%v", got, ok)
	}
}
