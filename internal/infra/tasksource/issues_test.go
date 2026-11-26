package tasksource

import (
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

func TestIssueNumberWithPrefix(t *testing.T) {
	cases := []struct {
		name   string
		id     core.TaskID
		prefix string
		want   int
	}{
		{"github", "GH-42", "GH-", 42},
		{"gitlab", "GL-42", "GL-", 42},
		{"no prefix", "42", "GH-", 42},
		{"single digit", "GL-7", "GL-", 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := issueNumber(tc.id, tc.prefix)
			if err != nil {
				t.Fatalf("issueNumber(%q, %q) error = %v", tc.id, tc.prefix, err)
			}
			if got != tc.want {
				t.Fatalf("issueNumber(%q, %q) = %d, want %d", tc.id, tc.prefix, got, tc.want)
			}
		})
	}
}

func TestIssueNumberInvalid(t *testing.T) {
	cases := []struct {
		name   string
		id     core.TaskID
		prefix string
	}{
		{"non numeric", "bad", "GH-"},
		{"wrong prefix", "GL-9", "GH-"},
		{"empty", "", "GL-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := issueNumber(tc.id, tc.prefix)
			if core.CodeOf(err) != core.CodeInvalidInput {
				t.Fatalf("issueNumber(%q, %q) error code = %v, want CodeInvalidInput", tc.id, tc.prefix, core.CodeOf(err))
			}
			if got != 0 {
				t.Fatalf("issueNumber(%q, %q) = %d, want 0", tc.id, tc.prefix, got)
			}
		})
	}
}
