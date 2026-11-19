package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
)

// TestClaimSingleOwnerUntilRelease walks the ADR 0032 p4 contract through the
// tool handlers: one owner holds the task, a retry by that owner is idempotent,
// a second owner is rejected, and only an explicit release by the owner frees
// the task for someone else.
func TestClaimSingleOwnerUntilRelease(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	_, first, err := h.Handle(ctx, nil, ClaimInput{
		TaskID: "T-1", OwnerID: "alice", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("alice claim: %v", err)
	}
	if first.TaskID != "T-1" || first.OwnerID != "alice" {
		t.Fatalf("claim = %+v, want task T-1 owned by alice", first)
	}
	const wantKey = "task-claim:k1"
	if first.IdempotencyKey != wantKey {
		t.Fatalf("idempotency key = %q, want %q", first.IdempotencyKey, wantKey)
	}

	_, _, err = h.Handle(ctx, nil, ClaimInput{
		TaskID: "T-1", OwnerID: "bob", IdempotencyKey: "k2",
	})
	if !domain.IsRejectDuplicate(err) {
		t.Fatalf("bob claim while alice owns the task: err = %v, want reject duplicate", err)
	}

	_, again, err := h.Handle(ctx, nil, ClaimInput{
		TaskID: "T-1", OwnerID: "alice", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("alice retry with same key: %v", err)
	}
	if again != first {
		t.Fatalf("idempotent retry = %+v, want original claim %+v", again, first)
	}

	_, _, err = h.HandleRelease(ctx, nil, ReleaseInput{TaskID: "T-1", OwnerID: "bob"})
	if !domain.IsRejectDuplicate(err) {
		t.Fatalf("bob release of alice's claim: err = %v, want reject duplicate", err)
	}

	_, released, err := h.HandleRelease(ctx, nil, ReleaseInput{TaskID: "T-1", OwnerID: "alice"})
	if err != nil {
		t.Fatalf("alice release: %v", err)
	}
	if released.TaskID != "T-1" || released.OwnerID != "alice" {
		t.Fatalf("release = %+v, want task T-1 released by alice", released)
	}

	_, next, err := h.Handle(ctx, nil, ClaimInput{
		TaskID: "T-1", OwnerID: "bob", IdempotencyKey: "k2",
	})
	if err != nil {
		t.Fatalf("bob claim after release: %v", err)
	}
	if next.OwnerID != "bob" {
		t.Fatalf("owner after release = %q, want bob", next.OwnerID)
	}
}

// TestClaimKeyDoesNotCollideWithTaskStart checks the T3.23 namespace contract
// at this boundary: the same raw key names a claim and a task start in separate
// slots, and one raw key cannot claim two different tasks.
func TestClaimKeyDoesNotCollideWithTaskStart(t *testing.T) {
	h := NewClaimHandler()
	claim, err := h.Claim(context.Background(), ClaimInput{
		TaskID: "T-2", OwnerID: "alice", IdempotencyKey: "shared",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if want := domain.IdempotencyKey(domain.StartNamespace, "shared"); claim.IdempotencyKey == want {
		t.Fatalf("claim key %q collides with task-start key %q", claim.IdempotencyKey, want)
	}

	_, err = h.Claim(context.Background(), ClaimInput{
		TaskID: "T-3", OwnerID: "alice", IdempotencyKey: "shared",
	})
	if !domain.IsRejectDuplicate(err) {
		t.Fatalf("reuse of one key for two tasks: err = %v, want reject duplicate", err)
	}
}

// TestClaimConcurrentSingleWinner is the observable form of atomicity: many
// concurrent claims of one task produce exactly one owner.
func TestClaimConcurrentSingleWinner(t *testing.T) {
	const contenders = 32
	h := NewClaimHandler()
	ctx := context.Background()

	var wg sync.WaitGroup
	winners := make(chan string, contenders)
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := fmt.Sprintf("owner-%02d", i)
			out, err := h.Claim(ctx, ClaimInput{
				TaskID: "T-race", OwnerID: owner, IdempotencyKey: fmt.Sprintf("k-%02d", i),
			})
			if err == nil {
				winners <- out.OwnerID
			}
		}(i)
	}
	wg.Wait()
	close(winners)

	got := 0
	for range winners {
		got++
	}
	if got != 1 {
		t.Fatalf("concurrent claims produced %d owners, want 1", got)
	}
}

// TestReleaseRequiresCurrentClaim checks that release is explicit and one-shot:
// an unclaimed task and a second release are both rejected.
func TestReleaseRequiresCurrentClaim(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	if _, err := h.Release(ctx, ReleaseInput{TaskID: "T-4", OwnerID: "alice"}); !domain.IsRejectDuplicate(err) {
		t.Fatalf("release of unclaimed task: err = %v, want reject duplicate", err)
	}
	if _, err := h.Claim(ctx, ClaimInput{TaskID: "T-4", OwnerID: "alice", IdempotencyKey: "k4"}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{TaskID: "T-4", OwnerID: "alice"}); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{TaskID: "T-4", OwnerID: "alice"}); !domain.IsRejectDuplicate(err) {
		t.Fatalf("repeat release: err = %v, want reject duplicate", err)
	}
}

// TestClaimInvalidInput checks that empty required fields never reach the
// store and are reported as invalid input.
func TestClaimInvalidInput(t *testing.T) {
	h := NewClaimHandler()
	cases := []struct {
		name string
		in   ClaimInput
	}{
		{"no task", ClaimInput{OwnerID: "alice", IdempotencyKey: "k"}},
		{"no owner", ClaimInput{TaskID: "T", IdempotencyKey: "k"}},
		{"no key", ClaimInput{TaskID: "T", OwnerID: "alice"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.Claim(context.Background(), tc.in)
			var app *domain.AppError
			if !errors.As(err, &app) || app.Code != domain.CodeInvalidInput {
				t.Fatalf("err = %v, want INVALID_INPUT", err)
			}
		})
	}
}

// connectClaimServer is a local in-memory client for the attach test, so this
// file does not depend on helpers in another tool's test file.
func connectClaimServer(t *testing.T, server *mcpsdk.Server) *mcpsdk.ClientSession {
	t.Helper()
	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-claim-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

// TestAttachClaimRegistersBothToolsOnce checks the integration hook without
// changing the default NewServer contract. A repeated attach must not duplicate
// either tool.
func TestAttachClaimRegistersBothToolsOnce(t *testing.T) {
	server := NewServer()
	AttachClaim(server, NewClaimHandler())
	AttachClaim(server, NewClaimHandler())

	session := connectClaimServer(t, server)
	counts := map[string]int{ClaimToolName: 0, ReleaseToolName: 0}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if _, tracked := counts[tool.Name]; tracked {
			counts[tool.Name]++
		}
	}
	for _, name := range []string{ClaimToolName, ReleaseToolName} {
		if counts[name] != 1 {
			t.Errorf("%s registered %d times, want 1", name, counts[name])
		}
	}
}

// TestClaimFencingTokenMonotonic checks ADR 0037 p5 at the observable boundary:
// every assignment, whether a new claim after release or a reassignment, takes
// a strictly higher fencing token. An idempotent retry keeps the original one.
func TestClaimFencingTokenMonotonic(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	first, err := h.Claim(ctx, ClaimInput{TaskID: "T-f", OwnerID: "alice", IdempotencyKey: "f1"})
	if err != nil {
		t.Fatalf("alice claim: %v", err)
	}
	if first.FencingToken == 0 {
		t.Fatal("first claim fencing token must be positive")
	}

	again, err := h.Claim(ctx, ClaimInput{TaskID: "T-f", OwnerID: "alice", IdempotencyKey: "f1"})
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if again.FencingToken != first.FencingToken {
		t.Fatalf("retry token = %d, want original %d", again.FencingToken, first.FencingToken)
	}

	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-f", OwnerID: "alice", FencingToken: first.FencingToken,
	}); err != nil {
		t.Fatalf("alice release with current token: %v", err)
	}
	second, err := h.Claim(ctx, ClaimInput{TaskID: "T-f", OwnerID: "bob", IdempotencyKey: "f2"})
	if err != nil {
		t.Fatalf("bob claim: %v", err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("claim after release token = %d, want > %d", second.FencingToken, first.FencingToken)
	}

	third, err := h.Reassign(ctx, ClaimInput{TaskID: "T-f", OwnerID: "carol", IdempotencyKey: "f3"})
	if err != nil {
		t.Fatalf("reassign to carol: %v", err)
	}
	if third.FencingToken <= second.FencingToken {
		t.Fatalf("reassign token = %d, want > %d", third.FencingToken, second.FencingToken)
	}

	thirdAgain, err := h.Reassign(ctx, ClaimInput{TaskID: "T-f", OwnerID: "carol", IdempotencyKey: "f3"})
	if err != nil {
		t.Fatalf("repeated reassign: %v", err)
	}
	if thirdAgain.FencingToken != third.FencingToken {
		t.Fatalf("repeated reassign token = %d, want same %d", thirdAgain.FencingToken, third.FencingToken)
	}
}

