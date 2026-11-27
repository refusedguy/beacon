package bench

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/refusedguy/beacon/internal/infra/tasksource"
	"github.com/refusedguy/beacon/pkg/core"
)

// Local card parsing is the uncached side of the card-parse row. The index is
// built from disk once and then reused, so each pass measures a full scan plus
// the per-card patch path that a claimed card takes.
const (
	cardLocalCards   = 500
	cardLocalPatches = 20
)

type cardLocalBench struct {
	ctx   context.Context
	root  string
	store *tasksource.Markdown
	ids   []core.TaskID
}

// newCardLocalBench writes bounded cards with Scope lines into a temp workspace.
// Setup happens once outside the timed loop.
func newCardLocalBench(b *testing.B) *cardLocalBench {
	b.Helper()
	root := b.TempDir()
	dir := filepath.Join(root, "docs", "tasks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		b.Fatal(err)
	}
	ids := make([]core.TaskID, 0, cardLocalCards)
	for i := 0; i < cardLocalCards; i++ {
		id := core.TaskID(fmt.Sprintf("T-%04d", i))
		ids = append(ids, id)
		content := fmt.Sprintf("---\nid: %s\ntitle: local card %d\nstatus: todo\nscope:\n  - f:pkg/core/run.go\n  - d:internal/app/\n---\n\n# %s. local card\n", id, i, id)
		path := filepath.Join(dir, string(id)+".md")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	return &cardLocalBench{
		ctx:   context.Background(),
		root:  root,
		store: tasksource.NewMarkdown(dir),
		ids:   ids,
	}
}

// runCardStoreLocalScan lists every card through the real task source and then
// marks the first cards in the workspace. Work is cards returned plus patches;
// errors are task-source failures. Keeping the status available means every
// pass sees the same card set.
func runCardStoreLocalScan(h *cardLocalBench) (int64, int64) {
	var work, errs int64
	cards, err := h.store.ListAvailable(h.ctx)
	if err != nil {
		errs++
	} else {
		work += int64(len(cards))
	}
	for i := 0; i < cardLocalPatches; i++ {
		if err := h.store.MarkInWorkspace(h.ctx, h.ids[i], h.root, "todo"); err != nil {
			errs++
		} else {
			work++
		}
	}
	return work, errs
}
