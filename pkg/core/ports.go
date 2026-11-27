package core

import "context"

// TaskSource is the port for reading and updating task cards.
type TaskSource interface {
	ListAvailable(ctx context.Context) ([]Task, error)
	Complete(ctx context.Context, id TaskID) error
	MarkInWorkspace(ctx context.Context, id TaskID, workspacePath, status string) error
	Create(ctx context.Context, task Task) (Task, error)
}

// EpicParser decomposes an epic task into its child tasks. The default
// implementation reads the epic's plan and assigns deterministic child IDs.
// Another decomposition strategy can replace it without changing callers.
type EpicParser interface {
	Children(epic Task) []Task
}

// Workspace is a checked-out branch used by a run.
type Workspace struct {
	Path   string
	Branch string
	Base   string
}

// WorkspaceManager is the port for creating and cleaning run worktrees.
type WorkspaceManager interface {
	Create(ctx context.Context, task Task) (Workspace, error)
	Clean(ctx context.Context, ws Workspace) (bool, error)
	Remove(ctx context.Context, ws Workspace) error
	ForceRemove(ctx context.Context, ws Workspace) error
	HasChanges(ctx context.Context, ws Workspace, base string) (bool, error)
	CommitPath(ctx context.Context, ws Workspace, pathspec, message string) error
}

// ModelRef names the model a harness session should use. The zero value means
// no model was resolved, so the harness keeps its own default. The JSON tags
// keep the OpenCode wire shape unchanged.
type ModelRef struct {
	ProviderID string `json:"providerID"`
	ID         string `json:"id"`
	Variant    string `json:"variant,omitempty"`
}

// IsZero reports whether no model is set.
func (m ModelRef) IsZero() bool {
	return m.ProviderID == "" && m.ID == "" && m.Variant == ""
}

// SpawnRequest is the input to an agent harness spawn.
type SpawnRequest struct {
	Task      Task
	Workspace Workspace
	RunID     RunID
	Prompt    string
	// ParentSessionID keeps the orchestrator session that owns the run.
	ParentSessionID string
	// ModelRef is the model resolved for this dispatch from the parent
	// session. It overrides the harness default when set.
	ModelRef ModelRef
	IsFixer  bool
}

// Session is a harness session bound to a run.
type Session struct {
	ID string
	PR int
}

// WakeContext supplies the context for waking an agent.
type WakeContext struct {
	RunID     RunID
	PRNumber  int
	HeadSHA   string
	Reason    string
	Excerpt   string
	Workspace string
	Branch    string
	RebaseTry int
	FixTry    int
}

// HarnessEvent is an event emitted by an agent harness.
type HarnessEvent struct {
	RunID     RunID
	SessionID string
	Kind      string
	Error     string
}

// AgentHarness is the port for starting and driving agent sessions.
type AgentHarness interface {
	Spawn(ctx context.Context, req SpawnRequest) (Session, error)
	Wake(ctx context.Context, sessionID string, wake WakeContext) error
	Stop(ctx context.Context, sessionID string) error
	IsAlive(ctx context.Context, sessionID string) (bool, error)
	NotifyParent(ctx context.Context, sessionID, text string) error
	Events() <-chan HarnessEvent
}

// WorktreeRefresher reconciles an editor's worktree registry after a worktree
// directory is removed. Harnesses without a worktree registry can omit it.
type WorktreeRefresher interface {
	RefreshWorktrees(ctx context.Context) error
}

// IdleWatcher watches a harness's sessions for idle workers and reports them
// through Events. A harness without an idle signal can omit this port.
type IdleWatcher interface {
	WatchIdle(ctx context.Context)
}

// ScmGateway is the port for source-control provider operations.
type ScmGateway interface {
	Observe(ctx context.Context, pr int) (Observation, error)
	UpdateBranch(ctx context.Context, pr int, expectedSHA string) (string, error)
	Merge(ctx context.Context, pr int, expectedSHA string, title string) error
	BaseBranchStatus(ctx context.Context, baseBranch string) (BaseStatus, error)
	FindOpenPRByBranch(ctx context.Context, branch string) (int, error)
	OpenPR(ctx context.Context, head, base, title string) (int, error)
}

// MergePolicy evaluates an observation into a merge decision.
type MergePolicy interface {
	Evaluate(Observation) MergeDecision
}

// RequiredCheckPolicy is the optional capability a policy may expose to name
// the checks a merge must pass. Decide uses it to scope the failure invariant:
// a failed optional check must not block a merge. A policy without the
// capability keeps the conservative all-checks behavior.
type RequiredCheckPolicy interface {
	RequiredChecks() ([]string, bool)
}

// RequiredCheckSource supplies the names of checks a merge must pass. Policy
// reads names through this port, so it does not depend on where they come from:
// an explicit config list, a CI workflow file and a future provider's check
// contexts can all implement it without a policy change.
type RequiredCheckSource interface {
	Names(ctx context.Context) ([]string, error)
}

// RunStore is the persistence port for runs, tasks and incidents.
type RunStore interface {
	Save(ctx context.Context, run *Run) error
	Get(ctx context.Context, id RunID) (*Run, error)
	FindByPR(ctx context.Context, pr int) (*Run, error)
	FindActiveByTask(ctx context.Context, task TaskID) (*Run, error)
	ListActive(ctx context.Context) ([]*Run, error)
	ListRecent(ctx context.Context, limit int) ([]*Run, error)
	ListByIncident(ctx context.Context, incidentID string) ([]*Run, error)
	ClaimDelivery(ctx context.Context, key string) (bool, error)
	ReleaseDelivery(ctx context.Context, key string) error
	SetApproval(ctx context.Context, id RunID, sha string) error
	Approved(ctx context.Context, id RunID, sha string) (bool, error)
	SyncTasks(ctx context.Context, tasks []Task) error
	ListReadyTasks(ctx context.Context) ([]Task, error)
	SetTaskStatus(ctx context.Context, taskID TaskID, status string) error
	CreateIncident(ctx context.Context, in *BaseIncident) (*BaseIncident, error)
	GetIncident(ctx context.Context, id string) (*BaseIncident, error)
	FindOpenIncident(ctx context.Context, repo, base, signature string) (*BaseIncident, error)
	ListOpenIncidents(ctx context.Context) ([]*BaseIncident, error)
	UpdateIncident(ctx context.Context, in *BaseIncident) error
}

// Notification is a message sent to a run's operator.
type Notification struct {
	RunID   RunID
	Subject string
	Body    string
}

// Notifier is the port for delivering notifications.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Concurrency bounds the number of in-flight runs and active agents.
type Concurrency struct {
	MaxInFlight     int
	MaxActiveAgents int
}

// AgentSlots owns the agent-slot budget policy. Available returns the number
// of free slots; a positive count permits another worker to start. The
// implementation owns the default limit and fails closed when active runs
// cannot be listed, so dispatch, promotion and wake checks share one policy.
// Callers that start an agent hold the implementation's acquisition lock from
// the Available call through the start, so concurrent paths cannot spend the
// same slot twice.
type AgentSlots interface {
	Available(ctx context.Context) int
}
