#!/usr/bin/env bash
# Проверяет реальные артефакты Beacon: сборку, тесты, MCP, состояние, логи, метрики и цикл демона.
# Каждый запуск использует новый изолированный каталог состояния. GitHub не трогается.
# Подробности: docs/runbooks/verification.md
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="${BEACON_REPO:-$(cd "$SCRIPT_DIR/.." && pwd -P)}"

GO="${GO:-${GO_BIN:-}}"
if [[ -z "$GO" || ! -x "$GO" ]]; then
  GO="$(command -v go || true)"
fi
PYTHON="${PYTHON:-python3}"
if ! command -v "$PYTHON" >/dev/null 2>&1; then
  printf 'FAIL  нужен %s в PATH\n' "$PYTHON" >&2
  exit 1
fi
if [[ -z "$GO" || ! -x "$GO" ]]; then
  printf 'FAIL  нужен go в PATH или переменная GO\n' >&2
  exit 1
fi

TMPBASE="${TMPDIR:-/tmp}"
if [[ -d /tmp/opencode ]]; then
  TMPBASE=/tmp/opencode
fi
WORK="$(mktemp -d "$TMPBASE/verify-beacon.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

export GOPROXY="${GOPROXY:-off}"
export XDG_STATE_HOME="$WORK/state"
export BEACON_LOG_FORMAT=text
export BEACON_LOG_LEVEL=debug
unset BEACON_STATE_DIR BEACON_PROJECT_ROOT BEACON_INIT_EVENT BEACON_INIT_BYPASS || true

run_beacon() { (cd "$REPO" && exec "$@"); }

project_name="$(basename "$REPO")"
project_hash="$(printf '%s' "$REPO" | sha256sum | cut -c1-16)"
STATE_DIR="$XDG_STATE_HOME/beacon/projects/$project_name-$project_hash"

FAILURES=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
section() { printf '\n== %s ==\n' "$*"; }

log_files() {
  find "$STATE_DIR/logs" -maxdepth 1 -type f -name 'beacon-*.log' -printf '%f\n' 2>/dev/null | sort
}
new_logs_since() {
  comm -13 <(printf '%s\n' "$1") <(log_files)
}

section "Репозиторий и окружение"
printf 'repo=%s\ngo=%s\nstate=%s\nwork=%s\n' "$REPO" "$GO" "$STATE_DIR" "$WORK"

section "Сборка и тесты"
if (cd "$REPO" && "$GO" build ./... >"$WORK/build.log" 2>&1); then
  pass 'go build ./...'
else
  fail 'go build ./...' "$(tail -n 20 "$WORK/build.log")"
fi
if (cd "$REPO" && "$GO" test ./... >"$WORK/test.log" 2>&1); then
  pass 'go test ./...'
else
  fail 'go test ./...' "$(tail -n 30 "$WORK/test.log")"
fi
if ! (cd "$REPO" && "$GO" build -o "$WORK/beacon" ./cmd/beacon >>"$WORK/build.log" 2>&1); then
  fail 'сборка бинарника beacon' "$(tail -n 20 "$WORK/build.log")"
  printf '\nИТОГ: %d проверок провалено\n' "$FAILURES" >&2
  exit 1
fi
pass 'бинарник beacon собран'

section "MCP: beacon mcp и beacon_stats"
mkdir -p "$WORK/mcpcheck"
cp "$REPO/evals/go.sum" "$WORK/mcpcheck/go.sum"
cat > "$WORK/mcpcheck/go.mod" <<EOF
module verifybeacon

go 1.26.0

require github.com/modelcontextprotocol/go-sdk v1.8.0
EOF
cat > "$WORK/mcpcheck/main.go" <<'GOEOF'
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "MCPFAIL: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) != 3 {
		die("нужны путь к бинарнику beacon и каталог проекта")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.Command(os.Args[1], "mcp")
	cmd.Dir = os.Args[2]
	transport := &mcpsdk.CommandTransport{Command: cmd, TerminateDuration: 2 * time.Second}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "verify-beacon", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, &mcpsdk.ClientSessionOptions{ProtocolVersion: "2025-06-18"})
	if err != nil {
		die("connect: %v", err)
	}
	defer session.Close()

	init := session.InitializeResult()
	if init == nil || init.ServerInfo == nil || init.ServerInfo.Name != "beacon" {
		die("initialize не вернул serverInfo.name=beacon")
	}
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		die("tools/list: %v", err)
	}
	if len(tools.Tools) != 7 {
		die("tools/list вернул %d инструментов, ожидалось 7", len(tools.Tools))
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "beacon_stats" {
			found = true
		}
	}
	if !found {
		die("beacon_stats нет в tools/list")
	}

	res, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "beacon_stats", Arguments: map[string]any{}})
	if err != nil {
		die("tools/call beacon_stats: %v", err)
	}
	if res.IsError {
		die("beacon_stats вернул isError=true")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		die("marshal structuredContent: %v", err)
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		die("decode structuredContent: %v", err)
	}
	for _, key := range []string{"activeWatches", "budget", "ladder", "persist", "version"} {
		if _, ok := obj[key]; !ok {
			die("beacon_stats без ключа %s", key)
		}
	}
	fmt.Printf("MCP_OK server=%s tools=%d stats_keys=%d\n", init.ServerInfo.Name, len(tools.Tools), len(obj))
}
GOEOF
if (cd "$WORK/mcpcheck" && GOFLAGS=-mod=mod "$GO" run . "$WORK/beacon" "$REPO" >"$WORK/mcp.out" 2>"$WORK/mcp.err"); then
  pass "MCP $(cat "$WORK/mcp.out")"
