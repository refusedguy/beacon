package worktree

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeGit answers the three git calls that gc.go makes. It records removals so
// a test can prove that a kept worktree never reached `git worktree remove`.
type fakeGit struct {
	listOut string
	listErr error

	status    map[string]string
	statusErr map[string]error
	removeErr map[string]error
	removed   []string
}

func (f *fakeGit) run(dir string, args ...string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "list":
		return f.listOut, f.listErr
	case len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
		if err := f.statusErr[dir]; err != nil {
			return "", err
		}
		return f.status[dir], nil
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "remove" && len(args) >= 4 && args[2] == "--":
		target := args[3]
		if err := f.removeErr[target]; err != nil {
			return "git: remove failed", err
		}
		f.removed = append(f.removed, target)
		return "", nil
	default:
		return "", fmt.Errorf("unexpected git call: dir=%q args=%q", dir, args)
	}
}

func keptReasons(t *testing.T, res Result) map[string]string {
	t.Helper()
	out := make(map[string]string, len(res.Kept))
	for _, k := range res.Kept {
		if _, dup := out[k.Dir]; dup {
			t.Fatalf("duplicate kept entry for %s", k.Dir)
		}
		out[k.Dir] = k.Reason
	}
	return out
}

func assertKept(t *testing.T, res Result, want map[string]string) {
	t.Helper()
	got := keptReasons(t, res)
	if len(got) != len(want) {
		t.Fatalf("kept = %v, want %v", got, want)
	}
	for dir, reason := range want {
		gotReason, ok := got[dir]
		if !ok {
			t.Fatalf("no kept entry for %s; got %v", dir, got)
		}
		if gotReason != reason {
			t.Errorf("kept[%s] = %q, want %q", dir, gotReason, reason)
		}
	}
}

func assertRemoved(t *testing.T, res Result, want ...string) {
	t.Helper()
	if len(res.Removed) != len(want) {
		t.Fatalf("removed = %v, want %v", res.Removed, want)
	}
	for i := range want {
		if res.Removed[i] != want[i] {
			t.Fatalf("removed = %v, want %v", res.Removed, want)
		}
	}
}

