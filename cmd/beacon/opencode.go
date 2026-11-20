package main

import (
	"context"
	"encoding/json"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// opencodePort is the part of the opencode client the CLI may use. Keeping it
// as a port lets future watch/spawn/deliver cards inject a fake without
// importing net/http into cmd/beacon, which the capabilities gate forbids
// (ADR 0005, ADR 0060 п1). The port knows no agent names from the domain core.
type opencodePort interface {
	ListAgents(context.Context) ([]opencode.Agent, error)
	Sessions(context.Context) ([]opencode.Session, error)
	Config(context.Context) (json.RawMessage, error)
	EventStream(context.Context, []string) <-chan opencode.EventResult
}

var _ opencodePort = (*opencode.Client)(nil)

// opencodeHTTPDoer is replaceable in tests. A nil value means the real HTTP
// client inside internal/infra/opencode.
var opencodeHTTPDoer opencode.Doer

// newOpencodePort reads $XDG_STATE_HOME/opencode/service.json and returns a
// live client for the address registered there. The client never starts a
// service and never hardcodes a port (ADR 0029, ADR 0060 п3).
func newOpencodePort() (opencodePort, error) {
	cfg, err := opencode.ReadConfig()
	if err != nil {
		return nil, err
	}
	return opencode.New(cfg, opencodeHTTPDoer)
}
