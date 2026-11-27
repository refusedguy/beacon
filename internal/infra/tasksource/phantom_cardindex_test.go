package tasksource

import (
	"os"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestCardIndexDetectsRemovalWithoutDirectoryMtimeChange(t *testing.T) {
	dir := t.TempDir()
	path := writeIndexCard(t, dir, "T-X", "available")
	store := CardStore{Dir: dir}
	resetCardIndex(dir)
	if _, err := store.indexedFind(core.TaskID("T-X")); err != nil {
		t.Fatalf("indexedFind before removal: %v", err)
	}

	// Restore the directory mtime after removing the card. Removal must be
	// detected by statting indexed paths, not by the directory timestamp.
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, dirInfo.ModTime(), dirInfo.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.indexedFind(core.TaskID("T-X")); err == nil {
		t.Fatal("removed card still found in index")
	}
}
