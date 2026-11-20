package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// captureNotifier records notices instead of writing to a chat.
type captureNotifier struct {
	notices []ParentNotice
	err     error
}

func (n *captureNotifier) Notify(_ context.Context, notice ParentNotice) error {
	if n.err != nil {
		return n.err
	}
	n.notices = append(n.notices, notice)
	return nil
}

func newTree(t *testing.T) *Registry {
	t.Helper()
	r := new(Registry)
	if _, err := r.Root("root", "Root chat", NewContext(map[string]string{
		"branch":  "fix/bs69",
		"project": "beacon",
	})); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestChildBoundToParent(t *testing.T) {
	r := newTree(t)
	child, err := r.Spawn("root", "child", "Builder")
	if err != nil {
		t.Fatal(err)
	}
	if child.ParentID != "root" {
		t.Fatalf("child parent = %q, want root", child.ParentID)
	}
	if child.IsRoot() {
		t.Fatal("child must not be a root")
	}

	root, ok := r.Get("root")
	if !ok || !root.IsRoot() {
		t.Fatalf("root lookup = %v, %v, want a root thread", root, ok)
	}
	if got := r.Children("root"); !reflect.DeepEqual(got, []ThreadID{"child"}) {
		t.Fatalf("root children = %v, want [child]", got)
	}
	if got := r.Children("child"); len(got) != 0 {
		t.Fatalf("child children = %v, want none", got)
	}
	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2", r.Len())
	}

	if _, err := r.Spawn("missing", "orphan", "Orphan"); !errors.Is(err, ErrUnknownParent) {
		t.Fatalf("spawn under missing parent error = %v, want ErrUnknownParent", err)
	}
	if _, err := r.Spawn("root", "child", "Again"); !errors.Is(err, ErrDuplicateThread) {
		t.Fatalf("duplicate spawn error = %v, want ErrDuplicateThread", err)
	}
	if _, err := r.Spawn("root", "blank", "  "); !errors.Is(err, ErrInvalidThread) {
		t.Fatalf("blank name error = %v, want ErrInvalidThread", err)
	}
}

func TestLineageBuilds(t *testing.T) {
	r := newTree(t)
	if _, err := r.Spawn("root", "child", "Builder"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Spawn("child", "grandchild", "Reviewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Spawn("root", "sibling", "Watcher"); err != nil {
		t.Fatal(err)
	}

	want := []ThreadID{"root", "child", "grandchild"}
	if got := r.Lineage("grandchild"); !reflect.DeepEqual(got, want) {
		t.Fatalf("grandchild lineage = %v, want %v", got, want)
	}
	if got := r.Lineage("root"); !reflect.DeepEqual(got, []ThreadID{"root"}) {
		t.Fatalf("root lineage = %v, want [root]", got)
	}
	if got := r.Lineage("sibling"); !reflect.DeepEqual(got, []ThreadID{"root", "sibling"}) {
		t.Fatalf("sibling lineage = %v, want [root sibling]", got)
	}

	wantIDs := []ThreadID{"child", "grandchild", "root", "sibling"}
	if got := r.IDs(); !reflect.DeepEqual(got, wantIDs) {
		t.Fatalf("IDs = %v, want %v", got, wantIDs)
	}
}

func TestContextTransfer(t *testing.T) {
	source := map[string]string{"branch": "fix/bs69", "project": "beacon"}
	ctx := NewContext(source)
	source["branch"] = "changed"
	if got, _ := ctx.Get("branch"); got != "fix/bs69" {
		t.Fatalf("source mutation leaked into snapshot: %q", got)
	}

	r := new(Registry)
	if _, err := r.Root("root", "Root chat", ctx); err != nil {
		t.Fatal(err)
	}
	child, err := r.Spawn("root", "child", "Builder")
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := map[string]string{"branch": "fix/bs69", "project": "beacon"}
	if got := child.Context.Entries(); !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("child context = %v, want %v", got, wantEntries)
	}
	if got := r.ContextOf("child").Entries(); !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("ContextOf child = %v, want %v", got, wantEntries)
	}

	entries := child.Context.Entries()
	entries["project"] = "tampered"
	if got := r.ContextOf("child").Entries(); !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("Entries mutation leaked into stored context: %v", got)
	}

	special, err := r.SpawnWith("root", "special", "Specialist", NewContext(map[string]string{
		"branch": "fix/special",
		"role":   "review",
	}))
	if err != nil {
		t.Fatal(err)
	}
	wantSpecial := map[string]string{
		"branch":  "fix/special",
		"project": "beacon",
		"role":    "review",
	}
	if got := special.Context.Entries(); !reflect.DeepEqual(got, wantSpecial) {
		t.Fatalf("special context = %v, want %v", got, wantSpecial)
	}
	if got := r.ContextOf("root").Entries(); !reflect.DeepEqual(got, wantEntries) {
		t.Fatalf("child spawn changed root context: %v", got)
	}
}

