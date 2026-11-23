package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// backlinkGit answers the two git calls ListWithBacklink makes. The porcelain
// listing is fixed per test; every listed checkout reports the same
// administration directory, which is where the backlink file lives.
type backlinkGit struct {
	listing   string
	admin     string
	gitDirErr error
}

func (b *backlinkGit) run(dir string, args ...string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "list":
		return b.listing, nil
	case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--git-dir":
		if b.gitDirErr != nil {
			return "", b.gitDirErr
		}
		return b.admin, nil
	default:
		return "", fmt.Errorf("unexpected git call: dir=%q args=%q", dir, args)
	}
}

// backlinkFixture builds a main entry and one linked checkout on disk. The .git
// file must exist so canonicalization can prove an identity.
type backlinkFixture struct {
	listing string
	wtDir   string
	admin   string
}

func newBacklinkFixture(t *testing.T) backlinkFixture {
	t.Helper()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	wtDir := filepath.Join(root, "wt")
	admin := filepath.Join(root, "admin", "wt")
	for _, dir := range []string{mainDir, wtDir, admin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeBacklinkFile(t, filepath.Join(wtDir, ".git"), "gitdir: "+admin+"\n")

	listing := "worktree " + mainDir + "\n" +
		"HEAD aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n" +
		"branch refs/heads/main\n\n" +
		"worktree " + wtDir + "\n" +
		"HEAD bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb\n" +
		"branch refs/heads/fix\n"
	return backlinkFixture{listing: listing, wtDir: wtDir, admin: admin}
}

func writeBacklinkFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestListWithBacklink(t *testing.T) {
	t.Run("correct backlink keeps the entry", func(t *testing.T) {
		f := newBacklinkFixture(t)
		writeBacklinkFile(t, filepath.Join(f.admin, "gitdir"), f.wtDir+"/.git\n")

		got, err := ListWithBacklink((&backlinkGit{listing: f.listing, admin: f.admin}).run)
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		want := []Entry{{Dir: f.wtDir, Head: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Branch: "fix"}}
		if len(got) != len(want) || (len(got) == 1 && got[0] != want[0]) {
			t.Fatalf("ListWithBacklink() = %+v, want %+v", got, want)
		}
	})

	t.Run("foreign checkout at a stale path is dropped", func(t *testing.T) {
		f := newBacklinkFixture(t)
		oldDir := filepath.Join(filepath.Dir(f.wtDir), "old")
		if err := os.MkdirAll(oldDir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", oldDir, err)
		}
		writeBacklinkFile(t, filepath.Join(oldDir, ".git"), "gitdir: somewhere\n")
		writeBacklinkFile(t, filepath.Join(f.admin, "gitdir"), oldDir+"/.git\n")

		got, err := ListWithBacklink((&backlinkGit{listing: f.listing, admin: f.admin}).run)
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ListWithBacklink() = %+v, want no entries", got)
		}
	})

	t.Run("missing backlink file drops the entry", func(t *testing.T) {
		f := newBacklinkFixture(t)
		got, err := ListWithBacklink((&backlinkGit{listing: f.listing, admin: f.admin}).run)
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ListWithBacklink() = %+v, want no entries", got)
		}
	})

	t.Run("backlink to a missing path drops the entry", func(t *testing.T) {
		f := newBacklinkFixture(t)
		writeBacklinkFile(t, filepath.Join(f.admin, "gitdir"), filepath.Join(f.wtDir, "gone", ".git")+"\n")

		got, err := ListWithBacklink((&backlinkGit{listing: f.listing, admin: f.admin}).run)
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ListWithBacklink() = %+v, want no entries", got)
		}
	})

	t.Run("failed git-dir lookup drops the entry", func(t *testing.T) {
		f := newBacklinkFixture(t)
		writeBacklinkFile(t, filepath.Join(f.admin, "gitdir"), f.wtDir+"/.git\n")
		run := &backlinkGit{
			listing:   f.listing,
			admin:     f.admin,
			gitDirErr: errors.New("exit status 128"),
		}
		got, err := ListWithBacklink(run.run)
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ListWithBacklink() = %+v, want no entries", got)
		}
	})
}

// backlinkListing is a listing with a known keep set. Each entry has its own
// administration directory, so a fake Runner answers rev-parse by directory.
type backlinkListing struct {
	listing string
	admins  map[string]string
	want    []Entry
}

