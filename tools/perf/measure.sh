#!/usr/bin/env bash
# T4.2. Замер SLO на 15/30/60 Watch собраным бинарником Beacon.
#
# Скрипт не запускает go test и не является локальным гейтом (ADR 0022).
# Он собирает бинарник с воспроизводимыми флагами ADR 0016, поднимает
# записанные GraphQL-фикстуры T2.16 на локальном HTTP-сервере и для каждого
# режима держит один процесс Beacon. Результат это девять строк таблицы:
# три режима по три прогона.
#
# Входные артефакты одного прогона лежат в каталоге состояния:
#   receipts/<obligationID>.<attempt>  квитанции T3.6, строка JSON на файл;
#   events.jsonl                       метки событий CI, поле at или mtime файла;
#   model-wake.jsonl                   отдельные метки пробуждения модели;
#   graphql-costs.jsonl                по одной строке {"cost": N} на ответ;
#   rate-limit.json                    снимки GET /rate_limit {"used": N};
#   points.json                        снимки счётчика {"used": N} до и после рестарта;
#   ready                              файл готовности процесса.
# Формат контракта описан в docs/perf/README.md.
#
# p95 считается только по квитанциям outcome=posted: разница между at
# квитанции и меткой события CI. Время ответа модели лежит в model-wake.jsonl
# и в p95 доставки не попадает. Поинты считаются по rateLimit.cost ответов
# и сверяются с used из GET /rate_limit. После рестарта счётчик обязан
# продолжить с уже использованного значения, а не с нуля (ADR 0013).

set -euo pipefail

usage() {
    cat <<'EOF'
Использование: bash tools/perf/measure.sh [флаги]

  --self-test          Проверить функции на синтетических артефактах в tmp.
  --bin PATH           Бинарник beacon. По умолчанию bin/beacon в корне репозитория.
  --state-dir PATH     Каталог состояния. По умолчанию временный каталог под /tmp/opencode.
  --modes LIST         Список режимов через запятую. По умолчанию 15,30,60.
  --reps N             Прогонов на режим. По умолчанию 3.
  --seed N             Ненулевое значение used в фикстуре /rate_limit. По умолчанию 137.
  --load-cmd CMD       Команда нагрузки. Получает PERF_WATCHES и PERF_STATE_DIR в окружении.
  --allow-blocked      Не возвращать код 2, если среда ещё не готова к замеру.
  --keep               Оставить каталог с сырыми данными (включено по умолчанию).
  --help               Показать эту справку.

Переменные окружения: PERF_BIN, PERF_STATE_DIR, PERF_MODES, PERF_REPS,
PERF_SEED_USED, PERF_LOAD_CMD, PERF_SETTLE_MS, PERF_OBSERVE_MS,
PERF_READY_TIMEOUT_MS, PERF_KEEP.
EOF
}

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
FIXTURE_DIR="$ROOT/internal/infra/github/testdata"
EVENTS_FIXTURE="$ROOT/tests/adapter/fixtures/events.jsonl"
FIXTURE_NAME="${PERF_FIXTURE:-pending-states/tier1.json}"
SETTLE_MS="${PERF_SETTLE_MS:-2000}"
OBSERVE_MS="${PERF_OBSERVE_MS:-5000}"
READY_TIMEOUT_MS="${PERF_READY_TIMEOUT_MS:-5000}"

BIN="${PERF_BIN:-$ROOT/bin/beacon}"
STATE_ROOT="${PERF_STATE_DIR:-}"
MODES_CSV="${PERF_MODES:-15,30,60}"
REPS="${PERF_REPS:-3}"
SEED_USED="${PERF_SEED_USED:-137}"
LOAD_CMD="${PERF_LOAD_CMD:-}"
ALLOW_BLOCKED=0
SELF_TEST=0
KEEP="${PERF_KEEP:-1}"
WORK=""
BG_PIDS=()
ROWS=()
BLOCKED_REASON=""

