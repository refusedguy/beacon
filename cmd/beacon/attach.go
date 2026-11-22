package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/app/session"
	"github.com/refusedguy/beacon/internal/infra/observe"
	"github.com/refusedguy/beacon/internal/infra/opencode"
	"github.com/refusedguy/beacon/internal/infra/store"
	"github.com/refusedguy/beacon/internal/infra/worktree"
)

const attachSessionsFile = "attach-sessions.json"

// attachSession is one session id saved for a task before the task starts.
// The id outlives a timeout kill, so attach can reopen the same session
// instead of allocating a new one (ADR 0043 §6). DeadlineAt is the task's hard
// limit from ADR 0042 §1: reaching it ends the task but never erases the id.
// Identity records the session id at save time. Resume checks it before
// opening a session, so a saved record that lost its id cannot open a
// different one (CCx-10). Records saved before Identity existed keep the old
// best-effort behaviour.
type attachSession struct {
	TaskID     string    `json:"taskID"`
	SessionID  string    `json:"sessionID"`
	Identity   string    `json:"identity,omitempty"`
	DeadlineAt time.Time `json:"deadlineAt,omitempty"`
	SavedAt    time.Time `json:"savedAt,omitempty"`
}

// attachSessionState is the registry in Beacon state. Last names the task
// whose session was saved most recently, so `attach --resume` continues it
// without naming the task again.
type attachSessionState struct {
	Sessions []attachSession `json:"sessions"`
	Last     string          `json:"last,omitempty"`
}

// attachReadFile, attachWriteFile and attachMkdirAll are replaceable in tests.
// Holding os functions in variables keeps direct os calls out of cmd/beacon,
// which the capabilities gate forbids (ADR 0005). attach.go already reads the
// service file through os.DirFS + fs.ReadFile for the same reason.
var (
	attachReadFile  = os.ReadFile
	attachWriteFile = os.WriteFile
	attachMkdirAll  = os.MkdirAll
)

// opencodeService is the registration file written by the background
// OpenCode service at $XDG_STATE_HOME/opencode/service.json. Beacon only
// needs the address; pid, version and password are intentionally ignored
// (docs/design/opencode-cli-manager.md:11).
type opencodeService struct {
	URL string `json:"url"`
}

// readServiceConfig reads the service address through opencode.Discover, the
// one reader of the registration file. A missing or malformed file means the
// background service is not running (ADR 0005, ADR 0092).
func readServiceConfig() (opencodeService, error) {
	cfg, err := opencode.Discover()
	if err != nil {
		return opencodeService{}, err
	}
	return opencodeService{URL: cfg.URL}, nil
}

// attachSessionsPath points at the session registry in Beacon state. It uses
// the same stateDir() as the rest of the CLI, so a task and its session id
// live under one root and survive a process restart.
func attachSessionsPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, attachSessionsFile), nil
}

// loadAttachSessions reads the registry. A missing file is an empty registry;
// a corrupt one is an error, so attach never silently opens a guessed session.
func loadAttachSessions() (attachSessionState, error) {
	path, err := attachSessionsPath()
	if err != nil {
		return attachSessionState{}, err
	}
	data, err := attachReadFile(path)
	if os.IsNotExist(err) {
		return attachSessionState{}, nil
	}
	if err != nil {
		return attachSessionState{}, fmt.Errorf("%s: не удалось прочитать реестр сессий: %w", path, err)
	}
	var st attachSessionState
	if err := json.Unmarshal(data, &st); err != nil {
		return attachSessionState{}, fmt.Errorf("%s: неверный реестр сессий: %w", path, err)
	}
	return st, nil
}

// saveAttachSessions writes the registry under Beacon state. The state
// directory is created on first use, so a fresh XDG_STATE_HOME is enough.
func saveAttachSessions(st attachSessionState) error {
	path, err := attachSessionsPath()
	if err != nil {
		return err
	}
	if err := attachMkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("%s: не создан каталог состояния: %w", path, err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("не собран реестр сессий: %w", err)
	}
	return attachWriteFile(path, append(data, '\n'), 0o600)
}

