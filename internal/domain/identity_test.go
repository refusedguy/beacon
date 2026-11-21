package domain

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestNextRevisionMonotonicNoRepeats(t *testing.T) {
	const steps = 1000
	seen := make(map[uint64]bool, steps+1)
	current := uint64(0)
	seen[current] = true

	for i := 0; i < steps; i++ {
		next := NextRevision(current)
		if next != current+1 {
			t.Fatalf("step %d: NextRevision(%d) = %d, want %d", i, current, next, current+1)
		}
		if seen[next] {
			t.Fatalf("step %d: revision %d repeated", i, next)
		}
		seen[next] = true
		current = next
	}

	if got := len(seen); got != steps+1 {
		t.Fatalf("got %d distinct revisions, want %d", got, steps+1)
	}
}

func TestNextRevisionFromNonZero(t *testing.T) {
	if got := NextRevision(41); got != 42 {
		t.Fatalf("NextRevision(41) = %d, want 42", got)
	}
}

func TestNextRevisionOverflowDoesNotReuse(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NextRevision(max uint64) must panic instead of reusing or lowering the revision")
		}
	}()
	_ = NextRevision(^uint64(0))
}

func TestNextRevisionMonotonicUnderEventPermutation(t *testing.T) {
	events := []string{"rebase-1", "head-changed", "rebase-2", "merged", "rebase-3"}
	orders := [][]int{
		{0, 1, 2, 3, 4},
		{4, 3, 2, 1, 0},
		{2, 0, 4, 1, 3},
	}

	for _, order := range orders {
		current := uint64(0)
		seen := make(map[uint64]bool, len(events))
		for _, idx := range order {
			_ = events[idx]
			current = NextRevision(current)
			if seen[current] {
				t.Fatalf("order %v: revision %d repeated", order, current)
			}
			seen[current] = true
		}
		for rev := uint64(1); rev <= uint64(len(events)); rev++ {
			if !seen[rev] {
				t.Fatalf("order %v: missing revision %d", order, rev)
			}
		}
	}
}

func TestObligationKeyStableOnRepeat(t *testing.T) {
	first := ObligationKey("w1", 3, "s1")
	for i := 0; i < 3; i++ {
		if got := ObligationKey("w1", 3, "s1"); got != first {
			t.Fatalf("call %d: key changed: %q, want %q", i, got, first)
		}
	}
}

func TestObligationKeyChangesWithComponent(t *testing.T) {
	base := ObligationKey("w1", 3, "s1")
	cases := []struct {
		component string
		key       string
	}{
		{"watchID", ObligationKey("w2", 3, "s1")},
		{"revision", ObligationKey("w1", 4, "s1")},
		{"subscriberID", ObligationKey("w1", 3, "s2")},
	}
	for _, tc := range cases {
		if tc.key == base {
			t.Errorf("changing %s did not change the key: %q", tc.component, base)
		}
	}
}

func TestObligationKeySingleChangeWithSeparators(t *testing.T) {
	base := ObligationKey("w:1", 2, "s")
	if got := ObligationKey("w", 2, "s"); got == base {
		t.Errorf("watchID change collided: %q", base)
	}
	if got := ObligationKey("w:1", 3, "s"); got == base {
		t.Errorf("revision change collided: %q", base)
	}
	if got := ObligationKey("w:1", 2, "2:s"); got == base {
		t.Errorf("subscriberID change collided: %q", base)
	}
}

func TestObligationKeyInjectiveAcrossTriples(t *testing.T) {
	// A plain "watchID:revision:subscriberID" join maps both triples below to
	// "w:1:2:s". Escaping the components keeps the keys distinct.
	a := ObligationKey("w:1", 2, "s")
	b := ObligationKey("w", 1, "2:s")
	if a == b {
		t.Fatalf("different triples share key %q", a)
	}
}

