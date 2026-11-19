package domain

import (
	"strconv"
	"strings"
)

// maxRevision is the largest revision a uint64 can hold. Once current reaches
// it, the strictly increasing sequence cannot be extended without reusing or
// decreasing a value, so NextRevision refuses to go further.
const maxRevision = ^uint64(0)

// obligationKeyEscaper keeps the ':' separator unambiguous when a watch or
// subscriber identifier contains a separator of its own. Escaping backslash
// first makes the encoding reversible, hence injective for every string.
var obligationKeyEscaper = strings.NewReplacer("\\", "\\\\", ":", "\\:")

// NextRevision returns the revision that follows current. Revisions are
// strictly increasing and are never reused or wrapped. At maxRevision no
// larger value exists, so NextRevision panics instead of returning a value
// that breaks the monotonic, non-reusable contract.
func NextRevision(current uint64) uint64 {
	if current == maxRevision {
		panic("domain: revision overflow: no unused revision remains")
	}
	return current + 1
}

// ObligationKey builds the identity of an obligation from its watch, revision
// and subscriber. The result is deterministic: the same triple always yields
// the same key, no matter when or in what order it is computed. The encoding is
// also injective, so two different triples never share a key.
func ObligationKey(watchID string, revision uint64, subscriberID string) string {
	return obligationKeyEscaper.Replace(watchID) + ":" +
		strconv.FormatUint(revision, 10) + ":" +
		obligationKeyEscaper.Replace(subscriberID)
}
