package httpapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/internal/domain"
)

// schemaDoc is the parsed OpenAPI file. The validator below covers the subset
// the contract uses: local $ref, type, required, properties, items, enum and
// additionalProperties. It needs no network and no schema dependency beyond
// the YAML parser already in the module graph.
type schemaDoc struct {
	root map[string]any
}

func loadOpenAPI(t *testing.T) schemaDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("../../../docs/api/openapi.yaml"))
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	return schemaDoc{root: root}
}

func (d schemaDoc) resolve(ref string) (map[string]any, error) {
	if !strings.HasPrefix(ref, "#/") {
		return nil, fmt.Errorf("unsupported ref %q", ref)
	}
	var cur any = d.root
	for _, raw := range strings.Split(strings.TrimPrefix(ref, "#/"), "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		node, ok := cur.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("ref %q: expected object before %q", ref, part)
		}
		cur, ok = node[part]
		if !ok {
			return nil, fmt.Errorf("ref %q: missing %q", ref, part)
		}
	}
	node, ok := cur.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("ref %q: target is not an object", ref)
	}
	return node, nil
}

func (d schemaDoc) schema(t *testing.T, name string) map[string]any {
	t.Helper()
	components, ok := d.root["components"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI contract has no components")
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI contract has no components.schemas")
	}
	schema, ok := schemas[name].(map[string]any)
	if !ok {
		t.Fatalf("OpenAPI contract has no schema %q", name)
	}
	return schema
}

func matchesType(decl any, value any) bool {
	types, ok := decl.([]any)
	if !ok {
		types = []any{decl}
	}
	for _, raw := range types {
		name, ok := raw.(string)
		if !ok {
			continue
		}
		switch name {
		case "object":
			if _, ok := value.(map[string]any); ok {
				return true
			}
		case "array":
			if _, ok := value.([]any); ok {
				return true
			}
		case "string":
			if _, ok := value.(string); ok {
				return true
			}
		case "integer":
			switch value.(type) {
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
				return true
			case float64:
				return value.(float64) == math.Trunc(value.(float64))
			}
		case "number":
			switch value.(type) {
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float64, float32:
				return true
			}
		case "boolean":
			if _, ok := value.(bool); ok {
				return true
			}
		case "null":
			if value == nil {
				return true
			}
		}
	}
	return false
}

func declaredProperties(schema map[string]any) map[string]bool {
	out := map[string]bool{}
	if props, ok := schema["properties"].(map[string]any); ok {
		for name := range props {
			out[name] = true
		}
	}
	return out
}

func (d schemaDoc) validate(schema map[string]any, value any, path string) []string {
	if ref, ok := schema["$ref"].(string); ok {
		resolved, err := d.resolve(ref)
		if err != nil {
			return []string{fmt.Sprintf("%s: %v", path, err)}
		}
		return d.validate(resolved, value, path)
	}
	var errs []string
	if decl, ok := schema["type"]; ok && !matchesType(decl, value) {
		return []string{fmt.Sprintf("%s: expected %v, got %T", path, decl, value)}
	}
	if enum, ok := schema["enum"].([]any); ok {
		found := false
		for _, item := range enum {
			if reflect.DeepEqual(item, value) {
				found = true
				break
			}
		}
		if !found {
			errs = append(errs, fmt.Sprintf("%s: %v is not in enum %v", path, value, enum))
		}
	}

	known := declaredProperties(schema)
	switch v := value.(type) {
	case map[string]any:
		if props, ok := schema["properties"].(map[string]any); ok {
			for name, raw := range props {
				if child, present := v[name]; present {
					sub, ok := raw.(map[string]any)
					if !ok {
						errs = append(errs, fmt.Sprintf("%s.%s: bad schema", path, name))
						continue
					}
					errs = append(errs, d.validate(sub, child, path+"."+name)...)
				}
			}
		}
		if required, ok := schema["required"].([]any); ok {
			for _, raw := range required {
				name, ok := raw.(string)
				if !ok {
					continue
				}
				if _, present := v[name]; !present {
					errs = append(errs, fmt.Sprintf("%s: missing required field %q", path, name))
				}
			}
		}
		switch extra := schema["additionalProperties"].(type) {
		case bool:
			if !extra {
				for name := range v {
					if !known[name] {
						errs = append(errs, fmt.Sprintf("%s: unexpected field %q", path, name))
					}
				}
			}
		case map[string]any:
			for name, child := range v {
				if !known[name] {
					errs = append(errs, d.validate(extra, child, path+"."+name)...)
				}
			}
		}
	case []any:
		if items, ok := schema["items"].(map[string]any); ok {
			for i, child := range v {
				errs = append(errs, d.validate(items, child, fmt.Sprintf("%s[%d]", path, i))...)
			}
		}
	}
	return errs
}

