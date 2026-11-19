package mcp

import (
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func requestWithMeta(meta mcpsdk.Meta) *mcpsdk.CallToolRequest {
	return &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{Meta: meta}}
}

func TestSessionIDFromRequestReadsKeyValueVerbatim(t *testing.T) {
	const value = "sess/with:symbols#and-dashes"
	got := SessionIDFromRequest(requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: value}))
	if got == nil {
		t.Fatalf("SessionIDFromRequest() = nil, want %q", value)
	}
	if *got != value {
		t.Errorf("SessionIDFromRequest() = %q, want %q", *got, value)
	}
}

func TestSessionIDFromRequestMissingKeyReturnsNil(t *testing.T) {
	got := SessionIDFromRequest(requestWithMeta(mcpsdk.Meta{"other/key": "value"}))
	if got != nil {
		t.Errorf("SessionIDFromRequest() = %q, want nil when the key is absent", *got)
	}
}

func TestSessionIDFromRequestEmptyValueReturnsNil(t *testing.T) {
	got := SessionIDFromRequest(requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: ""}))
	if got != nil {
		t.Errorf("SessionIDFromRequest() = %q, want nil for an empty value", *got)
	}
}

func TestSessionIDFromRequestAbsentMetaDoesNotBreak(t *testing.T) {
	cases := map[string]*mcpsdk.CallToolRequest{
		"nil request":     nil,
		"nil params":      {},
		"nil meta":        {Params: &mcpsdk.CallToolParamsRaw{}},
		"non-string meta": requestWithMeta(mcpsdk.Meta{sessionIDMetaKey: 42}),
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SessionIDFromRequest(req); got != nil {
				t.Errorf("SessionIDFromRequest() = %q, want nil", *got)
			}
		})
	}
}
