package worktree

import (
	"strings"
	"testing"
)

func TestCheckResume(t *testing.T) {
	const (
		branch   = "fix/cd-02"
		worktree = "/repo/worktrees/cd-02"
		existing = "/repo/worktrees/other"
	)

	tests := []struct {
		name         string
		input        ResumeInput
		want         string
		wantInReason []string
	}{
		{
			name: "branch is behind without local commits and is not checked out elsewhere",
			input: ResumeInput{
				Branch:   branch,
				Worktree: worktree,
				Behind:   2,
			},
			want:         ResumeOpen,
			wantInReason: []string{branch, worktree},
		},
		{
			name: "branch is checked out in another worktree",
			input: ResumeInput{
				Branch:              branch,
				Worktree:            worktree,
				CheckedOutElsewhere: true,
				Ahead:               0,
				Behind:              2,
				ExistingWorktree:    existing,
			},
			want:         ResumeReuse,
			wantInReason: []string{existing},
		},
		{
			name: "branch diverged from upstream",
			input: ResumeInput{
				Branch:   branch,
				Worktree: worktree,
				Ahead:    2,
				Behind:   1,
			},
			want:         ResumeRefuse,
			wantInReason: []string{"локальные коммиты", "2", "1"},
		},
		{
			name: "branch has local commits but is not behind",
			input: ResumeInput{
				Branch:   branch,
				Worktree: worktree,
				Ahead:    3,
			},
			want:         ResumeOpen,
			wantInReason: []string{branch},
		},
		{
			name: "occupied branch without a known worktree path",
			input: ResumeInput{
				Branch:              branch,
				CheckedOutElsewhere: true,
			},
			want:         ResumeReuse,
			wantInReason: []string{branch, "existing worktree"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckResume(tt.input)
			if got.Decision != tt.want {
				t.Fatalf("decision = %q, want %q (reason %q)", got.Decision, tt.want, got.Reason)
			}
			for _, part := range tt.wantInReason {
				if !strings.Contains(got.Reason, part) {
					t.Errorf("reason %q does not mention %q", got.Reason, part)
				}
			}
		})
	}
}
