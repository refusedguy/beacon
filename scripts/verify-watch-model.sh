#!/usr/bin/env bash
# PSK-03. Reproducible TLC verification of docs/design/watch-state-machine.tla.
#
# Usage:
#   bash scripts/verify-watch-model.sh              full self-test (rc 0)
#   bash scripts/verify-watch-model.sh --patch NAME run one control patch and
#                                                   exit with TLC's code (rc != 0)
#   NAME is MERGE_ONCE, VERDICT_REVISION or BLOCK_RESOLVED_ONLY.
#
# The full self-test:
#   1. checks the pinned TLC version and jar checksum,
#   2. model-checks the committed spec and prints state count and depth,
#   3. removes each guard in turn, checks TLC FAILS with the expected invariant
#      and prints the shortest counterexample path,
#   4. restores the guard and re-checks that the spec is green again.
#
# Environment:
#   TLC_JAR   path to a local tla2tools.jar (version is then not re-downloaded)

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SPEC="$ROOT/docs/design/watch-state-machine.tla"
MODULE="watch_state_machine"

TLC_VERSION="1.8.0"
TLC_URL="https://github.com/tlaplus/tlaplus/releases/download/v${TLC_VERSION}/tla2tools.jar"
TLC_SHA256="fdffef247f563ef6edbfd44958d2a82db79e7564e07cb673262d896353fa6713"

CACHE_DIR="${XDG_CACHE_HOME:-${HOME}/.cache}/beacon/tlc"
JAR="${TLC_JAR:-$CACHE_DIR/tla2tools-${TLC_VERSION}.jar}"

die() { echo "FAIL: $*" >&2; exit 1; }
info() { echo "== $*"; }

command -v java >/dev/null 2>&1 || die "java is required to run TLC"

if [[ ! -f "$JAR" ]]; then
    mkdir -p "$(dirname "$JAR")"
    echo "Downloading pinned TLC ${TLC_VERSION} -> ${JAR}"
    curl -fsSL --max-time 120 "$TLC_URL" -o "$JAR" \
        || die "cannot download TLC; set TLC_JAR to a local tla2tools-${TLC_VERSION}.jar"
fi

actual="$(sha256sum "$JAR" | awk '{print $1}')"
[[ "$actual" == "$TLC_SHA256" ]] \
    || die "TLC jar checksum mismatch: expected ${TLC_SHA256}, got ${actual}"
info "TLC ${TLC_VERSION} verified (sha256 ${TLC_SHA256:0:16}...)"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/beacon-watch-tlc.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

CATALOG="$WORK/StateCatalog.cfg"
cat > "$CATALOG" <<'CFG'
SPECIFICATION Spec
INVARIANT NoDoubleMerge VerdictNeedsRevision BlockClearedOnlyByResolved
CFG

clean() { sed -r 's/\x1B\[[0-9;]*[mK]//g'; }

# run_tlc SPEC_FILE LOG
# TLC requires the file name to equal the module name, hence the copy.
run_tlc() {
    local src="$1" log="$2" dest="$WORK/$MODULE.tla"
    cp "$src" "$dest"
    set +e
    java -Xmx512m -XX:+UseParallelGC -cp "$JAR" tlc2.TLC \
        -config "$CATALOG" -workers 1 "$dest" > "$log" 2>&1
    local rc=$?
    set -e
    return "$rc"
}

stats() {
    clean < "$1" | grep -E 'states generated, .* distinct states found|depth of the complete state graph search is' || true
}

# path LOG
# The shortest counterexample is the full behavior TLC prints after "the
# behavior up to this point is:". Trim TLC's trailing Error/Finished lines.
path() {
    clean < "$1" | awk '
        /The behavior up to this point is:/ { on=1; next }
        on && /^(Error|Finished|Starting|State [0-9]+ <|[0-9]+ states generated,)/ && seen { exit }
        on { print; seen=1 }
    '
}

patch_spec() {
    local name="$1" out="$2"
    case "$name" in
        MERGE_ONCE)            needle="MERGE_ONCE ==" ;;
        VERDICT_REVISION)      needle="VERDICT_REVISION ==" ;;
        BLOCK_RESOLVED_ONLY)   needle="BLOCK_RESOLVED_ONLY ==" ;;
        *) die "unknown patch: $name" ;;
    esac
    awk -v needle="$needle" '
        index($0, needle) == 1 && / TRUE/ { sub("TRUE", "FALSE"); }
        { print }
    ' "$SPEC" > "$out"
    grep -q "^${needle} FALSE" "$out" || die "control patch $name did not apply"
}

EXPECTED=(
    "MERGE_ONCE:NoDoubleMerge"
    "VERDICT_REVISION:VerdictNeedsRevision"
    "BLOCK_RESOLVED_ONLY:BlockClearedOnlyByResolved"
)

# --patch NAME: apply one control patch, leave TLC's non-zero code visible.
if [[ $# -gt 0 ]]; then
    [[ $# -eq 1 && "$1" == --patch || $# -eq 2 ]] || die "usage: $0 [--patch NAME]"
    if [[ $# -eq 2 ]]; then
        [[ "$1" == --patch ]] || die "usage: $0 [--patch NAME]"
        name="$2"
    else
        name="${1#--patch=}"
    fi
    patched="$WORK/patched.tla"
    patch_spec "$name" "$patched"
    log="$WORK/patched.log"
    info "control patch: $name (expecting invariant violation)"
    if run_tlc "$patched" "$log"; then
        clean < "$log"
        die "control patch $name did not break its invariant"
    fi
    clean < "$log" | grep -E 'Invariant .* is violated' || true
    path "$log"
    stats "$log"
    exit 1
fi

info "base model: $SPEC"
BASE_LOG="$WORK/base.log"
run_tlc "$SPEC" "$BASE_LOG" || { clean < "$BASE_LOG"; die "base model failed"; }
clean < "$BASE_LOG" | grep -E 'Model checking completed. No error has been found' || true
stats "$BASE_LOG"

for entry in "${EXPECTED[@]}"; do
    name="${entry%%:*}"
    invariant="${entry##*:}"
    patched="$WORK/${name}.tla"
    log="$WORK/${name}.log"
    patch_spec "$name" "$patched"

    info "control patch $name: removing guard must break $invariant"
    if run_tlc "$patched" "$log"; then
        clean < "$log"
        die "control patch $name did not break $invariant"
    fi
    clean < "$log" | grep -F "Invariant $invariant is violated." >/dev/null \
        || { clean < "$log" | grep -E 'Invariant .* is violated' || true
             die "control patch $name broke a different invariant"
           }
    clean < "$log" | grep -F "Invariant $invariant is violated."
    path "$log"
    stats "$log"
done

info "restored model: re-check after all control patches"
run_tlc "$SPEC" "$BASE_LOG" || { clean < "$BASE_LOG"; die "restored model failed"; }
clean < "$BASE_LOG" | grep -E 'Model checking completed. No error has been found' || true
stats "$BASE_LOG"

info "PASS: 3 invariants checked by TLC, 3 counterexamples reproduced, model restored"