func newBacklinkListing(t *testing.T, n int) backlinkListing {
	t.Helper()
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	if err := os.MkdirAll(mainDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", mainDir, err)
	}

	var listing strings.Builder
	listing.WriteString("worktree " + mainDir + "\n")
	listing.WriteString("HEAD " + strings.Repeat("a", 40) + "\n")
	listing.WriteString("branch refs/heads/main\n\n")

	admins := make(map[string]string, n)
	want := make([]Entry, 0, n)
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("wt%03d", i))
		admin := filepath.Join(root, fmt.Sprintf("admin%03d", i))
		for _, d := range []string{dir, admin} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", d, err)
			}
		}
		writeBacklinkFile(t, filepath.Join(dir, ".git"), "gitdir: "+admin+"\n")

		head := fmt.Sprintf("%040x", i)
		branch := fmt.Sprintf("branch-%03d", i)
		listing.WriteString("worktree " + dir + "\n")
		listing.WriteString("HEAD " + head + "\n")
		listing.WriteString("branch refs/heads/" + branch + "\n\n")
		admins[dir] = admin

		backlink := filepath.Join(dir, ".git")
		if i%3 == 0 {
			want = append(want, Entry{Dir: dir, Head: head, Branch: branch})
		} else {
			backlink = filepath.Join(dir, "gone", ".git")
		}
		writeBacklinkFile(t, filepath.Join(admin, "gitdir"), backlink+"\n")
	}
	return backlinkListing{listing: listing.String(), admins: admins, want: want}
}

// backlinkGitByDir answers git calls from a fixed listing and an admin map.
// Both fields are read only, so workers share it without a lock.
type backlinkGitByDir struct {
	listing string
	admins  map[string]string
}

func (b *backlinkGitByDir) run(dir string, args ...string) (string, error) {
	switch {
	case len(args) >= 2 && args[0] == "worktree" && args[1] == "list":
		return b.listing, nil
	case len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--git-dir":
		admin, ok := b.admins[dir]
		if !ok {
			return "", fmt.Errorf("unexpected dir %q", dir)
		}
		return admin, nil
	default:
		return "", fmt.Errorf("unexpected git call: dir=%q args=%q", dir, args)
	}
}

// backlinkGate counts concurrent Runner calls and holds rev-parse calls until
// the test releases them. The mutex keeps the counters safe for the workers.
type backlinkGate struct {
	mu      sync.Mutex
	base    Runner
	active  int
	max     int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBacklinkGate(base Runner, capacity int) *backlinkGate {
	return &backlinkGate{
		base:    base,
		started: make(chan struct{}, capacity),
		release: make(chan struct{}),
	}
}

func (g *backlinkGate) run(dir string, args ...string) (string, error) {
	if len(args) >= 2 && args[0] == "rev-parse" && args[1] == "--git-dir" {
		g.mu.Lock()
		g.active++
		if g.active > g.max {
			g.max = g.active
		}
		g.mu.Unlock()

		g.started <- struct{}{}
		<-g.release

		g.mu.Lock()
		g.active--
		g.mu.Unlock()
	}
	return g.base(dir, args...)
}

func (g *backlinkGate) unblock() {
	g.once.Do(func() { close(g.release) })
}

func (g *backlinkGate) maxCalls() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.max
}

func TestListWithBacklinkBoundsWorkers(t *testing.T) {
	const entries = 316
	f := newBacklinkListing(t, entries)
	gate := newBacklinkGate((&backlinkGitByDir{listing: f.listing, admins: f.admins}).run, entries)
	defer gate.unblock()

	type result struct {
		entries []Entry
		err     error
	}
	done := make(chan result, 1)
	go func() {
		got, err := ListWithBacklink(gate.run)
		done <- result{entries: got, err: err}
	}()

	for i := 0; i < backlinkWorkers; i++ {
		select {
		case <-gate.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d concurrent Runner calls reached before timeout", i)
		}
	}
	gate.unblock()

	res := <-done
	if res.err != nil {
		t.Fatalf("ListWithBacklink() error = %v", res.err)
	}
	if got := gate.maxCalls(); got != backlinkWorkers {
		t.Fatalf("max concurrent Runner calls = %d, want %d", got, backlinkWorkers)
	}
	if len(res.entries) != len(f.want) {
		t.Fatalf("ListWithBacklink() returned %d entries, want %d", len(res.entries), len(f.want))
	}
	for i := range f.want {
		if res.entries[i] != f.want[i] {
			t.Fatalf("entry %d = %+v, want %+v", i, res.entries[i], f.want[i])
		}
	}
}
