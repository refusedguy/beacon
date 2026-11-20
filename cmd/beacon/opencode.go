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

// sessionCreator is the narrow POST port shared by beacon spawn and deliver.
// Both commands write one message into a live session, so the same two methods
// serve both. Defining the port here lets the composition root install one
// client behind both commands as soon as T3.150 adds the POST routes, with no
// net/http import in cmd/beacon (ADR 0005, ADR 0060 п3 and п4).
type sessionCreator interface {
	CreateSession(parentID, agent, title string, metadata map[string]string) (string, error)
	Prompt(sessionID, text string) error
}

// serverEnsurer is the fallback-server port of T3.151. A client that can ensure
// a running server returns the registration it resolved, including the case
// where it had to spawn opencode serve on a free port. This is the expected
// T3.151 contract. installOpenCodePorts installs it by type assertion, so a
// client without it keeps the service.json-only behaviour and the build stays
// green until that card merges.
type serverEnsurer interface {
	EnsureServer(context.Context) (opencode.Config, error)
}

// serverEnsurerFunc adapts a package-level function to serverEnsurer. T3.151
// may expose EnsureServer as a package function; the adapter lets the wiring
// install it without moving the boundary into cmd/beacon.
type serverEnsurerFunc func(context.Context) (opencode.Config, error)

func (f serverEnsurerFunc) EnsureServer(ctx context.Context) (opencode.Config, error) {
	return f(ctx)
}

// installedServerEnsurer is the fallback server starter. Nil means the CLI
// still reads service.json only. wireOpenCodePorts installs one by type
// assertion when the client exposes the port.
var installedServerEnsurer serverEnsurer

// SetSessionCreator installs one POST-capable client as both the spawn and the
// delivery port. A nil argument clears both, so a command without a port
// reports an honest error instead of dereferencing nil.
func SetSessionCreator(c sessionCreator) {
	if c == nil {
		SetSessionSpawner(nil)
		SetDeliveryTarget(nil)
		return
	}
	SetSessionSpawner(c)
	SetDeliveryTarget(c)
}

// SetServerEnsurer installs the fallback server starter. A nil argument
// restores the service.json-only path.
func SetServerEnsurer(e serverEnsurer) { installedServerEnsurer = e }

// resolveOpencodeConfig prefers the installed fallback starter and otherwise
// reads service.json. Keeping the choice in one seam lets a stand-in drive the
// whole path in tests without a live server or a hardcoded port (ADR 0060 п3).
func resolveOpencodeConfig(ctx context.Context) (opencode.Config, error) {
	if installedServerEnsurer != nil {
		return installedServerEnsurer.EnsureServer(ctx)
	}
	return opencode.ReadConfig()
}

// opencodeHTTPDoer is replaceable in tests. A nil value means the real HTTP
// client inside internal/infra/opencode.
var opencodeHTTPDoer opencode.Doer

// newOpencodePort resolves the service registration and returns a live client
// for its address. The client never hardcodes a port (ADR 0029, ADR 0060 п3).
// Until T3.151 lands, resolution is service.json only; a client with the
// serverEnsurer port installs the fallback path through SetServerEnsurer.
func newOpencodePort() (opencodePort, error) {
	cfg, err := resolveOpencodeConfig(context.Background())
	if err != nil {
		return nil, err
	}
	return opencode.New(cfg, opencodeHTTPDoer)
}
