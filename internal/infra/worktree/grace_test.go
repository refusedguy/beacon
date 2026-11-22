package worktree

import (
	"errors"
	"testing"
	"time"
)

func TestGuardCheck(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		now         time.Time
		firstStatus string
		nextStatus  string
		statusErr   error
		removeErr   error
		want        GraceOutcome
		wantRemoves int
	}{
		{
			name:        "inside window defers without removing",
			now:         base.Add(10 * time.Second),
			firstStatus: "",
			nextStatus:  "",
			want:        GraceDeferred,
			wantRemoves: 0,
		},
		{
			name:        "change between checks cancels removal",
			now:         base.Add(DefaultGrace),
			firstStatus: "",
			nextStatus:  " M file\n",
			want:        GraceChanged,
			wantRemoves: 0,
		},
		{
			name:        "unchanged after window removes once",
			now:         base.Add(DefaultGrace),
			firstStatus: "",
			nextStatus:  "",
			want:        GraceRemoved,
			wantRemoves: 1,
		},
		{
			name:        "status error keeps candidate",
			now:         base.Add(DefaultGrace),
			firstStatus: "",
			statusErr:   errors.New("status failed"),
			want:        GraceStatusErr,
			wantRemoves: 0,
		},
		{
			name:        "remove failure is reported",
			now:         base.Add(DefaultGrace),
			firstStatus: "",
			nextStatus:  "",
			removeErr:   errors.New("remove failed"),
			want:        GraceRemoveErr,
			wantRemoves: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			removes := 0
			g := Guard{
				Grace: DefaultGrace,
				Now:   func() time.Time { return tt.now },
				Status: func(string) (string, error) {
					return tt.nextStatus, tt.statusErr
				},
				Remove: func(string) error {
					removes++
					return tt.removeErr
				},
			}
			c := Candidate{Dir: "/tmp/wt", ObservedAt: base, Status: tt.firstStatus}

			got := g.Check(c)

			if got != tt.want {
				t.Fatalf("outcome = %q, want %q", got, tt.want)
			}
			if removes != tt.wantRemoves {
				t.Fatalf("Remove called %d times, want %d", removes, tt.wantRemoves)
			}
		})
	}
}

func TestNewGuardDefersFreshCandidate(t *testing.T) {
	removes := 0
	g := NewGuard(
		func(string) (string, error) { return "", nil },
		func(string) error {
			removes++
			return nil
		},
	)
	c := Candidate{Dir: "/tmp/wt", ObservedAt: time.Now(), Status: ""}

	if got := g.Check(c); got != GraceDeferred {
		t.Fatalf("outcome = %q, want %q", got, GraceDeferred)
	}
	if removes != 0 {
		t.Fatalf("Remove called %d times, want 0", removes)
	}
}
