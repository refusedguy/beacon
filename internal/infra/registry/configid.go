// Package registry builds identifiers for the project agent registry.
// The configuration ID follows the OpenRig format: a sorted list of
// id=runtime pairs joined by commas, with no spaces and no preset alias.
package registry

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// Member is one roster entry that contributes an id=runtime pair to a
// configuration ID. Alias is a preset label written beside the ID in the
// author file. It is kept on the input so callers can pass a whole roster
// entry, but it is never emitted into the ID.
type Member struct {
	ID      string
	Runtime string
	Alias   string
}

type idPair struct {
	id      string
	runtime string
}

// ConfigurationID builds the canonical configuration ID from roster members.
// Pairs are sorted by id in byte order and joined with commas. Whitespace in
// an id or a runtime is rejected: the format has no room for it, and a padded
// value would make two equal configurations produce different IDs.
func ConfigurationID(members []Member) (string, error) {
	pairs := make([]idPair, 0, len(members))
	seen := make(map[string]bool, len(members))
	for _, m := range members {
		if err := validatePart("id", m.ID, m.ID); err != nil {
			return "", err
		}
		if err := validatePart("runtime", m.Runtime, m.ID); err != nil {
			return "", err
		}
		if seen[m.ID] {
			return "", fmt.Errorf("registry: configuration id: duplicate id %q", m.ID)
		}
		seen[m.ID] = true
		pairs = append(pairs, idPair{id: m.ID, runtime: m.Runtime})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].id < pairs[j].id })

	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, p.id+"="+p.runtime)
	}
	return strings.Join(parts, ","), nil
}

// validatePart rejects values that cannot appear in a canonical configuration
// ID. The field names the rejected part and the member names where it came
// from, so an author can fix the roster entry rather than guess.
func validatePart(field, value, memberID string) error {
	if value == "" {
		return fmt.Errorf("registry: configuration id: %s of member %q is empty", field, memberID)
	}
	if i := strings.IndexFunc(value, unicode.IsSpace); i >= 0 {
		return fmt.Errorf("registry: configuration id: %s %q of member %q contains whitespace at byte %d", field, value, memberID, i)
	}
	if strings.ContainsAny(value, ",=") {
		return fmt.Errorf("registry: configuration id: %s %q of member %q contains a separator", field, value, memberID)
	}
	return nil
}
