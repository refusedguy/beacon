package taskcli

import (
	"github.com/refusedguy/beacon/internal/infra/taskstore"
)

// rateLimitRepo owns the card I/O of the 429 fallback. The rate-limit state is
// the card itself: its path is the file, the shared lock guards it, LoadCards
// reads it and AtomicWrite writes it. The fallback rule stays in the service.
type rateLimitRepo struct {
	cfg Config
}

func newRateLimitRepo(cfg Config) rateLimitRepo {
	return rateLimitRepo{cfg: cfg}
}

func (r rateLimitRepo) lock() (func(), error) {
	lock, err := taskstore.AcquireLock(taskstore.LockPath(r.cfg.StateDir, r.cfg.Root), r.cfg.Root)
	if err != nil {
		return nil, err
	}
	return func() { _ = lock.Release() }, nil
}

func (r rateLimitRepo) load(id string) (*taskstore.Card, error) {
	cards, err := taskstore.LoadCards(r.cfg.TasksDir)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		if cards[i].ID == id {
			return &cards[i], nil
		}
	}
	return nil, taskstore.Validationf("карточка %s не найдена", id)
}

func (r rateLimitRepo) save(card taskstore.Card, text string) error {
	return taskstore.AtomicWrite(card.Path, []byte(text), 0644)
}
