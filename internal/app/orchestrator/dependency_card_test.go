package orchestrator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeDependencyCard(t *testing.T, dir, ref, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ref+".md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCardDoneSeparatesThreeStates(t *testing.T) {
	dir := t.TempDir()
	writeDependencyCard(t, dir, "T3.100", "# T3.100\nСтатус: done\n")
	writeDependencyCard(t, dir, "T3.101", "# T3.101\nСтатус: todo\n")
	r := &FileDependencyResolver{DocsDir: dir}

	for _, tc := range []struct {
		ref  string
		want DependencyCardStatus
	}{
		{"T3.100", DependencyCardDone},
		{"T3.101", DependencyCardNotDone},
		{"T3.999", DependencyCardNotFound},
	} {
		got, err := r.cardDone(tc.ref)
		if err != nil {
			t.Fatalf("cardDone(%q): %v", tc.ref, err)
		}
		if got != tc.want {
			t.Fatalf("cardDone(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
}

func TestSatisfiedMissingCardReturnsTypedError(t *testing.T) {
	r := &FileDependencyResolver{DocsDir: t.TempDir()}
	ok, err := r.Satisfied(context.Background(), "T3.999")
	if ok {
		t.Fatal("a missing card must not satisfy the dependency")
	}
	var missing *DependencyCardNotFoundError
	if !errors.As(err, &missing) || missing.Ref != "T3.999" {
		t.Fatalf("error = %v, want *DependencyCardNotFoundError for T3.999", err)
	}
}

func TestDecideDependencyCardStates(t *testing.T) {
	dir := t.TempDir()
	writeDependencyCard(t, dir, "T3.100", "# T3.100\nСтатус: done\n")
	writeDependencyCard(t, dir, "T3.101", "# T3.101\nСтатус: todo\n")
	r := &FileDependencyResolver{DocsDir: dir}

	tests := []struct {
		name       string
		refs       []string
		wantKind   DecisionKind
		wantReason string
		wantRefs   []string
	}{
		{"done", []string{"T3.100"}, DecisionMerge, VerdictReady, nil},
		{"not_done", []string{"T3.101"}, DecisionWaitDependency, "", []string{"T3.101"}},
		{"not_found", []string{"T3.999"}, DecisionDependencyNotFound, ReasonDependencyCardNotFound, []string{"T3.999"}},
		{
			name:       "missing_ref_wins_over_wait",
			refs:       []string{"T3.101", "T3.999"},
			wantKind:   DecisionDependencyNotFound,
			wantReason: ReasonDependencyCardNotFound,
			wantRefs:   []string{"T3.999"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := Watch{
				ID: "w1", PR: 42, Revision: 7, Verdict: VerdictReady,
				Policy: &AutoMergePolicy{WaitFor: tt.refs},
			}
			got := Decide(w, r, CIState{})
			if got.Kind != tt.wantKind || got.Reason != tt.wantReason {
				t.Fatalf("kind/reason = %q/%q, want %q/%q", got.Kind, got.Reason, tt.wantKind, tt.wantReason)
			}
			if !reflect.DeepEqual(got.Refs, tt.wantRefs) {
				t.Fatalf("refs = %q, want %q", got.Refs, tt.wantRefs)
			}
		})
	}
}

func TestSatisfiedRoutesEveryCardIDToCardStore(t *testing.T) {
	ids := []string{"T3.100", "ARDH-08", "ATX-R01", "BS.1", "S1.001"}
	dir := t.TempDir()
	for _, id := range ids {
		writeDependencyCard(t, dir, id, "# "+id+"\nСтатус: done\n")
	}
	runner := &fakeRunner{}
	r := &FileDependencyResolver{DocsDir: dir, Runner: runner}

	for _, id := range ids {
		ok, err := r.Satisfied(context.Background(), id)
		if err != nil || !ok {
			t.Fatalf("Satisfied(%q) = %t, %v, want true", id, ok, err)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("card refs must not reach git: %+v", runner.calls)
	}
}

func TestSatisfiedRejectsInvalidCardLikeRef(t *testing.T) {
	runner := &fakeRunner{}
	r := &FileDependencyResolver{DocsDir: t.TempDir(), Runner: runner}

	for _, ref := range []string{"T3.24x", "T3..4", "T3.4.5", "T3.abc"} {
		ok, err := r.Satisfied(context.Background(), ref)
		if ok || err == nil {
			t.Fatalf("Satisfied(%q) = %t, %v, want explicit rejection", ref, ok, err)
		}
	}
	if len(runner.calls) != 0 {
		t.Fatalf("invalid card refs must not reach git: %+v", runner.calls)
	}
}

func TestSatisfiedKeepsGitPaths(t *testing.T) {
	const present = "internal/app/orchestrator/store.go"
	runner := &fakeRunner{
		out: map[string][]byte{
			runnerKey("git", "show", "main:"+present): []byte("package orchestrator"),
		},
	}
	r := &FileDependencyResolver{DocsDir: t.TempDir(), Runner: runner}

	ok, err := r.Satisfied(context.Background(), present)
	if err != nil || !ok {
		t.Fatalf("Satisfied(%q) = %t, %v, want true", present, ok, err)
	}
	wantCall := runnerCall{"git", []string{"show", "main:" + present}}
	if len(runner.calls) != 1 || !reflect.DeepEqual(runner.calls[0], wantCall) {
		t.Fatalf("git calls = %+v, want %+v", runner.calls, wantCall)
	}
}
