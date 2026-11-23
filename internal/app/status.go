package app

import (
	"context"
	"time"

	"github.com/refusedguy/beacon/pkg/core"
)

type StatusService struct{ Store RunStore }

// DefaultRecentLimit is the number of finished runs returned when no limit is set.
const DefaultRecentLimit = 20

func (s StatusService) Active(ctx context.Context) ([]*core.Run, error) {
	return s.Store.ListActive(ctx)
}

func (s StatusService) Recent(ctx context.Context, limit int) ([]*core.Run, error) {
	if limit <= 0 {
		limit = DefaultRecentLimit
	}
	return s.Store.ListRecent(ctx, limit)
}

type WaitService struct {
	Store    RunStore
	Interval time.Duration
}

// WaitFor blocks until the run is terminal or the context deadline expires.
func (w WaitService) WaitFor(ctx context.Context, id core.RunID) (*core.Run, error) {
	interval := w.Interval
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		run, err := w.Store.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if run == nil {
			return nil, core.Errorf(core.CodeNotFound, "run %s", id)
		}
		if run.State.Terminal() || run.State == core.RunWaitingCI {
			return run, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}
