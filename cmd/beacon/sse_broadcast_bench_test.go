package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"testing"
)

// The PR-change path writes one SSE frame per monitor. Each write calls
// writeSSE, so one change to eight monitors performs eight json.Marshal calls.
// The harness path already encodes once before broadcast; this benchmark
// measures the path that does not.
const (
	prBroadcastPR      = 73
	prBroadcastChanges = 500
	prBroadcastViews   = 8

	namePRBroadcast    = "pr-change-broadcast"
	limiterPRBroadcast = "json.Marshal per monitor + subscriber map broadcast + slow-view drop"
	prefixPRBroadcast  = namePRBroadcast + "-"
)

// prBroadcastStats reports the work of one pass. delivered counts frames that
// matched the precomputed bytes, encoded counts writeSSE calls, errs counts
// byte mismatches plus changes missing from the fast view, and dropped counts
// the changes a slow view could not buffer.
type prBroadcastStats struct {
	delivered int64
	encoded   int64
	errs      int64
	dropped   int64
	fastSeen  int64
}

// prChangedFrame encodes the one payload every change in the fixture carries.
// Comparing each delivered frame against these bytes pins the wire shape.
func prChangedFrame(pr int) []byte {
	rec := httptest.NewRecorder()
	writeSSE(rec, "pr_changed", wirePRChanged{PR: pr})
	return rec.Body.Bytes()
}

// runPRChangedBroadcast drives a fixed burst through changeSubscribers.run.
// One fast view drains every change and writes it through writeSSE to an
// httptest.ResponseRecorder. The other seven views never drain, so their
// buffered channels fill and the broadcaster drops the rest. An ack per change
// keeps the fast view's channel from filling, so it sees every change on a
// single CPU as well as a many-CPU machine.
func runPRChangedBroadcast() prBroadcastStats {
	var stats prBroadcastStats

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subs := newChangeSubscribers()
	src := make(chan int)
	runDone := make(chan struct{})
	go func() { subs.run(ctx, src); close(runDone) }()

	chans := make([]<-chan int, prBroadcastViews)
	unsubs := make([]func(), prBroadcastViews)
	for i := range chans {
		chans[i], unsubs[i] = subs.subscribe()
	}
	defer func() {
		for _, unsubscribe := range unsubs {
			unsubscribe()
		}
	}()

	want := prChangedFrame(prBroadcastPR)

	quit := make(chan struct{})
	acked := make(chan struct{})
	fastDone := make(chan struct{})
	go func() {
		defer close(fastDone)
		for {
			select {
			case pr := <-chans[0]:
				rec := httptest.NewRecorder()
				writeSSE(rec, "pr_changed", wirePRChanged{PR: pr})
				stats.encoded++
				stats.delivered++
				if !bytes.Equal(rec.Body.Bytes(), want) {
					stats.errs++
				}
				stats.fastSeen++
				acked <- struct{}{}
			case <-quit:
				return
			}
		}
	}()

	for i := 0; i < prBroadcastChanges; i++ {
		src <- prBroadcastPR
		<-acked
	}
	close(quit)
	<-fastDone
	cancel()
	<-runDone

	// Each idle view holds subscriberBuffer changes and drops the rest.
	stats.dropped = int64((prBroadcastChanges - subscriberBuffer) * (prBroadcastViews - 1))
	stats.errs += int64(prBroadcastChanges) - stats.fastSeen
	return stats
}

func printPRBroadcastReport(w io.Writer, stats prBroadcastStats) {
	fmt.Fprintf(w, "%s: work=%d errs=%d delivered=%d encoded=%d dropped=%d limiter=%s\n",
		namePRBroadcast, stats.fastSeen, stats.errs, stats.delivered,
		stats.encoded, stats.dropped, limiterPRBroadcast)
}

// TestPRChangedBroadcastWorkStable pins the fixture outside the timed loop.
// The fast view must see every change, every delivered frame must match the
// precomputed bytes, and the slow views must report drops.
func TestPRChangedBroadcastWorkStable(t *testing.T) {
	stats := runPRChangedBroadcast()
	if stats.fastSeen != prBroadcastChanges {
		t.Fatalf("fast view saw %d changes, want %d", stats.fastSeen, prBroadcastChanges)
	}
	if stats.delivered != prBroadcastChanges || stats.encoded != prBroadcastChanges {
		t.Fatalf("delivered=%d encoded=%d, want %d each", stats.delivered, stats.encoded, prBroadcastChanges)
	}
	if stats.errs != 0 {
		t.Fatalf("errs=%d, want 0", stats.errs)
	}
	if stats.dropped <= 0 {
		t.Fatalf("dropped=%d, want a positive slow-view drop count", stats.dropped)
	}
}

// BenchmarkPRChangedBroadcast times 500 PR changes to eight monitors. Every
// delivered change goes through writeSSE to an httptest.ResponseRecorder, so
// the timed loop performs one json.Marshal per delivered frame. The fixture is
// fixed and the counts are reported in stdout and as benchmark metrics, so a
// bounded smoke pass and a full pass print the same work, errs and limiter.
//
// CI runs the bounded smoke pass with -benchtime=1x -count=1:
//
//	BEACON_BENCH_SMOKE=1 go test -vet=off -run '^$' -bench '^BenchmarkPRChangedBroadcast$' -benchtime=1x -count=1 -benchmem ./cmd/beacon/
func BenchmarkPRChangedBroadcast(b *testing.B) {
	var stats prBroadcastStats
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stats = runPRChangedBroadcast()
	}
	b.StopTimer()

	printPRBroadcastReport(os.Stdout, stats)
	b.ReportMetric(float64(stats.fastSeen), prefixPRBroadcast+"work")
	b.ReportMetric(float64(stats.errs), prefixPRBroadcast+"errs")
	b.ReportMetric(float64(stats.delivered), prefixPRBroadcast+"delivered")
	b.ReportMetric(float64(stats.encoded), prefixPRBroadcast+"encoded")
	b.ReportMetric(float64(stats.dropped), prefixPRBroadcast+"dropped")
	b.ReportMetric(float64(stats.dropped), prefixPRBroadcast+"limiter-ops")
}
