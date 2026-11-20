package recovery

import (
	"errors"
	"testing"
)

type fakePort struct {
	snap  Snapshot
	found bool
	err   error
	calls int
}

func (f *fakePort) LatestSnapshot() (Snapshot, bool, error) {
	f.calls++
	return f.snap, f.found, f.err
}

func TestBootSnapshotExists(t *testing.T) {
	p := &fakePort{
		found: true,
		snap: Snapshot{
			SchemaVersion: SchemaVersion,
			Payload:       []byte(`{"cursor":{"seq":42}}`),
		},
	}
	state, err := Boot(p)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if state.Cursor.Seq != 42 {
		t.Errorf("Cursor.Seq = %d, want 42", state.Cursor.Seq)
	}
	if !state.Found {
		t.Error("Found = false, want true")
	}
	if state.Reset {
		t.Error("Reset = true, want false")
	}
	if p.calls != 1 {
		t.Errorf("port calls = %d, want 1", p.calls)
	}
}

func TestBootNoSnapshot(t *testing.T) {
	p := &fakePort{found: false}
	state, err := Boot(p)
	if err != nil {
		t.Fatalf("Boot: %v", err)
	}
	if !state.Reset {
		t.Error("Reset = false, want true")
	}
	if state.Found {
		t.Error("Found = true, want false")
	}
	if state.Cursor.Seq != 0 {
		t.Errorf("Cursor.Seq = %d, want 0", state.Cursor.Seq)
	}
	if p.calls != 1 {
		t.Errorf("port calls = %d, want 1", p.calls)
	}
}

func TestBootBrokenSnapshot(t *testing.T) {
	cases := []struct {
		name string
		snap Snapshot
	}{
		{
			name: "malformed json",
			snap: Snapshot{SchemaVersion: SchemaVersion, Payload: []byte(`{not json`)},
		},
		{
			name: "schema mismatch",
			snap: Snapshot{SchemaVersion: SchemaVersion + 1, Payload: []byte(`{"cursor":{"seq":1}}`)},
		},
		{
			name: "negative cursor",
			snap: Snapshot{SchemaVersion: SchemaVersion, Payload: []byte(`{"cursor":{"seq":-1}}`)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakePort{found: true, snap: tc.snap}
			state, err := Boot(p)
			if !errors.Is(err, ErrBrokenSnapshot) {
				t.Fatalf("Boot error = %v, want ErrBrokenSnapshot", err)
			}
			if state != (State{}) {
				t.Errorf("state = %+v, want zero", state)
			}
		})
	}
}

func TestBootPortError(t *testing.T) {
	sentinel := errors.New("disk unavailable")
	p := &fakePort{found: true, err: sentinel}
	state, err := Boot(p)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Boot error = %v, want wrapped sentinel", err)
	}
	if state != (State{}) {
		t.Errorf("state = %+v, want zero", state)
	}
	if p.calls != 1 {
		t.Errorf("port calls = %d, want 1", p.calls)
	}
}

func TestBootNilPort(t *testing.T) {
	if _, err := Boot(nil); err == nil {
		t.Fatal("Boot(nil) error = nil, want error")
	}
}
