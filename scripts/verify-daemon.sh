#!/usr/bin/env bash
# Проверяет путь мержа на реальных командах beacon и реальном store.
# Внешний gh подменён локальной заглушкой: GitHub не меняется, PR не мержится и не закрывается.
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
WORK="$(mktemp -d "$TMPBASE/verify-daemon.XXXXXX")"
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
GH_CALLS="$WORK/gh-calls.log"
export GH_CALLS
: > "$GH_CALLS"

FAILURES=0
pass() { printf 'PASS  %s\n' "$*"; }
fail() { printf 'FAIL  %s\n' "$*" >&2; FAILURES=$((FAILURES + 1)); }
section() { printf '\n== %s ==\n' "$*"; }
count_merge() {
  local n
  n="$(grep -c "^pr merge $1 --auto --squash --delete-branch$" "$GH_CALLS" 2>/dev/null || true)"
  printf '%s' "${n:-0}"
}

section "Репозиторий и окружение"
printf 'repo=%s\ngo=%s\nstate=%s\nwork=%s\n' "$REPO" "$GO" "$STATE_DIR" "$WORK"

if ! (cd "$REPO" && "$GO" build -o "$WORK/beacon" ./cmd/beacon >"$WORK/build.log" 2>&1); then
  fail 'сборка бинарника beacon' "$(tail -n 20 "$WORK/build.log")"
  printf '\nИТОГ: %d проверок провалено\n' "$FAILURES" >&2
  exit 1
fi
pass 'бинарник beacon собран'

section "Заглушка gh и подготовка состояния"
mkdir -p "$WORK/bin"
cat > "$WORK/bin/gh" <<'EOF'
#!/usr/bin/env bash
# Локальная заглушка. PR нет, команды мержа возвращают успех и пишутся в GH_CALLS.
printf '%s\n' "$*" >> "$GH_CALLS"
case "${1:-} ${2:-}" in
  "pr merge")
    for arg in "$@"; do
      if [[ "$arg" == "--auto" ]]; then
        exit 0
      fi
    done
    printf '%s\n' 'add the --auto flag: подождите GitHub requirements' >&2
    exit 1
    ;;
  "pr view")
    if [[ "${3:-}" == "902" ]]; then
      merge_state=DIRTY
    else
      merge_state=CLEAN
    fi
    printf '{"headRefOid":"head-%s","baseRefName":"main","state":"OPEN","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"%s","reviewDecision":"APPROVED"}\n' \
      "${3:-0}" "$merge_state"
    exit 0
    ;;
  "pr checks")
    printf '%s\n' '[]'
    exit 0
    ;;
  "pr list")
    printf '%s\n' '[]'
    exit 0
    ;;
  "api graphql")
    printf '%s\n' '{"data":{}}'
    exit 0
    ;;
  *)
    printf '%s\n' '[]'
    exit 0
    ;;
esac
EOF
chmod +x "$WORK/bin/gh"
resolved_gh="$(PATH="$WORK/bin:$PATH" command -v gh || true)"
if [[ "$resolved_gh" != "$WORK/bin/gh" ]]; then
  fail 'gh не подменён заглушкой, запуск демона отменён' "resolved=$resolved_gh"
  printf '\nИТОГ: %d проверок провалено\n' "$FAILURES" >&2
  exit 1
fi
pass 'gh подменён локальной заглушкой, реальный GitHub не трогается'

mkdir -p "$STATE_DIR" "$WORK/seed"
cp "$REPO/go.sum" "$WORK/seed/go.sum"
cat > "$WORK/seed/go.mod" <<EOF
module github.com/refusedguy/beacon/verifydaemon

go 1.26.0

require github.com/refusedguy/beacon v0.0.0

replace github.com/refusedguy/beacon => $REPO
EOF
cat > "$WORK/seed/main.go" <<'GOEOF'
package main

import (
	"fmt"
	"os"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/store"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "нужен путь к state БД")
		os.Exit(1)
	}
	db, err := store.Open(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "store.Open: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	clean := domain.Snapshot{
		State:            "OPEN",
		Rollup:           "SUCCESS",
		Mergeable:        "MERGEABLE",
		MergeStateStatus: "CLEAN",
		ReviewDecision:   "APPROVED",
		SuitesValid:      true,
		Ship:             domain.ShipFacts{Build: true, Audit: true, TDD: true},
	}
	ready, _ := domain.Evaluate(clean, domain.Policy{})
	if ready != domain.VerdictReady {
		fmt.Fprintf(os.Stderr, "MERGEABLE+CLEAN дал вердикт %s, ожидался ready\n", ready)
		os.Exit(1)
	}
	conflict := clean
	conflict.MergeStateStatus = "DIRTY"
	rebase, _ := domain.Evaluate(conflict, domain.Policy{})
	if rebase != domain.VerdictRebase {
		fmt.Fprintf(os.Stderr, "DIRTY дал вердикт %s, ожидался rebase\n", rebase)
		os.Exit(1)
	}

	watches := []domain.Watch{
		{
			ID:          "watch:verify-ready",
			Repo:        "refusedguy/beacon",
			PR:          901,
			HeadSHA:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Status:      domain.StatusNotifying,
			Verdict:     ready,
			Revision:    1,
			MergePolicy: domain.DefaultMergePolicy(),
		},
		{
			ID:          "watch:verify-conflict",
			Repo:        "refusedguy/beacon",
			PR:          902,
			HeadSHA:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Status:      domain.StatusNotifying,
			Verdict:     rebase,
			Revision:    1,
			MergePolicy: domain.DefaultMergePolicy(),
		},
	}
	for _, w := range watches {
		if err := db.SaveWatch(w); err != nil {
			fmt.Fprintf(os.Stderr, "SaveWatch %s: %v\n", w.ID, err)
			os.Exit(1)
		}
	}
	fmt.Printf("SEED_OK ready=%s conflict=%s watches=%d\n", ready, rebase, len(watches))
}
GOEOF
if ! (cd "$WORK/seed" && GOFLAGS=-mod=mod "$GO" run . "$STATE_DIR/beacon.db" >"$WORK/seed.out" 2>"$WORK/seed.err"); then
  fail 'подготовка Watch в реальном store' "$(cat "$WORK/seed.err"; cat "$WORK/seed.out")"
  printf '\nИТОГ: %d проверок провалено\n' "$FAILURES" >&2
  exit 1
