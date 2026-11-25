package main

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// connectSSE opens a monitor stream and returns its frame scanner. The handler
// flushes the response headers on connect, so Do returns before any event.
func connectSSE(t *testing.T, srv *httptest.Server, token string) (*bufio.Scanner, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := srv.Client().Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("SSE status = %d, want 200", resp.StatusCode)
	}
	return bufio.NewScanner(resp.Body), func() {
		cancel()
		resp.Body.Close()
	}
}

// sseData reads frames until an event with the given name and returns its data.
func sseData(t *testing.T, stream *bufio.Scanner, name string) string {
	t.Helper()
	for stream.Scan() {
		if stream.Text() != "event: "+name {
			continue
		}
		if !stream.Scan() {
			t.Fatalf("SSE stream closed before %s data: %v", name, stream.Err())
		}
		return strings.TrimPrefix(stream.Text(), "data: ")
	}
	t.Fatalf("SSE stream closed before %s event: %v", name, stream.Err())
	return ""
}

func TestPRChangeReachesReconcilerAndEveryMonitor(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan int, 1)
	views := fanout(ctx, src, 2)
	subs := newChangeSubscribers()
	go subs.run(ctx, views[1])

	// The reconciler reads the first view. Stand in for it so the test can
	// observe the wake-up without standing up a full reconciler.
	woken := make(chan int, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case pr := <-views[0]:
				woken <- pr
			}
		}
	}()

	api := &apiServer{token: "secret", changes: subs}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()

	first, closeFirst := connectSSE(t, srv, "secret")
	defer closeFirst()
	second, closeSecond := connectSSE(t, srv, "secret")
	defer closeSecond()

	src <- 42

	select {
	case pr := <-woken:
		if pr != 42 {
			t.Fatalf("reconciler wake-up = %d, want 42", pr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not wake on the PR change")
	}

	for name, stream := range map[string]*bufio.Scanner{"first": first, "second": second} {
		data := sseData(t, stream, "pr_changed")
		if !strings.Contains(data, `"pr":42`) {
			t.Fatalf("%s monitor got %q, want pr 42", name, data)
		}
	}
}

func TestChangeSubscribersDropsSlowClientWithoutBlockingOthers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan int)
	subs := newChangeSubscribers()
	slow, unSlow := subs.subscribe()
	defer unSlow()
	fast, unFast := subs.subscribe()
	defer unFast()

	go subs.run(ctx, src)

	// Never drain slow. Broadcast more changes than its buffer holds.
	total := subscriberBuffer + 1
	for i := 0; i < total; i++ {
		src <- i
		select {
		case got := <-fast:
			if got != i {
				t.Fatalf("fast subscriber got %d, want %d", got, i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("fast subscriber stalled at change %d", i)
		}
	}

	// slow kept the first subscriberBuffer changes and dropped the last one.
	count := 0
	for {
		select {
		case <-slow:
			count++
		default:
			if count != subscriberBuffer {
				t.Fatalf("slow subscriber buffered %d changes, want %d", count, subscriberBuffer)
			}
			return
		}
	}
}