// TestStaleFencingTokenRejected is the acceptance of T3.51: after the task is
// reassigned, the lagging owner presenting its old token cannot change it. The
// stale call leaves the task with the new owner, whose own token still works.
func TestStaleFencingTokenRejected(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	alice, err := h.Claim(ctx, ClaimInput{TaskID: "T-s", OwnerID: "alice", IdempotencyKey: "s1"})
	if err != nil {
		t.Fatalf("alice claim: %v", err)
	}
	bob, err := h.Reassign(ctx, ClaimInput{TaskID: "T-s", OwnerID: "bob", IdempotencyKey: "s2"})
	if err != nil {
		t.Fatalf("reassign to bob: %v", err)
	}
	if bob.FencingToken <= alice.FencingToken {
		t.Fatalf("reassign token = %d, want > %d", bob.FencingToken, alice.FencingToken)
	}

	_, err = h.Release(ctx, ReleaseInput{
		TaskID: "T-s", OwnerID: "alice", FencingToken: alice.FencingToken,
	})
	if !IsStaleFencingToken(err) {
		t.Fatalf("lagging alice release with stale token: err = %v, want stale fencing token", err)
	}
	var stale *FencingTokenError
	if !errors.As(err, &stale) {
		t.Fatalf("err = %v, want *FencingTokenError", err)
	}
	if stale.Presented != alice.FencingToken || stale.Current != bob.FencingToken {
		t.Fatalf("stale token = %+v, want presented %d current %d", stale, alice.FencingToken, bob.FencingToken)
	}

	if _, err := h.Claim(ctx, ClaimInput{TaskID: "T-s", OwnerID: "carol", IdempotencyKey: "s3"}); !domain.IsRejectDuplicate(err) {
		t.Fatalf("claim of task after stale release: err = %v, want reject duplicate (state unchanged)", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-s", OwnerID: "bob", FencingToken: bob.FencingToken,
	}); err != nil {
		t.Fatalf("bob release with current token: %v", err)
	}
}

