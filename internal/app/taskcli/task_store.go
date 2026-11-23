package taskcli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// TaskStore is the port over task persistence. The only backend today is
// docs/tasks plus the taskstore helpers. A future JSON file, GitHub Issues or
// SQLite backend implements these four methods, so the claim, watch and merge
// callers do not change when the storage moves.
type TaskStore interface {
	// ListTodo returns the cards that can be claimed, in backend order.
	ListTodo() ([]Card, error)
	// Claim moves one todo card to in_progress. A missing card or a card that
	// is no longer todo returns the CLI validation error.
	Claim(id, agent, when string) (Card, error)
	// CloseWithEvidence marks a card done with a merged PR as evidence. The PR
	// marker in the card makes repeated calls a no-op.
	CloseWithEvidence(id string, pr int, at time.Time) error
	// LinkWatch binds a card to a Watch and PR. A link to a retired Watch is
	// moved to the live one.
	LinkWatch(watchID, cardID string, pr int) (TaskLink, error)
}

// TaskLink is the stored binding between a card and a Watch.
type TaskLink struct {
	CardID    string
	WatchID   string
	PR        int
	Created   bool
	Duplicate bool
}

// WatchLinkWriter is the narrow storage seam LinkWatch needs. The production
// implementation wraps the card_links table; tests use an in-memory one.
type WatchLinkWriter interface {
	Link(cardID, watchID string, pr int) (TaskLink, error)
	Unlink(cardID string) error
}

// FileTaskStore is the adapter over docs/tasks. It reuses the load, lock and
// close writers from taskstore instead of repeating their file format.
type FileTaskStore struct {
	TasksDir string
	Root     string
	Links    WatchLinkWriter

	// ReadCard defaults to os.ReadFile. The app layer keeps file reads behind
	// the seam, and tests inject a reader that models a competing writer.
	ReadCard func(path string) ([]byte, error)
	// WriteFile defaults to taskstore.AtomicWrite. The composition root keeps
	// its own write seam here so the merged-card path writes through it.
	WriteFile func(path string, data []byte, perm os.FileMode) error
	// Acquire defaults to the shared card lock. The app service injects its
	// own lock so the configured retry interval still applies.
	Acquire func() (cardLock, error)
}

// NewFileTaskStore builds the file adapter over one task directory.
func NewFileTaskStore(tasksDir, root string, links WatchLinkWriter) *FileTaskStore {
	return &FileTaskStore{
		TasksDir:  tasksDir,
		Root:      root,
		Links:     links,
		WriteFile: taskstore.AtomicWrite,
		Acquire: func() (cardLock, error) {
			return taskstore.AcquireLock(taskstore.LockPath("", root), root)
		},
	}
}

// compile-time proof that the file adapter satisfies the port.
var _ TaskStore = (*FileTaskStore)(nil)

// ListTodo reads the cards and keeps the todo ones. Order comes from LoadCards.
func (s *FileTaskStore) ListTodo() ([]Card, error) {
	cards, err := taskstore.LoadCards(s.TasksDir)
	if err != nil {
		return nil, err
	}
	out := make([]Card, 0, len(cards))
	for _, c := range cards {
		if c.Status == "todo" {
			out = append(out, c)
		}
	}
	return out, nil
}

// Claim writes the same three fields as the CLI take path, under the same lock.
func (s *FileTaskStore) Claim(id, agent, when string) (Card, error) {
	lock, err := s.lock()
	if err != nil {
		return Card{}, err
	}
	defer lock.Release()

	cards, err := taskstore.LoadCards(s.TasksDir)
	if err != nil {
		return Card{}, err
	}
	var target *Card
	for i := range cards {
		if cards[i].ID == id {
			target = &cards[i]
			break
		}
	}
	if target == nil {
		return Card{}, Validationf("карточка %s не найдена", id)
	}
	if target.Status != "todo" {
		return *target, Validationf("карточка %s уже взята агентом %s в %s", id, target.Agent, target.Time)
	}
	newText := taskstore.ReplaceFields(target.Raw, agent, when)
	if err := s.write(target.Path, []byte(newText), 0644); err != nil {
		return Card{}, err
	}
	target.Status = "in_progress"
	target.Agent = agent
	target.Time = when
	target.Raw = newText
	return *target, nil
}

