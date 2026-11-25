package core

import (
	"errors"
	"fmt"
	"testing"
)

func TestCodeOf(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want ErrorCode
	}{
		{"nil", nil, ""},
		{"unwrapped", Errorf(CodeNotFound, "missing task"), CodeNotFound},
		{"wrapped once", fmt.Errorf("dispatch: %w", Errorf(CodeConflict, "already claimed")), CodeConflict},
		{"wrapped twice", fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", Errorf(CodeInvalidInput, "bad card"))), CodeInvalidInput},
		{"unrelated", errors.New("network down"), CodeUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CodeOf(tc.err); got != tc.want {
				t.Fatalf("CodeOf(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
