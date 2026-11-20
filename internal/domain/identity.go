package domain

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
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

// GitHubDeliveryNamespace scopes an idempotency_key that identifies one
// GitHub webhook delivery (ADR 0150). X-GitHub-Delivery is a GUID unique to an
// event and is repeated byte-for-byte on manual redelivery. The namespace
// keeps a delivery key separate from a task start (ADR 0031), a clientRequestId
// (ADR 0028) and a completion notice (ADR 0044), even when the raw values match.
const GitHubDeliveryNamespace = "github-delivery"

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

// Signed task handles (ADR 0044 feature 1). A handle is an opaque name for a
// task, signed with HMAC-SHA256 so no instance can invent one. It is a pure
// function of the signing key and the task id: the same pair yields the same
// handle on every process and after any restart, with no in-memory table or
// database lookup. A handle proves only that Beacon issued the name; it grants
// no rights. Access is decided separately by AuthorizeTaskOwner against the
// owner recorded with the task. This fixes a format, not an A2A/MCP protocol,
// and it is neither the T3.51 fencing token nor the ADR 0028 clientRequestId.
const (
	// HandleVersion is the version tag embedded in and signed into every handle.
	HandleVersion = "v1"

	handleSeparator = "."
)

var (
	// ErrInvalidHandle reports a handle with an unknown version, a bad segment
	// layout, or invalid base64.
	ErrInvalidHandle = errors.New("domain: invalid task handle")
	// ErrInvalidHandleSignature reports a handle whose HMAC does not match, so
	// it was forged or signed with a different key.
	ErrInvalidHandleSignature = errors.New("domain: invalid task handle signature")
	// ErrEmptyHandleKey reports an empty signing key on issue or parse.
	ErrEmptyHandleKey = errors.New("domain: empty task handle signing key")
	// ErrEmptyTaskID reports an attempt to issue a handle for an empty task id.
	ErrEmptyTaskID = errors.New("domain: empty task id")
)

// handleMAC computes the HMAC-SHA256 over the signed part of a handle: the
// version and the encoded payload joined by the separator. Signing the version
// too keeps a handle from being relabelled to a different version.
func handleMAC(key []byte, version, payload string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(version))
	mac.Write([]byte(handleSeparator))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

// IssueTaskHandle returns an opaque, signed handle naming taskID. The result
// is deterministic in (key, taskID), so another process holding the same key
// reproduces the exact handle after a restart without any stored state. The
// handle carries the task id under base64url but confers no authority; callers
// must still check ownership with AuthorizeTaskOwner.
func IssueTaskHandle(key []byte, taskID string) (string, error) {
	if len(key) == 0 {
		return "", ErrEmptyHandleKey
	}
	if taskID == "" {
		return "", ErrEmptyTaskID
	}
	payload := base64.RawURLEncoding.EncodeToString([]byte(taskID))
	sig := base64.RawURLEncoding.EncodeToString(handleMAC(key, HandleVersion, payload))
	return HandleVersion + handleSeparator + payload + handleSeparator + sig, nil
}

// ParseTaskHandle verifies and decodes a handle issued by IssueTaskHandle. It
// returns the task id the handle names, or an error when the layout is invalid,
// the version is unknown, or the signature does not verify under key. Parsing
// needs no stored state: key is the only input besides the handle itself. The
// task id is returned without any authorization decision; the caller must
// still check ownership with AuthorizeTaskOwner.
func ParseTaskHandle(key []byte, handle string) (string, error) {
	if len(key) == 0 {
		return "", ErrEmptyHandleKey
	}
	parts := strings.Split(handle, handleSeparator)
	if len(parts) != 3 {
		return "", fmt.Errorf("%w: want 3 segments, got %d", ErrInvalidHandle, len(parts))
	}
	version, payload, sig := parts[0], parts[1], parts[2]
	if version != HandleVersion {
		return "", fmt.Errorf("%w: unknown version %q", ErrInvalidHandle, version)
	}
	rawTaskID, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return "", fmt.Errorf("%w: bad payload encoding: %v", ErrInvalidHandle, err)
	}
	if len(rawTaskID) == 0 {
		return "", fmt.Errorf("%w: empty task id", ErrInvalidHandle)
	}
	rawSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return "", fmt.Errorf("%w: bad signature encoding: %v", ErrInvalidHandle, err)
	}
	want := handleMAC(key, HandleVersion, payload)
	if !hmac.Equal(rawSig, want) {
		return "", ErrInvalidHandleSignature
	}
	return string(rawTaskID), nil
}

// AuthorizeTaskOwner decides access to a task named by a verified handle. It
// is intentionally separate from handle verification: a valid handle only
// proves the name was issued by Beacon, not that the caller owns the task. The
// expectedOwner is the owner recorded on the task; the handle is not consulted
// here, so a signed handle alone never grants access.
func AuthorizeTaskOwner(owner, expectedOwner string) bool {
	return owner != "" && owner == expectedOwner
}

