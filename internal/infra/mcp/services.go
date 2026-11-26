package mcp

import (
	"context"
	"reflect"

	"github.com/refusedguy/beacon/internal/app"
	"github.com/refusedguy/beacon/pkg/core"
)

// Services holds the application services the MCP tools call.
type Services struct {
	Dispatcher  *app.Dispatcher
	Control     *app.Control
	Status      app.StatusService
	Wait        app.WaitService
	TaskCreator *app.TaskCreator
	TaskDeleter *app.TaskDeleter
	TaskUpdater *app.TaskUpdater
	Reconcile   *app.ReconcileService
	Remote      func(ctx context.Context, action, runID string, pr int, sha string) error
}

// requireService returns a coded unavailable error when none of ports is
// configured. name stays in the message so a caller knows which port is
// missing. Ports are pointers or funcs, and a nil value of either kind does
// not equal nil once it is held in an interface, so the check reads the
// underlying value.
func requireService(name string, ports ...any) error {
	for _, port := range ports {
		if !isNilPort(port) {
			return nil
		}
	}
	return core.Errorf(core.CodeUnavailable, "%s is not configured", name)
}

func isNilPort(port any) bool {
	if port == nil {
		return true
	}
	v := reflect.ValueOf(port)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
