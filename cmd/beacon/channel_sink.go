package main

import (
	"context"

	appcommand "github.com/refusedguy/beacon/internal/app/command"
	"github.com/refusedguy/beacon/internal/infra/channel"
)

// channelCommandSink maps the concrete ingress command onto the app-owned
// projection. The adapter lives at the composition root so the processor
// declares no infra import (ARDH-15). The two shapes carry the same fields and
// JSON names, so the durable effect keeps its existing wire format.
type channelCommandSink struct {
	inner *appcommand.ChannelSink
}

var _ channel.CommandSink = (*channelCommandSink)(nil)

func (s *channelCommandSink) Accept(ctx context.Context, cmd channel.Command) error {
	if s == nil || s.inner == nil {
		return appcommand.ErrNilProcessor
	}
	return s.inner.Accept(ctx, appcommand.ChannelCommand{
		Actor:     string(cmd.Actor),
		Verb:      cmd.Verb,
		Target:    cmd.Target,
		Note:      cmd.Note,
		RequestID: cmd.RequestID,
	})
}