// rememberAttachSession saves sessionID for taskID. A second attach for the
// same task replaces the record instead of stacking duplicates, so resume
// always finds the most recent session. BEACON_TASK_DEADLINE, when set as an
// RFC3339 instant, records the task's hard limit without deleting the id. An
// invalid deadline is an error: Beacon refuses to write a session record that
// silently lost its hard limit.
func rememberAttachSession(taskID, sessionID string, now time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("пустой sessionID")
	}
	taskID = strings.TrimSpace(taskID)
	if now.IsZero() {
		now = time.Now().UTC()
	}

	rec := attachSession{TaskID: taskID, SessionID: sessionID, Identity: sessionID, SavedAt: now.UTC()}
	if raw := strings.TrimSpace(os.Getenv("BEACON_TASK_DEADLINE")); raw != "" {
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			observe.WarnConfig("BEACON_TASK_DEADLINE", raw, "ожидается RFC3339")
			return fmt.Errorf("неверный BEACON_TASK_DEADLINE=%q: ожидается RFC3339", raw)
		}
		rec.DeadlineAt = at.UTC()
	}

	st, err := loadAttachSessions()
	if err != nil {
		return err
	}
	kept := make([]attachSession, 0, len(st.Sessions)+1)
	for _, old := range st.Sessions {
		if old.TaskID == taskID {
			continue
		}
		kept = append(kept, old)
	}
	st.Sessions = append(kept, rec)
	st.Last = taskID
	return saveAttachSessions(st)
}

// savedAttachRecord returns the session record saved for taskID. With an empty
// taskID it uses the last task recorded by rememberAttachSession. The whole
// record is needed to judge a registry candidate: the task and the time the id
// was saved are compared with the row found in the registry (CCx-13).
func savedAttachRecord(taskID string) (attachSession, error) {
	st, err := loadAttachSessions()
	if err != nil {
		return attachSession{}, err
	}
	key := strings.TrimSpace(taskID)
	if key == "" {
		key = st.Last
	}
	for _, rec := range st.Sessions {
		if rec.TaskID == key && rec.SessionID != "" {
			return rec, nil
		}
	}
	return attachSession{}, nil
}

// savedAttachSession returns the session id saved for taskID. A deadline in
// the past does not hide the id: the task ended, but the session remains the
// one to resume (ADR 0043 §6).
func savedAttachSession(taskID string) (string, error) {
	rec, err := savedAttachRecord(taskID)
	return rec.SessionID, err
}

// attachRegistryRevision signs the saved-session registry by its raw bytes.
// Any change to a saved record changes the revision. Resume takes it before it
// reads a record and again before it opens the session. A mismatch means the
// record moved while the id was being chosen, so attach refuses to open it
// (CC-05).
func attachRegistryRevision() (string, error) {
	path, err := attachSessionsPath()
	if err != nil {
		return "", err
	}
	data, err := attachReadFile(path)
	if os.IsNotExist(err) {
		return session.SessionRevision(""), nil
	}
	if err != nil {
		return "", fmt.Errorf("%s: не удалось прочитать реестр сессий: %w", path, err)
	}
	return session.SessionRevision(string(data)), nil
}

// attachCandidateFits reports whether a registry row belongs to the session
// the caller asked to resume. A row without an id cannot open anything. When
// the row names a task, that task must match the task in attach-sessions.json.
// When the row has a last-event time, it must not predate the moment the id
// was saved; an older row describes a different session. Rows written before
// those fields existed lack them and stay usable, so attach keeps working
// with older state (CCx-13).
func attachCandidateFits(candidate session.Session, requested attachSession) bool {
	if strings.TrimSpace(candidate.ID) == "" {
		return false
	}
	if task := strings.TrimSpace(candidate.Metadata[spawnMetaTaskID]); task != "" && task != requested.TaskID {
		return false
	}
	if !candidate.LastSeen.IsZero() && !requested.SavedAt.IsZero() && candidate.LastSeen.Before(requested.SavedAt) {
		return false
	}
	return true
}