// CloseWithEvidence writes the canonical close fields and the PR confirmation.
// A card that already carries the marker is left untouched.
func (s *FileTaskStore) CloseWithEvidence(id string, pr int, at time.Time) error {
	cardID := strings.TrimSpace(id)
	if cardID == "" || pr <= 0 {
		return nil
	}
	lock, err := s.lock()
	if err != nil {
		return err
	}
	defer lock.Release()

	path := taskstore.CardPath(s.TasksDir, cardID)
	read := s.ReadCard
	if read == nil {
		read = os.ReadFile
	}
	data, err := read(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return Startupf("не прочитана карточка %s: %v", cardID, err)
	}
	text, changed, err := evidenceCloseText(string(data), pr, at)
	if err != nil || !changed {
		return err
	}
	return s.write(path, []byte(text), 0644)
}

// LinkWatch stores the link and repairs a link that still points at a closed
// Watch. The repair is the same unlink-then-link sequence the composition root
// used before the port existed.
func (s *FileTaskStore) LinkWatch(watchID, cardID string, pr int) (TaskLink, error) {
	if s == nil || s.Links == nil {
		return TaskLink{}, nil
	}
	return linkWatch(s.Links, watchID, cardID, pr)
}

// linkWatch is shared by every TaskStore adapter, so the repair rule lives in
// one place and the fake backend cannot drift from the file one.
func linkWatch(links WatchLinkWriter, watchID, cardID string, pr int) (TaskLink, error) {
	link, err := links.Link(cardID, watchID, pr)
	if err != nil {
		return TaskLink{}, err
	}
	if link.Duplicate && (link.WatchID != watchID || link.PR != pr) {
		if err := links.Unlink(cardID); err != nil {
			return TaskLink{}, err
		}
		return links.Link(cardID, watchID, pr)
	}
	return link, nil
}

func (s *FileTaskStore) lock() (cardLock, error) {
	if s != nil && s.Acquire != nil {
		return s.Acquire()
	}
	root := ""
	if s != nil {
		root = s.Root
	}
	return taskstore.AcquireLock(taskstore.LockPath("", root), root)
}

func (s *FileTaskStore) write(path string, data []byte, perm os.FileMode) error {
	write := taskstore.AtomicWrite
	if s != nil && s.WriteFile != nil {
		write = s.WriteFile
	}
	return write(path, data, perm)
}

// evidenceCloseText builds the close payload once for every adapter. changed
// is false when the card already carries the PR marker, so a repeated merged
// observation leaves the stored card untouched.
func evidenceCloseText(raw string, pr int, at time.Time) (string, bool, error) {
	marker := fmt.Sprintf("PR #%d MERGED", pr)
	if strings.Contains(raw, marker) {
		return raw, false, nil
	}
	text, err := taskstore.ApplyCloseFields(raw, evidenceCloseOptions(at))
	if err != nil {
		return "", false, err
	}
	text = clearCardField(text, "Состояние")
	text = clearCardField(text, "Вопрос")
	text += fmt.Sprintf("\n## Подтверждение\n- %s: %s (squash).\n",
		marker, at.UTC().Format("2006-01-02"))
	return text, true, nil
}

// evidenceCloseOptions is the canonical close payload for a merged PR. The
// timestamp is explicit so repeated observations write byte-identical fields.
func evidenceCloseOptions(at time.Time) taskstore.CloseCardOptions {
	return taskstore.CloseCardOptions{
		Status:   "done",
		Reason:   "already_landed",
		ClosedBy: "beacon",
		ClosedAt: at.UTC().Format(time.RFC3339),
	}
}

// clearCardField removes one field line. It matches setCardField(raw, name, "")
// in cmd/beacon: an absent field stays absent and no blank value is written.
func clearCardField(raw, name string) string {
	cr := ""
	if strings.Contains(raw, "\r\n") {
		cr = "\r"
	}
	prefix := name + ":"
	out := make([]string, 0, strings.Count(raw, "\n")+1)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, prefix) {
			continue
		}
		out = append(out, line+cr)
	}
	return strings.Join(out, "\n")
}

// cardPathFor is the path helper the contract tests use to seed and inspect a
// file backend without repeating filepath.Join in every assertion.
func cardPathFor(tasksDir, id string) string {
	return filepath.Join(tasksDir, strings.TrimSpace(id)+".md")
}
