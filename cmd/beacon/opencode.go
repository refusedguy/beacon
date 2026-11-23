package main

import (
	"context"
	"encoding/json"

	"github.com/refusedguy/beacon/internal/infra/opencode"
)

// opencodeReader is the read-only view of the one OpenCode client. It is the
// part beacon watch and registry sync need; it exists so the composition root
// can still install a stand-in without HTTP (ADR 0005, ADR 0060 п1).
type opencodeReader interface {
	ListAgents(context.Context) ([]opencode.Agent, error)
	Sessions(context.Context) ([]opencode.Session, error)
	Config(context.Context) (json.RawMessage, error)
	EventStream(context.Context, []string) <-chan opencode.EventResult
}

// opencodePort is the one port through which every OpenCode call in cmd/beacon
// goes. It carries both the read routes and the write routes. There is no
// second write port: spawn, deliver and wake all resolve the same installed
// object (ADR 0092). PromptWithOptions is the write route spawn uses for the
// first message of a created session (ADR 0060 п3, ADR 0309).
type opencodePort interface {
	opencodeReader
	CreateSession(parentID, agent, title string, metadata map[string]string) (string, error)
	Prompt(sessionID, text string) error
	PromptWithOptions(ctx context.Context, sessionID, text string, opts opencode.PromptOptions) error
}

// livePort is the single adapter from the context-aware HTTP client to the
// context-free port used by spawn and deliver. It replaces the old
// clientSessionCreator, sessionCreator and deliveryTarget adapters.
type livePort struct {
	client *opencode.Client
}

var _ opencodePort = (*livePort)(nil)

func (p *livePort) ListAgents(ctx context.Context) ([]opencode.Agent, error) {
	return p.client.ListAgents(ctx)
}

func (p *livePort) Sessions(ctx context.Context) ([]opencode.Session, error) {
	return p.client.Sessions(ctx)
}

func (p *livePort) Config(ctx context.Context) (json.RawMessage, error) {
	return p.client.Config(ctx)
}

func (p *livePort) EventStream(ctx context.Context, types []string) <-chan opencode.EventResult {
	return p.client.EventStream(ctx, types)
}

// CreateSession posts the session and returns its opaque id.
func (p *livePort) CreateSession(parentID, agent, title string, metadata map[string]string) (string, error) {
	session, err := p.client.CreateSession(context.Background(), parentID, agent, title, metadata)
	if err != nil {
		return "", err
	}
	return session.ID, nil
}

// Prompt posts one message into an existing session.
func (p *livePort) Prompt(sessionID, text string) error {
	return p.client.Prompt(context.Background(), sessionID, text)
}

// PromptWithOptions posts one message with the v2 id, delivery and resume
// fields. Spawn uses it so the first prompt of a created session continues its
// turn instead of starting a second context (ADR 0306, ADR 0309).
func (p *livePort) PromptWithOptions(ctx context.Context, sessionID, text string, opts opencode.PromptOptions) error {
	return p.client.PromptWithOptions(ctx, sessionID, text, opts)
}

// resolveOpencodeConfig reads the registration written by opencode serve.
// There is no fallback starter: Discover is the only source of the config
// (ADR 0029, ADR 0092).
func resolveOpencodeConfig() (opencode.Config, error) {
	return opencode.Discover()
}

// opencodeHTTPDoer is replaceable in tests. A nil value means the real HTTP
// client inside internal/infra/opencode.
var opencodeHTTPDoer opencode.Doer

// installedOpenCodePort is the single client object shared by every command.
var installedOpenCodePort opencodePort

// SetOpenCodePort installs the one port and fans it out to the command seams.
// A nil argument clears all of them, so a command without a client reports an
// honest error instead of dereferencing nil.
func SetOpenCodePort(p opencodePort) {
	installedOpenCodePort = p
	if p == nil {
		SetSessionSpawner(nil)
		SetDeliveryTarget(nil)
		return
	}
	SetSessionSpawner(p)
	SetDeliveryTarget(observingDeliveryTarget{next: p})
}

// openCodePort returns the installed client. It may be nil.
func openCodePort() opencodePort { return installedOpenCodePort }

// SetSessionCreator is the legacy seam kept for tests that install a narrow
// write stand-in. Production uses SetOpenCodePort.
func SetSessionCreator(c interface {
	CreateSession(string, string, string, map[string]string) (string, error)
	Prompt(string, string) error
	PromptWithOptions(context.Context, string, string, opencode.PromptOptions) error
}) {
	if c == nil {
		SetSessionSpawner(nil)
		SetDeliveryTarget(nil)
		return
	}
	SetSessionSpawner(c)
	SetDeliveryTarget(c)
}

// newOpencodePort resolves the registration and builds the one client. The
// address and password come from Discover; no fallback starter is installed.
func newOpencodePort() (opencodePort, error) {
	cfg, err := resolveOpencodeConfig()
	if err != nil {
		return nil, err
	}
	client, err := opencode.New(cfg, opencodeHTTPDoer)
	if err != nil {
		return nil, err
	}
	return &livePort{client: client}, nil
}

// newReadOnlyOpencodePort builds a client from an existing service.json only.
// It never installs a fallback server starter, so the MCP wake path cannot
// spawn opencode serve. A missing registration returns an error; callers that
// treat the port as optional leave the wake ports nil and use their CLI path.
func newReadOnlyOpencodePort() (opencodeReader, error) {
	cfg, err := opencode.Discover()
	if err != nil {
		return nil, err
	}
	return opencode.New(cfg, opencodeHTTPDoer)
}
