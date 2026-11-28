package app

import (
	"context"
	"errors"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// failingReadWriter serves a known card list until readErr is set. Write and
// Remove record attempts so a failed read can be shown to stop the batch.
type failingReadWriter struct {
	cards   []core.Task
	readErr error
	reads   int
	writes  int
	removes int
}

func (w *failingReadWriter) Available(context.Context) ([]core.Task, error) {
	w.reads++
	if w.readErr != nil {
		return nil, w.readErr
	}
	return w.cards, nil
}

func (w *failingReadWriter) Write(_ context.Context, tasks []core.Task) ([]core.Task, error) {
	w.writes++
	out := make([]core.Task, len(tasks))
	copy(out, tasks)
	return out, nil
}

func (w *failingReadWriter) Remove(_ context.Context, ids ...core.TaskID) {
	w.removes += len(ids)
}

func readErrorBatchInputs() []CreateTaskInput {
	return []CreateTaskInput{
		{ID: "T-EXISTING", Title: "Existing"},
		{Title: "New"},
	}
}

func TestCreateBatchFailsOnCardReadError(t *testing.T) {
	want := errors.New("task source read failed")
	inputs := readErrorBatchInputs()
	creator := &TaskCreator{}
	ids := creator.batchIDs(inputs)
	existing := core.Task{ID: ids[0], SourceRef: "docs/tasks/handoff.md"}
	writer := &failingReadWriter{cards: []core.Task{existing}, readErr: want}
	publisher := &recordingPublisher{}
	store := &graphStore{}
	creator.Writer = writer
	creator.Publisher = publisher
	creator.Store = store

	got, err := creator.CreateBatch(context.Background(), inputs)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if got != nil {
		t.Fatalf("results = %+v, want none after a failed read", got)
	}
	if writer.writes != 0 || writer.removes != 0 {
		t.Fatalf("writes = %d, removes = %d, want none after a failed read", writer.writes, writer.removes)
	}
	if publisher.calls != 0 || store.calls != 0 {
		t.Fatalf("publishes = %d, syncs = %d, want none after a failed read", publisher.calls, store.calls)
	}
	if len(writer.cards) != 1 || writer.cards[0].ID != existing.ID ||
		writer.cards[0].SourceRef != existing.SourceRef {
		t.Fatalf("existing cards changed: %+v", writer.cards)
	}
}

func TestCreateBatchRetryAfterReadErrorKeepsExistingCard(t *testing.T) {
	inputs := readErrorBatchInputs()
	creator := &TaskCreator{}
	ids := creator.batchIDs(inputs)
	existing := core.Task{ID: ids[0], SourceRef: "docs/tasks/handoff.md"}
	writer := &failingReadWriter{cards: []core.Task{existing}, readErr: errors.New("transient read")}
	creator.Writer = writer
	creator.Publisher = &recordingPublisher{}
	creator.Store = &graphStore{}
	ctx := context.Background()

	if _, err := creator.CreateBatch(ctx, inputs); err == nil {
		t.Fatal("first batch succeeded despite a failed read")
	}
	writer.readErr = nil
	got, err := creator.CreateBatch(ctx, inputs)
	if err != nil {
		t.Fatalf("retry after recovered read: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("results = %d, want 2", len(got))
	}
	if got[0].ID != string(ids[0]) || got[0].Path != existing.SourceRef || !got[0].Pushed {
		t.Fatalf("existing card result = %+v, want the source path", got[0])
	}
	if writer.writes != 1 {
		t.Fatalf("writes = %d, want only the new card", writer.writes)
	}
	if len(writer.cards) != 1 || writer.cards[0].ID != existing.ID ||
		writer.cards[0].SourceRef != existing.SourceRef {
		t.Fatalf("existing cards changed after retry: %+v", writer.cards)
	}
}
