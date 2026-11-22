// Package session holds the pure logic for observing which agent is running
// in a pane. The functions here parse process and pane snapshots, extract a
// session id from open file paths, and merge an observation with a saved
// record. They perform no I/O: reading /proc, running lsof, and executing
// commands stay at the caller boundary.
package session

import (
	"context"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// CommandTimeout bounds an external command used to collect an observation,
// such as lsof or a /proc scan. dmux uses the same 1500 ms budget.
const CommandTimeout = 1500 * time.Millisecond

// MetaSessionID is the metadata key that carries an observed session id.
const MetaSessionID = "ai.beacon/sessionID"

var (
	// sessionIDPattern finds a lowercase session UUID of the form 8-4-4-4-12.
	// The whole token is 36 characters drawn from hex digits and dashes.
	sessionIDPattern = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	// sessionIDExact validates a candidate before it replaces a saved value.
	sessionIDExact = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// CommandContext returns a context bounded by CommandTimeout. Callers at the
// I/O boundary use it so a stuck lsof or /proc read cannot stall observation.
func CommandContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, CommandTimeout)
}

// Process is one row of a process snapshot. Parent is the PID of the parent
// process, or 0 for a root. Cmd is the command line that started the process.
type Process struct {
	PID    int
	Parent int
	Cmd    string
}

// ParseProcessSnapshot parses lines in "pid parent command" form. The command
// may contain spaces. Malformed lines and lines with a non-positive PID are
// skipped, so a header row in a ps listing does not break the walk.
func ParseProcessSnapshot(lines []string) []Process {
	out := make([]Process, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil || pid <= 0 {
			continue
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		cmd := ""
		if len(fields) > 2 {
			cmd = strings.Join(fields[2:], " ")
		}
		out = append(out, Process{PID: pid, Parent: parent, Cmd: cmd})
	}
	return out
}

// PaneTree maps a pane identifier to the PID of the pane's root process.
type PaneTree map[string]int

// ParsePaneTree parses lines in "pane pid" form. Malformed lines and lines
// with a non-positive PID are skipped. An empty tree is a valid result: it
// means no pane was observed.
func ParsePaneTree(lines []string) PaneTree {
	tree := make(PaneTree)
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[1])
		if err != nil || pid <= 0 {
			continue
		}
		tree[fields[0]] = pid
	}
	return tree
}

// Agent is an active agent observed under a pane. Depth counts process edges
// from the pane root, so the root itself has depth 0. SessionID is filled from
// the agent's open files when the caller supplies them.
type Agent struct {
	PID       int
	Pane      string
	Depth     int
	SessionID string
}

// AgentMatch reports whether a process is the agent a caller is looking for.
type AgentMatch func(Process) bool

// IsAgentProcess reports whether a process is a known agent runtime. It
// matches the first word of Cmd case-insensitively, so flags and arguments do
// not matter. Its signature satisfies AgentMatch.
func IsAgentProcess(p Process) bool {
	first := ""
	if fields := strings.Fields(p.Cmd); len(fields) > 0 {
		first = strings.ToLower(fields[0])
	}
	switch first {
	case "claude", "codex", "opencode", "aider", "gemini", "cursor":
		return true
	default:
		return false
	}
}

// FindAgent walks the process descendants of every pane root and returns the
// matching agent with the smallest depth. Ties break on the lower PID, then
// on the pane identifier, so the result is deterministic. It reports false
// when no pane root or matching agent exists. A parent cycle stops that branch
// of the walk instead of looping forever.
func FindAgent(tree PaneTree, procs []Process, match AgentMatch) (*Agent, bool) {
	if match == nil {
		return nil, false
	}
	byPID := make(map[int]Process, len(procs))
	children := make(map[int][]Process)
	for _, p := range procs {
		byPID[p.PID] = p
		children[p.Parent] = append(children[p.Parent], p)
	}

	type step struct {
		pid   int
		depth int
	}
	var best *Agent
	for pane, root := range tree {
		seen := map[int]bool{root: true}
		queue := []step{{pid: root, depth: 0}}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]

			if proc, ok := byPID[cur.pid]; ok && match(proc) {
				cand := &Agent{PID: proc.PID, Pane: pane, Depth: cur.depth}
				if best == nil || betterAgent(cand, best) {
					best = cand
				}
			}
			for _, child := range children[cur.pid] {
				if seen[child.PID] {
					continue
				}
				seen[child.PID] = true
				queue = append(queue, step{pid: child.PID, depth: cur.depth + 1})
			}
		}
	}
	if best == nil {
		return nil, false
	}
	return best, true
}

func betterAgent(a, b *Agent) bool {
	if a.Depth != b.Depth {
		return a.Depth < b.Depth
	}
	if a.PID != b.PID {
		return a.PID < b.PID
	}
	return a.Pane < b.Pane
}

// ExtractSessionID returns the first session UUID found in paths. Only paths
// ending in .jsonl are considered, and the UUID must appear in the file name.
// The second result is false when no valid session id is present.
func ExtractSessionID(paths []string) (string, bool) {
	for _, p := range paths {
		base := filepath.Base(p)
		if !strings.HasSuffix(base, ".jsonl") {
			continue
		}
		if id := sessionIDPattern.FindString(base); id != "" {
			return id, true
		}
	}
	return "", false
}

// IsSessionID reports whether id is a session UUID. It is used before a saved
// value is replaced, so a guessed id never overwrites a confirmed one.
func IsSessionID(id string) bool {
	return sessionIDExact.MatchString(strings.TrimSpace(id))
}

// ObservationState separates a failed collection from an absent agent.
type ObservationState int

const (
	// ObservationUnknown means collection failed. The caller has no usable
	// process data, so it must keep whatever it already knows.
	ObservationUnknown ObservationState = iota
	// ObservationIdle means collection succeeded and no agent is active.
	ObservationIdle
	// ObservationActive means an agent is running under a pane.
	ObservationActive
)

// Snapshot is the dmux-style observation of all panes. A nil or empty map
// means collection failed. Every value may be nil, which means that pane has
// no active agent. A non-nil value is an active agent.
type Snapshot map[string]*Agent

// Observation is a classified snapshot. Agent is meaningful only when State
// is ObservationActive.
type Observation struct {
	State ObservationState
	Agent Agent
}

// Observe classifies a snapshot. An empty map is a collection failure, a map
// whose entries are all nil is an idle observation, and a map with at least
// one agent is active. When several panes report an agent, the one with the
// smallest depth wins, using FindAgent's tie break rules.
func Observe(s Snapshot) Observation {
	if len(s) == 0 {
		return Observation{State: ObservationUnknown}
	}
	var best *Agent
	for _, a := range s {
		if a == nil {
			continue
		}
		if best == nil || betterAgent(a, best) {
			best = a
		}
	}
	if best == nil {
		return Observation{State: ObservationIdle}
	}
	return Observation{State: ObservationActive, Agent: *best}
}

// MergeObservation applies obs to a copy of saved metadata. It never mutates
// saved. An unknown observation preserves every field. An idle observation
// clears the observed session id. An active observation writes the observed
// session id only when it is a valid UUID, so a saved value is not replaced
// by a presumed one.
func MergeObservation(saved map[string]string, obs Observation) map[string]string {
	out := make(map[string]string, len(saved)+1)
	for k, v := range saved {
		out[k] = v
	}
	switch obs.State {
	case ObservationIdle:
		delete(out, MetaSessionID)
	case ObservationActive:
		if IsSessionID(obs.Agent.SessionID) {
			out[MetaSessionID] = strings.TrimSpace(obs.Agent.SessionID)
		}
	}
	return out
}
