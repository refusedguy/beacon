// Package shared holds common helpers. Redaction rules live in pkg/core/redact.
package shared

import "github.com/refusedguy/beacon/pkg/core/redact"

const redacted = redact.Marker

// Redact removes secrets from s using the shared rule list.
func Redact(s string) string { return redact.Redact(s) }
