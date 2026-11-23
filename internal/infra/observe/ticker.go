package observe

import (
	"context"
	"time"
)

// RunTicker calls tick on each interval until ctx is done. It stops the ticker
// on return. The package is the shared leaf that every layer may import, so the
// context-aware loop has one implementation across cmd, app and infra.
func RunTicker(ctx context.Context, interval time.Duration, tick func(context.Context)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick(ctx)
		}
	}
}
