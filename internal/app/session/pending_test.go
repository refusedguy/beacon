package session

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func TestAddQueuesForSession(t *testing.T) {
	q := New()
	cmd := Pending{ClientRequestID: "req-1", Kind: "question", Payload: []byte("why")}

	created, err := q.Add("ses-a", cmd)
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if !created {
		t.Fatal("Add of a new request reported false")
	}
	if got := q.Len("ses-a"); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	if got := q.Len("ses-b"); got != 0 {
		t.Fatalf("other session Len = %d, want 0", got)
	}
	got, ok := q.Pop("ses-a")
	if !ok {
		t.Fatal("Pop found no command after Add")
	}
	if !reflect.DeepEqual(got, cmd) {
		t.Fatalf("Pop = %+v, want %+v", got, cmd)
	}
}

func TestPopIsFIFO(t *testing.T) {
	q := New()
	first := Pending{ClientRequestID: "req-1", Kind: "question"}
	second := Pending{ClientRequestID: "req-2", Kind: "approval"}
	third := Pending{ClientRequestID: "req-3", Kind: "question"}
	for _, cmd := range []Pending{first, second, third} {
		if _, err := q.Add("ses-a", cmd); err != nil {
			t.Fatalf("Add(%s): %v", cmd.ClientRequestID, err)
		}
	}

	for i, want := range []Pending{first, second, third} {
		got, ok := q.Pop("ses-a")
		if !ok {
			t.Fatalf("Pop %d found no command", i)
		}
		if got.ClientRequestID != want.ClientRequestID {
			t.Fatalf("Pop %d = %s, want %s", i, got.ClientRequestID, want.ClientRequestID)
		}
	}
	if _, ok := q.Pop("ses-a"); ok {
		t.Fatal("Pop returned a command after the queue drained")
	}
}

func TestPopSeparatesSessions(t *testing.T) {
	q := New()
	a := Pending{ClientRequestID: "req-a", Kind: "question"}
	b := Pending{ClientRequestID: "req-b", Kind: "approval"}
	if _, err := q.Add("ses-a", a); err != nil {
		t.Fatalf("Add a: %v", err)
	}
	if _, err := q.Add("ses-b", b); err != nil {
		t.Fatalf("Add b: %v", err)
	}

	got, ok := q.Pop("ses-b")
	if !ok || got.ClientRequestID != b.ClientRequestID {
		t.Fatalf("Pop ses-b = %+v, %v; want %s", got, ok, b.ClientRequestID)
	}
	if got, ok := q.Pop("ses-a"); !ok || got.ClientRequestID != a.ClientRequestID {
		t.Fatalf("Pop ses-a = %+v, %v; want %s", got, ok, a.ClientRequestID)
	}
}

