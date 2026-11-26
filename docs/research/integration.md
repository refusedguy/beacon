# Integration seams with runnable examples

The first integration report named the seams between daemon, monitor and worker
in prose. It gave no example, command or captured result. This dossier runs each
seam through a test in this repository. Every snippet below is condensed from a
test file. Every output block is copied from a command run on the worker machine
on 2026-11-26. The examples need no production code change.

## Protocol

Machine: Linux x86_64, 8 logical CPUs. Toolchain:
`go version go1.27.4-X:nodwarf5 linux/amd64`.

Run the first three seams together with the card's check command:

```sh
go test ./cmd/beacon ./internal/infra/mcp -run 'TestHTTP|TestControl' -v
```

Each section also gives the command for one seam. The SSE seam uses its own
command because its test name does not match that pattern. The worker machine
leaves the full repository suite to CI.

| Seam | Boundary | Test |
| --- | --- | --- |
| Bearer auth | monitor or worker to the loopback daemon | `TestHTTPRoutesRejectMissingBearerToken` |
| Control validation | operator request to the daemon control service | `TestHTTPControlAcceptsKnownActionAndRejectsUnknownAction` |
| MCP control | worker to daemon over stdio MCP | `TestControlBatchExampleReturnsJSONResult` |
| PR change fanout | reconciler to every monitor stream | `TestPRChangeReachesReconcilerAndEveryMonitor` |

## Seam 1: auth closes all three loopback routes

`apiServer.handler` wraps `/api/v1/runs`, `/api/v1/events` and
`/api/v1/control` in `a.auth` (`cmd/beacon/http.go:110-132`). `auth` reads the
`Authorization` header, checks the `Bearer ` prefix and compares the token with
`subtle.ConstantTimeCompare`. A missing or wrong token gets 401 before a handler
runs. `/healthz` stays outside this wrapper.

The test sends each route without a header and records every service call that
gets through. Condensed from `cmd/beacon/http_test.go:220-259`:

```go
routes := []struct{ method, path string }{
	{http.MethodGet, "/api/v1/runs"},
	{http.MethodGet, "/api/v1/events"},
	{http.MethodPost, "/api/v1/control"},
}
for _, route := range routes {
	req, _ := http.NewRequest(route.method, srv.URL+route.path,
		strings.NewReader(`{"action":"cancel","run_id":"r1"}`))
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("%s %s = %d, want 401", route.method, route.path, resp.StatusCode)
	}
	resp.Body.Close()
}
if len(services.recorded()) != 0 {
	t.Fatalf("service was reached without a token: %v", services.recorded())
}
```

The POST body is a valid cancel request. Without the token, the control handler
must not read it.

Command:

```sh
go test ./cmd/beacon -run '^TestHTTPRoutesRejectMissingBearerToken$' -v
```

Observed output:

```text
=== RUN   TestHTTPRoutesRejectMissingBearerToken
=== RUN   TestHTTPRoutesRejectMissingBearerToken/GET_/api/v1/runs
    http_test.go:252: GET /api/v1/runs without token -> 401 "unauthorized"
=== RUN   TestHTTPRoutesRejectMissingBearerToken/GET_/api/v1/events
    http_test.go:252: GET /api/v1/events without token -> 401 "unauthorized"
=== RUN   TestHTTPRoutesRejectMissingBearerToken/POST_/api/v1/control
    http_test.go:252: POST /api/v1/control without token -> 401 "unauthorized"
=== NAME  TestHTTPRoutesRejectMissingBearerToken
    http_test.go:258: service calls after three rejected requests: 0
--- PASS: TestHTTPRoutesRejectMissingBearerToken (0.00s)
    --- PASS: TestHTTPRoutesRejectMissingBearerToken/GET_/api/v1/runs (0.00s)
    --- PASS: TestHTTPRoutesRejectMissingBearerToken/GET_/api/v1/events (0.00s)
    --- PASS: TestHTTPRoutesRejectMissingBearerToken/POST_/api/v1/control (0.00s)
PASS
ok  	github.com/refusedguy/beacon/cmd/beacon	0.008s
```

The three 401 lines come from the route table. The last log line gives the
service call count. A handler that skipped `auth` would fail the status
assertion before reaching that count.

## Seam 2: control validation at the loopback boundary

`apiServer.control` decodes at most 64 KiB of JSON, parses the action and writes
a `WireControlResult` on success (`cmd/beacon/http.go:147-192`). Unknown actions
get 400 and never reach the control service. The test calls the same route twice
with a valid token. The first call proves the route can succeed, so a handler
that rejects every request cannot pass. Condensed from
`cmd/beacon/http_test.go:261-304`:

```go
status, body := post(t, `{"action":"cancel","run_id":"r1"}`)
if status != http.StatusOK || body != `{"ok":true,"action":"cancel","run_id":"r1","applied":true}` {
	t.Fatalf("known action -> %d %s, want 200 and the control result", status, body)
}

status, body = post(t, `{"action":"teleport","run_id":"r1"}`)
if status != http.StatusBadRequest || body != "unknown action" {
	t.Fatalf("unknown action -> %d %q, want 400", status, body)
}
if calls := services.recorded(); len(calls) != 1 {
	t.Fatalf("service calls after unknown action = %v, want [cancel r1] only", calls)
}
```

