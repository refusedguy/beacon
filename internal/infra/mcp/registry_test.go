package mcp

import (
	"context"
	"strings"
	"testing"

	msdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/pkg/core"
)

// TestRequireOrchestrator pins the role gate for the three metadata shapes a
// caller can send. A refusal must carry the permission code and name both the
// role it saw and the role it needs, so the caller can diagnose the denial.
func TestRequireOrchestrator(t *testing.T) {
	tests := []struct {
		name      string
		meta      msdk.Meta
		wantCode  core.ErrorCode
		wantRoles []string
	}{
		{
			name:      "worker",
			meta:      msdk.Meta{"beacon/agent": "worker"},
			wantCode:  core.CodePermissionDenied,
			wantRoles: []string{"worker", "orchestrator"},
		},
		{
			name:      "absent",
			meta:      nil,
			wantCode:  core.CodePermissionDenied,
			wantRoles: []string{"absent", "orchestrator"},
		},
		{
			name: "orchestrator",
			meta: msdk.Meta{"ai.opencode/agent": "orchestrator"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := &msdk.CallToolRequest{Params: &msdk.CallToolParamsRaw{Meta: tc.meta}}
			err := requireOrchestrator(req)
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("requireOrchestrator(%s) = %v, want nil", tc.name, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("requireOrchestrator(%s) = nil, want coded refusal", tc.name)
			}
			if got := core.CodeOf(err); got != tc.wantCode {
				t.Errorf("code = %q, want %q", got, tc.wantCode)
			}
			message := err.Error()
			for _, role := range tc.wantRoles {
				if !strings.Contains(message, role) {
					t.Errorf("message %q does not name role %q", message, role)
				}
			}
		})
	}
}

// TestOrchestratorToolRefusesAbsentMetadata checks the gate where callers meet
// it. With no services wired, any execution would fail differently, so a coded
// refusal proves the tool stopped before its handler ran.
func TestOrchestratorToolRefusesAbsentMetadata(t *testing.T) {
	server := NewServer(Services{})
	clientTransport, serverTransport := msdk.NewInMemoryTransports()
	ss, err := server.Connect(context.Background(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := msdk.NewClient(&msdk.Implementation{Name: "test-client"}, nil)
	cs, err := client.Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.CallTool(context.Background(), &msdk.CallToolParams{
		Name: "beacon_task_create", Arguments: map[string]any{"title": "x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError || len(res.Content) == 0 {
		t.Fatalf("result = %+v, want a tool error", res)
	}
	text, ok := res.Content[0].(*msdk.TextContent)
	if !ok {
		t.Fatalf("content = %+v, want text", res.Content)
	}
	for _, want := range []string{string(core.CodePermissionDenied), "observed role \"absent\"", "required role \"orchestrator\""} {
		if !strings.Contains(text.Text, want) {
			t.Errorf("text %q does not contain %q", text.Text, want)
		}
	}
}