// attachRecordForSession finds the saved record whose session id is sessionID.
// attach-sessions.json is the journal this repair reads. When the state row is
// gone, the record still carries the id and task line needed to rebuild it.
func attachRecordForSession(sessionID string) (attachSession, bool, error) {
	want := strings.TrimSpace(sessionID)
	if want == "" {
		return attachSession{}, false, nil
	}
	st, err := loadAttachSessions()
	if err != nil {
		return attachSession{}, false, err
	}
	for _, rec := range st.Sessions {
		if rec.SessionID == want {
			return rec, true, nil
		}
	}
	return attachSession{}, false, nil
}

// restoredAttachSeed builds the state row for a session whose row was lost.
// The id and task line come from the saved attach record, which ADR 0043 §6
// makes the source of truth for resume.
func restoredAttachSeed(rec attachSession) session.Session {
	metadata := map[string]string{}
	if task := strings.TrimSpace(rec.TaskID); task != "" {
		metadata[store.SessionMetaTaskID] = task
	}
	return session.Session{
		ID:        rec.SessionID,
		Status:    session.SessionStatusActive,
		CreatedAt: rec.SavedAt,
		Metadata:  metadata,
	}
}

// registryResumeSession confirms the saved session id in the durable session
// registry (WIRING-488). Resume reaches GetSession through the app-layer port,
// so cmd never opens a database for this lookup. The registry is best effort:
// when it is absent or reports an error, attach keeps the id from
// attach-sessions.json, which ADR 0043 §6 makes the source of truth for resume.
// A clean miss means the state row was lost while the journal still has the
// id, so attach rebuilds the row from that record and resumes it (CCx-12).
// A stored id replaces the saved one only when the row fits the requested
// session. When identity is recorded with the saved session, attach checks it
// against the saved id and the registry row first. An identity mismatch
// returns an error, so the caller refuses to open a session built from a
// substituted id (CCx-10, CCx-13).
func registryResumeSession(requested attachSession) (string, error) {
	sessionID := requested.SessionID
	identity := strings.TrimSpace(requested.Identity)
	if identity != "" && identity != sessionID {
		return "", fmt.Errorf("%w: сохранённый identity %q не совпадает с id %q", session.ErrSessionIdentityMismatch, identity, sessionID)
	}
	reg := sessionRegistryForSpawn()
	if reg == nil {
		return sessionID, nil
	}
	var (
		stored session.Session
		found  bool
		err    error
	)
	if identity != "" {
		stored, found, err = reg.ResumeIdentified(sessionID)
	} else {
		stored, found, err = reg.Resume(sessionID)
	}
	if err != nil {
		if errors.Is(err, session.ErrSessionIdentityMismatch) {
			return "", err
		}
		return sessionID, nil
	}
	if !found {
		rec, ok, recErr := attachRecordForSession(sessionID)
		if recErr != nil || !ok {
			return sessionID, nil
		}
		rebuilt, rebuildErr := reg.Restore(restoredAttachSeed(rec))
		if rebuildErr != nil {
			return sessionID, nil
		}
		stored = rebuilt
	}
	if !attachCandidateFits(stored, requested) {
		return sessionID, nil
	}
	if id := strings.TrimSpace(stored.ID); id != "" {
		return id, nil
	}
	return sessionID, nil
}

// Journal record kinds used by the fork reverse scan. A compaction record
// replaces the history before it; a message record belongs to the tail a fork
// keeps verbatim.
const (
	attachJournalMessage    = "message"
	attachJournalCompaction = "compaction"
)

// attachJournalRecord is one entry in a session journal. A compaction carries
// its replacement text and a window number. Both fields must be present for
// the record to stop a reverse scan (docs/research/comp-codex.md, CC-05).
type attachJournalRecord struct {
	Kind      string
	Text      string
	Window    int
	HasWindow bool
}

// completeCompaction reports whether e is a usable fork cutoff.
func (e attachJournalRecord) completeCompaction() bool {
	return e.Kind == attachJournalCompaction && strings.TrimSpace(e.Text) != "" && e.HasWindow
}

