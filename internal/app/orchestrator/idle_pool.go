// Package orchestrator holds the policy that reuses an idle agent before the
// orchestrator opens a new spawn slot. The model is pure: the caller supplies
// the candidate work, the idle agents and the current time, so this file reads
// no clock and no session store.
package orchestrator

import (
	"strings"
	"time"
)

// DefaultIdleFreshness bounds how old an agent's last observation may be for
// SelectIdle to reuse it. A caller that needs another bound passes one to
// SelectIdle.
const DefaultIdleFreshness = 5 * time.Minute

// IdleAgent is one agent that finished a card and can take another one.
// SessionID identifies the agent. OwnerFiles and KnownCardIDs record the work
// it already knows. WorktreePath points at the working copy it can reuse.
type IdleAgent struct {
	SessionID    string    `json:"sessionID"`
	KnownCardIDs []string  `json:"knownCardIDs,omitempty"`
	OwnerFiles   []string  `json:"ownerFiles,omitempty"`
	LastSeen     time.Time `json:"lastSeen"`
	WorktreePath string    `json:"worktreePath,omitempty"`
}

// SelectIdle returns the freshest idle agent that knows the candidate's work.
// An agent matches when it shares an OwnerFile or a KnownCardID with the
// candidate and its LastSeen is no older than freshness. A LastSeen after now
// does not match, so a skewed clock cannot make a stale agent look fresh.
//
// The freshness bound is optional. With no bound SelectIdle uses
// DefaultIdleFreshness. A bound of zero keeps only agents seen at exactly now;
// a negative bound rejects every agent. Ties go to the lowest SessionID, so
// the same input always picks the same agent. The result is a copy, which
// lets the caller change it without changing the pool.
//
// No match returns nil. The caller then keeps the current rule, where one
// completion signal opens one spawn slot. This file only prepares the policy;
// it does not wire the policy into the daemon.
func SelectIdle(candidate IdleAgent, agents []IdleAgent, now time.Time, freshness ...time.Duration) *IdleAgent {
	bound := DefaultIdleFreshness
	if len(freshness) > 0 {
		bound = freshness[0]
	}

	best := -1
	for i := range agents {
		a := agents[i]
		if a.SessionID == "" || !sharesIdleWork(candidate, a) {
			continue
		}
		age := now.Sub(a.LastSeen)
		if age < 0 || age > bound {
			continue
		}
		if best < 0 || a.LastSeen.After(agents[best].LastSeen) ||
			(a.LastSeen.Equal(agents[best].LastSeen) && a.SessionID < agents[best].SessionID) {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	out := agents[best]
	return &out
}

// sharesIdleWork reports whether the agent knows any file or card the
// candidate names. Empty names never match, so an empty candidate selects
// nobody.
func sharesIdleWork(candidate, agent IdleAgent) bool {
	for _, file := range candidate.OwnerFiles {
		if hasIdleToken(agent.OwnerFiles, file) {
			return true
		}
	}
	for _, id := range candidate.KnownCardIDs {
		if hasIdleToken(agent.KnownCardIDs, id) {
			return true
		}
	}
	return false
}

// hasIdleToken compares one token after trimming surrounding space. File
// paths stay case sensitive, since the worktree treats them that way.
func hasIdleToken(values []string, want string) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	for _, value := range values {
		if strings.TrimSpace(value) == want {
			return true
		}
	}
	return false
}