// TestStaleFencingTokenRejectedForSameOwner isolates the fencing check from the
// owner check: the same owner holding a renewed lease cannot use its old token.
func TestStaleFencingTokenRejectedForSameOwner(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	first, err := h.Claim(ctx, ClaimInput{TaskID: "T-o", OwnerID: "alice", IdempotencyKey: "o1"})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-o", OwnerID: "alice", FencingToken: first.FencingToken,
	}); err != nil {
		t.Fatalf("first release: %v", err)
	}
	second, err := h.Claim(ctx, ClaimInput{TaskID: "T-o", OwnerID: "alice", IdempotencyKey: "o2"})
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("second token = %d, want > %d", second.FencingToken, first.FencingToken)
	}

	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-o", OwnerID: "alice", FencingToken: first.FencingToken,
	}); !IsStaleFencingToken(err) {
		t.Fatalf("release with old token by current owner: err = %v, want stale fencing token", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-o", OwnerID: "alice", FencingToken: second.FencingToken,
	}); err != nil {
		t.Fatalf("release with current token: %v", err)
	}
}

// TestFencingTokenFutureRejected checks that a token greater than the task's
// current one is also refused: only the lease the task actually holds can
// change it.
func TestFencingTokenFutureRejected(t *testing.T) {
	h := NewClaimHandler()
	current, err := h.Claim(context.Background(), ClaimInput{
		TaskID: "T-x", OwnerID: "alice", IdempotencyKey: "x1",
	})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	_, err = h.Release(context.Background(), ReleaseInput{
		TaskID: "T-x", OwnerID: "alice", FencingToken: current.FencingToken + 1,
	})
	if !IsStaleFencingToken(err) {
		t.Fatalf("release with future token: err = %v, want stale fencing token", err)
	}
}