HWM_LIMIT_KB=102400
P95_LIMIT_MS=15000
POINT_LIMIT=500
POINT_TARGET=360
RESTART_LIMIT_MS=5000

while [ "$#" -gt 0 ]; do
    case "$1" in
        --self-test) SELF_TEST=1; shift ;;
        --bin) BIN="${2:?}"; shift 2 ;;
        --state-dir) STATE_ROOT="${2:?}"; shift 2 ;;
        --modes) MODES_CSV="${2:?}"; shift 2 ;;
        --reps) REPS="${2:?}"; shift 2 ;;
        --seed) SEED_USED="${2:?}"; shift 2 ;;
        --load-cmd) LOAD_CMD="${2:?}"; shift 2 ;;
        --allow-blocked) ALLOW_BLOCKED=1; shift ;;
        --keep) KEEP=1; shift ;;
        --help|-h) usage; exit 0 ;;
        *) printf 'неизвестный флаг: %s\n' "$1" >&2; usage >&2; exit 2 ;;
    esac
done

info() { printf '[perf] %s\n' "$*" >&2; }
warn() { printf '[perf] ПРЕДУПРЕЖДЕНИЕ: %s\n' "$*" >&2; }
die() { printf '[perf] ОШИБКА: %s\n' "$*" >&2; exit 2; }

now_ms() { date +%s%3N; }

make_work() {
    local base=/tmp/opencode
    [ -d "$base" ] || base="${TMPDIR:-/tmp}"
    WORK="$(mktemp -d "$base/perf-w42.XXXXXX")"
    info "рабочий каталог: $WORK"
}

cleanup() {
    local pid
    for pid in "${BG_PIDS[@]:-}"; do
        [ -n "$pid" ] || continue
        kill "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    done
    if [ "${KEEP:-0}" != 1 ] && [ -n "$WORK" ] && [ -d "$WORK" ]; then
        rm -rf -- "$WORK"
    elif [ -n "$WORK" ]; then
        info "сырые данные сохранены: $WORK"
    fi
}

# --- Измеряемые функции. Они чистые и проверяются режимом --self-test. ---

