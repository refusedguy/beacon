//go:build eval

// Command eval прогоняет Harbor-подобные проверки MCP-сервера beacon.
//
// Раннер не пишет свой JSON-клиент: используется официальный go-sdk и его
// CommandTransport, который поднимает MCP-сервер отдельным процессом и
// общается с ним по stdin/stdout. Процесс: eval-сервер из evals/cmd/evalserver,
// собранный через mcp.NewServerWithDeps с реальным SQLite-store. Причина:
// штатная команда `beacon mcp` вызывает mcp.Run → NewServer() без Store и не
// принимает store ни через env, ни через флаг. Подробности в evals/README.md.
//
// Запуск: GO=/tmp/opencode/go126/bin/go bash evals/run.sh
// Обычный go test ./... этот файл не видит из-за build tag eval.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	serverName = "beacon"
	// contractToolCount: ровно семь имён из контракта. Порядок не важен.
	contractToolCount = 7
	// evalHeadSHA передаётся явно, чтобы обработчик не ходил в GitHub.
	evalHeadSHA = "0123456789abcdef0123456789abcdef01234567"
	// sessionIDMetaKey: ключ, под которым get_result ищет идентификатор
	// вызывающего в _meta. Значение непрозрачно для ядра (ADR 0001).
	sessionIDMetaKey = "ai.opencode/sessionID"
	evalSessionID    = "beacon-evals-session"
)

var contractTools = []string{
	"beacon_watch_pr",
	"beacon_wait_result",
	"beacon_get_result",
	"beacon_list_running",
	"beacon_ack",
	"beacon_cancel_watch",
	"beacon_stats",
}

type evalResult struct {
	name     string
	expected string
	actual   string
	pass     bool
}

func main() {
	os.Exit(run())
}

