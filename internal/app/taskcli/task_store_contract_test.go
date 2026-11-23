package taskcli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// memLinks is the in-memory WatchLinkWriter used by the file backend in the
// contract suite. It keeps the same "one row per card" rule as card_links.
type memLinks struct {
	byCard map[string]TaskLink
}

func newMemLinks() *memLinks { return &memLinks{byCard: map[string]TaskLink{}} }

func (m *memLinks) Link(cardID, watchID string, pr int) (TaskLink, error) {
	if old, ok := m.byCard[cardID]; ok {
		return TaskLink{CardID: old.CardID, WatchID: old.WatchID, PR: old.PR, Duplicate: true}, nil
	}
	link := TaskLink{CardID: cardID, WatchID: watchID, PR: pr, Created: true}
	m.byCard[cardID] = link
	return link, nil
}

func (m *memLinks) Unlink(cardID string) error {
	delete(m.byCard, cardID)
	return nil
}

func (m *memLinks) byWatch(watchID string) (TaskLink, bool) {
	for _, link := range m.byCard {
		if link.WatchID == watchID {
			return link, true
		}
	}
	return TaskLink{}, false
}

// FakeTaskStore is an independent in-memory implementation of the port. It is
// the negative control for the contract suite: if the port leaked file-system
// details, this implementation could not pass the same tests.
type FakeTaskStore struct {
	cards map[string]Card
	links *memLinks
}

var _ TaskStore = (*FakeTaskStore)(nil)

func NewFakeTaskStore() *FakeTaskStore {
	return &FakeTaskStore{cards: map[string]Card{}, links: newMemLinks()}
}

