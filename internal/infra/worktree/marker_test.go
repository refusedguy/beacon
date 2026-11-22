package worktree

import (
	"os"
	"path/filepath"
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