func run() int {
	results := make([]evalResult, 0, 8)
	record := func(name, expected, actual string, pass bool) {
		results = append(results, evalResult{name: name, expected: expected, actual: actual, pass: pass})
	}

	repoRoot, err := findRepoRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: %v\n", err)
		return 1
	}
	serverBin, err := buildEvalServer(repoRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: %v\n", err)
		return 1
	}
	defer func() { _ = os.Remove(serverBin) }()

	stateRoot, cleanup, err := tempStateRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: %v\n", err)
		return 1
	}
	defer cleanup()
	stateHome := filepath.Join(stateRoot, "state")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.Command(serverBin)
	cmd.Dir = repoRoot
	cmd.Env = evalEnv(stateRoot, stateHome)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	client := mcpsdk.NewClient(
		&mcpsdk.Implementation{Name: "beacon-evals", Version: "v0.0.0"},
		nil,
	)
	session, err := client.Connect(ctx, &mcpsdk.CommandTransport{
		Command:           cmd,
		TerminateDuration: 2 * time.Second,
	}, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "eval: CommandTransport подключение: %v\n%s\n", err, stderr.String())
		return 1
	}
	defer func() { _ = session.Close() }()

	fmt.Println("beacon MCP: CommandTransport → evalserver (NewServerWithDeps + реальный SQLite store)")
	fmt.Printf("XDG_STATE_HOME: %s\n\n", stateHome)

	// eval 1: handshake.
	initResult := session.InitializeResult()
	name, version := "", ""
	if initResult != nil && initResult.ServerInfo != nil {
		name, version = initResult.ServerInfo.Name, initResult.ServerInfo.Version
	}
	record("01_handshake", "serverInfo.name=beacon, version непустой",
		fmt.Sprintf("name=%s, version=%s", name, version),
		name == serverName && version != "")

	// eval 2: catalog.
	wantTools := slices.Clone(contractTools)
	sort.Strings(wantTools)
	tools, listErr := session.ListTools(ctx, &mcpsdk.ListToolsParams{})
	gotTools := make([]string, 0, contractToolCount)
	if listErr == nil {
		for _, tool := range tools.Tools {
			gotTools = append(gotTools, tool.Name)
		}
	}
	sort.Strings(gotTools)
	actualTools := strings.Join(gotTools, ",")
	if listErr != nil {
		actualTools = "tools/list: " + listErr.Error()
	}
	record("02_catalog", fmt.Sprintf("ровно %d контрактных имён", contractToolCount),
		fmt.Sprintf("%d: %s", len(gotTools), actualTools),
		listErr == nil && slices.Equal(gotTools, wantTools))

	// eval 3: stats.
	statsResult, statsErr := callTool(ctx, session, "beacon_stats", map[string]any{})
	statsActual := "нет ответа"
	statsPass := false
	switch {
	case statsErr != nil:
		statsActual = "rpc: " + statsErr.Error()
	case statsResult.IsError:
		statsActual = "isError=true: " + resultText(statsResult)
	default:
		keys, keysErr := structuredKeys(statsResult)
		if keysErr != nil {
			statsActual = "структурное содержимое не JSON: " + keysErr.Error()
		} else {
			need := []string{"activeWatches", "budget", "ladder", "persist", "version"}
			missing := []string{}
			for _, key := range need {
				if _, ok := keys[key]; !ok {
					missing = append(missing, key)
				}
			}
			var s struct {
				ActiveWatches int `json:"activeWatches"`
				Budget        int `json:"budget"`
			}
			_ = decodeStructured(statsResult, &s)
			statsActual = fmt.Sprintf("isError=false, поля есть, activeWatches=%d, budget=%d", s.ActiveWatches, s.Budget)
			if len(missing) > 0 {
				statsActual = fmt.Sprintf("нет полей: %s", strings.Join(missing, ","))
			} else {
				statsPass = true
			}
		}
	}
	record("03_stats", "isError=false, JSON с activeWatches, budget, ladder, persist, version", statsActual, statsPass)

	// eval 4: watch. headSHA задан явно: CIProvider не нужен, сеть не нужна.
	watchArgs := map[string]any{
		"repo":    "refusedguy/beacon",
		"pr":      1,
		"role":    "executor",
		"headSHA": evalHeadSHA,
		"notify":  map[string]any{"adapter": "opencode", "target": evalSessionID},
	}
	watchResult, watchErr := callTool(ctx, session, "beacon_watch_pr", watchArgs)
	watchID, revision, status, verdict := "", uint64(0), "", ""
	watchActual := "нет ответа"
	watchPass := false
	switch {
	case watchErr != nil:
		watchActual = "rpc: " + watchErr.Error()
	case watchResult.IsError:
		watchActual = "isError=true: " + resultText(watchResult)
	default:
		var w struct {
			WatchID  string `json:"watchID"`
			Revision uint64 `json:"revision"`
			Status   string `json:"status"`
			Verdict  string `json:"verdict"`
		}
		if err := decodeStructured(watchResult, &w); err != nil {
			watchActual = "структурное содержимое не разобрано: " + err.Error()
		} else {
			watchID, revision, status, verdict = w.WatchID, w.Revision, w.Status, w.Verdict
			watchActual = fmt.Sprintf("isError=false, watchID=%s, revision=%d, status=%s, verdict=%s", watchID, revision, status, verdict)
			watchPass = watchID != "" && status != "" && verdict != ""
		}
	}
	record("04_watch", "watchID, revision, status, verdict без isError", watchActual, watchPass)

	// eval 5: list.
	listResult, listCallErr := callTool(ctx, session, "beacon_list_running", map[string]any{})
	listActual := "нет ответа"
	listPass := false
	switch {
	case listCallErr != nil:
		listActual = "rpc: " + listCallErr.Error()
	case watchID == "":
		listActual = "нет watchID из eval 04_watch"
	case listResult.IsError:
		listActual = "isError=true: " + resultText(listResult)
	default:
		var l struct {
			Watches []struct {
				ID string `json:"id"`
			} `json:"watches"`
		}
		if err := decodeStructured(listResult, &l); err != nil {
			listActual = "структурное содержимое не разобрано: " + err.Error()
		} else {
			found := false
			for _, w := range l.Watches {
				if w.ID == watchID {
					found = true
					break
				}
			}
			listActual = fmt.Sprintf("watches=%d, найден %s: %t", len(l.Watches), watchID, found)
			listPass = found
		}
	}
	record("05_list", fmt.Sprintf("watch %s в watches", orNone(watchID)), listActual, listPass)

	// eval 6: get. sessionID из _meta должен совпадать с notify.target из watch.
	getResult, getErr := callTool(ctx, session, "beacon_get_result", map[string]any{"watchID": watchID})
	obligationID := ""
	getActual := "нет ответа"
	getPass := false
	switch {
	case getErr != nil:
		getActual = "rpc: " + getErr.Error()
	case watchID == "":
		getActual = "нет watchID из eval 04_watch"
	case getResult.IsError:
		getActual = "isError=true: " + resultText(getResult)
	default:
		keys, keysErr := structuredKeys(getResult)
		if keysErr != nil {
			getActual = "структурное содержимое не JSON: " + keysErr.Error()
		} else {
			_, hasStatus := keys["status"]
			_, hasVerdict := keys["verdict"]
			var g struct {
				Status       string `json:"status"`
				Verdict      string `json:"verdict"`
				ObligationID string `json:"obligationID"`
			}
			_ = decodeStructured(getResult, &g)
			obligationID = g.ObligationID
			getActual = fmt.Sprintf("isError=false, status=%s, verdict=%s, obligationID=%s", g.Status, g.Verdict, orNone(obligationID))
			getPass = hasStatus && hasVerdict
		}
	}
	record("06_get", "структура status/verdict без isError", getActual, getPass)

	// eval 7: ack.
	ackResult, ackErr := callTool(ctx, session, "beacon_ack", map[string]any{
		"watchID":      watchID,
		"obligationID": obligationID,
	})
	ackActual := "нет ответа"
	ackPass := false
	switch {
	case ackErr != nil:
		ackActual = "rpc: " + ackErr.Error()
	case ackResult.IsError:
		ackActual = "isError=true: " + resultText(ackResult)
	default:
		var o struct {
			OK bool `json:"ok"`
		}
		if err := decodeStructured(ackResult, &o); err != nil {
			ackActual = "структурное содержимое не разобрано: " + err.Error()
		} else {
			ackActual = fmt.Sprintf("ok=%t", o.OK)
			ackPass = o.OK
		}
	}
	record("07_ack", fmt.Sprintf("ok=true для %s / %s", orNone(watchID), orNone(obligationID)), ackActual, ackPass)

	// eval 8: cancel.
	cancelResult, cancelErr := callTool(ctx, session, "beacon_cancel_watch", map[string]any{"watchID": watchID})
	cancelActual := "нет ответа"
	cancelPass := false
	switch {
	case cancelErr != nil:
		cancelActual = "rpc: " + cancelErr.Error()
	case cancelResult.IsError:
		cancelActual = "isError=true: " + resultText(cancelResult)
	default:
		var o struct {
			OK bool `json:"ok"`
		}
		if err := decodeStructured(cancelResult, &o); err != nil {
			cancelActual = "структурное содержимое не разобрано: " + err.Error()
		} else {
			cancelActual = fmt.Sprintf("ok=%t", o.OK)
			cancelPass = o.OK
		}
	}
	record("08_cancel", fmt.Sprintf("ok=true для %s", orNone(watchID)), cancelActual, cancelPass)

	printTable(results)
	passed, failed := 0, 0
	for _, r := range results {
		if r.pass {
			passed++
		} else {
			failed++
		}
	}
	fmt.Printf("\n%d passed, %d failed\n", passed, failed)
	if failed > 0 {
		return 1
	}
	return 0
}

