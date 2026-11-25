package core

import "testing"

func TestOverlap(t *testing.T) {
	cases := []struct {
		name string
		a, b Task
		want bool
	}{
		{
			name: "same file",
			a:    Task{ID: "A", Scope: []ScopePath{ScopeFile("internal/app/a.go")}},
			b:    Task{ID: "B", Scope: []ScopePath{ScopeFile("internal/app/a.go")}},
			want: true,
		},
		{
			name: "different files",
			a:    Task{ID: "A", Scope: []ScopePath{ScopeFile("internal/app/a.go")}},
			b:    Task{ID: "B", Scope: []ScopePath{ScopeFile("internal/app/b.go")}},
		},
		{
			name: "directory covers file",
			a:    Task{ID: "A", Scope: []ScopePath{ScopeDir("internal/app")}},
			b:    Task{ID: "B", Scope: []ScopePath{ScopeFile("internal/app/b.go")}},
			want: true,
		},
		{
			name: "dependency",
			a:    Task{ID: "A"},
			b:    Task{ID: "B", DependsOn: []TaskID{"A"}},
			want: true,
		},
		{
			name: "same id",
			a:    Task{ID: "A"},
			b:    Task{ID: "A"},
			want: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.a.Overlaps(tc.b); got != tc.want {
				t.Fatalf("Overlaps = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestScopeKindAndValue(t *testing.T) {
	cases := []struct {
		scope ScopePath
		kind  byte
		value string
	}{
		{ScopeFile("src/a.go"), 'f', "src/a.go"},
		{ScopeDir("src"), 'd', "src"},
		{ScopePattern("src/**/*.go"), 'g', "src/**/*.go"},
		{ScopeAll(), 'a', "*"},
	}
	for _, tc := range cases {
		t.Run(string(tc.scope), func(t *testing.T) {
			if got := tc.scope.Kind(); got != tc.kind {
				t.Errorf("Kind = %q, want %q", got, tc.kind)
			}
			if got := Kind(tc.scope); got != tc.kind {
				t.Errorf("Kind(scope) = %q, want %q", got, tc.kind)
			}
			if got := tc.scope.Value(); got != tc.value {
				t.Errorf("Value = %q, want %q", got, tc.value)
			}
			if got := Value(tc.scope); got != tc.value {
				t.Errorf("Value(scope) = %q, want %q", got, tc.value)
			}
		})
	}
}

func TestScopeOverlapAuthority(t *testing.T) {
	dir := ScopeDir("src")
	glob := ScopePattern("src/**/*.go")
	if !dir.Covers(glob) {
		t.Fatal("scope src must cover src/**/*.go")
	}
	a := Task{ID: "A", Scope: []ScopePath{dir}}
	b := Task{ID: "B", Scope: []ScopePath{glob}}
	if !OverlapByPaths(a, b) {
		t.Fatal("scope src must overlap src/**/*.go")
	}

	other := ScopePattern("src/lib/*.go")
	if !OverlapByPaths(Task{ID: "C", Scope: []ScopePath{glob}}, Task{ID: "D", Scope: []ScopePath{other}}) {
		t.Fatal("glob patterns with a common prefix must overlap")
	}

	unrelated := ScopeFile("docs/readme.md")
	if OverlapByPaths(a, Task{ID: "E", Scope: []ScopePath{unrelated}}) {
		t.Fatal("scope src must not overlap docs/readme.md")
	}
}

func TestTaskTreeUsesParentField(t *testing.T) {
	tasks := []Task{
		{ID: "T-EPIC", Kind: TaskKindEpic},
		{ID: "child-a", Parent: "T-EPIC"},
		{ID: "child-b", Parent: "T-EPIC"},
		{ID: "standalone"},
	}
	tree := NewTaskTree(tasks)

	children := tree.Children("T-EPIC")
	if len(children) != 2 || children[0].ID != "child-a" || children[1].ID != "child-b" {
		t.Fatalf("children = %+v", children)
	}
	parent, ok := tree.Parent("child-b")
	if !ok || parent.ID != "T-EPIC" {
		t.Fatalf("parent = %+v, ok = %v", parent, ok)
	}
	if _, ok := tree.Parent("standalone"); ok {
		t.Fatal("standalone task must have no parent")
	}
	if got := tree.Children("standalone"); len(got) != 0 {
		t.Fatalf("standalone children = %+v", got)
	}
	if _, ok := tree.Task("missing"); ok {
		t.Fatal("missing task must not be found")
	}
}

func TestSetParentStampsEveryChild(t *testing.T) {
	children := SetParent([]Task{{ID: "one"}, {ID: "two"}}, "T-EPIC")
	for _, child := range children {
		if child.Parent != "T-EPIC" {
			t.Fatalf("child %q parent = %q", child.ID, child.Parent)
		}
	}
}