func TestIdempotencyKeyStableOnRepeat(t *testing.T) {
	first := IdempotencyKey(StartNamespace, "req-abc")
	for i := 0; i < 3; i++ {
		if got := IdempotencyKey(StartNamespace, "req-abc"); got != first {
			t.Fatalf("call %d: key changed: %q, want %q", i, got, first)
		}
	}
}

func TestIdempotencyKeySeparateFromClientRequestID(t *testing.T) {
	// ADR 0031 idempotency_key and ADR 0028 clientRequestId are different keys.
	// The namespace makes the start key differ from the raw client request id
	// and from the same value scoped under another namespace.
	raw := "req-abc"
	start := IdempotencyKey(StartNamespace, raw)
	if start == raw {
		t.Fatalf("start key must be namespaced, got raw %q", raw)
	}
	if other := IdempotencyKey("client-request", raw); other == start {
		t.Fatalf("different namespaces share key %q", start)
	}
}

func TestIdempotencyKeyInjectiveWithSeparators(t *testing.T) {
	base := IdempotencyKey(StartNamespace, "req:1")
	cases := []struct {
		namespace string
		key       string
	}{
		{"task-start", "req:2"},
		{"other", "req:1"},
	}
	for _, tc := range cases {
		if got := IdempotencyKey(tc.namespace, tc.key); got == base {
			t.Errorf("(%q, %q) collided with %q", tc.namespace, tc.key, base)
		}
	}
	if IdempotencyKey("a:b", "c") == IdempotencyKey("a", "b:c") {
		t.Error("escaping failed: two different pairs share a key")
	}
}

func TestRejectDuplicateConflict(t *testing.T) {
	err := NewRejectDuplicate("req-abc", "task-1")
	if err.Code != CodeRejectDuplicate {
		t.Errorf("Code = %q, want %q", err.Code, CodeRejectDuplicate)
	}
	if err.Status != 409 {
		t.Errorf("Status = %d, want 409", err.Status)
	}
	if !IsRejectDuplicate(err) {
		t.Error("IsRejectDuplicate must recognize the conflict")
	}
	if IsRejectDuplicate(NewAppError(CodeInvalidInput, "bad")) {
		t.Error("IsRejectDuplicate must reject an unrelated error")
	}
	if !IsRejectDuplicate(fmt.Errorf("wrapped: %w", err)) {
		t.Error("IsRejectDuplicate must recognize a wrapped conflict")
	}
}

func testHandleKey() []byte {
	return []byte("test-signing-key-not-for-production")
}

func TestIssueTaskHandleFormatAndOpaque(t *testing.T) {
	key := testHandleKey()
	const taskID = "task-abc-123"
	handle, err := IssueTaskHandle(key, taskID)
	if err != nil {
		t.Fatalf("IssueTaskHandle: %v", err)
	}

	parts := strings.Split(handle, ".")
	if len(parts) != 3 {
		t.Fatalf("handle %q has %d segments, want 3", handle, len(parts))
	}
	if parts[0] != HandleVersion {
		t.Errorf("version = %q, want %q", parts[0], HandleVersion)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("payload is not base64url: %v", err)
	}
	if string(decoded) != taskID {
		t.Errorf("payload = %q, want %q", decoded, taskID)
	}
	if strings.Contains(handle, taskID) {
		t.Errorf("handle %q leaks the raw task id in plaintext", handle)
	}
}

func TestIssueTaskHandleDeterministicAcrossProcesses(t *testing.T) {
	key := testHandleKey()
	const taskID = "task-abc-123"

	// "Process A" issues the handle.
	issuedA, err := IssueTaskHandle(key, taskID)
	if err != nil {
		t.Fatalf("issue in process A: %v", err)
	}
	// "Process B" issues again after a restart and parses A's handle using
	// only the key, with no in-memory table.
	issuedB, err := IssueTaskHandle(key, taskID)
	if err != nil {
		t.Fatalf("issue in process B: %v", err)
	}
	if issuedA != issuedB {
		t.Fatalf("restart changed the handle: %q != %q", issuedA, issuedB)
	}
	got, err := ParseTaskHandle(key, issuedA)
	if err != nil {
		t.Fatalf("process B parse: %v", err)
	}
	if got != taskID {
		t.Fatalf("parsed task id = %q, want %q", got, taskID)
	}
}

