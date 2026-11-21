package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const attachSessionsFile = "attach-sessions.json"

// attachSession is one session id saved for a task before the task starts.
// The id outlives a timeout kill, so attach can reopen the same session
// instead of allocating a new one (ADR 0043 §6). DeadlineAt is the task's hard
// limit from ADR 0042 §1: reaching it ends the task but never erases the id.
type attachSession struct {
	TaskID     string    `json:"taskID"`
	SessionID  string    `json:"sessionID"`
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

// serviceConfigPath returns the registration file of the running OpenCode
// service. It mirrors stateDir() but points at the opencode state directory
// instead of beacon.
func serviceConfigPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("не удалось определить домашний каталог: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "opencode", "service.json"), nil
}

// readServiceConfig reads and validates the service registration file. A
// missing or malformed file means the background service is not running.
func readServiceConfig() (opencodeService, error) {
	path, err := serviceConfigPath()
	if err != nil {
		return opencodeService{}, err
	}
	// os.DirFS + fs.ReadFile keeps file access out of cmd's forbidden
	// os.ReadFile capability (ADR 0005 capabilities gate).
	data, err := fs.ReadFile(os.DirFS(filepath.Dir(path)), filepath.Base(path))
	if err != nil {
		return opencodeService{}, fmt.Errorf("%s: %w", path, err)
	}
	var cfg opencodeService
	if err := json.Unmarshal(data, &cfg); err != nil {
		return opencodeService{}, fmt.Errorf("%s: неверный JSON: %w", path, err)
	}
	if cfg.URL == "" {
		return opencodeService{}, fmt.Errorf("%s: нет поля url", path)
	}
	return cfg, nil
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
// RFC3339 instant, records the task's hard limit without deleting the id.
func rememberAttachSession(taskID, sessionID string, now time.Time) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return fmt.Errorf("пустой sessionID")
	}
	taskID = strings.TrimSpace(taskID)
	if now.IsZero() {
		now = time.Now().UTC()
	}

	rec := attachSession{TaskID: taskID, SessionID: sessionID, SavedAt: now.UTC()}
	if raw := strings.TrimSpace(os.Getenv("BEACON_TASK_DEADLINE")); raw != "" {
		if at, err := time.Parse(time.RFC3339, raw); err == nil {
			rec.DeadlineAt = at.UTC()
		}
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

// savedAttachSession returns the session id saved for taskID. With an empty
// taskID it uses the last task recorded by rememberAttachSession. A deadline
// in the past does not hide the id: the task ended, but the session remains
// the one to resume (ADR 0043 §6).
func savedAttachSession(taskID string) (string, error) {
	st, err := loadAttachSessions()
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(taskID)
	if key == "" {
		key = st.Last
	}
	for _, rec := range st.Sessions {
		if rec.TaskID == key && rec.SessionID != "" {
			return rec.SessionID, nil
		}
	}
	return "", nil
}

// registryResumeSession confirms the saved session id in the durable session
// registry (WIRING-488). Resume reaches GetSession through the app-layer port,
// so cmd never opens a database for this lookup. The registry is best effort:
// when it is absent, reports an error or has no row, attach keeps the id from
// attach-sessions.json, which ADR 0043 §6 makes the source of truth for resume.
// A stored id replaces the saved one so both registries converge on one session.
func registryResumeSession(sessionID string) string {
	reg := sessionRegistryForSpawn()
	if reg == nil {
		return sessionID
	}
	stored, found, err := reg.Resume(sessionID)
	if err != nil || !found {
		return sessionID
	}
	if id := strings.TrimSpace(stored.ID); id != "" {
		return id
	}
	return sessionID
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

	if resume {
		saved, err := savedAttachSession(taskID)
		if err != nil {
			fmt.Fprintf(c.errOut, "beacon attach: не удалось прочитать сохранённую сессию (%v); запустите: beacon attach <sessionID>\n", err)
			return 2
		}
		if saved == "" {
			if taskID == "" {
				fmt.Fprintln(c.errOut, "beacon attach: сохранённой сессии нет; запустите: beacon attach <sessionID>")
			} else {
				fmt.Fprintf(c.errOut, "beacon attach: для задачи %s нет сохранённой сессии\n", taskID)
			}
			return 2
		}
		sessionID = saved
		sessionID = registryResumeSession(sessionID)
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

	res := c.run(c.root, "opencode", []string{"--server", cfg.URL, "--session", sessionID}, nil)
	emit(res, c.out, c.errOut)
	return res.code
}
