package checksource

import (
	"context"
	"reflect"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

// staticSource is a fixed RequiredCheckSource used to stand in for any
// provider implementation.
type staticSource struct {
	names []string
	err   error
	calls int
}

func (s *staticSource) Names(ctx context.Context) ([]string, error) {
	s.calls++
	return s.names, s.err
}

func TestConfigNamesReturnsExplicitList(t *testing.T) {
	fallback := &staticSource{names: []string{"from-fallback"}}
	src := Config{Explicit: []string{"ci", "lint"}, Fallback: fallback}
	got, err := src.Names(context.Background())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if want := []string{"ci", "lint"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	if fallback.calls != 0 {
		t.Fatalf("explicit list called fallback %d times", fallback.calls)
	}
}

func TestConfigNamesReturnsACopy(t *testing.T) {
	explicit := []string{"ci"}
	src := Config{Explicit: explicit}
	got, err := src.Names(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got[0] = "changed"
	if explicit[0] != "ci" {
		t.Fatalf("Names exposed the backing slice: %v", explicit)
	}
}

func TestConfigNamesUsesFallbackWhenExplicitEmpty(t *testing.T) {
	fallback := &staticSource{names: []string{"verify", "test"}}
	src := Config{Fallback: fallback}
	got, err := src.Names(context.Background())
	if err != nil {
		t.Fatalf("Names: %v", err)
	}
	if want := []string{"verify", "test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Names = %v, want %v", got, want)
	}
	if fallback.calls != 1 {
		t.Fatalf("fallback called %d times, want 1", fallback.calls)
	}
}

func TestConfigNamesPropagatesFallbackError(t *testing.T) {
	wantErr := context.DeadlineExceeded
	src := Config{Fallback: &staticSource{err: wantErr}}
	if _, err := src.Names(context.Background()); err != wantErr {
		t.Fatalf("Names error = %v, want %v", err, wantErr)
	}
}

func TestConfigNamesWithNoNamesReportsNone(t *testing.T) {
	src := Config{}
	got, err := src.Names(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("Names = %v, want none", got)
	}
}

var _ core.RequiredCheckSource = Config{}
