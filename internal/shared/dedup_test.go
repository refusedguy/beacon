package shared

import (
	"reflect"
	"testing"
)

func TestDistinctByKeepsFirstOccurrence(t *testing.T) {
	in := []string{"b", "a", "b", "c", "a", "d"}
	got := DistinctBy(in, func(s string) string { return s })
	want := []string{"b", "a", "c", "d"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DistinctBy() = %v, want %v", got, want)
	}
}

func TestDistinctByUsesKeyFunction(t *testing.T) {
	type row struct {
		id   int
		name string
	}
	in := []row{{1, "first"}, {2, "first"}, {3, "second"}}
	got := DistinctBy(in, func(r row) string { return r.name })
	want := []row{{1, "first"}, {3, "second"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DistinctBy() = %v, want %v", got, want)
	}
}

func TestDistinctByEmptyInputIsNonNil(t *testing.T) {
	got := DistinctBy([]string{}, func(s string) string { return s })
	if got == nil {
		t.Fatal("DistinctBy() returned nil for empty input")
	}
	if len(got) != 0 {
		t.Fatalf("DistinctBy() len = %d, want 0", len(got))
	}
}

func TestContains(t *testing.T) {
	in := []int{2, 4, 6}
	if !Contains(in, 4) {
		t.Fatal("Contains() = false for present value")
	}
	if Contains(in, 5) {
		t.Fatal("Contains() = true for absent value")
	}
	if Contains([]int{}, 4) {
		t.Fatal("Contains() = true for empty input")
	}
}

func TestFilterKeepsOrder(t *testing.T) {
	got := Filter([]int{1, 2, 3, 4, 5}, func(n int) bool { return n%2 == 0 })
	want := []int{2, 4}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Filter() = %v, want %v", got, want)
	}
}

func TestFilterEmptyInputIsNonNil(t *testing.T) {
	got := Filter([]string{}, func(s string) bool { return true })
	if got == nil {
		t.Fatal("Filter() returned nil for empty input")
	}
	if len(got) != 0 {
		t.Fatalf("Filter() len = %d, want 0", len(got))
	}
}
