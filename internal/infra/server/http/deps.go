package httproute

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/refusedguy/beacon/internal/domain"
)

// WatchReader is the read-only port behind the live watches projection. It has
// no write method, so a handler holding it cannot change Beacon state.
type WatchReader interface {
	Watches(ctx context.Context) ([]domain.Watch, error)
}

// WatchReaderFunc adapts a function to WatchReader without adding a type to
// the composition root.
type WatchReaderFunc func(ctx context.Context) ([]domain.Watch, error)

func (f WatchReaderFunc) Watches(ctx context.Context) ([]domain.Watch, error) { return f(ctx) }

// Command is one state-changing request a handler sends to a CommandSink. Kind
// names the operation, Target names the object it acts on and Actor identifies
// the caller for the audit log.
type Command struct {
	Kind   string
	Target string
	Actor  string
}

// CommandResult acknowledges one command. The sink assigns ID and decides
// whether Status needs a value.
type CommandResult struct {
	ID     string
	Status string
}

// CommandSink applies one state-changing command. The sink owns idempotency
// and audit; the transport only names the command.
type CommandSink interface {
	Apply(ctx context.Context, cmd Command) (CommandResult, error)
}

// CommandSinkFunc adapts a function to CommandSink without adding a type to
// the composition root.
type CommandSinkFunc func(ctx context.Context, cmd Command) (CommandResult, error)

func (f CommandSinkFunc) Apply(ctx context.Context, cmd Command) (CommandResult, error) {
	return f(ctx, cmd)
}

// Deps holds the ports an HTTP handler may call. The composition root builds
// one value at startup and passes the same pointer to every handler. A test
// builds the same value with mocks, so a handler needs neither a live store
// nor a network listener. Adding a service means one more port here and one
// more accessor, with no change to the router.
type Deps struct {
	watches  WatchReader
	commands CommandSink
}

// NewDeps builds Deps from the given ports. A nil port is kept rather than
// replaced: Validate reports it and the accessors return a typed error.
func NewDeps(watches WatchReader, commands CommandSink) *Deps {
	return &Deps{watches: watches, commands: commands}
}

// ErrMissingPort marks an error caused by a port that was never injected.
var ErrMissingPort = errors.New("httproute: missing port")

// MissingError names the ports a Deps value lacks. It unwraps to
// ErrMissingPort so callers can test the cause with errors.Is.
type MissingError struct {
	Ports []string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("httproute: missing ports: %s", strings.Join(e.Ports, ", "))
}

func (e *MissingError) Unwrap() error { return ErrMissingPort }

func missing(name string) error {
	return &MissingError{Ports: []string{name}}
}

// ReadWatches reads the live watches through the injected reader. A missing
// reader yields ErrMissingPort instead of a nil-interface panic.
func (d *Deps) ReadWatches(ctx context.Context) ([]domain.Watch, error) {
	if d == nil || d.watches == nil {
		return nil, missing("watches")
	}
	return d.watches.Watches(ctx)
}

// Apply sends cmd to the injected sink. A missing sink yields ErrMissingPort
// instead of a nil-interface panic.
func (d *Deps) Apply(ctx context.Context, cmd Command) (CommandResult, error) {
	if d == nil || d.commands == nil {
		return CommandResult{}, missing("commands")
	}
	return d.commands.Apply(ctx, cmd)
}

// Validate reports every port that was not injected. The composition root
// calls it before serving, so a wiring mistake stops startup instead of a
// request. A nil Deps reports both ports as missing.
func (d *Deps) Validate() error {
	missingPorts := make([]string, 0, 2)
	if d == nil || d.watches == nil {
		missingPorts = append(missingPorts, "watches")
	}
	if d == nil || d.commands == nil {
		missingPorts = append(missingPorts, "commands")
	}
	if len(missingPorts) == 0 {
		return nil
	}
	return &MissingError{Ports: missingPorts}
}