// TestLeaseInvalidatedAfterSourceRestored is the acceptance of ADR 0038 p1 and
// T3.53: after the source is restored, the old lease cannot continue the task
// even when the same owner returns with a still-current fencing token. Only a
// new lease can change the task again.
func TestLeaseInvalidatedAfterSourceRestored(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	lease, err := h.Claim(ctx, ClaimInput{TaskID: "T-r", OwnerID: "alice", IdempotencyKey: "r1"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if lease.SourceEpoch == 0 {
		t.Fatal("claim source epoch must be positive")
	}

	recovered, err := h.Recover(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if recovered.SourceEpoch <= lease.SourceEpoch {
		t.Fatalf("recovered epoch = %d, want > %d", recovered.SourceEpoch, lease.SourceEpoch)
	}

	_, err = h.Release(ctx, ReleaseInput{
		TaskID:       "T-r",
		OwnerID:      "alice",
		FencingToken: lease.FencingToken,
		SourceEpoch:  lease.SourceEpoch,
	})
	if !IsLeaseInvalidated(err) {
		t.Fatalf("stale lease release: err = %v, want lease invalidated", err)
	}
	if IsStaleFencingToken(err) {
		t.Fatalf("stale lease release: err = %v, must not be a fencing-token error", err)
	}
	var invalid *LeaseInvalidatedError
	if !errors.As(err, &invalid) {
		t.Fatalf("err = %v, want *LeaseInvalidatedError", err)
	}
	if invalid.LeaseEpoch != lease.SourceEpoch || invalid.SourceEpoch != recovered.SourceEpoch {
		t.Fatalf("invalidated = %+v, want lease epoch %d source epoch %d", invalid, lease.SourceEpoch, recovered.SourceEpoch)
	}

	// The refused call left the state untouched: the stale lease still owns it.
	if _, err := h.Claim(ctx, ClaimInput{TaskID: "T-r", OwnerID: "bob", IdempotencyKey: "r2"}); !domain.IsRejectDuplicate(err) {
		t.Fatalf("bob claim after stale release: err = %v, want reject duplicate (state unchanged)", err)
	}

	// A fresh lease at the restored epoch works; the old one never does.
	fresh, err := h.Reassign(ctx, ClaimInput{TaskID: "T-r", OwnerID: "alice", IdempotencyKey: "r1"})
	if err != nil {
		t.Fatalf("reassign after recovery: %v", err)
	}
	if fresh.SourceEpoch != recovered.SourceEpoch {
		t.Fatalf("fresh epoch = %d, want %d", fresh.SourceEpoch, recovered.SourceEpoch)
	}
	if fresh.FencingToken <= lease.FencingToken {
		t.Fatalf("fresh token = %d, want > %d", fresh.FencingToken, lease.FencingToken)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID:       "T-r",
		OwnerID:      "alice",
		FencingToken: fresh.FencingToken,
		SourceEpoch:  fresh.SourceEpoch,
	}); err != nil {
		t.Fatalf("release with fresh lease: %v", err)
	}
}

// TestStaleLeaseInvalidWithCurrentFencingToken checks that invalidation is
// detected at the store even when the caller pins no source epoch. The fencing
// token is still the task's current one, so only the crash fact rejects the
// release.
func TestStaleLeaseInvalidWithCurrentFencingToken(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	lease, err := h.Claim(ctx, ClaimInput{TaskID: "T-i", OwnerID: "alice", IdempotencyKey: "i1"})
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := h.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	_, err = h.Release(ctx, ReleaseInput{TaskID: "T-i", OwnerID: "alice", FencingToken: lease.FencingToken})
	if !IsLeaseInvalidated(err) {
		t.Fatalf("release with current token after crash: err = %v, want lease invalidated", err)
	}
	if IsStaleFencingToken(err) {
		t.Fatalf("release with current token after crash: err = %v, must not be a fencing-token error", err)
	}
}

// TestClaimAfterRecoveryTakesNewLease shows the remedy: the returning owner
// takes a new lease, idempotent retries keep that new lease, and the old token
// then fails under the T3.51 fencing check inside the new epoch.
func TestClaimAfterRecoveryTakesNewLease(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	first, err := h.Claim(ctx, ClaimInput{TaskID: "T-a", OwnerID: "alice", IdempotencyKey: "a1"})
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if _, err := h.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	second, err := h.Claim(ctx, ClaimInput{TaskID: "T-a", OwnerID: "alice", IdempotencyKey: "a1"})
	if err != nil {
		t.Fatalf("re-claim after recovery: %v", err)
	}
	if second.SourceEpoch <= first.SourceEpoch {
		t.Fatalf("new epoch = %d, want > %d", second.SourceEpoch, first.SourceEpoch)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("new token = %d, want > %d", second.FencingToken, first.FencingToken)
	}

	again, err := h.Claim(ctx, ClaimInput{TaskID: "T-a", OwnerID: "alice", IdempotencyKey: "a1"})
	if err != nil {
		t.Fatalf("retry at new epoch: %v", err)
	}
	if again != second {
		t.Fatalf("retry = %+v, want new lease %+v", again, second)
	}

	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-a", OwnerID: "alice", FencingToken: first.FencingToken,
	}); !IsStaleFencingToken(err) {
		t.Fatalf("old token after new lease: err = %v, want stale fencing token", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID:       "T-a",
		OwnerID:      "alice",
		FencingToken: second.FencingToken,
		SourceEpoch:  second.SourceEpoch,
	}); err != nil {
		t.Fatalf("release with new lease: %v", err)
	}
}

