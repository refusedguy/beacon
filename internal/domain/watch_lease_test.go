package domain

import (
	"testing"
	"time"
)

// TestWatchHandleCarriesIssueTimeAndTTL proves the handle is self describing:
// both the issue time and the lease length can be read back from the string.
func TestWatchHandleCarriesIssueTimeAndTTL(t *testing.T) {
	issuedAt := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	handle := watchID("org/repo", 7, "abc123", issuedAt)

	gotIssued, gotTTL, ok := ParseWatchHandle(handle)
	if !ok {
		t.Fatalf("ParseWatchHandle(%q) returned ok=false", handle)
	}
	if !gotIssued.Equal(issuedAt) {
		t.Errorf("issuedAt = %v, want %v", gotIssued, issuedAt)
	}
	if gotTTL != WatchHandleTTL {
		t.Errorf("ttl = %v, want %v", gotTTL, WatchHandleTTL)
	}
	if NewWatchHandle("org/repo", 7, "abc123", issuedAt) != handle {
		t.Error("NewWatchHandle must build the same handle as watchID")
	}
}

// TestWatchHandleLiveAndDead covers the two handle states the MCP layer relies
// on: a handle inside its lease is usable, one past it is stale.
func TestWatchHandleLiveAndDead(t *testing.T) {
	issuedAt := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	handle := watchID("org/repo", 7, "abc123", issuedAt)

	if WatchHandleExpired(handle, issuedAt.Add(WatchHandleTTL-time.Second)) {
		t.Error("handle inside its lease must not be expired")
	}
	if !WatchHandleExpired(handle, issuedAt.Add(WatchHandleTTL)) {
		t.Error("handle at its deadline must be expired")
	}
	if !WatchHandleExpired(handle, issuedAt.Add(WatchHandleTTL+time.Hour)) {
		t.Error("handle past its lease must be expired")
	}
}

// TestWatchHandleWithoutLeaseNeverExpires keeps pre-lease handles working.
func TestWatchHandleWithoutLeaseNeverExpires(t *testing.T) {
	legacy := "watch:org/repo:7:abc123"
	if _, _, ok := ParseWatchHandle(legacy); ok {
		t.Fatalf("legacy handle %q must not parse as leased", legacy)
	}
	farFuture := time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
	if WatchHandleExpired(legacy, farFuture) {
		t.Error("a handle without a lease must never expire")
	}
}

// TestParseWatchHandleRejectsMalformedSuffix guards the parser against strings
// that look leased but carry no valid numbers.
func TestParseWatchHandleRejectsMalformedSuffix(t *testing.T) {
	for _, handle := range []string{
		"watch:org/repo:7:abc123@not-a-time:1800",
		"watch:org/repo:7:abc123@1763712000:not-a-ttl",
		"watch:org/repo:7:abc123@1763712000:0",
		"watch:org/repo:7:abc123@1763712000",
	} {
		if _, _, ok := ParseWatchHandle(handle); ok {
			t.Errorf("ParseWatchHandle(%q) accepted a malformed suffix", handle)
		}
	}
}