func TestParsePorcelain(t *testing.T) {
	tests := []struct {
		name    string
		listing string
		want    []Entry
	}{
		{
			name: "main attached and detached and locked blocks",
			listing: "worktree /repo/main\n" +
				"HEAD ABCDEF0123456789ABCDEF0123456789ABCDEF01\n" +
				"branch refs/heads/main\n\n" +
				"worktree /repo/detached\n" +
				"HEAD 2222222222222222222222222222222222222222\n" +
				"detached\n\n" +
				"worktree /repo/locked\n" +
				"HEAD 3333333333333333333333333333333333333333\n" +
				"branch refs/heads/locked\n" +
				"locked\n",
			want: []Entry{
				{Dir: "/repo/main", Head: "abcdef0123456789abcdef0123456789abcdef01", Branch: "main"},
				{Dir: "/repo/detached", Head: "2222222222222222222222222222222222222222"},
				{Dir: "/repo/locked", Head: "3333333333333333333333333333333333333333", Branch: "locked", Locked: true},
			},
		},
		{
			name:    "unknown keys are ignored and empty blocks are skipped",
			listing: "worktree /repo/x\nHEAD 4444444444444444444444444444444444444444\nprunable gitdir file points to non-existent location\n\n\nworktree /repo/y\nHEAD 5555555555555555555555555555555555555555\n",
			want: []Entry{
				{Dir: "/repo/x", Head: "4444444444444444444444444444444444444444"},
				{Dir: "/repo/y", Head: "5555555555555555555555555555555555555555"},
			},
		},
		{
			name:    "empty listing",
			listing: "",
			want:    nil,
		},
		{
			name:    "last block without trailing newline",
			listing: "worktree /repo/z\nHEAD 6666666666666666666666666666666666666666",
			want: []Entry{
				{Dir: "/repo/z", Head: "6666666666666666666666666666666666666666"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePorcelain(tt.listing)
			if len(got) != len(tt.want) {
				t.Fatalf("parsePorcelain() = %+v, want %+v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("entry[%d] = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestList(t *testing.T) {
	t.Run("drops the main worktree", func(t *testing.T) {
		f := &fakeGit{listOut: "worktree /main\nHEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nbranch refs/heads/main\n\n" +
			"worktree /wt/one\nHEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\nbranch refs/heads/one\n\n" +
			"worktree /wt/two\nHEAD cccccccccccccccccccccccccccccccccccccccc\n"}
		got, err := List(f.run)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		want := []Entry{
			{Dir: "/wt/one", Head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Branch: "one"},
			{Dir: "/wt/two", Head: "cccccccccccccccccccccccccccccccccccccccc"},
		}
		if len(got) != len(want) {
			t.Fatalf("List() = %+v, want %+v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("entry[%d] = %+v, want %+v", i, got[i], want[i])
			}
		}
	})

	t.Run("only main worktree gives empty result", func(t *testing.T) {
		f := &fakeGit{listOut: "worktree /main\nHEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n"}
		got, err := List(f.run)
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("List() = %+v, want empty", got)
		}
	})

	t.Run("git error wraps command and git text", func(t *testing.T) {
		f := &fakeGit{listOut: "fatal: not a git repository", listErr: errors.New("exit status 128")}
		_, err := List(f.run)
		if err == nil {
			t.Fatal("List() error = nil, want wrapped git error")
		}
		for _, want := range []string{"worktree: list", "exit status 128", "fatal: not a git repository"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not contain %q", err, want)
			}
		}
	})
}

func TestWatchIDForHead(t *testing.T) {
	tests := []struct {
		name  string
		heads map[string]string
		head  string
		want  string
	}{
		{
			name:  "full head matches exact key",
			heads: map[string]string{"abcdef0123456789": "watch-1"},
			head:  "abcdef0123456789",
			want:  "watch-1",
		},
		{
			name:  "short head matches longer key by prefix",
			heads: map[string]string{"abcdef0123456789": "watch-1"},
			head:  "abcdef0",
			want:  "watch-1",
		},
		{
			name:  "head shorter than seven characters is ignored",
			heads: map[string]string{"abcdef0123456789": "watch-1"},
			head:  "abc",
			want:  "",
		},
		{
			name:  "head is case and whitespace insensitive",
			heads: map[string]string{"abcdef0123456789": "watch-1"},
			head:  "  ABCDEF0 \n",
			want:  "watch-1",
		},
		{
			name:  "two matching keys give the sorted first key",
			heads: map[string]string{"abcdef1999999999": "watch-B", "abcdef1111111111": "watch-A"},
			head:  "abcdef1",
			want:  "watch-A",
		},
		{
			name:  "no match",
			heads: map[string]string{"abcdef0123456789": "watch-1"},
			head:  "9999999",
			want:  "",
		},
		{
			name:  "empty head",
			heads: map[string]string{"abcdef0123456789": "watch-1"},
			head:  "",
			want:  "",
		},
		{
			name:  "empty map",
			heads: map[string]string{},
			head:  "abcdef0",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := WatchIDForHead(tt.heads, tt.head); got != tt.want {
				t.Errorf("WatchIDForHead() = %q, want %q", got, tt.want)
			}
		})
	}
}

const collectListing = "worktree /main\n" +
	"HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
	"branch refs/heads/main\n\n" +
	"worktree /locked\n" +
	"HEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
	"branch refs/heads/locked\n" +
	"locked\n\n" +
	"worktree /protected\n" +
	"HEAD cccccccccccccccccccccccccccccccccccccccc\n" +
	"branch refs/heads/protected\n\n" +
	"worktree /watched\n" +
	"HEAD dddddddddddddddddddddddddddddddddddddddd\n" +
	"branch refs/heads/watched\n\n" +
	"worktree /dirty\n" +
	"HEAD eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee\n" +
	"branch refs/heads/dirty\n\n" +
	"worktree /statusfail\n" +
	"HEAD ffffffffffffffffffffffffffffffffffffffff\n" +
	"branch refs/heads/statusfail\n\n" +
	"worktree /clean\n" +
	"HEAD 1111111111111111111111111111111111111111\n" +
	"branch refs/heads/clean\n\n" +
	"worktree /detached\n" +
	"HEAD 2222222222222222222222222222222222222222\n"

func TestCollect(t *testing.T) {
	t.Run("removes only the clean unlocked unprotected unwatched worktree", func(t *testing.T) {
		f := &fakeGit{
			listOut: collectListing,
			status: map[string]string{
				"/dirty":    " M file.go\n",
				"/detached": "?? new.go\n",
			},
			statusErr: map[string]error{
				"/statusfail": errors.New("exit status 1"),
			},
		}
		heads := map[string]string{"dddddddddddddddddddddddddddddddddddddddd": "watch-live"}
		res, err := Collect(f.run, heads, "/protected")
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		assertRemoved(t, res, "/clean")
		if len(f.removed) != 1 || f.removed[0] != "/clean" {
			t.Fatalf("git remove calls = %v, want [/clean]", f.removed)
		}
		assertKept(t, res, map[string]string{
			"/locked":     ReasonLocked,
			"/protected":  ReasonProtectedDir,
			"/watched":    ReasonActiveWatch + ":watch-live",
			"/dirty":      ReasonDirty,
			"/detached":   ReasonDirty,
			"/statusfail": ReasonStatusError + ":worktree: status /statusfail: exit status 1: ",
		})

		kept := keptReasons(t, res)
		if _, present := kept["/main"]; present {
			t.Error("main worktree became a collection candidate")
		}
		for _, k := range res.Kept {
			if k.Dir == "/dirty" && k.Branch != "dirty" {
				t.Errorf("kept branch for /dirty = %q, want dirty", k.Branch)
			}
		}
	})

	t.Run("short head still matches a live watch", func(t *testing.T) {
		f := &fakeGit{listOut: "worktree /main\nHEAD 0000000000000000000000000000000000000000\n\n" +
			"worktree /short\nHEAD abcdef1\nbranch refs/heads/short\n"}
		res, err := Collect(f.run, map[string]string{"abcdef1234567890": "watch-short"})
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		assertRemoved(t, res)
		assertKept(t, res, map[string]string{"/short": ReasonActiveWatch + ":watch-short"})
	})

	t.Run("uppercase head matches a live watch after parsing", func(t *testing.T) {
		f := &fakeGit{listOut: "worktree /main\nHEAD 0000000000000000000000000000000000000000\n\n" +
			"worktree /upper\nHEAD DDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDDD\nbranch refs/heads/upper\n"}
		res, err := Collect(f.run, map[string]string{"dddddddddddddddddddddddddddddddddddddddd": "watch-upper"})
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		assertRemoved(t, res)
		assertKept(t, res, map[string]string{"/upper": ReasonActiveWatch + ":watch-upper"})
	})

	t.Run("failed remove keeps the worktree", func(t *testing.T) {
		f := &fakeGit{
			listOut:   "worktree /main\nHEAD 0000000000000000000000000000000000000000\n\nworktree /badremove\nHEAD 1111111111111111111111111111111111111111\n",
			removeErr: map[string]error{"/badremove": errors.New("permission denied")},
		}
		res, err := Collect(f.run, nil)
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		assertRemoved(t, res)
		assertKept(t, res, map[string]string{
			"/badremove": ReasonRemoveError + ":worktree: remove /badremove: permission denied: git: remove failed",
		})
	})

	t.Run("list error aborts the pass", func(t *testing.T) {
		f := &fakeGit{listOut: "fatal: bad repository", listErr: errors.New("exit status 128")}
		_, err := Collect(f.run, nil)
		if err == nil {
			t.Fatal("Collect() error = nil, want list error")
		}
		if !strings.Contains(err.Error(), "fatal: bad repository") {
			t.Errorf("error %q does not contain git text", err)
		}
	})

	t.Run("empty listing collects nothing", func(t *testing.T) {
		f := &fakeGit{listOut: ""}
		res, err := Collect(f.run, nil)
		if err != nil {
			t.Fatalf("Collect() error = %v", err)
		}
		assertRemoved(t, res)
		assertKept(t, res, map[string]string{})
	})
}

func TestDecideGuard(t *testing.T) {
	base := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	candidate := Candidate{Dir: "/candidate", ObservedAt: base, Status: ""}

	tests := []struct {
		name        string
		now         time.Time
		nextStatus  string
		statusErr   error
		removeErr   error
		wantReason  string
		wantRemoved bool
		wantRemoves int
	}{
		{
			name:       "fresh candidate defers without removing",
			now:        base.Add(10 * time.Second),
			nextStatus: "",
			wantReason: ReasonDeferred,
		},
		{
			name:       "change between checks cancels removal",
			now:        base.Add(DefaultGrace),
			nextStatus: " M file.go\n",
			wantReason: ReasonChanged,
		},
		{
			name:       "status error keeps the candidate",
			now:        base.Add(DefaultGrace),
			statusErr:  errors.New("status failed"),
			wantReason: ReasonStatusError,
		},
		{
			name:        "remove error keeps the candidate",
			now:         base.Add(DefaultGrace),
			nextStatus:  "",
			removeErr:   errors.New("remove failed"),
			wantReason:  ReasonRemoveError,
			wantRemoves: 1,
		},
		{
			name:        "unchanged after the window removes once",
			now:         base.Add(DefaultGrace),
			nextStatus:  "",
			wantRemoved: true,
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

			reason, removed := DecideGuard(g, candidate)

			if reason != tt.wantReason || removed != tt.wantRemoved {
				t.Fatalf("DecideGuard() = (%q, %v), want (%q, %v)", reason, removed, tt.wantReason, tt.wantRemoved)
			}
			if removes != tt.wantRemoves {
				t.Fatalf("Remove calls = %d, want %d", removes, tt.wantRemoves)
			}
		})
	}
}