func TestCancelRemovesCommand(t *testing.T) {
	q := New()
	first := Pending{ClientRequestID: "req-1", Kind: "question"}
	second := Pending{ClientRequestID: "req-2", Kind: "approval"}
	last := Pending{ClientRequestID: "req-3", Kind: "question"}
	for _, cmd := range []Pending{first, second, last} {
		if _, err := q.Add("ses-a", cmd); err != nil {
			t.Fatalf("Add(%s): %v", cmd.ClientRequestID, err)
		}
	}

	if !q.Cancel("ses-a", "req-2") {
		t.Fatal("Cancel reported false for a queued command")
	}
	if q.Cancel("ses-a", "req-2") {
		t.Fatal("Cancel reported true for an already-cancelled command")
	}
	if got := q.Len("ses-a"); got != 2 {
		t.Fatalf("Len = %d, want 2", got)
	}
	order := []string{}
	for {
		cmd, ok := q.Pop("ses-a")
		if !ok {
			break
		}
		order = append(order, cmd.ClientRequestID)
	}
	want := []string{"req-1", "req-3"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
}

func TestCancelOnOtherSessionDoesNothing(t *testing.T) {
	q := New()
	if _, err := q.Add("ses-a", Pending{ClientRequestID: "req-1", Kind: "question"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if q.Cancel("ses-b", "req-1") {
		t.Fatal("Cancel removed a command from another session")
	}
	if got := q.Len("ses-a"); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
}

func TestAddRepeatsByClientRequestID(t *testing.T) {
	q := New()
	cmd := Pending{ClientRequestID: "req-1", Kind: "question", Payload: []byte("first")}

	created, err := q.Add("ses-a", cmd)
	if err != nil || !created {
		t.Fatalf("first Add = %v, %v; want true, nil", created, err)
	}
	repeat := cmd
	repeat.Payload = []byte("different")
	created, err = q.Add("ses-a", repeat)
	if err != nil {
		t.Fatalf("repeat Add: %v", err)
	}
	if created {
		t.Fatal("repeat Add reported true")
	}
	if got := q.Len("ses-a"); got != 1 {
		t.Fatalf("Len = %d, want 1", got)
	}
	got, ok := q.Pop("ses-a")
	if !ok || !reflect.DeepEqual(got, cmd) {
		t.Fatalf("Pop = %+v, %v; want original %+v", got, ok, cmd)
	}
}

func TestRepeatAfterPopIsStillIdempotent(t *testing.T) {
	q := New()
	cmd := Pending{ClientRequestID: "req-1", Kind: "question"}
	if _, err := q.Add("ses-a", cmd); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, ok := q.Pop("ses-a"); !ok {
		t.Fatal("Pop found no command")
	}
	created, err := q.Add("ses-a", cmd)
	if err != nil {
		t.Fatalf("repeat Add after Pop: %v", err)
	}
	if created {
		t.Fatal("repeat Add after Pop queued a command")
	}
	if got := q.Len("ses-a"); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
}

func TestRepeatAcrossSessionsIsIdempotent(t *testing.T) {
	q := New()
	cmd := Pending{ClientRequestID: "req-1", Kind: "question"}
	if _, err := q.Add("ses-a", cmd); err != nil {
		t.Fatalf("Add ses-a: %v", err)
	}
	created, err := q.Add("ses-b", cmd)
	if err != nil {
		t.Fatalf("Add ses-b: %v", err)
	}
	if created {
		t.Fatal("same client request ID queued in a second session")
	}
	if got := q.Len("ses-b"); got != 0 {
		t.Fatalf("ses-b Len = %d, want 0", got)
	}
}

func TestPopOnEmptyQueue(t *testing.T) {
	q := New()
	got, ok := q.Pop("ses-a")
	if ok {
		t.Fatalf("Pop on empty queue returned %+v", got)
	}
	if got.ClientRequestID != "" || got.Kind != "" || got.Payload != nil {
		t.Fatalf("Pop on empty queue = %+v, want zero", got)
	}
	if got := q.Len("ses-a"); got != 0 {
		t.Fatalf("Len = %d, want 0", got)
	}
}

func TestAddRejectsEmptyIdentity(t *testing.T) {
	q := New()
	if _, err := q.Add("  ", Pending{ClientRequestID: "req-1", Kind: "question"}); !errors.Is(err, ErrEmptySession) {
		t.Fatalf("Add with blank session = %v, want %v", err, ErrEmptySession)
	}
	if _, err := q.Add("ses-a", Pending{ClientRequestID: " ", Kind: "question"}); !errors.Is(err, ErrEmptyRequestID) {
		t.Fatalf("Add with blank request ID = %v, want %v", err, ErrEmptyRequestID)
	}
	if got := q.Len("ses-a"); got != 0 {
		t.Fatalf("Len = %d, want 0 after rejected adds", got)
	}
}

func TestZeroValueQueueIsUsable(t *testing.T) {
	var q Queue
	cmd := Pending{ClientRequestID: "req-1", Kind: "question"}
	created, err := q.Add("ses-a", cmd)
	if err != nil || !created {
		t.Fatalf("Add on zero value = %v, %v; want true, nil", created, err)
	}
	if got, ok := q.Pop("ses-a"); !ok || got.ClientRequestID != cmd.ClientRequestID {
		t.Fatalf("Pop = %+v, %v; want %s", got, ok, cmd.ClientRequestID)
	}
}

func TestSeenIsBoundedOldestFirst(t *testing.T) {
	q := New()
	for i := 0; i < maxQueueSeen+1; i++ {
		id := "req-" + strconv.Itoa(i)
		if _, err := q.Add("ses-a", Pending{ClientRequestID: id, Kind: "question"}); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}

	if got := len(q.seen); got != maxQueueSeen {
		t.Fatalf("seen = %d, want %d", got, maxQueueSeen)
	}
	if got := len(q.seenOrder); got != maxQueueSeen {
		t.Fatalf("seenOrder = %d, want %d", got, maxQueueSeen)
	}
	if _, ok := q.seen["req-0"]; ok {
		t.Fatal("oldest client request ID survived eviction")
	}
	if _, ok := q.seen["req-"+strconv.Itoa(maxQueueSeen)]; !ok {
		t.Fatal("newest client request ID missing")
	}
	if got := q.Len("ses-a"); got != maxQueueSeen {
		t.Fatalf("queue Len = %d, want %d", got, maxQueueSeen)
	}
}

func TestCancelStillWorksAfterSeenEviction(t *testing.T) {
	q := New()
	kept := Pending{ClientRequestID: "kept", Kind: "question"}
	if _, err := q.Add("ses-a", kept); err != nil {
		t.Fatalf("Add kept: %v", err)
	}
	for i := 0; i < maxQueueSeen; i++ {
		id := "fill-" + strconv.Itoa(i)
		if _, err := q.Add("ses-b", Pending{ClientRequestID: id, Kind: "question"}); err != nil {
			t.Fatalf("Add(%s): %v", id, err)
		}
	}
	if !q.Cancel("ses-a", "kept") {
		t.Fatal("Cancel failed for a command whose seen ID was evicted")
	}
	if got := q.Len("ses-a"); got != 0 {
		t.Fatalf("Len = %d, want 0 after Cancel", got)
	}
}