// callTool добавляет непрозрачный sessionID в _meta: обработчики get_result и
// cancel_watch читают его оттуда, а watch_pr использует как notify.target.
func callTool(ctx context.Context, session *mcpsdk.ClientSession, name string, args map[string]any) (*mcpsdk.CallToolResult, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	params := &mcpsdk.CallToolParams{
		Name:      name,
		Arguments: args,
	}
	params.SetMeta(map[string]any{sessionIDMetaKey: evalSessionID})
	return session.CallTool(callCtx, params)
}

func structuredKeys(result *mcpsdk.CallToolResult) (map[string]json.RawMessage, error) {
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil, err
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, err
	}
	return keys, nil
}

func decodeStructured(result *mcpsdk.CallToolResult, out any) error {
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// resultText достаёт человеческий текст из content ответа tools/call.
func resultText(result *mcpsdk.CallToolResult) string {
	parts := make([]string, 0, len(result.Content))
	for _, c := range result.Content {
		raw, err := json.Marshal(c)
		if err != nil {
			continue
		}
		var item map[string]any
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if text, ok := item["text"].(string); ok && text != "" {
			parts = append(parts, text)
		}
	}
	if len(parts) == 0 {
		return "<пусто>"
	}
	return strings.Join(parts, "; ")
}

func printTable(results []evalResult) {
	headers := []string{"NAME", "EXPECTED", "ACTUAL", "PASS/FAIL"}
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, r := range results {
		row := []string{r.name, r.expected, clip(r.actual, 96), passLabel(r.pass)}
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	printRow(headers, widths)
	separator := make([]string, len(widths))
	for i, width := range widths {
		separator[i] = strings.Repeat("-", width)
	}
	printRow(separator, widths)
	for _, r := range results {
		printRow([]string{r.name, r.expected, clip(r.actual, 96), passLabel(r.pass)}, widths)
	}
}

func printRow(cells []string, widths []int) {
	parts := make([]string, len(cells))
	for i, cell := range cells {
		parts[i] = fmt.Sprintf("%-*s", widths[i], cell)
	}
	fmt.Println(strings.Join(parts, " | "))
}

func passLabel(pass bool) string {
	if pass {
		return "PASS"
	}
	return "FAIL"
}

// clip ограничивает длину ячейки, чтобы таблица оставалась читаемой.
func clip(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func orNone(s string) string {
	if s == "" {
		return "<нет>"
	}
	return s
}

// buildEvalServer собирает eval-сервер с тем же go, что запустил раннер.
func buildEvalServer(repoRoot string) (string, error) {
	goBin := os.Getenv("GO")
	if goBin == "" {
		goBin = "go"
	}
	out := filepath.Join(os.TempDir(), fmt.Sprintf("beacon-evalserver-%d", os.Getpid()))
	cmd := exec.Command(goBin, "build", "-tags=eval", "-o", out, "./evals/cmd/evalserver")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "GOPROXY=off")
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("собрать evalserver: %v\n%s", err, combined)
	}
	return out, nil
}

// findRepoRoot возвращает каталог с go.mod, то есть корень ворктри.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil && !info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod не найден выше текущего каталога")
		}
		dir = parent
	}
}

// tempStateRoot создаёт отдельный временный корень XDG. Даже если в окружении
// задан XDG_STATE_HOME, eval его игнорирует: пользовательское состояние не
// меняется во время прогона.
func tempStateRoot() (string, func(), error) {
	base := os.TempDir()
	if info, err := os.Stat("/tmp/opencode"); err == nil && info.IsDir() {
		base = "/tmp/opencode"
	}
	dir, err := os.MkdirTemp(base, "beacon-eval-state-")
	if err != nil {
		return "", nil, err
	}
	for _, name := range []string{"state", "data", "cache", "config"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o700); err != nil {
			_ = os.RemoveAll(dir)
			return "", nil, err
		}
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// evalEnv убирает GitHub-токены и подменяет XDG-каталоги на временные, чтобы
// eval не мог выйти в сеть и не тронул состояние пользователя.
func evalEnv(root, stateHome string) []string {
	blocked := map[string]bool{
		"GITHUB_TOKEN":      true,
		"GH_TOKEN":          true,
		"GITHUB_TOKEN_FILE": true,
	}
	env := make([]string, 0, len(os.Environ())+4)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if blocked[key] {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"XDG_STATE_HOME="+stateHome,
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
	)
}
