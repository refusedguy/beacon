package worktree

import (
	"errors"
	"testing"
)

func TestGuardWatch(t *testing.T) {
	const (
		headSHA  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		aheadSHA = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		otherSHA = "cccccccccccccccccccccccccccccccccccccccc"
	)

	tests := []struct {
		name       string
		entry      Entry
		watches    []WatchAnchor
		ancestors  map[string]string
		wantID     string
		wantReason string
		wantCalls  int
	}{
		{
			name:       "HEAD совпадает с записанным SHA Watch",
			entry:      Entry{Dir: "/repo/wt1", Head: headSHA, Branch: "fix/wt1"},
			watches:    []WatchAnchor{{ID: "W1", HeadSHA: headSHA}},
			wantID:     "W1",
			wantReason: GuardReasonExactHead,
		},
		{
			name:       "HEAD ушёл вперёд после локального коммита",
			entry:      Entry{Dir: "/repo/wt2", Head: aheadSHA},
			watches:    []WatchAnchor{{ID: "W2", HeadSHA: headSHA}},
			ancestors:  map[string]string{headSHA: headSHA},
			wantID:     "W2",
			wantReason: GuardReasonAncestorHead,
			wantCalls:  1,
		},
		{
			name:  "живого Watch нет",
			entry: Entry{Dir: "/repo/wt3", Head: headSHA},
		},
		{
			name:       "ветка Watch совпадает с веткой worktree",
			entry:      Entry{Dir: "/repo/wt4", Head: otherSHA, Branch: "fix/wt4"},
			watches:    []WatchAnchor{{ID: "W4", Branch: "fix/wt4"}},
			wantID:     "W4",
			wantReason: GuardReasonWatchBranch,
		},
		{
			name:       "реестр привязок указывает на каталог worktree",
			entry:      Entry{Dir: "/repo/wt5", Head: otherSHA},
			watches:    []WatchAnchor{{ID: "W5", Dir: "/repo/wt5/"}},
			wantID:     "W5",
			wantReason: GuardReasonWatchBinding,
		},
		{
			name:      "чужой Watch с другой историей не защищает",
			entry:     Entry{Dir: "/repo/wt6", Head: otherSHA},
			watches:   []WatchAnchor{{ID: "W6", HeadSHA: headSHA}},
			wantCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			run := func(dir string, args ...string) (string, error) {
				calls++
				if dir != tt.entry.Dir {
					t.Fatalf("merge-base dir = %q, want %q", dir, tt.entry.Dir)
				}
				if len(args) != 3 || args[0] != "merge-base" {
					t.Fatalf("unexpected git call: %v", args)
				}
				if base, ok := tt.ancestors[args[2]]; ok {
					return base, nil
				}
				return "", errors.New("no merge base")
			}

			got, ok := GuardWatch(run, tt.entry, tt.watches)
			wantProtected := tt.wantID != ""
			if ok != wantProtected {
				t.Fatalf("protected = %v, want %v (answer %+v)", ok, wantProtected, got)
			}
			if got.ID != tt.wantID || got.Reason != tt.wantReason {
				t.Fatalf("answer = %+v, want ID %q and reason %q", got, tt.wantID, tt.wantReason)
			}
			if calls != tt.wantCalls {
				t.Fatalf("git calls = %d, want %d", calls, tt.wantCalls)
			}
		})
	}
}