fi
if grep -q 'SEED_OK ready=ready conflict=rebase watches=2' "$WORK/seed.out"; then
  pass "вердикты посчитаны реальным движком: $(cat "$WORK/seed.out")"
else
  fail 'вердикты не совпали с ожиданием' "$(cat "$WORK/seed.out")"
fi

section "Первый проход демона"
PATH="$WORK/bin:$PATH" run_beacon "$WORK/beacon" daemon --once --wake-target ses_verify_daemon \
  >"$WORK/pass1.out" 2>&1
rc1=$?
first_ready="$(count_merge 901)"
first_conflict="$(count_merge 902)"
if [[ "$rc1" -eq 0 ]]; then
  pass 'daemon --once завершился с кодом 0'
else
  fail 'daemon --once завершился с ошибкой' "rc=$rc1 $(tail -n 20 "$WORK/pass1.out")"
fi
if [[ "$first_ready" -eq 1 ]]; then
  pass 'готовый PR 901 получил ровно одну команду gh pr merge с --auto'
else
  fail 'готовый PR 901 не получил одну команду gh pr merge с --auto' "calls=$first_ready $(cat "$GH_CALLS")"
fi
if [[ "$first_conflict" -eq 0 ]]; then
  pass 'конфликтующий PR 902 не получил команду gh pr merge'
else
  fail 'конфликтующий PR 902 получил команду gh pr merge' "calls=$first_conflict"
fi

section "Второй проход демона"
: > "$GH_CALLS"
PATH="$WORK/bin:$PATH" run_beacon "$WORK/beacon" daemon --once --wake-target ses_verify_daemon \
  >"$WORK/pass2.out" 2>&1
rc2=$?
second_ready="$(count_merge 901)"
second_conflict="$(count_merge 902)"
if [[ "$rc2" -eq 0 ]]; then
  pass 'второй daemon --once завершился с кодом 0'
else
  fail 'второй daemon --once завершился с ошибкой' "rc=$rc2 $(tail -n 20 "$WORK/pass2.out")"
fi
if [[ "$second_ready" -eq 0 && "$second_conflict" -eq 0 ]]; then
  pass 'повторный проход не повторил мерж и не отправил 902 под --auto'
else
  fail 'повторный проход снова вызвал мерж' "901=$second_ready 902=$second_conflict"
fi

section "Журнал и ledgers"
if grep -R -q -i 'add the --auto flag' "$STATE_DIR/logs" "$WORK/pass1.out" "$WORK/pass2.out" 2>/dev/null; then
  fail 'в логах есть повторяющаяся ошибка про --auto'
else
  pass 'в логах нет ошибок "add the --auto flag"'
fi
if "$PYTHON" - "$STATE_DIR/beacon.db" >"$WORK/ledger.out" 2>"$WORK/ledger.err" <<'PYEOF'
import sqlite3
import sys

con = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
try:
    tables = {row[0] for row in con.execute("SELECT name FROM sqlite_master WHERE type='table'")}
    if "orchestrator_merges" not in tables:
        print("нет таблицы orchestrator_merges")
        sys.exit(1)
    rows = con.execute("SELECT watch_id, revision FROM orchestrator_merges").fetchall()
finally:
    con.close()
ok = ("watch:verify-ready", 1) in rows and not any(r[0] == "watch:verify-conflict" for r in rows)
print("LEDGER_OK rows=%s" % (sorted(rows),))
sys.exit(0 if ok else 1)
PYEOF
then
  pass "журнал мержа в store: $(cat "$WORK/ledger.out")"
else
  fail 'журнал мержа в store не совпал с ожиданием' "$(cat "$WORK/ledger.err"; cat "$WORK/ledger.out")"
fi

printf '\n'
if [[ "$FAILURES" -gt 0 ]]; then
  printf 'ИТОГ: %d проверок провалено\n' "$FAILURES" >&2
  exit 1
fi
printf 'ИТОГ: все проверки пройдены\n'