func TestIssueTaskHandleDistinctPerTask(t *testing.T) {
	key := testHandleKey()
	a, err := IssueTaskHandle(key, "task-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := IssueTaskHandle(key, "task-2")
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("different task ids share handle %q", a)
	}
}

func TestIssueTaskHandleRejectsEmptyInputs(t *testing.T) {
	if _, err := IssueTaskHandle(nil, "task-1"); !errors.Is(err, ErrEmptyHandleKey) {
		t.Errorf("empty key: err = %v, want ErrEmptyHandleKey", err)
	}
	if _, err := IssueTaskHandle([]byte("key"), ""); !errors.Is(err, ErrEmptyTaskID) {
		t.Errorf("empty task id: err = %v, want ErrEmptyTaskID", err)
	}
}

func TestParseTaskHandleRejectsTampering(t *testing.T) {
	key := testHandleKey()
	handle, err := IssueTaskHandle(key, "task-abc")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(handle, ".")

	cases := []struct {
		name   string
		handle string
	}{
		{"tampered payload", parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte("task-xyz")) + "." + parts[2]},
		{"tampered signature", parts[0] + "." + parts[1] + "." + base64.RawURLEncoding.EncodeToString([]byte("forged"))},
		{"wrong key", handle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useKey := key
			if tc.name == "wrong key" {
				useKey = []byte("a-different-signing-key")
			}
			if _, err := ParseTaskHandle(useKey, tc.handle); !errors.Is(err, ErrInvalidHandleSignature) {
				t.Fatalf("err = %v, want ErrInvalidHandleSignature", err)
			}
		})
	}
}

func TestParseTaskHandleRejectsMalformed(t *testing.T) {
	key := testHandleKey()
	cases := []struct {
		name   string
		handle string
	}{
		{"too few segments", "v1.payload"},
		{"too many segments", "v1.payload.sig.extra"},
		{"unknown version", "v9.cGF5bG9hZA.sig"},
		{"bad payload base64", "v1.!!!.c2ln"},
		{"bad signature base64", "v1.cGF5bG9hZA.!!!"},
		{"empty payload", "v1..c2ln"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTaskHandle(key, tc.handle); !errors.Is(err, ErrInvalidHandle) {
				t.Fatalf("err = %v, want ErrInvalidHandle", err)
			}
		})
	}
	if _, err := ParseTaskHandle(nil, "v1.cGF5bG9hZA.c2ln"); !errors.Is(err, ErrEmptyHandleKey) {
		t.Errorf("empty key: err = %v, want ErrEmptyHandleKey", err)
	}
}

func TestParseTaskHandleRejectsRelabelledVersion(t *testing.T) {
	key := testHandleKey()
	handle, err := IssueTaskHandle(key, "task-abc")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(handle, ".")

	// The version tag is part of the handle and is signed with it. A handle
	// relabelled to a version this code does not know is refused before any
	// authorization decision is made.
	relabelled := "v2." + parts[1] + "." + parts[2]
	if _, err := ParseTaskHandle(key, relabelled); !errors.Is(err, ErrInvalidHandle) {
		t.Fatalf("relabelled version: err = %v, want ErrInvalidHandle", err)
	}
}

func TestHandleIsNotAuthorizationAndNotOtherKeys(t *testing.T) {
	key := testHandleKey()
	const taskID = "task-abc"
	handle, err := IssueTaskHandle(key, taskID)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseTaskHandle(key, handle)
	if err != nil {
		t.Fatal(err)
	}
	if parsed != taskID {
		t.Fatalf("parsed = %q, want %q", parsed, taskID)
	}

	// The handle is not the raw task id, a T3.51 fencing token stand-in, or
	// the ADR 0028/0031 idempotency key.
	if handle == taskID {
		t.Error("handle must not equal the raw task id")
	}
	if handle == IdempotencyKey(StartNamespace, taskID) {
		t.Error("handle must not be an idempotency key")
	}

	// A verified handle alone never grants access: the owner is checked
	// separately, and the handle plays no part in that decision.
	if !AuthorizeTaskOwner("alice", "alice") {
		t.Error("matching owner must be authorized")
	}
	if AuthorizeTaskOwner("bob", "alice") {
		t.Error("a valid handle with the wrong owner must not be authorized")
	}
	if AuthorizeTaskOwner("", "alice") {
		t.Error("an empty owner must not be authorized")
	}
}

