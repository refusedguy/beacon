package tasksource

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	maxScopePaths = 4
	maxHotFiles   = 2
)

// hotFiles names every path claimed by two or more non-terminal cards in the
// corpus this guard landed on. A contested path outside this list means a new
// card introduced overlap, so TestCardScopeGuardCorpus fails until someone
// updates the list on purpose. The list is the CI counterpart of the hot-file
// singleton rule in AGENTS.md.
var hotFiles = []string{
	".github/workflows/ci.yml",
	"cmd/beacon/main.go",
	"cmd/beacon/serve.go",
	"internal/app/dispatcher.go",
	"internal/app/reconcile_observation.go",
	"internal/app/reconciler.go",
	"internal/bench/run_watcher_bench.js",
	"internal/infra/mcp/server.go",
	"internal/infra/sqlite/store.go",
}

// legacyCards names the non-terminal cards whose Scope predates this guard.
// They keep their scopes until their owners split them. Every other card must
// satisfy the four rules in scopeGuardViolations.
var legacyCards = []string{
	"T-1bde70ab",
	"T-3167b3c4",
	"T-56b0a7dd",
	"T-5dbcac20",
	"T-5df65c18",
	"T-82a6985d",
	"T-a1000001",
	"T-a1000002",
	"T-a1000007",
	"T-a1000009",
	"T-b1000002",
	"T-b1000005",
	"T-b1000006",
	"T-b1000008",
	"T-c1000001",
	"T-c1000002",
	"T-c1000003",
	"T-c1000004",
	"T-c1000005",
	"T-c1000012",
	"T-c1000016",
	"T-c1000019",
	"T-d1000001",
	"T-d1000004",
	"T-d1000005",
	"T-d1000007",
	"T-e5cf1ace",
	"T-ef17cf6f",
}

type scopeCard struct {
	id    string
	scope []string
}

// readScopeCards decodes non-terminal cards under dir. Done and dropped cards
// no longer hold a scope, so they cannot starve dispatch and are skipped.
func readScopeCards(t *testing.T, dir string) []scopeCard {
	t.Helper()
	paths, err := (CardStore{Dir: dir}).cards()
	if err != nil {
		t.Fatalf("cards(%s): %v", dir, err)
	}
	var out []scopeCard
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		fm, _, hasFM := splitFrontmatterLines(cardLines(string(b)))
		if !hasFM {
			continue
		}
		var schema cardSchema
		if err := yaml.Unmarshal([]byte(fm), &schema); err != nil {
			t.Fatalf("%s: malformed frontmatter: %v", path, err)
		}
		switch strings.ToLower(strings.TrimSpace(schema.Status)) {
		case "done", "dropped", "готово", "отменено":
			continue
		}
		out = append(out, scopeCard{
			id:    strings.TrimSpace(schema.ID),
			scope: schema.combinedScope(),
		})
	}
	return out
}

func stringSet(values []string) map[string]bool {
	out := make(map[string]bool, len(values))
	for _, v := range values {
		out[v] = true
	}
	return out
}

func ownersByPath(cards []scopeCard) map[string][]string {
	out := make(map[string][]string)
	for _, c := range cards {
		for _, p := range c.scope {
			out[p] = append(out[p], c.id)
		}
	}
	return out
}

// scopeGuardViolations applies the four card-scope rules to one card against
// the whole non-terminal corpus. A contested path is one named by two or more
// non-terminal cards; it fails for each owner rather than for the pair alone.
func scopeGuardViolations(c scopeCard, owners map[string][]string, hot map[string]bool) []string {
	var out []string
	if len(c.scope) == 0 {
		out = append(out, "empty Scope")
	}
	if len(c.scope) > maxScopePaths {
		out = append(out, fmt.Sprintf("%d scope paths, want at most %d", len(c.scope), maxScopePaths))
	}
	hotCount := 0
	for _, p := range c.scope {
		if n := len(owners[p]); n > 1 {
			out = append(out, fmt.Sprintf("path %q claimed by %d non-terminal cards", p, n))
		}
		if hot[p] {
			hotCount++
		}
	}
	if hotCount > maxHotFiles {
		out = append(out, fmt.Sprintf("%d hot files, want at most %d", hotCount, maxHotFiles))
	}
	return out
}

