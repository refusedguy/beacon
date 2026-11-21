package gate

import (
	"errors"
	"testing"
)

func TestRunFixturesAllPass(t *testing.T) {
	cleaned := 0
	list := []FixtureCase{
		{Files: map[string]string{"a.go": "package a\n"}, Want: func(fs []Finding) bool { return len(fs) == 0 }},
		{Files: map[string]string{"b.go": "package b\n"}, Want: func(fs []Finding) bool { return len(fs) == 0 }},
	}
	analyse := func(root string) ([]Finding, error) { return nil, nil }
	cleanup := func(string) { cleaned++ }

	if !RunFixtures(list, analyse, cleanup) {
		t.Fatal("RunFixtures should pass when every Want holds")
	}
	if cleaned != len(list) {
		t.Fatalf("cleanup calls = %d, want %d", cleaned, len(list))
	}
}

func TestRunFixturesReportsFailures(t *testing.T) {
	cleaned := 0
	list := []FixtureCase{
		{Files: map[string]string{"a.go": "package a\n"}, Want: func(fs []Finding) bool { return true }},
		{Files: map[string]string{"b.go": "package b\n"}, Want: func(fs []Finding) bool { return false }},
	}
	analyse := func(root string) ([]Finding, error) { return nil, nil }
	cleanup := func(string) { cleaned++ }

	if RunFixtures(list, analyse, cleanup) {
		t.Fatal("RunFixtures should fail when a Want does not hold")
	}
	if cleaned != len(list) {
		t.Fatalf("cleanup calls = %d, want %d", cleaned, len(list))
	}
}

func TestRunFixturesAnalyseError(t *testing.T) {
	list := []FixtureCase{
		{Files: map[string]string{"a.go": "package a\n"}, Want: func(fs []Finding) bool { return true }},
	}
	analyse := func(root string) ([]Finding, error) { return nil, errors.New("boom") }
	cleaned := 0
	cleanup := func(string) { cleaned++ }

	if RunFixtures(list, analyse, cleanup) {
		t.Fatal("RunFixtures should fail when analyse returns an error")
	}
	if cleaned != 1 {
		t.Fatalf("cleanup calls = %d, want 1", cleaned)
	}
}
