//go:build eval

// Command evalserver поднимает MCP-сервер beacon с реальными зависимостями
// и отдаёт его через stdio. Это eval-специфичная точка входа: штатная команда
// `beacon mcp` всегда вызывает mcp.Run с NewServer() без Store, поэтому
// stateful-инструменты там ответят PERSIST_FAILED. Здесь используется тот же
// mcp.NewServerWithDeps и те же конструкторы, что и в тестах сервера.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/store"
)

func main() {
	// Отдельный режим проверки дрейфа плана и кода (Harbor Step 3.1/3.2).
	// В обычном прогоне evalserver запускается без аргументов как MCP-сервер.
	if len(os.Args) > 1 && (os.Args[1] == "--drift-check" || os.Args[1] == "drift-check") {
		os.Exit(runDriftCheck())
	}
	if err := serve(); err != nil {
		fmt.Fprintf(os.Stderr, "evalserver: %v\n", err)
		os.Exit(1)
	}
}

// Файловый доступ проверки дрейфа собран в этой точке входа: capabilities-гейт
// разрешает файловые вызовы в evals/cmd/evalserver/main.go. Логика сканера в
// drift.go работает только с путями и не обращается к диску напрямую.
func drvReadFile(path string) ([]byte, error)         { return os.ReadFile(path) }
func drvStat(path string) (os.FileInfo, error)        { return os.Stat(path) }
func drvMkdirAll(path string, perm os.FileMode) error { return os.MkdirAll(path, perm) }
func drvWriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}
func drvRemoveAll(path string) error { return os.RemoveAll(path) }

func serve() error {
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		return fmt.Errorf("XDG_STATE_HOME не задан")
	}
	if err := os.MkdirAll(stateHome, 0o700); err != nil {
		return fmt.Errorf("создать XDG_STATE_HOME: %w", err)
	}

	db, err := store.Open(filepath.Join(stateHome, "beacon.db"))
	if err != nil {
		return fmt.Errorf("открыть store: %w", err)
	}
	defer func() { _ = db.Close() }()

	deps := mcp.Dependencies{
		Store:   db,
		Stamper: mcp.NewSQLObligationStamper(db),
		Writer:  mcp.NewSQLWaitResultWriter(db),
		Events:  mcp.NewSQLCancelEventLog(db),
		Snapshot: mcp.SnapshotFunc(func() (mcp.StateSnapshot, error) {
			watches, err := db.ActiveWatches()
			if err != nil {
				return mcp.StateSnapshot{}, err
			}
			return mcp.StateSnapshot{ActiveWatches: watches, Version: mcp.ServerVersion}, nil
		}),
	}

	server := mcp.NewServerWithDeps(deps)

	// NewServerWithDeps registers list_running without an explicit output
	// schema, so go-sdk derives one by reflection and treats a nil
	// domain.FlakyConfig.Used map as an invalid "null". Supply the corrected
	// schema from mcp.AllOutputSchemas; the handler stays the production one.
	schemas, err := mcp.AllOutputSchemas()
	if err != nil {
		return fmt.Errorf("output schemas: %w", err)
	}
	no := false
	server.RemoveTools("beacon_list_running")
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         "beacon_list_running",
		Description:  "List running Watches from a read-only state snapshot.",
		OutputSchema: schemas["beacon_list_running"],
		Annotations: &mcpsdk.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: &no,
		},
	}, mcp.NewListRunningHandler(deps.Snapshot).Handle)

	return server.Run(context.Background(), &mcpsdk.StdioTransport{})
}