// TestRecoveryAdvancesEpochMonotonically checks that each recovery installs a
// strictly higher source epoch, so leases from every earlier generation are
// invalidated in order.
func TestRecoveryAdvancesEpochMonotonically(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	first, err := h.Recover(ctx)
	if err != nil {
		t.Fatalf("first recover: %v", err)
	}
	second, err := h.Recover(ctx)
	if err != nil {
		t.Fatalf("second recover: %v", err)
	}
	if second.SourceEpoch <= first.SourceEpoch {
		t.Fatalf("second epoch = %d, want > %d", second.SourceEpoch, first.SourceEpoch)
	}
}

// TestFencingTokenAndSourceEpochCoexist checks that invalidation is a separate
// fact from the lease number: a reassignment still produces a stale-token
// rejection inside one epoch, while recovery produces a lease-invalidation
// rejection for a token that is otherwise current.
func TestFencingTokenAndSourceEpochCoexist(t *testing.T) {
	h := NewClaimHandler()
	ctx := context.Background()

	alice, err := h.Claim(ctx, ClaimInput{TaskID: "T-c", OwnerID: "alice", IdempotencyKey: "c1"})
	if err != nil {
		t.Fatalf("alice claim: %v", err)
	}
	bob, err := h.Reassign(ctx, ClaimInput{TaskID: "T-c", OwnerID: "bob", IdempotencyKey: "c2"})
	if err != nil {
		t.Fatalf("reassign to bob: %v", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID: "T-c", OwnerID: "alice", FencingToken: alice.FencingToken,
	}); !IsStaleFencingToken(err) {
		t.Fatalf("lagging alice after reassign: err = %v, want stale fencing token", err)
	}

	if _, err := h.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}
	carol, err := h.Reassign(ctx, ClaimInput{TaskID: "T-c", OwnerID: "carol", IdempotencyKey: "c3"})
	if err != nil {
		t.Fatalf("reassign to carol after recovery: %v", err)
	}
	if carol.SourceEpoch <= bob.SourceEpoch {
		t.Fatalf("carol epoch = %d, want > %d", carol.SourceEpoch, bob.SourceEpoch)
	}
	if carol.FencingToken <= bob.FencingToken {
		t.Fatalf("carol token = %d, want > %d", carol.FencingToken, bob.FencingToken)
	}

	if _, err := h.Release(ctx, ReleaseInput{
		TaskID:       "T-c",
		OwnerID:      "bob",
		FencingToken: bob.FencingToken,
		SourceEpoch:  bob.SourceEpoch,
	}); !IsLeaseInvalidated(err) {
		t.Fatalf("bob after recovery: err = %v, want lease invalidated", err)
	}
	if _, err := h.Release(ctx, ReleaseInput{
		TaskID:       "T-c",
		OwnerID:      "carol",
		FencingToken: carol.FencingToken,
		SourceEpoch:  carol.SourceEpoch,
	}); err != nil {
		t.Fatalf("carol release with fresh lease: %v", err)
	}
}

