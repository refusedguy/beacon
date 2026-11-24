package core

import "context"

type TaskSource interface {
	ListAvailable(ctx context.Context) ([]Task, error)
	Claim(ctx context.Context, id TaskID) error
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

type Workspace struct {
	Path   string
	Branch string
	Base   string
}

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

type Session struct {
	ID string
	PR int
}

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

type HarnessEvent struct {
	RunID     RunID
	SessionID string
	Kind      string
	Error     string
}

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

type ScmGateway interface {
	Observe(ctx context.Context, pr int) (Observation, error)
	UpdateBranch(ctx context.Context, pr int, expectedSHA string) (string, error)
	Merge(ctx context.Context, pr int, expectedSHA string, title string) error
	BaseBranchStatus(ctx context.Context, baseBranch string) (BaseStatus, error)
	FindOpenPRByBranch(ctx context.Context, branch string) (int, error)
	OpenPR(ctx context.Context, head, base, title string) (int, error)
}

type MergePolicy interface {
	Evaluate(Observation) MergeDecision
}

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

type Notification struct {
	RunID   RunID
	Subject string
	Body    string
}

type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

type Concurrency struct {
	MaxInFlight     int
	MaxActiveAgents int
}

// AgentSlots owns the agent-slot budget policy. Available returns the number
// of free slots; a positive count permits another worker to start. The
// implementation owns the default limit and fails open when active runs cannot
// be listed, so dispatch, promotion and wake checks share one policy.
type AgentSlots interface {
	Available(ctx context.Context) int
}
