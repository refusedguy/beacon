package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRawMarker publishes a hand-written marker body at the hashed path.
// Tests use it to feed records that the write path would never produce.
func writeRawMarker(t *testing.T, store, worktreeDir, body string) {
	t.Helper()
	path := MarkerPath(store, worktreeDir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// Every marker written by this version carries the current schema version and
// keeps it after a touch, so the reader never rejects its own writes.
func TestMarkerWritesStampCurrentVersion(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")

	m, err := WriteMarker(store, wt, "ses_123", markerNow)
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != MarkerVersion {
		t.Fatalf("версия записанного маркера %d, ожидалась %d", m.Version, MarkerVersion)
	}
	got, ok, err := ReadMarker(store, wt)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Version != MarkerVersion {
		t.Fatalf("прочитанный маркер: %+v, ok=%v", got, ok)
	}

	touched, err := TouchMarker(store, wt, "ses_other", markerNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if touched.Version != MarkerVersion {
		t.Fatalf("версия после touch %d, ожидалась %d", touched.Version, MarkerVersion)
	}
}

// The version gate accepts a current record and refuses an old record or a
// future record with a typed error. A record written before versioning has no
// version field, so it decodes to zero and takes the old record path.
func TestReadMarkerVersionOutcomes(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	base := fmt.Sprintf(`"path":%q,"agentID":"ses_123","updatedAt":"2026-11-22T10:00:00Z"`, cleanDir(wt))

	t.Run("запись текущей версии принята", func(t *testing.T) {
		body := "{" + base + fmt.Sprintf(`,"version":%d}`, MarkerVersion)
		writeRawMarker(t, store, wt, body)
		m, ok, err := ReadMarker(store, wt)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || m.AgentID != "ses_123" || m.Version != MarkerVersion {
			t.Fatalf("маркер: %+v, ok=%v", m, ok)
		}
	})

	cases := []struct {
		name string
		body string
	}{
		{"запись без поля версии", "{" + base + "}"},
		{"старая версия", "{" + base + `,"version":0}`},
		{"будущая версия", "{" + base + fmt.Sprintf(`,"version":%d}`, MarkerVersion+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeRawMarker(t, store, wt, tc.body)
			if _, _, err := ReadMarker(store, wt); !errors.Is(err, ErrMarkerVersion) {
				t.Fatalf("версия не отвергнута: %v", err)
			}
		})
	}
}

// An unknown field is a read error rather than a silently ignored extra field.
func TestReadMarkerRejectsUnknownField(t *testing.T) {
	store := t.TempDir()
	wt := filepath.Join(t.TempDir(), "wt")
	body := fmt.Sprintf(`{"version":%d,"path":%q,"agentID":"ses_123","updatedAt":"2026-11-22T10:00:00Z","ownerThreadID":"ses_x"}`, MarkerVersion, cleanDir(wt))
	writeRawMarker(t, store, wt, body)

	_, _, err := ReadMarker(store, wt)
	if err == nil {
		t.Fatal("запись с лишним полем прочитана без ошибки")
	}
	if errors.Is(err, ErrMarkerVersion) {
		t.Fatalf("ожидалась ошибка разбора, получена ошибка версии: %v", err)
	}
	if !strings.Contains(err.Error(), "ownerThreadID") {
		t.Fatalf("ошибка не называет лишнее поле: %v", err)
	}
}
