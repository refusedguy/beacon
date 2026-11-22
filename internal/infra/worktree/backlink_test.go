package worktree

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
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

		got, err := ListWithBacklink(&backlinkGit{listing: f.listing, admin: f.admin})
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

		got, err := ListWithBacklink(&backlinkGit{listing: f.listing, admin: f.admin})
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ListWithBacklink() = %+v, want no entries", got)
		}
	})

	t.Run("missing backlink file drops the entry", func(t *testing.T) {
		f := newBacklinkFixture(t)
		got, err := ListWithBacklink(&backlinkGit{listing: f.listing, admin: f.admin})
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

		got, err := ListWithBacklink(&backlinkGit{listing: f.listing, admin: f.admin})
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
		got, err := ListWithBacklink(run)
		if err != nil {
			t.Fatalf("ListWithBacklink() error = %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("ListWithBacklink() = %+v, want no entries", got)
		}
	})
}
