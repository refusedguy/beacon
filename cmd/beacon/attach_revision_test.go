package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/refusedguy/beacon/internal/app/session"
)

// attachRevisionReader serves registry bytes from a script. Replacing
// attachReadFile lets a test change the saved record between two reads without
// a second process (CC-05).
type attachRevisionReader struct {
	t      *testing.T
	path   string
	script [][]byte
	calls  int
}

func (r *attachRevisionReader) read(path string) ([]byte, error) {
	if path != r.path {
		r.t.Fatalf("read path = %q, want %q", path, r.path)
	}
	if r.calls >= len(r.script) {
		r.t.Fatalf("unexpected registry read %d", r.calls+1)
	}
	data := r.script[r.calls]
	r.calls++
	return append([]byte(nil), data...), nil
}

// attachRevisionSetup writes a service file, installs a scripted registry
// reader for states, and restores the reader after the test.
func attachRevisionSetup(t *testing.T, states ...attachSessionState) *attachRevisionReader {
	t.Helper()
	t313Service(t, "http://127.0.0.1:4096")
	base := os.Getenv("XDG_STATE_HOME")
	path := filepath.Join(base, "beacon", attachSessionsFile)
	reader := &attachRevisionReader{t: t, path: path}
	for _, st := range states {
		data, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		reader.script = append(reader.script, data)
	}
	previous := attachReadFile
	attachReadFile = reader.read
	t.Cleanup(func() { attachReadFile = previous })
	return reader
}

func attachRevisionSavedState(id string) attachSessionState {
	return attachSessionState{
		Sessions: []attachSession{{TaskID: "T9.9", SessionID: id}},
		Last:     "T9.9",
	}
}

func attachRevisionInstallStore(t *testing.T, id string) {
	t.Helper()
	st := &attachResumeStore{
		want:  session.Session{ID: id, Status: session.SessionStatusActive},
		found: true,
	}
	attachResumeInstallRegistry(t, st)
}

func TestAttachResumeRevisionFenceRejectsChangedRegistry(t *testing.T) {
	reader := attachRevisionSetup(t,
		attachRevisionSavedState("sess-saved"),
		attachRevisionSavedState("sess-saved"),
		attachRevisionSavedState("sess-other"),
	)
	attachRevisionInstallStore(t, "sess-saved")

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", "T9.9"}); code != 2 {
		t.Fatalf("exit = %d, want 2, stderr = %q", code, errOut.String())
	}
	if len(*calls) != 0 {
		t.Fatalf("opened a session from a changed registry: %+v", *calls)
	}
	if !strings.Contains(errOut.String(), "перечитайте список") {
		t.Fatalf("stderr has no reread instruction: %q", errOut.String())
	}
	if reader.calls != 3 {
		t.Fatalf("registry reads = %d, want 3", reader.calls)
	}
}

func TestAttachResumeRevisionFenceAllowsUnchangedRegistry(t *testing.T) {
	saved := attachRevisionSavedState("sess-saved")
	reader := attachRevisionSetup(t, saved, saved, saved)
	attachRevisionInstallStore(t, "sess-saved")

	c, _, errOut, calls := t313CLI(t, func(t313Call) runResult { return runResult{code: 0} })
	if code := c.runAttach([]string{"--resume", "T9.9"}); code != 0 {
		t.Fatalf("exit = %d, want 0, stderr = %q", code, errOut.String())
	}
	wantArgs := []string{"--server", "http://127.0.0.1:4096", "--session", "sess-saved"}
	if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0].args, wantArgs) {
		t.Fatalf("calls = %+v, want one attach to %q", *calls, wantArgs)
	}
	if reader.calls != 3 {
		t.Fatalf("registry reads = %d, want 3", reader.calls)
	}
}
