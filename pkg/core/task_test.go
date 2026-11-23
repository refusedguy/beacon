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
