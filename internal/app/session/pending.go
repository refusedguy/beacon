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

// Queue holds deferred commands grouped by session ID. A client request ID
// seen once is never queued again, so a client retry cannot create a duplicate.
// The zero value is ready to use. Callers serialise access from multiple
// goroutines themselves.
type Queue struct {
	bySession map[string][]Pending
	seen      map[string]struct{}
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
	q.seen[cmd.ClientRequestID] = struct{}{}
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
