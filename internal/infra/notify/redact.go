package notify

import "github.com/refusedguy/beacon/internal/infra/shared"

// Redact delegates to the single implementation in shared.
func Redact(s string) string { return shared.Redact(s) }
