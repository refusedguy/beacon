package projects

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSource is the mock Source port. It returns a fixed project list or a
// fixed error and records how many times List was called.
type fakeSource struct {
	projects []Project
	err      error
	calls    int
}

func (f *fakeSource) List(_ context.Context) ([]Project, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.projects, nil
}

func mustResolver(t *testing.T, source Source) *Resolver {
	t.Helper()
	r, err := New(source)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return r
}

func TestResolveFoundByKey(t *testing.T) {
	source := &fakeSource{projects: []Project{
		{ID: "id-a", Key: "alpha", Path: "/work/alpha"},
		{ID: "id-b", Key: "beta", Path: "/work/beta"},
	}}
	r := mustResolver(t, source)

	got, err := r.Resolve(context.Background(), "beta")
	if err != nil {
		t.Fatalf("Resolve(beta) error = %v", err)
	}
	if got.ID != "id-b" || got.Key != "beta" {
		t.Fatalf("Resolve(beta) = %#v, want id-b/beta", got)
	}
	if source.calls != 1 {
		t.Fatalf("port calls = %d, want 1", source.calls)
	}
}

func TestResolveFoundByPath(t *testing.T) {
	projects := []Project{{ID: "id-a", Key: "alpha", Path: "/work/alpha"}}
	tests := []struct {
		name string
		key  string
	}{
		{"exact path", "/work/alpha"},
		{"trailing slash", "/work/alpha/"},
		{"redundant segments", "/work/./tmp/../alpha"},
		{"surrounding space", "  /work/alpha  "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := mustResolver(t, &fakeSource{projects: projects})
			got, err := r.Resolve(context.Background(), tt.key)
			if err != nil {
				t.Fatalf("Resolve(%q) error = %v", tt.key, err)
			}
			if got.ID != "id-a" {
				t.Fatalf("Resolve(%q) ID = %q, want id-a", tt.key, got.ID)
			}
		})
	}
}

func TestResolveNotFound(t *testing.T) {
	source := &fakeSource{projects: []Project{
		{ID: "id-a", Key: "alpha", Path: "/work/alpha"},
	}}
	r := mustResolver(t, source)

	if _, err := r.Resolve(context.Background(), "gamma"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve(gamma) error = %v, want ErrNotFound", err)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	source := &fakeSource{projects: []Project{
		{ID: "id-b", Key: "shared", Path: "/work/b"},
		{ID: "id-a", Key: "shared", Path: "/work/a"},
	}}
	r := mustResolver(t, source)

	if _, err := r.Resolve(context.Background(), "shared"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Resolve(shared) error = %v, want ErrAmbiguous", err)
	}
}

func TestResolveAmbiguousAcrossKeyAndPath(t *testing.T) {
	source := &fakeSource{projects: []Project{
		{ID: "id-a", Key: "/work/target", Path: "/work/a"},
		{ID: "id-b", Key: "beta", Path: "/work/target"},
	}}
	r := mustResolver(t, source)

	if _, err := r.Resolve(context.Background(), "/work/target"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Resolve(/work/target) error = %v, want ErrAmbiguous", err)
	}
}

func TestResolveOneProjectViaKeyAndPathIsNotAmbiguous(t *testing.T) {
	source := &fakeSource{projects: []Project{
		{ID: "id-a", Key: "/work/alpha", Path: "/work/alpha"},
	}}
	r := mustResolver(t, source)

	// The key matches both project.Key and the canonical project.Path. The
	// dedup by ID must still yield one project, not an ambiguity.
	got, err := r.Resolve(context.Background(), "/work/alpha")
	if err != nil {
		t.Fatalf("Resolve(/work/alpha) error = %v", err)
	}
	if got.ID != "id-a" {
		t.Fatalf("Resolve(/work/alpha) ID = %q, want id-a", got.ID)
	}
}

func TestResolveRejectsEmptyKey(t *testing.T) {
	source := &fakeSource{projects: []Project{
		{ID: "id-a", Key: "alpha", Path: "/work/alpha"},
	}}
	r := mustResolver(t, source)

	for _, key := range []string{"", "   ", "\t\n"} {
		if _, err := r.Resolve(context.Background(), key); !errors.Is(err, ErrEmptyKey) {
			t.Fatalf("Resolve(%q) error = %v, want ErrEmptyKey", key, err)
		}
	}
	if source.calls != 0 {
		t.Fatalf("port calls = %d, want 0 for empty keys", source.calls)
	}
}

func TestResolvePortError(t *testing.T) {
	boom := errors.New("port offline")
	source := &fakeSource{err: boom}
	r := mustResolver(t, source)

	_, err := r.Resolve(context.Background(), "alpha")
	if !errors.Is(err, ErrPort) {
		t.Fatalf("Resolve(alpha) error = %v, want ErrPort", err)
	}
	if !strings.Contains(err.Error(), boom.Error()) {
		t.Fatalf("Resolve(alpha) error = %v, want port cause in message", err)
	}
}

func TestNewRejectsNilSource(t *testing.T) {
	if _, err := New(nil); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("New(nil) error = %v, want ErrMissingPort", err)
	}
}

func TestZeroResolverReportsMissingPort(t *testing.T) {
	var r Resolver
	if _, err := r.Resolve(context.Background(), "alpha"); !errors.Is(err, ErrMissingPort) {
		t.Fatalf("Resolve on zero Resolver error = %v, want ErrMissingPort", err)
	}
}

func TestResolveAcceptsNilContext(t *testing.T) {
	r := mustResolver(t, &fakeSource{projects: []Project{
		{ID: "id-a", Key: "alpha", Path: "/work/alpha"},
	}})
	if _, err := r.Resolve(nil, "alpha"); err != nil {
		t.Fatalf("Resolve(nil, alpha) error = %v", err)
	}
}
