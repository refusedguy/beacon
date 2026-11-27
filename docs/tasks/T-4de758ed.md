---
id: T-4de758ed
title: Give daemon goroutines a stop path
status: available
scope:
    - cmd/beacon/config_watch.go
    - cmd/beacon/serve.go
    - cmd/beacon/mcp.go
    - cmd/beacon/http.go
priority: 2
kind: task
require_review: llm
---

Goal: make long-lived daemon goroutines stop when the daemon stops.
Evidence: config_watch.go:334 starts a relay pump with no context and its changed channel is replaced on reload so the loop never sees a close; setForwarder at :345 builds a context from context.Background and only a later setForwarder cancels it, so the last forwarder survives shutdown; init at :331-334 starts two forwarders on the same channel and cancels the first immediately; WatchIdle at :441 closes done when the port method returns, but the OpenCode implementation returns after launching watchLoop, so the wait proves nothing; the SSE handler at cmd/beacon/http.go:439 keeps three subscriptions until the HTTP request context ends, and srv.Shutdown does not cancel active requests; newPingBroadcaster allocates a ticker in the constructor that only run stops.
Steps: add a done channel to eventRelay, close it when configWatcher.Run returns, and add a done case to pump; add Stop on reloadableHarness that cancels the active forwarder and defer it in serve and in the daemon branch of mcp; drop the redundant first setForwarder in init; give the SSE handler a daemon context case so defers run on shutdown; move the ping ticker allocation into run; treat the WatchIdle port change as a separate follow-up because it touches both harness adapters.
Accept: after a clean shutdown no relay, forwarder or SSE subscription remains registered, and no ticker is owned by a constructor.