The response body is the exact wire shape. `applied` is true because the daemon
accepted the action. The second call leaves the recorded call count at one.

Command:

```sh
go test ./cmd/beacon -run '^TestHTTPControlAcceptsKnownActionAndRejectsUnknownAction$' -v
```

Observed output:

```text
=== RUN   TestHTTPControlAcceptsKnownActionAndRejectsUnknownAction
    http_test.go:292: POST /api/v1/control {"action":"cancel","run_id":"r1"} with token -> 200 {"ok":true,"action":"cancel","run_id":"r1","applied":true}
    http_test.go:302: POST /api/v1/control {"action":"teleport","run_id":"r1"} with token -> 400 "unknown action", service calls still 1
--- PASS: TestHTTPControlAcceptsKnownActionAndRejectsUnknownAction (0.00s)
PASS
ok  	github.com/refusedguy/beacon/cmd/beacon	0.008s
```

Both lines use the same token. Only the action differs, so the status change
comes from action parsing.

## Seam 3: MCP control returns one JSON result per run

A worker calls `beacon_control` over stdio. With `run_ids`, the tool returns a
JSON batch with one item per ID (`internal/infra/mcp/tool_control.go:33-96`).
Each item carries its own status, so one bad ID does not hide a good one. The
example starts with r1 in `blocked` and r2 in `pending`. Unblock is valid only
for a blocked run. The full test is at
`internal/infra/mcp/tool_control_test.go:526-560`:

```go
const request = `{"action":"unblock","run_ids":["r1","r2"]}`
var arguments map[string]any
if err := json.Unmarshal([]byte(request), &arguments); err != nil {
	t.Fatal(err)
}
res := callControl(t, cs, arguments)
got := parseControlBatch(t, res)
```

The test then checks both result items and reads the stored states back. The
stored states are the observable effect of the call, not just the returned text.

Command:

```sh
go test ./internal/infra/mcp -run '^TestControlBatchExampleReturnsJSONResult$' -v
```

Observed output:

```text
=== RUN   TestControlBatchExampleReturnsJSONResult
    tool_control_test.go:557: request: {"action":"unblock","run_ids":["r1","r2"]}
    tool_control_test.go:558: result: {"action":"unblock","results":[{"id":"r1","status":"ok"},{"id":"r2","status":"error","error":"invalid_transition: cannot unblock from pending"}]}
    tool_control_test.go:559: r1 state=waiting_ci r2 state=pending
--- PASS: TestControlBatchExampleReturnsJSONResult (0.01s)
PASS
ok  	github.com/refusedguy/beacon/internal/infra/mcp	0.024s
```

The result has `ok` for r1 and `invalid_transition` for r2. r1 moved to
`waiting_ci`. r2 stayed `pending`, so the rejected ID produced no state write.

## Seam 4: a PR change reaches every monitor

The daemon broadcasts PR numbers from the reconciler to each SSE connection.
`changeSubscribers.run` copies one change to every subscriber and skips a full
channel (`cmd/beacon/http.go:49-65`). The existing test opens two streams, sends
one PR number and checks both frames. Condensed from
`cmd/beacon/http_test.go:84-109`:

```go
api := &apiServer{token: "secret", changes: subs}
srv := httptest.NewServer(api.handler())
defer srv.Close()

first, closeFirst := connectSSE(t, srv, "secret")
defer closeFirst()
second, closeSecond := connectSSE(t, srv, "secret")
defer closeSecond()

src <- 42

for name, stream := range map[string]*bufio.Scanner{"first": first, "second": second} {
	data := sseData(t, stream, "pr_changed")
	if !strings.Contains(data, `"pr":42`) {
		t.Fatalf("%s monitor got %q, want pr 42", name, data)
	}
}
```

The frame is a notification. The monitor re-reads `/api/v1/runs` after it and
does not treat the frame as run state. `docs/research/functional.md` traces that
path through the TUI.

Command:

```sh
go test ./cmd/beacon -run '^TestPRChangeReachesReconcilerAndEveryMonitor$' -v
```

Observed output:

```text
=== RUN   TestPRChangeReachesReconcilerAndEveryMonitor
--- PASS: TestPRChangeReachesReconcilerAndEveryMonitor (0.00s)
PASS
ok  	github.com/refusedguy/beacon/cmd/beacon	0.022s
```

This test has no log lines. Its exit status is the evidence: both streams
received the `pr_changed` frame and the reconciler view woke on the same change.

## Scope of these examples

The HTTP seams use `httptest.NewServer`, which binds a loopback port. The MCP
seam uses an in-memory transport. No test calls GitHub, starts an agent or
touches a worktree.

Two parts of the contract stay outside these tests. The TUI client rejects
endpoints whose host is not loopback (`ui/tui/api.go:117-120`). The generated
`opencode.json` permissions decide which agents may call the orchestrator tools.
Both rules are documented in `AGENTS.md`.