func TestIdentityValueFormat(t *testing.T) {
	valid := []string{
		"req-abc",
		"ses_executor_1",
		"watch-A",
		"a:b.c-d",
		"webhook-delivery",
		"delivery-01",
	}
	invalid := []string{
		"",
		"   ",
		" req",
		"req ",
		"req id",
		"req\tid",
		"req\nid",
		"req\x00id",
		strings.Repeat("x", maxIdentityLen+1),
	}
	for _, raw := range valid {
		if !validIdentityValue(raw) {
			t.Errorf("valid value %q was rejected", raw)
		}
	}
	for _, raw := range invalid {
		if validIdentityValue(raw) {
			t.Errorf("invalid value %q was accepted", raw)
		}
	}
}

func TestNewIdentityTypesValidate(t *testing.T) {
	tests := []struct {
		name string
		make func(string) error
		want error
	}{
		{"client request id", func(s string) error { _, err := NewClientRequestID(s); return err }, ErrInvalidClientRequestID},
		{"actor id", func(s string) error { _, err := NewActorID(s); return err }, ErrInvalidActorID},
		{"idempotency value", func(s string) error { _, err := NewIdempotencyValue(s); return err }, ErrInvalidIdempotencyValue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.make("valid-value"); err != nil {
				t.Fatalf("valid value rejected: %v", err)
			}
			for _, raw := range []string{"", "  ", "bad value", "bad\nvalue"} {
				if err := tc.make(raw); !errors.Is(err, tc.want) {
					t.Errorf("make(%q) error = %v, want %v", raw, err, tc.want)
				}
			}
		})
	}
}

func TestIdentityTypeZeroValueInvalid(t *testing.T) {
	if (ClientRequestID("")).Valid() {
		t.Error("zero ClientRequestID must be invalid")
	}
	if (ActorID("")).Valid() {
		t.Error("zero ActorID must be invalid")
	}
	if (IdempotencyValue("")).Valid() {
		t.Error("zero IdempotencyValue must be invalid")
	}
}

func TestIdentityKeysDeterministic(t *testing.T) {
	id, err := NewClientRequestID("req-abc")
	if err != nil {
		t.Fatal(err)
	}
	first := CommandKey(id)
	for i := 0; i < 3; i++ {
		if got := CommandKey(id); got != first {
			t.Fatalf("call %d: command key changed: %q, want %q", i, got, first)
		}
	}

	delivery, err := NewIdempotencyValue("delivery-1")
	if err != nil {
		t.Fatal(err)
	}
	firstDelivery := DeliveryKey(delivery)
	for i := 0; i < 3; i++ {
		if got := DeliveryKey(delivery); got != firstDelivery {
			t.Fatalf("call %d: delivery key changed: %q, want %q", i, got, firstDelivery)
		}
	}
}

func TestIdentityKeysDifferentNamespacesDoNotShareSlot(t *testing.T) {
	raw, err := NewIdempotencyValue("same-value")
	if err != nil {
		t.Fatal(err)
	}
	command := ScopedKey(CommandNamespace, raw)
	delivery := DeliveryKey(raw)
	start := IdempotencyKey(StartNamespace, raw.String())
	obligation := ScopedKey(ObligationNamespace, raw)

	seen := map[string]string{}
	for name, key := range map[string]string{
		"command": command, "delivery": delivery, "start": start, "obligation": obligation,
	} {
		if prev, ok := seen[key]; ok {
			t.Errorf("%s and %s share key %q", prev, name, key)
		}
		seen[key] = name
	}
}