func (d schemaDoc) validateBody(t *testing.T, name string, body []byte) {
	t.Helper()
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		t.Fatalf("%s body is not JSON: %v (%s)", name, err, body)
	}
	if errs := d.validate(d.schema(t, name), value, name); len(errs) > 0 {
		t.Errorf("%s violates the contract:\n  %s", name, strings.Join(errs, "\n  "))
	}
}

func (d schemaDoc) validateAllExamples(t *testing.T, node any, path string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]any:
		if schema, ok := v["schema"].(map[string]any); ok {
			if examples, ok := v["examples"].(map[string]any); ok {
				for name, raw := range examples {
					example, ok := raw.(map[string]any)
					if !ok {
						continue
					}
					value, ok := example["value"]
					if !ok {
						continue
					}
					if errs := d.validate(schema, value, path+".examples."+name); len(errs) > 0 {
						t.Errorf("example %s violates the contract:\n  %s", path+"."+name, strings.Join(errs, "\n  "))
					}
				}
			}
		}
		for name, child := range v {
			d.validateAllExamples(t, child, path+"."+name)
		}
	case []any:
		for i, child := range v {
			d.validateAllExamples(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

func TestOpenAPIDocument(t *testing.T) {
	doc := loadOpenAPI(t)
	if version, _ := doc.root["openapi"].(string); version != "3.1.0" {
		t.Fatalf("openapi = %q, want 3.1.0", version)
	}
	paths, ok := doc.root["paths"].(map[string]any)
	if !ok {
		t.Fatal("OpenAPI contract has no paths")
	}
	for _, route := range []string{"/api/v1/health", "/api/v1/snapshot", "/api/v1/events", "/api/v1/human/decide"} {
		if _, ok := paths[route]; !ok {
			t.Errorf("OpenAPI contract has no path %q", route)
		}
	}
	components := doc.root["components"].(map[string]any)
	security := components["securitySchemes"].(map[string]any)
	bearer, ok := security["bearerAuth"].(map[string]any)
	if !ok || bearer["type"] != "http" || bearer["scheme"] != "bearer" {
		t.Fatalf("bearerAuth = %+v", bearer)
	}
	doc.validateAllExamples(t, doc.root, "openapi")
}

func contractWatch() domain.Watch {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return domain.Watch{
		ID: "w1", Repo: "refusedguy/beacon", PR: 7, HeadSHA: "abc", BaseRef: "main", Issue: "42",
		Status: domain.StatusWatching, Verdict: domain.VerdictReady, Revision: 3,
		Checks: map[string]domain.Check{
			"ci": {State: "completed", Conclusion: "success", Required: true, DetailsURL: "https://example.test/checks/1"},
		},
		Merge:       domain.MergeInfo{Mergeable: "MERGEABLE", MergeStateStatus: "CLEAN", ReviewDecision: "APPROVED"},
		Flaky:       domain.FlakyConfig{Names: []string{}, MaxRetries: 2, Used: map[string]int{}},
		Policy:      domain.Policy{RequireUpToDate: true, MaxWakesPerWatch: 6},
		MergePolicy: domain.MergePolicy{AutoMerge: true, WaitFor: []string{}, WaitForMain: []string{}},
		Subscribers: []domain.Subscriber{{
			ID: "s1", Role: domain.RoleExecutor, Adapter: "opencode", Target: "ses_1",
			AddedAt: now, LastActivity: now,
		}},
		DeadlineAt: now, NoChecksSince: now, CreatedAt: now, ClosedAt: now, CloseReason: "",
	}
}

func contractSnapshot() Snapshot {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	return Snapshot{
		LiveWatches: []domain.Watch{contractWatch()},
		BlockerIncidents: []domain.BlockerIncident{{
			ID: "b1", Repo: "refusedguy/beacon", BaseRef: "main", Signature: "ci:red",
			FailedChecks: []string{}, BlockedWatchIDs: []string{"w1"},
			MaxFixAttempts: 2, Status: domain.BlockerStatusOpen, CreatedAt: now, ResolvedAt: now,
		}},
		BoundOrchestrator: &domain.OrchestratorBinding{
			ProjectKey: "beacon", SessionID: "ses_orchestrator", Adapter: "opencode", BoundAt: now,
		},
		Escalations: []string{},
		PointStats:  PointStats{Used: 12, Budget: 500},
		Metrics:     json.RawMessage(`{"poll_cycles_total":3}`),
		LastCycle:   LastCycle{At: now, WatchesUpdated: 2, DurationMs: 15},
		LoadErrors:  []string{},
	}
}

func TestContractHealth(t *testing.T) {
	doc := loadOpenAPI(t)
	srv := newTestServer(t, stubProvider{}, &stubSink{}, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, authorized(httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)))
	if w.Code != http.StatusOK {
		t.Fatalf("health status = %d", w.Code)
	}
	doc.validateBody(t, "HealthResponse", w.Body.Bytes())

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("health without token = %d", w.Code)
	}
	doc.validateBody(t, "Error", w.Body.Bytes())
}

