package server

import (
	"errors"
	"fmt"
	"strings"
)

// Canonical names of the numbers the guard reconciles. Both sides of Source
// use these exact keys. Adding a key here tightens the contract: an adapter
// that forgets to supply it is reported as drift, not silently skipped.
const (
	// KeyMCPTools is the size of the MCP tool registry. Changing the number of
	// tools without editing the document breaks the guard (BS.52).
	KeyMCPTools = "mcp_tools"

	// KeyEventSchemaVersion is the schema version stamped on SSE envelopes.
	KeyEventSchemaVersion = "event_schema_version"

	// KeyReplayPageSize is the SSE replay page size from BS.17.
	KeyReplayPageSize = "replay_page_size"
)

// Keys is the ordered set of numbers Check reconciles. The fixed order keeps
// reports deterministic; map iteration order is never part of the output.
var Keys = []string{KeyMCPTools, KeyEventSchemaVersion, KeyReplayPageSize}

// ErrDocumentMissing tells Check that the architecture document is absent.
// Callers may treat this as "guard not applicable" rather than drift. It is a
// sentinel so errors.Is works through Check's wrapper.
var ErrDocumentMissing = errors.New("architecture document missing")

// Source is the port for the two sides of the reconciliation. DocumentNumbers
// holds the numbers claimed by the document; CodeNumbers holds the numbers
// measured from code and metrics. Neither method is called by a constructor,
// so Check stays a pure function of the values Source returns.
type Source interface {
	// DocumentNumbers returns the numbers parsed from the architecture
	// document, keyed by the Key* constants. It reports ErrDocumentMissing
	// when the document cannot be found.
	DocumentNumbers() (map[string]int, error)

	// CodeNumbers returns the same numbers measured from code and metrics.
	CodeNumbers() map[string]int
}

// Mismatch is one number that disagrees between the document and the code.
// Missing names which side omitted the key: "document", "code", or empty when
// both sides supplied a value and only the values differ.
type Mismatch struct {
	Key      string
	Document int
	Code     int
	Missing  string
}

// String renders one mismatch as a single log line.
func (m Mismatch) String() string {
	switch m.Missing {
	case "document":
		return fmt.Sprintf("docsguard: %s missing from document (code %d)", m.Key, m.Code)
	case "code":
		return fmt.Sprintf("docsguard: %s missing from code (document %d)", m.Key, m.Document)
	default:
		return fmt.Sprintf("docsguard: %s: document %d != code %d", m.Key, m.Document, m.Code)
	}
}

// Check compares the documented numbers to the measured ones. It returns the
// empty slice when they agree. A missing document is reported with
// ErrDocumentMissing (wrapped, but discoverable with errors.Is) and yields no
// mismatches. Any other source error is wrapped and returned.
//
// A key absent on either side counts as drift: a document that stopped stating
// a number is as stale as one that states the wrong number.
func Check(src Source) ([]Mismatch, error) {
	document, err := src.DocumentNumbers()
	if err != nil {
		if errors.Is(err, ErrDocumentMissing) {
			return nil, fmt.Errorf("docsguard: %w", ErrDocumentMissing)
		}
		return nil, fmt.Errorf("docsguard: document numbers: %w", err)
	}

	code := src.CodeNumbers()

	mismatches := make([]Mismatch, 0)
	for _, key := range Keys {
		docValue, inDoc := document[key]
		codeValue, inCode := code[key]
		switch {
		case !inDoc:
			mismatches = append(mismatches, Mismatch{Key: key, Code: codeValue, Missing: "document"})
		case !inCode:
			mismatches = append(mismatches, Mismatch{Key: key, Document: docValue, Missing: "code"})
		case docValue != codeValue:
			mismatches = append(mismatches, Mismatch{Key: key, Document: docValue, Code: codeValue})
		}
	}
	return mismatches, nil
}

// Report renders mismatches as one newline-separated block for a gate log. It
// returns an empty string when there is nothing to report.
func Report(mismatches []Mismatch) string {
	lines := make([]string, 0, len(mismatches))
	for _, m := range mismatches {
		lines = append(lines, m.String())
	}
	return strings.Join(lines, "\n")
}