func TestEscapeKeyComponent(t *testing.T) {
	if got, want := EscapeKeyComponent(`a:b\c`), `a\:b\\c`; got != want {
		t.Errorf("EscapeKeyComponent = %q, want %q", got, want)
	}
	if EscapeKeyComponent("a:b") == EscapeKeyComponent("a")+":"+EscapeKeyComponent("b") {
		t.Error("escaping failed: a separator inside a component is ambiguous")
	}
}

func TestParseAddressValid(t *testing.T) {
	cases := []struct {
		raw    string
		member string
		rig    string
	}{
		{"member@rig", "member", "rig"},
		{"pod-member@rig-alpha", "pod-member", "rig-alpha"},
	}
	for _, tc := range cases {
		addr, err := ParseAddress(tc.raw)
		if err != nil {
			t.Fatalf("ParseAddress(%q): %v", tc.raw, err)
		}
		if addr.String() != tc.raw {
			t.Errorf("String = %q, want %q", addr.String(), tc.raw)
		}
		if addr.Member() != tc.member {
			t.Errorf("Member = %q, want %q", addr.Member(), tc.member)
		}
		if addr.Rig() != tc.rig {
			t.Errorf("Rig = %q, want %q", addr.Rig(), tc.rig)
		}
		if !addr.Valid() {
			t.Errorf("address %q reported invalid", tc.raw)
		}
	}
}

func TestParseAddressInvalid(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"no separator", "member"},
		{"empty member", "@rig"},
		{"empty rig", "member@"},
		{"two separators", "member@rig@extra"},
		{"whitespace in member", "mem ber@rig"},
		{"whitespace in rig", "member@rig one"},
		{"control character", "member@rig\x00"},
		{"over long member", strings.Repeat("m", maxIdentityLen+1) + "@rig"},
		{"over long rig", "member@" + strings.Repeat("r", maxIdentityLen+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, err := ParseAddress(tc.raw)
			if err == nil {
				t.Fatalf("ParseAddress(%q) = %q, want error", tc.raw, addr)
			}
			if !errors.Is(err, ErrInvalidSeatAddress) {
				t.Fatalf("error = %v, want ErrInvalidSeatAddress", err)
			}
			if addr != "" {
				t.Fatalf("invalid parse returned address %q", addr)
			}
		})
	}
}

func TestResolveAddressStableSeat(t *testing.T) {
	addr, err := ParseAddress("member@rig")
	if err != nil {
		t.Fatal(err)
	}
	seat, err := ResolveAddress(addr)
	if err != nil {
		t.Fatal(err)
	}
	if want := Seat("member@rig"); seat != want {
		t.Fatalf("seat = %q, want %q", seat, want)
	}
	if !seat.Valid() {
		t.Fatal("resolved seat reported invalid")
	}
	// Resolution has no session input. The seat and any owner key built from it
	// stay the same when the session behind the address changes.
	for i := 0; i < 3; i++ {
		got, err := ResolveAddress(addr)
		if err != nil {
			t.Fatal(err)
		}
		if got != seat {
			t.Fatalf("call %d: seat = %q, want %q", i, got, seat)
		}
		if key, want := ObligationKey("w1", 3, got.String()), ObligationKey("w1", 3, seat.String()); key != want {
			t.Fatalf("call %d: owner key changed: %q, want %q", i, key, want)
		}
	}
}

func TestResolveAddressRejectsInvalid(t *testing.T) {
	for _, raw := range []string{"bad-address", "member@", "@rig", ""} {
		addr := SeatAddress(raw)
		if addr.Valid() {
			t.Errorf("address %q reported valid", raw)
		}
		if _, err := ResolveAddress(addr); !errors.Is(err, ErrInvalidSeatAddress) {
			t.Errorf("ResolveAddress(%q) error = %v, want ErrInvalidSeatAddress", raw, err)
		}
	}
}