// forkHistory scans the journal from the end and stops at the newest complete
// compaction. It returns that compaction and the records written after it, so
// a fork inherits the frozen prefix and the new tail instead of the whole
// journal. An incomplete compaction is skipped and the scan continues toward
// the start. When no complete compaction exists, all records are returned.
// A cutoff recorded in session metadata bounds the scan to that index.
func forkHistory(metadata map[string]string, journal []attachJournalRecord) (*attachJournalRecord, []attachJournalRecord) {
	last := len(journal) - 1
	if cutoff, ok := session.CompactionCutoff(metadata); ok && cutoff >= 0 && cutoff < len(journal) {
		last = cutoff
	}
	for i := last; i >= 0; i-- {
		if !journal[i].completeCompaction() {
			continue
		}
		compaction := journal[i]
		return &compaction, append([]attachJournalRecord(nil), journal[i+1:]...)
	}
	return nil, append([]attachJournalRecord(nil), journal...)
}

// parseAttachArgs accepts the original `attach <sessionID>` plus the resume
// form `attach --resume [taskID]`. Anything else is a usage error.
func parseAttachArgs(args []string) (taskID, sessionID string, resume, ok bool) {
	if len(args) == 1 && args[0] == "--resume" {
		return "", "", true, true
	}
	if len(args) == 2 && args[0] == "--resume" && strings.TrimSpace(args[1]) != "" {
		return strings.TrimSpace(args[1]), "", true, true
	}
	if len(args) == 1 && args[0] != "--resume" && strings.TrimSpace(args[0]) != "" {
		return "", strings.TrimSpace(args[0]), false, true
	}
	return "", "", false, false
}

// attachResumeGit is the git seam at the work-restoration boundary. cmd/beacon
// never shells out itself (ADR 0005); the shared worktree runner keeps the
// guard testable and matches the runner used by beacon gc (WIRING-1686).
var attachResumeGit = worktree.CommandRunner