// TestRecoverIsNotAClientTool keeps recovery a kernel decision: it must not be
// registered as an MCP tool, so the client-facing tool set stays unchanged.
func TestRecoverIsNotAClientTool(t *testing.T) {
	server := NewServer()
	AttachClaim(server, NewClaimHandler())
	session := connectClaimServer(t, server)
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if tool.Name == "beacon_recover" {
			t.Fatal("beacon_recover must not be registered as a client tool")
		}
	}
}

// leaseTestClock is a local clock so the TTL contract can be tested without
// sleeping. The file keeps its own clock helper instead of sharing one with
// another tool's tests.
type leaseTestClock struct {
	t time.Time
}

func (c *leaseTestClock) Now() time.Time { return c.t }

func (c *leaseTestClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

// TestStaleWorktreeOwnerCannotWriteAfterTTL is the acceptance of ADR 0044 p2:
// a live lease lets its owner write, the TTL end frees the worktree, and a
// former owner with the old token cannot write into the re-leased directory.
func TestStaleWorktreeOwnerCannotWriteAfterTTL(t *testing.T) {
	start := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	clock := &leaseTestClock{t: start}
	h := NewWorktreeLeaseHandler(clock)
	ctx := context.Background()

	lease, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-1", OwnerID: "alice", TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("alice lease: %v", err)
	}
	if lease.WorktreeID != "wt-1" || lease.OwnerID != "alice" {
		t.Fatalf("lease = %+v, want wt-1 owned by alice", lease)
	}
	if lease.FencingToken == 0 {
		t.Fatal("lease fencing token must be positive")
	}
	if !lease.ExpiresAt.Equal(start.Add(time.Hour)) {
		t.Fatalf("expiresAt = %v, want %v", lease.ExpiresAt, start.Add(time.Hour))
	}
	if _, err := h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-1", OwnerID: "alice", FencingToken: lease.FencingToken,
	}); err != nil {
		t.Fatalf("alice write while lease live: %v", err)
	}

	// The TTL elapses without an explicit release.
	clock.Advance(2 * time.Hour)
	if _, err := h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-1", OwnerID: "alice", FencingToken: lease.FencingToken,
	}); !IsWorktreeLeaseExpired(err) {
		t.Fatalf("alice write after TTL: err = %v, want worktree lease expired", err)
	}

	bob, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-1", OwnerID: "bob", TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("bob lease after TTL: %v", err)
	}
	if bob.FencingToken <= lease.FencingToken {
		t.Fatalf("bob token = %d, want > %d", bob.FencingToken, lease.FencingToken)
	}

	_, err = h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-1", OwnerID: "alice", FencingToken: lease.FencingToken,
	})
	if !IsStaleWorktreeLease(err) {
		t.Fatalf("alice write with old token after re-lease: err = %v, want stale worktree lease", err)
	}
	var stale *WorktreeLeaseStaleError
	if !errors.As(err, &stale) || stale.Presented != lease.FencingToken || stale.Current != bob.FencingToken {
		t.Fatalf("stale = %+v, want presented %d current %d", stale, lease.FencingToken, bob.FencingToken)
	}
	if _, err := h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-1", OwnerID: "bob", FencingToken: bob.FencingToken,
	}); err != nil {
		t.Fatalf("bob write with current token: %v", err)
	}
}

