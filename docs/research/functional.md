# Event to rendered run

This note records the path an SSE event takes through the monitor and the test
that proves the path end to end. The test drives a real `Client`, a real
`model`, and an `httptest` daemon. It changes no production code and no wire
shape.

## Flow

`main.go` starts `client.Stream`. Each `Event` from `/api/v1/events` reaches a
callback that sends `refreshMsg{}` to the Bubble Tea program. The callback
ignores the event body.

`model.Update(refreshMsg{})` returns `m.reload()`. Running that command calls
`Client.Runs` on `GET /api/v1/runs` and returns `runsMsg` with the fetched
runs. `model.Update(runsMsg)` stores the runs and sets the status to `loaded 1
run(s)`. `model.View()` then renders a line with the run ID and the task ID.

The test daemon serves both routes. The event route writes one SSE frame and
holds the connection open until the test cancels the context. The runs route
returns one run in the `{"runs": [...]}` envelope and records each hit in a
buffered channel.

## Minimal example

```go
client := NewClient(srv.URL, "tok")
m := newModel(client)

events := make(chan Event, 1)
go client.Stream(ctx, func(ev Event) {
	select {
	case events <- ev:
	default:
	}
})

ev := <-events                       // real Event from /api/v1/events
next, cmd := m.Update(refreshMsg{})  // what main.go sends for each Event
msg := cmd().(runsMsg)               // reload calls GET /api/v1/runs
final, _ := next.Update(msg)
view := final.(model).View()

if !strings.Contains(view, "run-42") {
	t.Fatalf("View dropped the run:\n%s", view)
}
```

The full test lives in `ui/tui/model_test.go`. It also checks the event
fields, asserts that the reload command is not nil, checks that the runs
endpoint was hit, and checks the loaded count in the status line.

## Run the test

```
cd ui/tui && go test -run TestEventRefreshesRenderedRuns -v .
```

The `ui/tui` module has one package, so `cd ui/tui && go test .` runs every
test in it. The worker machine leaves the full repository suite to CI.

## Observed output

```
=== RUN   TestEventRefreshesRenderedRuns
    model_test.go:162: event id=9 type=run.updated run_id=run-42
    model_test.go:163: reload command returned runsMsg with 1 run(s)
    model_test.go:164: rendered line: "           > run-42                   ready            pr=42    T-functional review approved"
--- PASS: TestEventRefreshesRenderedRuns (0.00s)
PASS
ok  	github.com/refusedguy/beacon/ui/tui	0.008s
```

The first line reports the event that arrived from the stream. The second
reports the `runsMsg` from the reload command. The third shows the run in the
rendered view. The assertions fail if `View` drops the run or if
`Update(refreshMsg{})` stops returning a reload command.
