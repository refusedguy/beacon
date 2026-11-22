package gate

import (
	"strings"
	"testing"
)

// TestExceptionsConsistencySelfTest plants each inconsistency the exception
// tables must reject. The planted rows stay in the test: the embedded configs
// are compile-time data and cannot be mutated from Go.
func TestExceptionsConsistencySelfTest(t *testing.T) {
	cases := []struct {
		name string
		ex   Exceptions
		want string // substring of the expected error; empty means accept
	}{
		{
			name: "consistent tables",
			ex: Exceptions{
				BaselineLimit: 1,
				Baseline:      []BaselineEntry{{Rule: "R1", Path: "a.go", Task: "T-1"}},
				Permanent:     []PermanentEntry{{Rule: "R2", Path: "b.go", Reason: "standing"}},
			},
		},
		{
			name: "limit below row count",
			ex: Exceptions{
				BaselineLimit: 0,
				Baseline:      []BaselineEntry{{Rule: "R1", Path: "a.go", Task: "T-1"}},
			},
			want: "limit",
		},
		{
			name: "baseline and permanent overlap",
			ex: Exceptions{
				BaselineLimit: 1,
				Baseline:      []BaselineEntry{{Rule: "R1", Path: "a.go", Task: "T-1"}},
				Permanent:     []PermanentEntry{{Rule: "R1", Path: "a.go", Reason: "standing"}},
			},
			want: "also appears in permanent",
		},
		{
			name: "baseline row without task",
			ex: Exceptions{
				BaselineLimit: 1,
				Baseline:      []BaselineEntry{{Rule: "R1", Path: "a.go"}},
			},
			want: "has no task",
		},
		{
			name: "permanent row without reason",
			ex: Exceptions{
				Permanent: []PermanentEntry{{Rule: "R2", Path: "b.go"}},
			},
			want: "has no reason",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.ex.validate()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("validate rejected a consistent table: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validate accepted a table with %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestLoadExceptionsUsesConsistencyCheck(t *testing.T) {
	ex, err := LoadExceptions()
	if err != nil {
		t.Fatalf("embedded exception tables are inconsistent: %v", err)
	}
	if len(ex.Baseline) > ex.BaselineLimit {
		t.Fatalf("embedded baseline has %d rows, limit is %d", len(ex.Baseline), ex.BaselineLimit)
	}
}
