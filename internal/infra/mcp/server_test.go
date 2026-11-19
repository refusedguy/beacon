package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectTestSession wires a client and server over in-memory transports.
func connectTestSession(t *testing.T) *mcpsdk.ClientSession {
	t.Helper()
	ctx := context.Background()

	serverTransport, clientTransport := mcpsdk.NewInMemoryTransports()
	serverSession, err := NewServer().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "beacon-test", Version: "v0.0.1"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = serverSession.Wait()
	})
	return clientSession
}

func TestToolsListReturnsExactlySevenStubs(t *testing.T) {
	session := connectTestSession(t)

	var got []string
	for tool, err := range session.Tools(context.Background(), nil) {
		if err != nil {
			t.Fatalf("tools/list: %v", err)
		}
		got = append(got, tool.Name)
	}
	if len(got) != 7 {
		t.Fatalf("tools/list returned %d tools (%v), want 7", len(got), got)
	}

	want := slices.Clone(ToolNames)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("tools/list names = %v, want %v", got, want)
	}
}

func TestHandshakeInstructionsCarryCycleProtocol(t *testing.T) {
	session := connectTestSession(t)

	instructions := session.InitializeResult().Instructions
	if instructions == "" {
		t.Fatal("handshake returned empty instructions")
	}

	for _, want := range []string{
		"cycle protocol",
		"beacon_watch_pr",
		"beacon_wait_result",
		"beacon_get_result",
		"beacon_list_running",
		"beacon_ack",
		"beacon_cancel_watch",
		"beacon_stats",
		"waiting, watchID",
		"keep_working",
		"rebase",
		"retry",
		"ready",
		"verdict",
	} {
		if !strings.Contains(instructions, want) {
			t.Errorf("handshake instructions missing cycle-protocol token %q", want)
		}
	}
}

func TestLegacyProtocolWithoutExperimentalRevision(t *testing.T) {
	for _, version := range LegacyProtocolVersions {
		if version >= "2026-07-28" {
			t.Errorf("supported version %q is not a legacy handshake version", version)
		}
	}

	session := connectTestSession(t)
	negotiated := session.InitializeResult().ProtocolVersion
	if negotiated == "" || negotiated >= "2026-07-28" {
		t.Fatalf("negotiated protocol version = %q, want a legacy version", negotiated)
	}
	if !slices.Contains(LegacyProtocolVersions, negotiated) {
		t.Fatalf("negotiated version %q is outside the legacy set %v", negotiated, LegacyProtocolVersions)
	}
}