// attachResumeGitOutput runs one read-only git command and returns its trimmed
// output. A failing command yields an empty string: the guard is best effort
// and never blocks resume because git could not be queried.
func attachResumeGitOutput(dir string, args ...string) string {
	out, err := attachResumeGit(dir, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// attachResumeOtherWorktree reports whether the branch is checked out in a
// worktree other than the current one, together with that worktree's path. It
// parses `git worktree list --porcelain`; the current worktree is excluded so
// a normal resume is not mistaken for a takeover (WIRING-1686).
func attachResumeOtherWorktree(dir, branch string) (bool, string) {
	if branch == "" || branch == "HEAD" {
		return false, ""
	}
	current := attachResumeGitOutput(dir, "rev-parse", "--show-toplevel")
	listing := attachResumeGitOutput(dir, "worktree", "list", "--porcelain")
	want := "refs/heads/" + branch
	var path string
	for _, line := range strings.Split(listing, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			path = strings.TrimSpace(strings.TrimPrefix(line, "worktree "))
		case line == "branch "+want:
			if path != "" && path != current {
				return true, path
			}
		case line == "":
			path = ""
		}
	}
	return false, ""
}

// attachResumeCounts reads the ahead and behind counts for the current branch.
// The left side of `rev-list --left-right --count <upstream>...HEAD` is commits
// only on the upstream (behind), the right side is local commits (ahead).
// A branch without a configured upstream is compared with origin/main.
func attachResumeCounts(dir string) (ahead, behind int) {
	upstream := attachResumeGitOutput(dir, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}")
	if upstream == "" {
		upstream = "origin/main"
	}
	fields := strings.Fields(attachResumeGitOutput(dir, "rev-list", "--left-right", "--count", upstream+"...HEAD"))
	if len(fields) != 2 {
		return 0, 0
	}
	behindN, errBehind := strconv.Atoi(fields[0])
	aheadN, errAhead := strconv.Atoi(fields[1])
	if errBehind != nil || errAhead != nil {
		return 0, 0
	}
	return aheadN, behindN
}

// attachResumeCheck collects the branch facts at the resume boundary and asks
// worktree.CheckResume how the branch may be opened again (WIRING-1686). It is
// the caller-side half of the guard: git runs here, the pure decision in
// internal/infra/worktree/resume_guard.go. Missing facts stay zero so an
// unrecognised repository degrades to the open decision instead of blocking.
func attachResumeCheck(dir string) worktree.ResumeAnswer {
	branch := attachResumeGitOutput(dir, "rev-parse", "--abbrev-ref", "HEAD")
	wtPath := attachResumeGitOutput(dir, "rev-parse", "--show-toplevel")
	checkedOutElsewhere, existing := attachResumeOtherWorktree(dir, branch)
	ahead, behind := attachResumeCounts(dir)
	return worktree.CheckResume(worktree.ResumeInput{
		Branch:              branch,
		Worktree:            wtPath,
		CheckedOutElsewhere: checkedOutElsewhere,
		Ahead:               ahead,
		Behind:              behind,
		ExistingWorktree:    existing,
	})
}

// runAttach implements `beacon attach <sessionID>` and `beacon attach --resume
// [taskID]`. A plain attach opens the given session and saves its id for later.
// After a timeout kill, --resume opens that saved session instead of starting
// a new one. It never starts a server and never draws its own TUI (ADR 0029,
// docs/design/opencode-cli-manager.md:88-93).
func (c *CLI) runAttach(args []string) int {
	taskID, sessionID, resume, ok := parseAttachArgs(args)
	if !ok {
		fmt.Fprintln(c.errOut, "beacon attach: нужна одна команда: beacon attach <sessionID>")
		return 2
	}

	var resumeRevision string

	if resume {
		rev, revErr := attachRegistryRevision()
		if revErr != nil {
			fmt.Fprintf(c.errOut, "beacon attach: не удалось снять подпись реестра сессий (%v); перечитайте список сессий\n", revErr)
			return 2
		}
		resumeRevision = rev

		saved, err := savedAttachRecord(taskID)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon attach: не удалось прочитать сохранённую сессию (%v); запустите: beacon attach <sessionID>\n", err)
			return 2
		}
		if saved.SessionID == "" {
			if taskID == "" {
				fmt.Fprintln(c.errOut, "beacon attach: сохранённой сессии нет; запустите: beacon attach <sessionID>")
			} else {
				fmt.Fprintf(c.errOut, "beacon attach: для задачи %s нет сохранённой сессии\n", taskID)
			}
			return 2
		}
		sessionID, err = registryResumeSession(saved)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon attach: расхождение identity сессии: %v\n", err)
			return 2
		}

		// WIRING-1686: at the work-restoration boundary, ask the pure guard
		// whether the branch can be opened again. A diverged branch is refused
		// so local commits are never overwritten; a branch held by another
		// worktree is reused instead of cloned (CD-02).
		answer := attachResumeCheck(c.root)
		switch answer.Decision {
		case worktree.ResumeRefuse:
			fmt.Fprintf(c.errOut, "beacon attach: %s\n", answer.Reason)
			return 2
		case worktree.ResumeReuse:
			fmt.Fprintf(c.out, "beacon attach: %s\n", answer.Reason)
		}
	}

	cfg, err := readServiceConfig()
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon attach: сервис opencode не запущен (%v); запустите: opencode service start\n", err)
		return 2
	}

	if !resume {
		// Tasks are known to the launcher, not to attach. BEACON_TASK_ID is
		// the seam that binds the saved session to its task; without it the
		// session is still saved as the last one for a bare --resume.
		if taskID == "" {
			taskID = strings.TrimSpace(os.Getenv("BEACON_TASK_ID"))
		}
		if err := rememberAttachSession(taskID, sessionID, time.Time{}); err != nil {
			fmt.Fprintf(c.errOut, "beacon attach: сессия %s открыта, но не сохранена для возобновления: %v\n", sessionID, err)
		}
	}

	if resume {
		after, revErr := attachRegistryRevision()
		if revErr != nil {
			fmt.Fprintf(c.errOut, "beacon attach: не удалось проверить реестр сессий (%v); перечитайте список сессий\n", revErr)
			return 2
		}
		if after != resumeRevision {
			fmt.Fprintf(c.errOut, "beacon attach: сессия изменилась между выбором id и подключением (%v); перечитайте список сессий\n", session.ErrSessionRevisionChanged)
			return 2
		}
	}

	res := c.run(c.root, "opencode", []string{"--server", cfg.URL, "--session", sessionID}, nil)
	emit(res, c.out, c.errOut)
	return res.code
}
