package gate

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// reachabilityJSON is the exception table for the reachability gate. It is
// separate from baseline.json and permanent.json because those tables are
// applied by every gate; reachability rules would appear as stale rows there.
//
//go:embed config/reachability.json
var reachabilityJSON []byte

// LoadReachabilityExceptions reads and validates the reachability exception
// table. The returned Exceptions is passed to Apply like any other table.
func LoadReachabilityExceptions() (Exceptions, error) {
	var perm struct {
		Permanent []PermanentEntry `json:"permanent"`
	}
	if err := json.Unmarshal(reachabilityJSON, &perm); err != nil {
		return Exceptions{}, fmt.Errorf("parse reachability exceptions: %w", err)
	}
	for _, p := range perm.Permanent {
		if p.Reason == "" {
			return Exceptions{}, fmt.Errorf("reachability exception %s %s has no reason", p.Rule, p.Path)
		}
	}
	return Exceptions{Permanent: perm.Permanent}, nil
}
