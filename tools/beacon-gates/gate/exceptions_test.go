package gate

import (
	"fmt"
	"strings"
	"testing"
)

// validate is the exception-table policy check. It lives in the test file so
// the committed gates binary stays byte-identical to a rebuild from main. The
// embedded tables are compile-time data, so a self-test in CI is enough to keep
// an inconsistent table out of the tree. Moving this check into production
// would require a binary rebuild, which is outside the ATX-C07 OwnerFiles.
func (ex Exceptions) validate() error {
	for _, b := range ex.Baseline {
		if b.Rule == "" || b.Path == "" {
			return fmt.Errorf("baseline entry has no rule or path: %+v", b)
		}
		if b.Task == "" {
			return fmt.Errorf("baseline entry %s %s has no task", b.Rule, b.Path)
		}
	}
	for _, p := range ex.Permanent {
		if p.Rule == "" || p.Path == "" {
			return fmt.Errorf("permanent entry has no rule or path: %+v", p)
		}
		if p.Reason == "" {
			return fmt.Errorf("permanent entry %s %s has no reason", p.Rule, p.Path)
		}
	}
	if len(ex.Baseline) > ex.BaselineLimit {
		return fmt.Errorf("baseline has %d rows, limit is %d", len(ex.Baseline), ex.BaselineLimit)
	}
	permanent := make(map[string]bool, len(ex.Permanent))
	for _, p := range ex.Permanent {
		permanent[entryKey(p.Rule, p.Path)] = true
	}
	for _, b := range ex.Baseline {
		if permanent[entryKey(b.Rule, b.Path)] {
			return fmt.Errorf("baseline entry %s %s also appears in permanent", b.Rule, b.Path)
		}
	}
	return nil
}

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
			name: "baseline row without rule or path",
			ex: Exceptions{
				BaselineLimit: 1,
				Baseline:      []BaselineEntry{{Path: "a.go", Task: "T-1"}},
			},
			want: "no rule or path",
		},
		{
			name: "permanent row without reason",
			ex: Exceptions{
				Permanent: []PermanentEntry{{Rule: "R2", Path: "b.go"}},
			},
			want: "has no reason",
		},
		{
			name: "permanent row without rule or path",
			ex: Exceptions{
				Permanent: []PermanentEntry{{Reason: "standing"}},
			},
			want: "no rule or path",
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
	if err := ex.validate(); err != nil {
		t.Fatalf("embedded exception tables are inconsistent: %v", err)
	}
}
