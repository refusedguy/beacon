package domain

import (
	"errors"
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

// StartNamespace scopes an idempotency_key that identifies a task start
// (ADR 0031 §5). The namespace keeps this key separate from the
// clientRequestId of ADR 0028: they are different keys for different
// operations and never share a storage slot, even when a caller supplies the
// same raw string for both.
const StartNamespace = "task-start"

// CodeRejectDuplicate is the conflict returned when a task start repeats an
// idempotency_key that already produced a task (ADR 0031 §5). It mirrors the
// RejectDuplicate policy of durable-mcp: a repeat is rejected as a duplicate
// and the original task is returned instead of a new one being created.
const CodeRejectDuplicate ErrorCode = "REJECT_DUPLICATE"

// NewRejectDuplicate builds the duplicate-start conflict. The task id names
// the one the first start created, so callers can return it with the conflict.
// The status matches other 409 conflicts such as CodeHeadSHAChanged.
func NewRejectDuplicate(key, taskID string) *AppError {
	return &AppError{
		Code:    CodeRejectDuplicate,
		Message: "duplicate idempotency key " + strconv.Quote(key) + ": task " + taskID + " already started",
		Status:  409,
	}
}

// IsRejectDuplicate reports whether err is the duplicate-start conflict.
func IsRejectDuplicate(err error) bool {
	var app *AppError
	if !errors.As(err, &app) {
		return false
	}
	return app.Code == CodeRejectDuplicate
}

// IdempotencyKey builds a deterministic, injective key for one idempotent
// operation. A repeated (namespace, key) pair always yields the same string,
// and different pairs never collide. Escaping the components keeps the encoding
// unambiguous when either contains the ':' separator. Callers pass a namespace
// so an idempotency_key from ADR 0031 cannot collide with a clientRequestId
// from ADR 0028 even when the raw values are identical.
func IdempotencyKey(namespace, key string) string {
	return obligationKeyEscaper.Replace(namespace) + ":" + obligationKeyEscaper.Replace(key)
}