// TestScopeGuardRules pins each rule with a small corpus. A new card with five
// paths or a shared hot file must produce a violation here before it can reach
// dispatch and starve other cards.
func TestScopeGuardRules(t *testing.T) {
	hot := stringSet([]string{"hot1.go", "hot2.go", "hot3.go"})
	tests := []struct {
		name    string
		cards   []scopeCard
		wantSub string
	}{
		{
			name:  "clean card",
			cards: []scopeCard{{"T-A", []string{"a.go", "a_test.go"}}, {"T-B", []string{"b.go"}}},
		},
		{
			name:  "at the path limit",
			cards: []scopeCard{{"T-A", []string{"a.go", "b.go", "c.go", "d.go"}}},
		},
		{
			name:  "at the hot file limit",
			cards: []scopeCard{{"T-A", []string{"hot1.go", "hot2.go"}}},
		},
		{
			name:    "empty scope",
			cards:   []scopeCard{{"T-A", nil}},
			wantSub: "empty Scope",
		},
		{
			name:    "five paths",
			cards:   []scopeCard{{"T-A", []string{"a.go", "b.go", "c.go", "d.go", "e.go"}}},
			wantSub: "5 scope paths",
		},
		{
			name:    "shared path",
			cards:   []scopeCard{{"T-A", []string{"shared.go"}}, {"T-B", []string{"shared.go"}}},
			wantSub: "claimed by 2 non-terminal cards",
		},
		{
			name:    "three hot files",
			cards:   []scopeCard{{"T-A", []string{"hot1.go", "hot2.go", "hot3.go"}}},
			wantSub: "3 hot files",
		},
		{
			name:    "shared hot file",
			cards:   []scopeCard{{"T-A", []string{"hot1.go"}}, {"T-B", []string{"hot1.go"}}},
			wantSub: "claimed by 2 non-terminal cards",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			owners := ownersByPath(tc.cards)
			got := scopeGuardViolations(tc.cards[0], owners, hot)
			if tc.wantSub == "" {
				if len(got) != 0 {
					t.Fatalf("violations = %q, want none", got)
				}
				return
			}
			for _, v := range got {
				if strings.Contains(v, tc.wantSub) {
					return
				}
			}
			t.Fatalf("violations = %q, want one containing %q", got, tc.wantSub)
		})
	}
}

// TestCardScopeGuardCorpus walks docs/tasks and enforces the rules on every
// non-terminal card that is not on the legacy list. It first checks that the
// explicit hot list matches the contested paths in the corpus, so a new owner
// of a contested path or a new contested path fails the test.
func TestCardScopeGuardCorpus(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "docs", "tasks")
	cards := readScopeCards(t, dir)
	if len(cards) == 0 {
		t.Fatalf("no non-terminal cards under %s", dir)
	}
	owners := ownersByPath(cards)

	var derived []string
	for p, ids := range owners {
		if len(ids) > 1 {
			derived = append(derived, p)
		}
	}
	sort.Strings(derived)
	listed := append([]string(nil), hotFiles...)
	sort.Strings(listed)
	if strings.Join(derived, "\n") != strings.Join(listed, "\n") {
		t.Fatalf("hot list and corpus disagree:\nlisted:  %v\ncorpus:  %v", listed, derived)
	}

	legacy := stringSet(legacyCards)
	hot := stringSet(hotFiles)
	for _, c := range cards {
		if legacy[c.id] {
			continue
		}
		for _, v := range scopeGuardViolations(c, owners, hot) {
			t.Errorf("%s: %s", c.id, v)
		}
	}
}
