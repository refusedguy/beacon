package cibridge

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
)

// WatchResult is the orchestrator's own Watch as read back from the store.
type WatchResult struct {
	WatchID      string
	Verdict      domain.Verdict
	Revision     uint64
	ObligationID string
	ReadAt       time.Time
}

// RegisterSelfWatch creates the orchestrator's Watch on its own PR. The card id
// is the subscriber: it is the same target beacon_watch_pr receives as
// notify.target. Registration is idempotent by watch id and never resets a
// verdict.
func (b *readAPI) RegisterSelfWatch(cardID, repo string, pr int, headSHA string) (domain.Watch, error) {
	if b == nil || b.store == nil {
		return domain.Watch{}, fmt.Errorf("cibridge: register self watch: store is nil")
	}
	if cardID == "" || repo == "" || headSHA == "" || pr <= 0 {
		return domain.Watch{}, fmt.Errorf("cibridge: register self watch: cardID, repo, pr and headSHA are required")
	}
	id := fmt.Sprintf("watch:%s:%d:%s", repo, pr, headSHA)
	if existing, err := b.store.Watch(id); err == nil {
		return existing, nil
	}
	now := b.clock.Now()
	w := domain.Watch{
		ID:      id,
		Repo:    repo,
		PR:      pr,
		HeadSHA: headSHA,
		Status:  domain.StatusWatching,
		Verdict: domain.VerdictWatching,
		Subscribers: []domain.Subscriber{{
			ID:           cardID,
			Role:         domain.RoleOrchestrator,
			Adapter:      "opencode",
			Target:       cardID,
			AddedAt:      now,
			LastActivity: now,
		}},
		CreatedAt: now,
	}
	if err := b.store.SaveWatch(w); err != nil {
		return domain.Watch{}, fmt.Errorf("cibridge: save self watch: %w", err)
	}
	return w, nil
}

// ReadVerdict reads the verdict of a Watch together with the subscriber's
// obligation and stamps readAt on the first read. A stubbed watch id or a
// subscriber that does not own an obligation is an error: there is nothing in
// the store to confirm.
func (b *readAPI) ReadVerdict(ctx context.Context, watchID, subscriberID string) (WatchResult, error) {
	if b == nil || b.store == nil {
		return WatchResult{}, fmt.Errorf("cibridge: read verdict: store is nil")
	}
	w, err := b.store.Watch(watchID)
	if err != nil {
		return WatchResult{}, fmt.Errorf("cibridge: watch %s: %w", watchID, err)
	}
	id := domain.ObligationKey(watchID, w.Revision, subscriberID)
	o, err := b.store.Obligation(id)
	if err != nil {
		return WatchResult{}, fmt.Errorf("cibridge: obligation %s: %w", id, err)
	}
	if b.reader != nil {
		o, _, err = b.reader.StampRead(ctx, id, b.clock.Now())
		if err != nil {
			return WatchResult{}, fmt.Errorf("cibridge: stamp readAt: %w", err)
		}
	}
	return WatchResult{
		WatchID:      w.ID,
		Verdict:      w.Verdict,
		Revision:     w.Revision,
		ObligationID: o.ID,
		ReadAt:       o.ReadAt,
	}, nil
}

// ===== T3.225 сквозной прогон watch, CI, указатель, wake, прочтение (#320) =====

// ChainFacts is the external evidence of one full run.
type ChainFacts struct {
	WatchID      string
	Revision     uint64
	Verdict      domain.Verdict
	ObligationID string
	WakeTarget   string
	ReadAt       time.Time
}

// ConfirmRead verifies the read side of the chain against the store.
func (b *readAPI) ConfirmRead(watchID, obligationID string) (ChainFacts, error) {
	if b == nil || b.store == nil {
		return ChainFacts{}, errors.New("cibridge: no store")
	}
	o, err := b.store.Obligation(obligationID)
	if err != nil {
		return ChainFacts{}, err
	}
	if o.WatchID != watchID {
		return ChainFacts{}, fmt.Errorf("cibridge: obligation %s belongs to %s, not %s", obligationID, o.WatchID, watchID)
	}
	if o.ReadAt.IsZero() {
		return ChainFacts{}, fmt.Errorf("cibridge: obligation %s has no readAt: chain incomplete", obligationID)
	}
	w, err := b.store.Watch(watchID)
	if err != nil {
		return ChainFacts{}, err
	}
	target := ""
	for _, sub := range w.Subscribers {
		if sub.ID == o.SubscriberID {
			target = sub.Target
			break
		}
	}
	return ChainFacts{
		WatchID:      watchID,
		Revision:     o.Revision,
		Verdict:      o.VerdictPointer,
		ObligationID: obligationID,
		WakeTarget:   target,
		ReadAt:       o.ReadAt,
	}, nil
}