// ---------------------------------------------------------------------------
// Validated identity keys (BS.21, ADR 0193/0196).
//
// The brand types below carry already-validated identity values. Keeping them
// as distinct types means a client request id cannot be passed where an actor
// id is expected. The exported IdempotencyKey function above stays as the
// deterministic key builder: it scopes a raw value to a namespace, so keys for
// different operations never share a storage slot (ADR 0193).
// ---------------------------------------------------------------------------

const (
	// CommandNamespace scopes keys for client commands identified by a
	// ClientRequestID (ADR 0028).
	CommandNamespace = "client-command"

	// ObligationNamespace scopes keys for wake obligations identified by a
	// watch, revision and subscriber (ADR 0088).
	ObligationNamespace = "obligation"
)

// maxIdentityLen caps identity values so a hostile client cannot grow a key
// without bound. Every identifier Beacon issues is far shorter than this.
const maxIdentityLen = 256

var (
	// ErrInvalidClientRequestID reports a client request id that is empty,
	// whitespace-only, contains whitespace or control characters, or is too
	// long.
	ErrInvalidClientRequestID = errors.New("domain: invalid client request id")
	// ErrInvalidActorID reports an actor id with the same defects.
	ErrInvalidActorID = errors.New("domain: invalid actor id")
	// ErrInvalidIdempotencyValue reports a raw idempotency value with the same
	// defects.
	ErrInvalidIdempotencyValue = errors.New("domain: invalid idempotency value")
)

// validIdentityValue reports whether raw is usable as an identity component.
// It rejects empty values, whitespace, control characters and over-long
// values. Everything else, including separators such as ':' and '.', is
// accepted so existing identifiers keep working.
func validIdentityValue(raw string) bool {
	if raw == "" || len(raw) > maxIdentityLen {
		return false
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// ClientRequestID is a validated identifier for one client command (ADR 0028).
// The zero value is invalid; build one with NewClientRequestID.
type ClientRequestID string

// NewClientRequestID validates raw as a client request id.
func NewClientRequestID(raw string) (ClientRequestID, error) {
	if !validIdentityValue(raw) {
		return "", fmt.Errorf("%w: %q", ErrInvalidClientRequestID, raw)
	}
	return ClientRequestID(raw), nil
}

// String returns the raw identifier.
func (id ClientRequestID) String() string { return string(id) }

// Valid reports whether the value satisfies the identity format.
func (id ClientRequestID) Valid() bool { return validIdentityValue(string(id)) }

// ActorID is a validated identifier for the actor that issued a command
// (BS.22). It may name a session, an agent or an ingress label. The zero value
// is invalid; build one with NewActorID.
type ActorID string

// NewActorID validates raw as an actor id.
func NewActorID(raw string) (ActorID, error) {
	if !validIdentityValue(raw) {
		return "", fmt.Errorf("%w: %q", ErrInvalidActorID, raw)
	}
	return ActorID(raw), nil
}

// String returns the raw identifier.
func (id ActorID) String() string { return string(id) }

// Valid reports whether the value satisfies the identity format.
func (id ActorID) Valid() bool { return validIdentityValue(string(id)) }

// IdempotencyValue is a validated raw value for one idempotent operation. The
// namespaced key is built by the exported IdempotencyKey function, which keeps
// its name for existing callers. The zero value is invalid; build one with
// NewIdempotencyValue.
type IdempotencyValue string

// NewIdempotencyValue validates raw as an idempotency value.
func NewIdempotencyValue(raw string) (IdempotencyValue, error) {
	if !validIdentityValue(raw) {
		return "", fmt.Errorf("%w: %q", ErrInvalidIdempotencyValue, raw)
	}
	return IdempotencyValue(raw), nil
}

// String returns the raw value.
func (v IdempotencyValue) String() string { return string(v) }

// Valid reports whether the value satisfies the identity format.
func (v IdempotencyValue) Valid() bool { return validIdentityValue(string(v)) }

// EscapeKeyComponent escapes one component of a composite key. Use it when a
// caller assembles a key outside IdempotencyKey or ObligationKey so a ':'
// or '\' inside a component cannot change the meaning of the key. Escaping
// backslash first keeps the encoding reversible.
func EscapeKeyComponent(component string) string {
	return obligationKeyEscaper.Replace(component)
}

// CommandKey returns the deterministic idempotency key for one client command.
// The same ClientRequestID always yields the same key.
func CommandKey(id ClientRequestID) string {
	return IdempotencyKey(CommandNamespace, id.String())
}

// DeliveryKey returns the deterministic idempotency key for one webhook
// delivery. It shares a namespace with the store's delivery journal.
func DeliveryKey(deliveryID IdempotencyValue) string {
	return IdempotencyKey(GitHubDeliveryNamespace, deliveryID.String())
}

// ScopedKey returns the deterministic idempotency key for a validated value
// under an explicit namespace. Different namespaces never share a key.
func ScopedKey(namespace string, value IdempotencyValue) string {
	return IdempotencyKey(namespace, value.String())
}