func TestQuestionNotifiesParent(t *testing.T) {
	r := newTree(t)
	if _, err := r.Spawn("root", "child", "Builder"); err != nil {
		t.Fatal(err)
	}
	n := &captureNotifier{}

	notice, err := r.Ask(context.Background(), n, "child", "  which branch?  ")
	if err != nil {
		t.Fatal(err)
	}
	want := ParentNotice{
		ParentID:  "root",
		ChildID:   "child",
		ChildName: "Builder",
		Question:  "which branch?",
	}
	if notice != want {
		t.Fatalf("notice = %+v, want %+v", notice, want)
	}
	if !reflect.DeepEqual(n.notices, []ParentNotice{want}) {
		t.Fatalf("delivered notices = %v, want [%+v]", n.notices, want)
	}

	// A nil notifier builds the projection without delivering it.
	built, err := r.Ask(context.Background(), nil, "child", "second question")
	if err != nil {
		t.Fatal(err)
	}
	if built.Question != "second question" {
		t.Fatalf("built question = %q", built.Question)
	}
	if len(n.notices) != 1 {
		t.Fatalf("nil notifier delivered a notice: %v", n.notices)
	}

	// A delivery failure is returned with the notice so the caller can retry.
	boom := errors.New("delivery failed")
	n.err = boom
	failed, err := r.Ask(context.Background(), n, "child", "retry me")
	if !errors.Is(err, boom) {
		t.Fatalf("notifier error = %v, want boom", err)
	}
	if failed.Question != "retry me" {
		t.Fatalf("failed notice question = %q", failed.Question)
	}
}

func TestQuestionErrors(t *testing.T) {
	r := newTree(t)
	if _, err := r.Spawn("root", "child", "Builder"); err != nil {
		t.Fatal(err)
	}
	n := &captureNotifier{}

	if _, err := r.Ask(context.Background(), n, "missing", "why"); !errors.Is(err, ErrUnknownThread) {
		t.Fatalf("unknown child error = %v, want ErrUnknownThread", err)
	}
	if _, err := r.Ask(context.Background(), n, "child", "   "); !errors.Is(err, ErrEmptyQuestion) {
		t.Fatalf("empty question error = %v, want ErrEmptyQuestion", err)
	}
	if _, err := r.Ask(context.Background(), n, "root", "why"); !errors.Is(err, ErrNoParent) {
		t.Fatalf("root question error = %v, want ErrNoParent", err)
	}
	if len(n.notices) != 0 {
		t.Fatalf("failed Ask delivered a notice: %v", n.notices)
	}
}

func TestEmptyRegistry(t *testing.T) {
	var r Registry
	if r.Len() != 0 {
		t.Fatalf("Len = %d, want 0", r.Len())
	}
	if got := r.IDs(); len(got) != 0 {
		t.Fatalf("IDs = %v, want none", got)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get found a thread in an empty registry")
	}
	if got := r.Children("missing"); len(got) != 0 {
		t.Fatalf("Children = %v, want none", got)
	}
	if got := r.Lineage("missing"); len(got) != 0 {
		t.Fatalf("Lineage = %v, want none", got)
	}
	if got := r.ContextOf("missing"); got.Len() != 0 {
		t.Fatalf("ContextOf = %v, want empty", got.Entries())
	}
	n := &captureNotifier{}
	if _, err := r.Ask(context.Background(), n, "missing", "why"); !errors.Is(err, ErrUnknownThread) {
		t.Fatalf("Ask error = %v, want ErrUnknownThread", err)
	}
	if len(n.notices) != 0 {
		t.Fatalf("empty registry delivered a notice: %v", n.notices)
	}
}