func (f *FakeTaskStore) ListTodo() ([]Card, error) {
	out := make([]Card, 0, len(f.cards))
	for _, card := range f.cards {
		if card.Status == "todo" {
			out = append(out, card)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *FakeTaskStore) Claim(id, agent, when string) (Card, error) {
	card, ok := f.cards[id]
	if !ok {
		return Card{}, Validationf("карточка %s не найдена", id)
	}
	if card.Status != "todo" {
		return card, Validationf("карточка %s уже взята агентом %s в %s", id, card.Agent, card.Time)
	}
	card.Raw = ReplaceFields(card.Raw, agent, when)
	card.Status = "in_progress"
	card.Agent = agent
	card.Time = when
	f.cards[id] = card
	return card, nil
}

func (f *FakeTaskStore) CloseWithEvidence(id string, pr int, at time.Time) error {
	card, ok := f.cards[id]
	if !ok || pr <= 0 {
		return nil
	}
	text, changed, err := evidenceCloseText(card.Raw, pr, at)
	if err != nil || !changed {
		return err
	}
	card.Raw = text
	card.Status = "done"
	card.Reason = "already_landed"
	card.ClosedBy = "beacon"
	card.ClosedAt = at.UTC().Format(time.RFC3339)
	f.cards[id] = card
	return nil
}

func (f *FakeTaskStore) LinkWatch(watchID, cardID string, pr int) (TaskLink, error) {
	return linkWatch(f.links, watchID, cardID, pr)
}

type storedCardState struct {
	Exists   bool
	Status   string
	Reason   string
	ClosedBy string
	ClosedAt string
	Raw      string
}

type taskStoreContractEnv struct {
	store TaskStore
	seed  func(t *testing.T, id, status, agent, when string)
	state func(t *testing.T, id string) storedCardState
	link  func(t *testing.T, watchID string) (TaskLink, bool)
}

func contractCardText(id, status, agent, when string) string {
	return fmt.Sprintf("# %s. Контракт\nСтатус: %s\nФаза: P4\nТип: task\n"+
		"Владелец файлов: internal/app/taskcli\nЗависимости: нет\nСсылка: контракт\n"+
		"Агент: %s\nВремя: %s\nЦель: ц\nКонтекст: к\nШаги: ш\nПроверка: п\nПриёмка: пр\n",
		id, status, agent, when)
}

func contractStateFromText(t *testing.T, id, text string) storedCardState {
	t.Helper()
	card, err := taskstore.ParseCard(id+".md", []byte(text))
	if err != nil {
		t.Fatalf("parse %s: %v", id, err)
	}
	return storedCardState{
		Exists: true, Status: card.Status, Reason: card.Reason,
		ClosedBy: card.ClosedBy, ClosedAt: card.ClosedAt, Raw: text,
	}
}

func runTaskStoreContract(t *testing.T, env taskStoreContractEnv) {
	t.Helper()

	t.Run("ListTodoReturnsOnlyTodoCards", func(t *testing.T) {
		env.seed(t, "T4.1", "todo", "нет", "нет")
		env.seed(t, "T4.2", "in_progress", "agent-1", "2026-11-23T00:00:00Z")
		env.seed(t, "T4.3", "todo", "нет", "нет")
		cards, err := env.store.ListTodo()
		if err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(cards))
		for _, card := range cards {
			got = append(got, card.ID)
		}
		if len(got) != 2 || got[0] != "T4.1" || got[1] != "T4.3" {
			t.Fatalf("todo ids = %v, want [T4.1 T4.3]", got)
		}
	})

	t.Run("ClaimMovesTodoCardAndRejectsSecondClaim", func(t *testing.T) {
		env.seed(t, "T4.1", "todo", "нет", "нет")
		when := "2026-11-23T05:00:00Z"
		claimed, err := env.store.Claim("T4.1", "agent-2", when)
		if err != nil {
			t.Fatal(err)
		}
		if claimed.Status != "in_progress" || claimed.Agent != "agent-2" || claimed.Time != when {
			t.Fatalf("claimed card = %+v", claimed)
		}
		state := env.state(t, "T4.1")
		if state.Status != "in_progress" || !strings.Contains(state.Raw, "Агент: agent-2") {
			t.Fatalf("stored card = %+v", state)
		}
		if _, err := env.store.Claim("T4.1", "agent-3", when); err == nil ||
			!strings.Contains(err.Error(), "уже взята") {
			t.Fatalf("second claim error = %v", err)
		}
	})

	t.Run("ClaimRejectsMissingCard", func(t *testing.T) {
		if _, err := env.store.Claim("T4.9", "agent-2", "2026-11-23T05:00:00Z"); err == nil ||
			!strings.Contains(err.Error(), "не найдена") {
			t.Fatalf("missing claim error = %v", err)
		}
	})

	t.Run("CloseWithEvidenceWritesCloseFields", func(t *testing.T) {
		env.seed(t, "T4.1", "todo", "нет", "нет")
		at := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
		if err := env.store.CloseWithEvidence("T4.1", 42, at); err != nil {
			t.Fatal(err)
		}
		state := env.state(t, "T4.1")
		if state.Status != "done" || state.Reason != "already_landed" ||
			state.ClosedBy != "beacon" || state.ClosedAt != at.Format(time.RFC3339) {
			t.Fatalf("closed card = %+v", state)
		}
		if !strings.Contains(state.Raw, "PR #42 MERGED") {
			t.Fatalf("close marker missing: %s", state.Raw)
		}
	})

	t.Run("CloseWithEvidenceIsIdempotent", func(t *testing.T) {
		env.seed(t, "T4.1", "todo", "нет", "нет")
		at := time.Date(2026, 11, 23, 6, 0, 0, 0, time.UTC)
		if err := env.store.CloseWithEvidence("T4.1", 42, at); err != nil {
			t.Fatal(err)
		}
		before := env.state(t, "T4.1").Raw
		if err := env.store.CloseWithEvidence("T4.1", 42, at); err != nil {
			t.Fatal(err)
		}
		if after := env.state(t, "T4.1").Raw; after != before {
			t.Fatalf("repeated close changed the card:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})

	t.Run("CloseWithEvidenceMissingCardIsNoop", func(t *testing.T) {
		if err := env.store.CloseWithEvidence("T4.9", 42, time.Now()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("LinkWatchKeepsOneRowPerCardAndRepairsRetiredWatch", func(t *testing.T) {
		first, err := env.store.LinkWatch("watch-1", "T4.1", 42)
		if err != nil || !first.Created {
			t.Fatalf("first link = %+v, err = %v", first, err)
		}
		repeat, err := env.store.LinkWatch("watch-1", "T4.1", 42)
		if err != nil || !repeat.Duplicate {
			t.Fatalf("repeat link = %+v, err = %v", repeat, err)
		}
		got, ok := env.link(t, "watch-1")
		if !ok || got.CardID != "T4.1" || got.PR != 42 {
			t.Fatalf("stored link = %+v, ok = %v", got, ok)
		}
		if _, err := env.store.LinkWatch("watch-2", "T4.1", 43); err != nil {
			t.Fatal(err)
		}
		if _, ok := env.link(t, "watch-1"); ok {
			t.Fatal("retired watch still holds the card link")
		}
		got, ok = env.link(t, "watch-2")
		if !ok || got.CardID != "T4.1" || got.PR != 43 {
			t.Fatalf("repaired link = %+v, ok = %v", got, ok)
		}
	})
}

func TestTaskStoreContractFileAdapter(t *testing.T) {
	tasksDir := t.TempDir()
	links := newMemLinks()
	store := NewFileTaskStore(tasksDir, t.TempDir(), links)
	env := taskStoreContractEnv{
		store: store,
		seed: func(t *testing.T, id, status, agent, when string) {
			t.Helper()
			if err := os.WriteFile(cardPathFor(tasksDir, id),
				[]byte(contractCardText(id, status, agent, when)), 0644); err != nil {
				t.Fatal(err)
			}
		},
		state: func(t *testing.T, id string) storedCardState {
			t.Helper()
			data, err := os.ReadFile(cardPathFor(tasksDir, id))
			if errors.Is(err, os.ErrNotExist) {
				return storedCardState{}
			}
			if err != nil {
				t.Fatal(err)
			}
			return contractStateFromText(t, id, string(data))
		},
		link: func(t *testing.T, watchID string) (TaskLink, bool) {
			t.Helper()
			return links.byWatch(watchID)
		},
	}
	runTaskStoreContract(t, env)
}

func TestTaskStoreContractFakeAdapter(t *testing.T) {
	store := NewFakeTaskStore()
	env := taskStoreContractEnv{
		store: store,
		seed: func(t *testing.T, id, status, agent, when string) {
			t.Helper()
			card, err := taskstore.ParseCard(id+".md", []byte(contractCardText(id, status, agent, when)))
			if err != nil {
				t.Fatal(err)
			}
			store.cards[id] = card
		},
		state: func(t *testing.T, id string) storedCardState {
			t.Helper()
			card, ok := store.cards[id]
			if !ok {
				return storedCardState{}
			}
			return contractStateFromText(t, id, card.Raw)
		},
		link: func(t *testing.T, watchID string) (TaskLink, bool) {
			t.Helper()
			return store.links.byWatch(watchID)
		},
	}
	runTaskStoreContract(t, env)
}
