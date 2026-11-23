// Package session holds the pure logic for deferred session commands. A queue
// stores commands per session and hands them out in order. It performs no I/O:
// callers persist and deliver commands themselves.
package session

import (
	"errors"
	"strings"
)

// Pending is one deferred command waiting for a session. ClientRequestID
// identifies the client request and makes Add idempotent.
type Pending struct {
	ClientRequestID string
	Kind            string
	Payload         []byte
}

var (
	ErrEmptySession   = errors.New("session: empty session id")
	ErrEmptyRequestID = errors.New("session: empty client request id")
)

// maxQueueSeen bounds the client request ID dedup set. An ID older than this
// window is no longer remembered, so a retry that late can be queued again.
// The bound is larger than any real retry window; dedup holds for normal use.
const maxQueueSeen = 4096

// Queue holds deferred commands grouped by session ID. A client request ID
// seen once is never queued again while it is inside the dedup window. The zero
// value is ready to use. Callers serialise access from multiple goroutines
// themselves.
type Queue struct {
	bySession map[string][]Pending
	seen      map[string]struct{}
	seenOrder []string // client request IDs in insertion order, for oldest-first eviction
}

// New returns an empty Queue.
func New() *Queue {
	return &Queue{}
}

// Add appends cmd to sessionID's queue. It reports true only on the first call
// for cmd.ClientRequestID. A repeat leaves the queue unchanged and reports
// false.
func (q *Queue) Add(sessionID string, cmd Pending) (bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return false, ErrEmptySession
	}
	if strings.TrimSpace(cmd.ClientRequestID) == "" {
		return false, ErrEmptyRequestID
	}
	q.ensure()
	if _, ok := q.seen[cmd.ClientRequestID]; ok {
		return false, nil
	}
	if len(q.seen) >= maxQueueSeen {
		oldest := q.seenOrder[0]
		delete(q.seen, oldest)
		q.forgetSeen(oldest)
	}
	q.seen[cmd.ClientRequestID] = struct{}{}
	q.seenOrder = append(q.seenOrder, cmd.ClientRequestID)
	q.bySession[sessionID] = append(q.bySession[sessionID], cmd)
	return true, nil
}

// Pop removes and returns the oldest pending command for sessionID. It reports
// false when that queue is empty. The client request ID stays seen, so a retry
// after Pop is still a repeat.
func (q *Queue) Pop(sessionID string) (Pending, bool) {
	items := q.bySession[sessionID]
	if len(items) == 0 {
		return Pending{}, false
	}
	cmd := items[0]
	if len(items) == 1 {
		delete(q.bySession, sessionID)
	} else {
		q.bySession[sessionID] = items[1:]
	}
	return cmd, true
}

// Cancel removes cmd from sessionID's queue. It reports whether it removed a
// command. The client request ID stays seen, so a cancelled command is not
// queued again by a retry.
func (q *Queue) Cancel(sessionID, clientRequestID string) bool {
	items := q.bySession[sessionID]
	for i, cmd := range items {
		if cmd.ClientRequestID != clientRequestID {
			continue
		}
		if len(items) == 1 {
			delete(q.bySession, sessionID)
		} else {
			q.bySession[sessionID] = append(items[:i:i], items[i+1:]...)
		}
		return true
	}
	return false
}

// Len reports how many commands wait for sessionID.
func (q *Queue) Len(sessionID string) int {
	return len(q.bySession[sessionID])
}

func (q *Queue) ensure() {
	if q.bySession == nil {
		q.bySession = make(map[string][]Pending)
	}
	if q.seen == nil {
		q.seen = make(map[string]struct{})
	}
}

// forgetSeen removes an ID from the eviction order. The caller serialises
// access to the Queue.
func (q *Queue) forgetSeen(id string) {
	for i, k := range q.seenOrder {
		if k == id {
			copy(q.seenOrder[i:], q.seenOrder[i+1:])
			q.seenOrder[len(q.seenOrder)-1] = ""
			q.seenOrder = q.seenOrder[:len(q.seenOrder)-1]
			return
		}
	}
}