// TestWorktreeLeaseIsWholeWorktreeExclusive checks that the lease unit is the
// worktree, not a task: a second owner cannot take the same directory, and a
// different worktree is independent.
func TestWorktreeLeaseIsWholeWorktreeExclusive(t *testing.T) {
	clock := &leaseTestClock{t: time.Date(2026, 11, 20, 13, 0, 0, 0, time.UTC)}
	h := NewWorktreeLeaseHandler(clock)
	ctx := context.Background()

	alice, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-a", OwnerID: "alice", TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("alice lease: %v", err)
	}
	_, err = h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-a", OwnerID: "bob", TTLSeconds: 3600})
	if !IsWorktreeLeaseBusy(err) {
		t.Fatalf("bob lease of a live worktree: err = %v, want busy", err)
	}
	var busy *WorktreeLeaseBusyError
	if !errors.As(err, &busy) || busy.OwnerID != "alice" {
		t.Fatalf("busy = %+v, want holder alice", busy)
	}

	if _, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-b", OwnerID: "bob", TTLSeconds: 3600}); err != nil {
		t.Fatalf("bob lease of another worktree: %v", err)
	}
	_, err = h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-a", OwnerID: "bob", FencingToken: alice.FencingToken,
	})
	var wrongOwner *WorktreeLeaseOwnerError
	if !errors.As(err, &wrongOwner) || wrongOwner.Holder != "alice" || wrongOwner.OwnerID != "bob" {
		t.Fatalf("bob write into alice's worktree: err = %+v, want owner error for alice", err)
	}
}

// TestWorktreeLeaseFencingTokenMonotonic checks that one store issues strictly
// increasing tokens across different worktrees and across a TTL re-lease. This
// is what makes a token kept by an old owner stale after the worktree is free.
func TestWorktreeLeaseFencingTokenMonotonic(t *testing.T) {
	clock := &leaseTestClock{t: time.Date(2026, 11, 20, 14, 0, 0, 0, time.UTC)}
	h := NewWorktreeLeaseHandler(clock)
	ctx := context.Background()

	first, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-1", OwnerID: "alice", TTLSeconds: 60})
	if err != nil {
		t.Fatalf("first lease: %v", err)
	}
	second, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-2", OwnerID: "bob", TTLSeconds: 60})
	if err != nil {
		t.Fatalf("second lease: %v", err)
	}
	if second.FencingToken <= first.FencingToken {
		t.Fatalf("second token = %d, want > %d", second.FencingToken, first.FencingToken)
	}

	clock.Advance(2 * time.Minute)
	third, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-1", OwnerID: "carol", TTLSeconds: 60})
	if err != nil {
		t.Fatalf("re-lease after TTL: %v", err)
	}
	if third.FencingToken <= second.FencingToken {
		t.Fatalf("re-lease token = %d, want > %d", third.FencingToken, second.FencingToken)
	}
	if _, err := h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-1", OwnerID: "alice", FencingToken: first.FencingToken,
	}); !IsStaleWorktreeLease(err) {
		t.Fatalf("old token after re-lease: err = %v, want stale worktree lease", err)
	}
}

// TestWorktreeLeaseRelease checks explicit release: only the owner with the
// current token can free the lease, and the token stops authorizing writes
// afterwards.
func TestWorktreeLeaseRelease(t *testing.T) {
	clock := &leaseTestClock{t: time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)}
	h := NewWorktreeLeaseHandler(clock)
	ctx := context.Background()

	lease, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-r", OwnerID: "alice", TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if _, err := h.Release(ctx, WorktreeReleaseInput{
		WorktreeID: "wt-r", OwnerID: "alice", FencingToken: lease.FencingToken + 1,
	}); !IsStaleWorktreeLease(err) {
		t.Fatalf("release with wrong token: err = %v, want stale worktree lease", err)
	}
	var notOwner *WorktreeLeaseOwnerError
	if _, err := h.Release(ctx, WorktreeReleaseInput{
		WorktreeID: "wt-r", OwnerID: "bob", FencingToken: lease.FencingToken,
	}); !errors.As(err, &notOwner) {
		t.Fatalf("release by non-owner: err = %v, want owner error", err)
	}
	if _, err := h.Release(ctx, WorktreeReleaseInput{
		WorktreeID: "wt-r", OwnerID: "alice", FencingToken: lease.FencingToken,
	}); err != nil {
		t.Fatalf("owner release: %v", err)
	}
	if _, err := h.Release(ctx, WorktreeReleaseInput{
		WorktreeID: "wt-r", OwnerID: "alice", FencingToken: lease.FencingToken,
	}); !IsWorktreeLeaseExpired(err) {
		t.Fatalf("repeat release: err = %v, want worktree lease expired", err)
	}
	if _, err := h.Write(ctx, WorktreeWriteInput{
		WorktreeID: "wt-r", OwnerID: "alice", FencingToken: lease.FencingToken,
	}); !IsWorktreeLeaseExpired(err) {
		t.Fatalf("write after release: err = %v, want worktree lease expired", err)
	}
	next, err := h.Acquire(ctx, WorktreeLeaseInput{WorktreeID: "wt-r", OwnerID: "bob", TTLSeconds: 3600})
	if err != nil {
		t.Fatalf("re-lease after release: %v", err)
	}
	if next.FencingToken <= lease.FencingToken {
		t.Fatalf("re-lease token = %d, want > %d", next.FencingToken, lease.FencingToken)
	}
}