else
  fail 'MCP beacon_stats' "$(cat "$WORK/mcp.err"; tail -n 10 "$WORK/mcp.out")"
fi

section "Проект-scoped состояние"
if [[ -f "$STATE_DIR/beacon.db" ]]; then
  pass "state БД существует: $STATE_DIR/beacon.db"
else
  fail 'state БД не найдена' "$STATE_DIR/beacon.db"
fi
if "$PYTHON" - "$STATE_DIR/beacon.db" >"$WORK/db.out" 2>"$WORK/db.err" <<'PYEOF'
import os
import sqlite3
import sys

path = sys.argv[1]
if not os.path.isfile(path):
    print("нет файла", path)
    sys.exit(1)
con = sqlite3.connect("file:" + path + "?mode=ro", uri=True)
try:
    verdict = con.execute("PRAGMA quick_check").fetchone()[0]
    tables = [row[0] for row in con.execute("SELECT name FROM sqlite_master WHERE type='table'")]
finally:
    con.close()
print(f"DB_OK quick_check={verdict} tables={len(tables)}")
sys.exit(0 if verdict == "ok" and tables else 1)
PYEOF
then
  pass "state БД доступна: $(cat "$WORK/db.out")"
else
  fail 'state БД недоступна' "$(cat "$WORK/db.err"; cat "$WORK/db.out")"
fi

section "Лог-файл на каждый запуск"
metrics_ok=1
for run in 1 2; do
  before="$(log_files)"
  if ! run_beacon "$WORK/beacon" metrics --format json >"$WORK/metrics.json" 2>"$WORK/metrics.err"; then
    fail "beacon metrics запуск $run" "$(cat "$WORK/metrics.err")"
    metrics_ok=0
    continue
  fi
  new_file="$(new_logs_since "$before" | head -n 1)"
  if [[ -z "$new_file" ]]; then
    fail "новый лог на запуск $run не создан"
    continue
  fi
  latest="$(readlink "$STATE_DIR/logs/beacon.latest.log" 2>/dev/null || true)"
  if [[ "$latest" != "$new_file" ]]; then
    fail "beacon.latest.log не указывает на $new_file" "latest=$latest"
  else
    pass "запуск $run создал лог $new_file, latest указывает на него"
  fi
done

section "Метрики"
if [[ "$metrics_ok" -eq 1 ]]; then
  if "$PYTHON" - "$WORK/metrics.json" >"$WORK/metrics.check" 2>&1 <<'PYEOF'
import json
import sys

with open(sys.argv[1], encoding="utf-8") as fh:
    data = json.load(fh)
want = {
    "poll_cycles",
    "verdict_published",
    "merge_attempt",
    "merge_success",
    "wake_delivered",
    "wake_failed",
    "rebase_requested",
}
missing = sorted(want - set(data))
if missing:
    print("нет ключей:", ", ".join(missing))
    sys.exit(1)
print(f"METRICS_OK keys={len(data)}")
PYEOF
  then
    pass "beacon metrics --format json: $(cat "$WORK/metrics.check")"
  else
    fail 'метрики без ожидаемых ключей' "$(cat "$WORK/metrics.check")"
  fi
fi

section "Демон: живой процесс и циклы по логу"
mkdir -p "$WORK/bin"
cat > "$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Заглушка только для локальной проверки: PR нет, GitHub не меняется.
if [[ "${1:-} ${2:-}" == "api graphql" ]]; then
  printf '%s\n' '{"data":{}}'
else
  printf '%s\n' '[]'
fi
EOF
chmod +x "$WORK/bin/gh"

daemon_before="$(log_files)"
PATH="$WORK/bin:$PATH" run_beacon "$WORK/beacon" daemon --interval 300ms --wake-target ses_verify_beacon \
  >"$WORK/daemon.out" 2>&1 &
DAEMON_PID=$!
daemon_log=""
for _ in $(seq 1 50); do
  daemon_log="$(new_logs_since "$daemon_before" | head -n 1)"
  [[ -n "$daemon_log" ]] && break
  sleep 0.1
done
if [[ -z "$daemon_log" ]]; then
  fail 'демон не создал свой лог'
else
  cycles=0
  for _ in $(seq 1 100); do
    cycles="$(grep -c 'event=daemon_pass' "$STATE_DIR/logs/$daemon_log" 2>/dev/null || true)"
    if [[ -z "$cycles" ]]; then cycles=0; fi
    if [[ "$cycles" -ge 2 ]]; then break; fi
    kill -0 "$DAEMON_PID" 2>/dev/null || break
    sleep 0.1
  done
  if kill -0 "$DAEMON_PID" 2>/dev/null; then
    pass "демон жив (pid=$DAEMON_PID)"
  else
    fail 'демон завершился до проверки' "$(tail -n 20 "$WORK/daemon.out")"
  fi
  if [[ "$cycles" -ge 2 ]]; then
    pass "демон сделал $cycles циклов, записано в $daemon_log"
  else
    fail 'демон не записал два цикла в лог' "cycles=$cycles log=$daemon_log"
  fi
fi
kill -TERM "$DAEMON_PID" 2>/dev/null || true
wait "$DAEMON_PID" 2>/dev/null || true

printf '\n'
if [[ "$FAILURES" -gt 0 ]]; then
  printf 'ИТОГ: %d проверок провалено\n' "$FAILURES" >&2
  exit 1
fi
printf 'ИТОГ: все проверки пройдены\n'
