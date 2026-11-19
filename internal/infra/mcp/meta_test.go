package mcp

import (
	"encoding/json"
	"reflect"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
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

func TestStreamMetaRoundTrip(t *testing.T) {
	result := &mcpsdk.CallToolResult{}

	ev := domain.StreamEvent{Seq: 7, WatchID: "w1", Payload: []byte("chunk")}
	if err := SetChunkMeta(result, ev); err != nil {
		t.Fatalf("SetChunkMeta: %v", err)
	}
	gotEv, ok := ChunkMetaFromResult(result)
	if !ok {
		t.Fatal("ChunkMetaFromResult: chunk meta not found")
	}
	if gotEv.Seq != 7 || gotEv.WatchID != "w1" || string(gotEv.Payload) != "chunk" {
		t.Errorf("chunk meta = %+v, want seq 7, watchID w1, payload chunk", gotEv)
	}

	final := domain.StreamFinal{
		Chunks:     7,
		Missing:    []uint64{2, 5},
		Duplicates: 3,
		Streamed:   true,
	}
	if err := SetFinalMeta(result, final); err != nil {
		t.Fatalf("SetFinalMeta: %v", err)
	}
	gotFinal, ok := FinalMetaFromResult(result)
	if !ok {
		t.Fatal("FinalMetaFromResult: final meta not found")
	}
	if gotFinal.Chunks != 7 || gotFinal.Duplicates != 3 || !gotFinal.Streamed {
		t.Errorf("final meta = %+v, want chunks 7, duplicates 3, streamed true", gotFinal)
	}
	if !reflect.DeepEqual(gotFinal.Missing, []uint64{2, 5}) {
		t.Errorf("final missing = %v, want [2 5]", gotFinal.Missing)
	}
}

func TestStreamMetaSurvivesResultJSONRoundTrip(t *testing.T) {
	result := &mcpsdk.CallToolResult{}
	ev := domain.StreamEvent{Seq: 2, WatchID: "w2"}
	if err := SetChunkMeta(result, ev); err != nil {
		t.Fatalf("SetChunkMeta: %v", err)
	}

	b, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	var decoded mcpsdk.CallToolResult
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	got, ok := ChunkMetaFromResult(&decoded)
	if !ok {
		t.Fatal("chunk meta lost across the wire")
	}
	if got.Seq != 2 || got.WatchID != "w2" {
		t.Errorf("chunk meta after wire = %+v, want seq 2, watchID w2", got)
	}
}

func TestStreamMetaAbsentAndInvalid(t *testing.T) {
	if _, ok := ChunkMetaFromResult(nil); ok {
		t.Error("nil result must not carry chunk meta")
	}
	if _, ok := FinalMetaFromResult(&mcpsdk.CallToolResult{}); ok {
		t.Error("result without _meta must not carry final meta")
	}
	if err := SetChunkMeta(nil, domain.StreamEvent{}); err == nil {
		t.Error("SetChunkMeta on a nil result must fail")
	}

	invalid := &mcpsdk.CallToolResult{Meta: mcpsdk.Meta{chunkMetaKey: "not an object"}}
	if _, ok := ChunkMetaFromResult(invalid); ok {
		t.Error("a malformed chunk meta must not decode")
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
