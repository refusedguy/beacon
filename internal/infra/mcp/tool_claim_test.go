package mcp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

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