# compute_p95 STATE EVENTS PREFIX
# Читает только квитанции posted. model-wake.jsonl разбирается отдельно.
compute_p95() {
    python3 - "$1" "$2" "$3" <<'PY'
import glob
import json
import math
import os
import sys
from datetime import datetime, timezone

state, events_path, prefix = sys.argv[1:4]


def to_ms(value):
    if isinstance(value, bool) or value is None:
        return None
    if isinstance(value, (int, float)):
        number = float(value)
        if number >= 1e17:
            return int(number / 1e6)
        if number >= 1e12:
            return int(number / 1e3)
        return int(number * 1000)
    if isinstance(value, str):
        text = value.strip()
        if text.endswith("Z"):
            text = text[:-1] + "+00:00"
        try:
            stamp = datetime.fromisoformat(text)
        except ValueError:
            return None
        if stamp.tzinfo is None:
            stamp = stamp.replace(tzinfo=timezone.utc)
        return int(stamp.timestamp() * 1000)
    return None


def percentile(values):
    if not values:
        return -1
    ordered = sorted(values)
    rank = max(1, math.ceil(0.95 * len(ordered)))
    return ordered[rank - 1]


try:
    with open(events_path, "r", encoding="utf-8", errors="replace") as handle:
        event_lines = handle.read().splitlines()
except OSError:
    event_lines = []

try:
    event_mtime = int(os.path.getmtime(events_path) * 1000)
except OSError:
    event_mtime = None

event_at = {}
for line in event_lines:
    line = line.strip()
    if not line:
        continue
    try:
        row = json.loads(line)
    except (ValueError, TypeError):
        continue
    obligation = row.get("obligationID") or row.get("obligationId")
    if not obligation:
        continue
    stamp = to_ms(row.get("at") or row.get("eventAt") or row.get("timestamp"))
    event_at[str(obligation)] = stamp if stamp is not None else event_mtime

delivery = []
seen = set()
for path in sorted(glob.glob(os.path.join(state, "receipts", "*"))):
    if not os.path.isfile(path):
        continue
    with open(path, "r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                receipt = json.loads(line)
            except (ValueError, TypeError):
                continue
            if str(receipt.get("outcome", "")).strip().lower() != "posted":
                continue
            obligation = str(receipt.get("obligationID") or receipt.get("obligationId") or "")
            attempt = receipt.get("attempt", 0)
            key = (obligation, attempt)
            if key in seen:
                continue
            seen.add(key)
            delivered = to_ms(receipt.get("deliveredAt") or receipt.get("at"))
            started = event_at.get(obligation)
            if delivered is None or started is None:
                continue
            delta = delivered - started
            if delta >= 0:
                delivery.append(delta)

wake = []
wake_path = os.path.join(state, "model-wake.jsonl")
if os.path.isfile(wake_path):
    with open(wake_path, "r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                row = json.loads(line)
            except (ValueError, TypeError):
                continue
            woke = to_ms(row.get("wokeAt"))
            started = to_ms(row.get("eventAt"))
            if woke is not None and started is not None and woke >= started:
                wake.append(woke - started)

result = {
    "p95_ms": percentile(delivery),
    "samples": len(delivery),
    "wake_p95_ms": percentile(wake),
    "wake_samples": len(wake),
}
for key, value in result.items():
    with open(prefix + "." + key, "w", encoding="utf-8") as out:
        out.write(str(value) + "\n")
print("%(p95_ms)d %(samples)d %(wake_p95_ms)d %(wake_samples)d" % result)
PY
}

# compute_points STATE COST_LOG RATE_LOG PREFIX
# Возвращает cost_sum, used_delta, pts/h и флаги сверки и продолжения счётчика.
compute_points() {
    python3 - "$1" "$2" "$3" "$4" "$5" "$6" <<'PY'
import json
import math
import os
import sys

state, cost_log, rate_log, points_before, points_after, prefix = sys.argv[1:7]


def read_jsonl(path):
    rows = []
    if not os.path.isfile(path):
        return rows
    with open(path, "r", encoding="utf-8", errors="replace") as handle:
        for line in handle:
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except (ValueError, TypeError):
                continue
    return rows


def stamp_ms(row):
    value = row.get("at")
    if isinstance(value, (int, float)):
        number = float(value)
        if number >= 1e12:
            return number / 1e3
        return number
    if isinstance(value, str):
        from datetime import datetime, timezone
        text = value[:-1] + "+00:00" if value.endswith("Z") else value
        try:
            parsed = datetime.fromisoformat(text)
        except ValueError:
            return None
        if parsed.tzinfo is None:
            parsed = parsed.replace(tzinfo=timezone.utc)
        return parsed.timestamp() * 1000
    return None


costs = read_jsonl(cost_log)
rates = read_jsonl(rate_log)
cost_sum = sum(int(row.get("cost", 0)) for row in costs)
used_values = []
for row in rates:
    try:
        used_values.append(int(row.get("used", 0)))
    except (TypeError, ValueError):
        continue
used_delta = used_values[-1] - used_values[0] if len(used_values) >= 2 else 0

stamps = [stamp_ms(row) for row in costs]
stamps = [value for value in stamps if value is not None]
window_s = (max(stamps) - min(stamps)) / 1000.0 if len(stamps) >= 2 else 0.0
if window_s < 1.0:
    window_s = 1.0
points_per_hour = int(round(cost_sum * 3600.0 / window_s))
reconciled = "ok" if abs(used_delta - cost_sum) <= 2 else "fail"

before = read_jsonl(points_before)
after = read_jsonl(points_after)
before_used = before[-1].get("used") if before else None
after_used = after[-1].get("used") if after else None
if before_used is None or after_used is None:
    continuity = "blocked"
else:
    continuity = "ok" if after_used >= before_used and before_used > 0 else "fail"

result = {
    "cost_sum": cost_sum,
    "used_delta": used_delta,
    "points_per_hour": points_per_hour,
    "window_s": int(window_s),
    "reconciled": reconciled,
    "continuity": continuity,
    "before_used": before_used if before_used is not None else -1,
    "after_used": after_used if after_used is not None else -1,
}
for key, value in result.items():
    with open(prefix + "." + key, "w", encoding="utf-8") as out:
        out.write(str(value) + "\n")
print("%(cost_sum)d %(used_delta)d %(points_per_hour)d %(reconciled)s %(continuity)s" % result)
PY
}

read_value() { cat "$1" 2>/dev/null | tr -d '\n'; }

# --- Драйвер фикстур и процесса ---

write_fixture_server() {
    cat > "$WORK/fixture_server.py" <<'PY'
import json
import os
import sys
import time
from datetime import datetime, timedelta, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

port_file, fixture_path, state, seed_raw, snapshot = sys.argv[1:6]
seed = int(seed_raw)
with open(fixture_path, "rb") as handle:
    fixture = handle.read()
try:
    parsed = json.loads(fixture)
    cost = int(parsed["rateLimit"]["cost"])
    if cost < 1:
        raise ValueError("cost < 1")
except (KeyError, TypeError, ValueError) as exc:
    sys.stderr.write("fixture without rateLimit.cost: %s\n" % exc)
    sys.exit(2)

used = seed
reset = int((datetime.now(timezone.utc) + timedelta(hours=1)).timestamp())


def stamp():
    return datetime.now(timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def append(path, row):
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(row, sort_keys=True) + "\n")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        return

    def send_json(self, payload, status=200):
        body = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        global used
        if self.path.split("?", 1)[0] == "/ready":
            self.send_json({"ready": True})
            return
        if self.path.split("?", 1)[0] == "/rate_limit":
            resource = {"limit": 5000, "used": used, "remaining": max(0, 5000 - used), "reset": reset}
            self.send_json({"resources": {"core": resource, "graphql": resource}, "rate": resource})
            append(snapshot, {"at": stamp(), "used": used, "source": "GET /rate_limit"})
            return
        self.send_json({"error": "not found"}, 404)

    def do_POST(self):
        global used
        if self.path.split("?", 1)[0] != "/graphql":
            self.send_json({"error": "not found"}, 404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        if length:
            self.rfile.read(length)
        used += cost
        append(os.path.join(state, "graphql-costs.jsonl"),
               {"at": stamp(), "cost": cost, "source": "recorded fixture"})
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(fixture)))
        self.end_headers()
        self.wfile.write(fixture)


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
with open(port_file, "w", encoding="utf-8") as handle:
    handle.write(str(server.server_port) + "\n")
server.serve_forever()
PY
}

start_fixture_server() {
    local state="$1" port_file="$WORK/fixture.port"
    : > "$state/rate-limit.json"
    : > "$state/graphql-costs.jsonl"
    python3 "$WORK/fixture_server.py" "$port_file" "$FIXTURE_DIR/$FIXTURE_NAME" \
        "$state" "$SEED_USED" "$state/rate-limit.json" \
        >"$WORK/fixture.out" 2>"$WORK/fixture.err" &
    FIXTURE_PID=$!
    BG_PIDS+=("$FIXTURE_PID")
    local waited=0
    while [ ! -s "$port_file" ]; do
        if ! kill -0 "$FIXTURE_PID" 2>/dev/null; then
            cat "$WORK/fixture.err" >&2 || true
            die "сервер фикстур не стартовал"
        fi
        [ "$waited" -ge 5000 ] && die "таймаут старта сервера фикстур"
        sleep 0.05
        waited=$((waited + 50))
    done
    FIXTURE_PORT="$(tr -d '\n' < "$port_file")"
    FIXTURE_URL="http://127.0.0.1:$FIXTURE_PORT"
}

stop_pid() {
    local pid="${1:-}"
    [ -n "$pid" ] || return 0
    kill "$pid" 2>/dev/null || true
    local waited=0
    while kill -0 "$pid" 2>/dev/null && [ "$waited" -lt 2000 ]; do
        sleep 0.05
        waited=$((waited + 50))
    done
    if kill -0 "$pid" 2>/dev/null; then
        kill -9 "$pid" 2>/dev/null || true
        wait "$pid" 2>/dev/null || true
    fi
}

start_beacon() {
    local watches="$1" state="$2" log="$3"
    rm -f "$state/ready"
    local env=(
        "XDG_STATE_HOME=$state"
        "BEACON_PERF_WATCHES=$watches"
        "BEACON_GRAPHQL_URL=$FIXTURE_URL"
        "BEACON_PERF_SEED_USED=$SEED_USED"
    )
    if [ -n "$LOAD_CMD" ]; then
        env "PERF_WATCHES=$watches" "PERF_STATE_DIR=$state" \
            "PERF_GRAPHQL_URL=$FIXTURE_URL" bash -c "$LOAD_CMD" \
            >>"$log" 2>&1 &
    else
        env "${env[@]}" "$BIN" mcp >>"$log" 2>&1 &
    fi
    BEACON_PID=$!
    BG_PIDS+=("$BEACON_PID")
}

wait_ready() {
    local state="$1" timeout="$2" waited=0
    while [ ! -f "$state/ready" ]; do
        if ! kill -0 "$BEACON_PID" 2>/dev/null; then
            return 1
        fi
        [ "$waited" -ge "$timeout" ] && return 1
        sleep 0.05
        waited=$((waited + 50))
    done
    printf '%s' "$waited"
}

prepare_state() {
    local state="$1"
    mkdir -p "$state/receipts"
    cp "$EVENTS_FIXTURE" "$state/events.jsonl"
    : > "$state/graphql-costs.jsonl"
    : > "$state/rate-limit.json"
    : > "$state/points.json"
}

probe_runtime() {
    [ -n "$LOAD_CMD" ] && return 0
    [ -x "$BIN" ] || { BLOCKED_REASON="нет исполняемого бинарника $BIN"; return 1; }
    if ! "$BIN" list --format json 2>/dev/null | grep -q '"name":[[:space:]]*"mcp"'; then
        BLOCKED_REASON="в бинаре нет подкоманды mcp; нужен entrypoint демона из T3.9 и следующих карточек"
        return 1
    fi
    return 0
}

count_gh_watch() {
    local n
    n="$(pgrep -fc 'gh pr checks --[w]atch' 2>/dev/null || true)"
    printf '%s' "${n:-0}"
}

sample_hwm() {
    local pid="$1" state="$2" best=0 value waited=0
    local end=$(( $(now_ms) + OBSERVE_MS ))
    while [ "$(now_ms)" -lt "$end" ]; do
        value="$(awk '/^VmHWM:/ {print $2}' "/proc/$pid/status" 2>/dev/null || true)"
        if [[ "$value" =~ ^[0-9]+$ ]] && [ "$value" -gt "$best" ]; then
            best="$value"
        fi
        sleep 0.2
        waited=$((waited + 200))
        [ "$waited" -ge "$((OBSERVE_MS + 1000))" ] && break
    done
    printf '%s' "$best"
}

self_test() {
    make_work
    local state="$WORK/self-test"
    prepare_state "$state"
    python3 - "$state" <<'PY'
import json
import os
import sys
from datetime import datetime, timedelta, timezone

state = sys.argv[1]
base = datetime(2026, 11, 19, 12, 0, 0, tzinfo=timezone.utc)
with open(os.path.join(state, "events.jsonl"), "w", encoding="utf-8") as out:
    for i in range(5):
        row = {"obligationID": "ob-%d" % i, "watchID": "watch-1",
               "revision": 1, "at": (base + timedelta(seconds=i)).isoformat().replace("+00:00", "Z")}
        out.write(json.dumps(row) + "\n")
for i in range(5):
    path = os.path.join(state, "receipts", "ob-%d.1" % i)
    with open(path, "w", encoding="utf-8") as out:
        row = {"obligationID": "ob-%d" % i, "attempt": 1, "outcome": "posted",
               "at": (base + timedelta(seconds=i, milliseconds=(i + 1) * 1000)).isoformat().replace("+00:00", "Z")}
        out.write(json.dumps(row) + "\n")
with open(os.path.join(state, "receipts", "ob-9.1"), "w", encoding="utf-8") as out:
    out.write(json.dumps({"obligationID": "ob-9", "attempt": 1, "outcome": "transport-failed",
                          "at": base.isoformat().replace("+00:00", "Z")}) + "\n")
with open(os.path.join(state, "model-wake.jsonl"), "w", encoding="utf-8") as out:
    for i in range(5):
        row = {"obligationID": "ob-%d" % i,
               "eventAt": (base + timedelta(seconds=i)).isoformat().replace("+00:00", "Z"),
               "wokeAt": (base + timedelta(seconds=i, milliseconds=90000)).isoformat().replace("+00:00", "Z")}
        out.write(json.dumps(row) + "\n")
PY
    local prefix="$WORK/self-test-metrics"
    local out p95 samples wake_p95 wake_samples
    out="$(compute_p95 "$state" "$state/events.jsonl" "$prefix")"
    read -r p95 samples wake_p95 wake_samples <<< "$out"
    [ "$p95" = 5000 ] || die "self-test p95: ожидалось 5000, получено $p95"
    [ "$samples" = 5 ] || die "self-test samples: ожидалось 5, получено $samples"
    [ "$wake_p95" = 90000 ] || die "self-test wake p95: ожидалось 90000, получено $wake_p95"
    [ "$wake_p95" != "$p95" ] || die "self-test: латентность модели смешалась с p95 доставки"

    printf '%s\n' '{"at":"2026-11-19T12:00:00Z","cost":1}' \
        '{"at":"2026-11-19T12:00:01Z","cost":1}' > "$state/graphql-costs.jsonl"
    printf '%s\n' '{"at":"2026-11-19T12:00:00Z","used":137}' \
        '{"at":"2026-11-19T12:00:01Z","used":139}' > "$state/rate-limit.json"
    printf '%s\n' '{"at":"2026-11-19T12:00:00Z","used":139}' > "$WORK/before.jsonl"
    printf '%s\n' '{"at":"2026-11-19T12:00:02Z","used":139}' > "$WORK/after.jsonl"
    out="$(compute_points "$state" "$state/graphql-costs.jsonl" "$state/rate-limit.json" \
        "$WORK/before.jsonl" "$WORK/after.jsonl" "$WORK/self-test-points")"
    [[ "$out" == *" ok ok" ]] || die "self-test points: ожидались ok ok, получено $out"
    printf '%s\n' '{"at":"2026-11-19T12:00:02Z","used":0}' > "$WORK/after.jsonl"
    out="$(compute_points "$state" "$state/graphql-costs.jsonl" "$state/rate-limit.json" \
        "$WORK/before.jsonl" "$WORK/after.jsonl" "$WORK/self-test-points")"
    [[ "$out" == *" ok fail" ]] || die "self-test restart: счётчик с нуля не обнаружен"
    info "self-test ok: p95 по posted, латентность модели отделена, рестарт с нуля обнаруживается"
}

# --- Основной прогон ---

for tool in python3 awk grep date mktemp; do
    command -v "$tool" >/dev/null 2>&1 || die "нет команды $tool"
done

if [ "$SELF_TEST" = 1 ]; then
    self_test
    exit 0
fi

[ -f "$FIXTURE_DIR/$FIXTURE_NAME" ] || die "нет фикстуры $FIXTURE_DIR/$FIXTURE_NAME"
[ -f "$EVENTS_FIXTURE" ] || die "нет фикстуры событий $EVENTS_FIXTURE"
[[ "$REPS" =~ ^[0-9]+$ ]] && [ "$REPS" -gt 0 ] || die "неверное число прогонов: $REPS"

make_work
trap cleanup EXIT

input_modes=()
IFS=',' read -r -a input_modes <<< "$MODES_CSV"
for mode in "${input_modes[@]}"; do
    [[ "$mode" =~ ^[0-9]+$ ]] && [ "$mode" -gt 0 ] || die "неверный режим: $mode"
done

GO_VERSION="$(go version 2>/dev/null | awk '{print $3}' || true)"
[ -n "$GO_VERSION" ] || GO_VERSION="не определена"

info "дата: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
info "Go: $GO_VERSION; GOOS/GOARCH: $(go env GOOS 2>/dev/null)/$(go env GOARCH 2>/dev/null)"
info "сборка: CGO_ENABLED=0 -trimpath -buildvcs=false -ldflags='-buildid= -s -w'"
info "фикстура: $FIXTURE_NAME; seed /rate_limit: $SEED_USED"
info "окно: прогреве $SETTLE_MS мс, наблюдение $OBSERVE_MS мс, прогонов $REPS"

if [ ! -x "$BIN" ]; then
    info "собираю бинарник: $BIN"
    mkdir -p "$(dirname "$BIN")"
    (cd "$ROOT" && CGO_ENABLED=0 go build -trimpath -buildvcs=false \
        -ldflags="-buildid= -s -w" -o "$BIN" ./cmd/beacon) \
        || die "сборка бинарника не удалась"
fi

BUILD_SHA="$(cd "$ROOT" && git rev-parse --short HEAD 2>/dev/null || printf 'нет')"
info "коммит: $BUILD_SHA"

RUNTIME_READY=1
probe_runtime || RUNTIME_READY=0

for mode in "${input_modes[@]}"; do
    for ((run = 1; run <= REPS; run++)); do
        run_dir="$WORK/mode-$mode-run-$run"
        state="$run_dir/state"
        mkdir -p "$run_dir"
        prepare_state "$state"

        if [ "$RUNTIME_READY" -ne 1 ]; then
            ROWS+=("$mode|$run|—|—|—|—|—|BLOCKED|$BLOCKED_REASON")
            warn "режим $mode прогон $run: замер заблокирован: $BLOCKED_REASON"
            continue
        fi

        start_fixture_server "$state"
        local_log="$run_dir/beacon.log"
        start_beacon "$mode" "$state" "$local_log"
        ready_ms="$(wait_ready "$state" "$READY_TIMEOUT_MS" || true)"
        if [ -z "$ready_ms" ]; then
            ROWS+=("$mode|$run|—|—|—|—|—|BLOCKED|процесс не создал ready за ${READY_TIMEOUT_MS} мс")
            stop_pid "${BEACON_PID:-}"
            stop_pid "${FIXTURE_PID:-}"
            continue
        fi

        sleep "$((SETTLE_MS / 1000))"
        hwm="$(sample_hwm "$BEACON_PID" "$state")"

        metrics_prefix="$run_dir/metrics"
        p95_out="$(compute_p95 "$state" "$state/events.jsonl" "$metrics_prefix")"
        read -r p95_ms p95_samples wake_p95_ms wake_samples <<< "$p95_out"
        printf '%s' "$wake_p95_ms" > "$run_dir/model-wake-p95-ms"
        printf '%s' "$wake_samples" > "$run_dir/model-wake-samples"

        cp "$state/points.json" "$run_dir/points-before.jsonl" 2>/dev/null || : > "$run_dir/points-before.jsonl"
        restart_start="$(now_ms)"
        stop_pid "$BEACON_PID"
        start_beacon "$mode" "$state" "$local_log"
        ready_ms="$(wait_ready "$state" "$READY_TIMEOUT_MS" || true)"
        restart_ms=0
        if [ -n "$ready_ms" ]; then
            restart_ms=$(( $(now_ms) - restart_start ))
        fi
        cp "$state/points.json" "$run_dir/points-after.jsonl" 2>/dev/null || : > "$run_dir/points-after.jsonl"

        points_out="$(compute_points "$state" "$state/graphql-costs.jsonl" "$state/rate-limit.json" \
            "$run_dir/points-before.jsonl" "$run_dir/points-after.jsonl" "$run_dir/points")"
        read -r cost_sum used_delta points_per_hour reconciled continuity <<< "$points_out"
        gh_watch="$(count_gh_watch)"

        printf '%s|%s|%s|%s|%s|%s|%s|%s\n' \
            "$mode" "$run" "$hwm" "$p95_ms" "$points_per_hour" "$restart_ms" \
            "$gh_watch" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$run_dir/row.csv"
        {
            printf 'go_version=%s\ncommit=%s\nfixture=%s\nseed_used=%s\n' \
                "$GO_VERSION" "$BUILD_SHA" "$FIXTURE_NAME" "$SEED_USED"
            printf 'cost_sum=%s\nused_delta=%s\nreconciled=%s\ncontinuity=%s\n' \
                "$cost_sum" "$used_delta" "$reconciled" "$continuity"
            printf 'delivery_samples=%s\nmodel_wake_p95_ms=%s\nmodel_wake_samples=%s\n' \
                "$p95_samples" "$wake_p95_ms" "$wake_samples"
        } > "$run_dir/meta.txt"

        reason=""
        if [ "$hwm" -ge "$HWM_LIMIT_KB" ]; then reason="${reason}VKhWM>${HWM_LIMIT_KB}KB "; fi
        if [ "$p95_ms" -lt 0 ] || [ "$p95_ms" -ge "$P95_LIMIT_MS" ]; then reason="${reason}p95>${P95_LIMIT_MS}ms "; fi
        if [ "$points_per_hour" -gt "$POINT_LIMIT" ]; then reason="${reason}points>${POINT_LIMIT}/h "; fi
        if [ "$restart_ms" -le 0 ] || [ "$restart_ms" -gt "$RESTART_LIMIT_MS" ]; then reason="${reason}restart>${RESTART_LIMIT_MS}ms "; fi
        if [ "$gh_watch" != 0 ]; then reason="${reason}gh-watch=${gh_watch} "; fi
        if [ "$reconciled" != ok ]; then reason="${reason}rate-limit-mismatch "; fi
        if [ "$continuity" != ok ]; then reason="${reason}counter-${continuity} "; fi

        status=PASS
        [ -n "$reason" ] && status=FAIL
        ROWS+=("$mode|$run|$hwm|$p95_ms|$points_per_hour|$restart_ms|$gh_watch|$status|$reason")

        stop_pid "${BEACON_PID:-}"
        stop_pid "${FIXTURE_PID:-}"
        info "режим $mode прогон $run: $status"
    done
done

printf '\n'
printf '| Режим | Прогон | VmHWM, КБ | p95 доставки, мс | Поинтов/час | Рестарт, мс | gh --watch | Статус | Причина |\n'
printf '|---:|---:|---:|---:|---:|---:|---:|---|---|\n'
for row in "${ROWS[@]}"; do
    IFS='|' read -r mode run hwm p95 points restart gh status reason <<< "$row"
    printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' \
        "$mode" "$run" "$hwm" "$p95" "$points" "$restart" "$gh" "$status" "$reason"
done

printf '\n'
printf 'Поинты/час это экстраполяция из окна наблюдения, а не часовой soak.\n'
printf 'Цель 360 поинтов/час, жёсткий предел %s. p95 только по квитанциям posted.\n' "$POINT_LIMIT"
printf 'Латентность пробуждения модели лежит в отдельном файле %s/model-wake-* и в таблицу не входит.\n' "$WORK"
printf 'Сырые данные: %s\n' "$WORK"

has_fail=0
has_blocked=0
for row in "${ROWS[@]}"; do
    [[ "$row" == *"|FAIL|"* ]] && has_fail=1
    [[ "$row" == *"|BLOCKED|"* ]] && has_blocked=1
done
if [ "$has_fail" -eq 1 ] || { [ "$has_blocked" -eq 1 ] && [ "$ALLOW_BLOCKED" -ne 1 ]; }; then
    exit 2
fi
exit 0