// TestWorktreeLeaseInvalidInput checks that missing fields and a non-positive
// TTL are rejected at the boundary before the store is touched.
func TestWorktreeLeaseInvalidInput(t *testing.T) {
	h := NewWorktreeLeaseHandler(nil)
	ctx := context.Background()

	assertInvalid := func(t *testing.T, err error) {
		t.Helper()
		var app *domain.AppError
		if !errors.As(err, &app) || app.Code != domain.CodeInvalidInput {
			t.Fatalf("err = %v, want INVALID_INPUT", err)
		}
	}

	for _, tc := range []struct {
		name string
		in   WorktreeLeaseInput
	}{
		{"no worktree", WorktreeLeaseInput{OwnerID: "alice", TTLSeconds: 60}},
		{"no owner", WorktreeLeaseInput{WorktreeID: "wt", TTLSeconds: 60}},
		{"zero ttl", WorktreeLeaseInput{WorktreeID: "wt", OwnerID: "alice"}},
		{"negative ttl", WorktreeLeaseInput{WorktreeID: "wt", OwnerID: "alice", TTLSeconds: -1}},
	} {
		t.Run("acquire/"+tc.name, func(t *testing.T) {
			_, err := h.Acquire(ctx, tc.in)
			assertInvalid(t, err)
		})
	}
	for _, tc := range []struct {
		name string
		in   WorktreeWriteInput
	}{
		{"no worktree", WorktreeWriteInput{OwnerID: "alice", FencingToken: 1}},
		{"no owner", WorktreeWriteInput{WorktreeID: "wt", FencingToken: 1}},
		{"no token", WorktreeWriteInput{WorktreeID: "wt", OwnerID: "alice"}},
	} {
		t.Run("write/"+tc.name, func(t *testing.T) {
			_, err := h.Write(ctx, tc.in)
			assertInvalid(t, err)
		})
	}
	for _, tc := range []struct {
		name string
		in   WorktreeReleaseInput
	}{
		{"no worktree", WorktreeReleaseInput{OwnerID: "alice", FencingToken: 1}},
		{"no owner", WorktreeReleaseInput{WorktreeID: "wt", FencingToken: 1}},
		{"no token", WorktreeReleaseInput{WorktreeID: "wt", OwnerID: "alice"}},
	} {
		t.Run("release/"+tc.name, func(t *testing.T) {
			_, err := h.Release(ctx, tc.in)
			assertInvalid(t, err)
		})
	}
}

// TestAttachWorktreeLeaseRegistersToolsOnce checks the integration hook without
// changing the default NewServer contract. A repeated attach must not duplicate
// any of the three tools.
func TestAttachWorktreeLeaseRegistersToolsOnce(t *testing.T) {
	server := NewServer()
	AttachWorktreeLease(server, NewWorktreeLeaseHandler(nil))
	AttachWorktreeLease(server, NewWorktreeLeaseHandler(nil))

	session := connectClaimServer(t, server)
	counts := map[string]int{
		LeaseWorktreeToolName:   0,
		WriteWorktreeToolName:   0,
		ReleaseWorktreeToolName: 0,
	}
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		if _, tracked := counts[tool.Name]; tracked {
			counts[tool.Name]++
		}
	}
	for _, name := range []string{LeaseWorktreeToolName, WriteWorktreeToolName, ReleaseWorktreeToolName} {
		if counts[name] != 1 {
			t.Errorf("%s registered %d times, want 1", name, counts[name])
		}
	}
}
