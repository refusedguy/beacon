// This file owns the ticker that drives reminder passes. It takes a notify
// function instead of a notifier type, so no edge from internal/app to itself
// is needed and cmd can supply the concrete notifier.
package orchestrator

import (
	"context"
	"time"

	// ARDH-15: observe is the documented cross-cutting exception. The loop
	// reports a failed pass here instead of threading a logger through callers.
	"github.com/refusedguy/beacon/internal/infra/observe"
)

// DefaultReminderInterval is the pass interval used when the caller passes a
// non-positive duration.
const DefaultReminderInterval = time.Minute

// NotifyFunc runs one reminder pass. The composition root builds it over the
// app notifier and the store adapter.
type NotifyFunc func(ctx context.Context) error

// RunNotifyLoop ticks until ctx is done and calls notify on each tick. A
// non-positive interval falls back to DefaultReminderInterval. A notify error
// goes to the observe seam and never stops the loop. A nil notify returns at
// once.
func RunNotifyLoop(ctx context.Context, notify NotifyFunc, interval time.Duration) {
	if notify == nil {
		return
	}
	if interval <= 0 {
		interval = DefaultReminderInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := notify(ctx); err != nil {
				observe.ActorError("reminder-loop", "", err)
			}
		}
	}
}