func TestContractSnapshot(t *testing.T) {
	doc := loadOpenAPI(t)
	now := time.Date(2026, 11, 20, 11, 58, 0, 0, time.UTC)
	history := SnapshotHistory{
		RecentWatches: []WatchHistory{{
			ID: "w9", Repo: "refusedguy/beacon", PR: 9, HeadSHA: "deadbeef",
			Outcome: OutcomeMerged, ClosedAt: now,
		}},
		HourlyStats: HourlyStats{Points: 12, Merges: 3, Blockers: 1},
	}
	srv := newTestServer(t, historyProvider{stubProvider: stubProvider{snap: contractSnapshot()}, history: history}, &stubSink{}, NewEventBus(0))

	status, body := getSnapshotBody(t, srv)
	if status != http.StatusOK {
		t.Fatalf("snapshot status = %d, body %s", status, body)
	}
	doc.validateBody(t, "SnapshotResponse", body)

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"recentWatches", "hourlyStats"} {
		if _, ok := raw[field]; !ok {
			t.Errorf("snapshot omitted %q with a history provider", field)
		}
	}
}

func TestContractSnapshotWithoutHistory(t *testing.T) {
	doc := loadOpenAPI(t)
	srv := newTestServer(t, stubProvider{snap: contractSnapshot()}, &stubSink{}, NewEventBus(0))

	status, body := getSnapshotBody(t, srv)
	if status != http.StatusOK {
		t.Fatalf("snapshot status = %d", status)
	}
	doc.validateBody(t, "SnapshotResponse", body)
}

func TestContractDecide(t *testing.T) {
	doc := loadOpenAPI(t)
	sink := &stubSink{result: DecisionResult{DecisionID: "journal-9", Status: "watching/ready"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(`{"command":"approve","watchID":"w1"}`, "http://127.0.0.1:7432"))
	if w.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body %s", w.Code, w.Body.String())
	}
	doc.validateBody(t, "DecisionResult", w.Body.Bytes())

	cases := []struct {
		name string
		req  *http.Request
	}{
		{"unauthorized", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/human/decide", strings.NewReader(`{"command":"approve","watchID":"w1"}`))
			r.Header.Set("Content-Type", "application/json")
			return r
		}()},
		{"forbidden", decideRequest(`{"command":"approve","watchID":"w1"}`, "http://evil.example")},
		{"badInput", decideRequest(`{"command":"destroy","watchID":"w1"}`, "")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			srv.handler().ServeHTTP(w, tc.req)
			doc.validateBody(t, "Error", w.Body.Bytes())
		})
	}
}

func TestContractDecideVersioned(t *testing.T) {
	doc := loadOpenAPI(t)
	sink := &stubSink{result: DecisionResult{DecisionID: "journal-9"}}
	srv := newTestServer(t, stubProvider{}, sink, NewEventBus(0))

	w := httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(
		`{"command":"approve","watchID":"w1","actor":"ext-app","contract":"beacon.human-decide.v1"}`,
		"http://127.0.0.1:7432",
	))
	if w.Code != http.StatusOK {
		t.Fatalf("decide status = %d, body %s", w.Code, w.Body.String())
	}
	doc.validateBody(t, "DecisionResult", w.Body.Bytes())
	var result DecisionResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Contract != HumanDecideContract {
		t.Fatalf("contract = %q, want %q", result.Contract, HumanDecideContract)
	}
	if len(sink.calls) != 1 || sink.calls[0].Actor != "ext-app" {
		t.Fatalf("sink calls = %+v", sink.calls)
	}

	w = httptest.NewRecorder()
	srv.handler().ServeHTTP(w, decideRequest(
		`{"command":"approve","watchID":"w1","contract":"beacon.human-decide.v9"}`,
		"http://127.0.0.1:7432",
	))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsupported contract status = %d, body %s", w.Code, w.Body.String())
	}
	doc.validateBody(t, "Error", w.Body.Bytes())
	if !strings.Contains(w.Body.String(), "UNSUPPORTED_CONTRACT") {
		t.Fatalf("body = %s, want UNSUPPORTED_CONTRACT", w.Body.String())
	}
	if len(sink.calls) != 1 {
		t.Fatalf("unsupported contract reached the sink: %+v", sink.calls)
	}
}

func TestContractEvents(t *testing.T) {
	doc := loadOpenAPI(t)
	bus := NewEventBus(10)
	bus.Publish(Event{Type: "cycle", Payload: json.RawMessage(`{"n":1}`)})
	srv := newTestServer(t, stubProvider{}, &stubSink{}, bus)
	httpSrv := httptest.NewServer(srv.handler())
	defer httpSrv.Close()

	resp, reader, cancel := connectEvents(t, srv, httpSrv.URL+"/api/v1/events?replay=1")
	defer cancel()
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	frame := readFrame(t, reader)
	data, ok := frame["data"]
	if !ok {
		t.Fatalf("SSE frame has no data: %v", frame)
	}
	doc.validateBody(t, "Event", []byte(data))
}
