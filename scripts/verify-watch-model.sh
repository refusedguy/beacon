#!/usr/bin/env bash
# Model-check the Watch state machine with a pinned TLC build.
set -euo pipefail

TLC_VERSION="1.7.4"
TLC_SHA1="bee4a54f3ee3d4afc347c3240ec2d9e93b075104"
TLC_URL="https://github.com/tlaplus/tlaplus/releases/download/v${TLC_VERSION}/tla2tools.jar"

CACHE_DIR="${TLC_CACHE_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/beacon/tla}"
JAR="$CACHE_DIR/tla2tools-${TLC_VERSION}.jar"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
MODEL="$ROOT/docs/design/watch-state-machine.tla"

CONTROL=""
if [[ $# -gt 0 ]]; then
  case "$1" in
    --control=*) CONTROL="${1#--control=}" ;;
    *)
      echo "usage: $0 [--control=double-merge|silent-verdict|early-unblock]" >&2
      exit 2
      ;;
  esac
fi

command -v java >/dev/null 2>&1 || { echo "java is required" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 is required" >&2; exit 2; }

if [[ ! -f "$JAR" ]]; then
  mkdir -p "$CACHE_DIR"
  curl -fsSL --retry 2 -o "$JAR.part" "$TLC_URL"
  mv "$JAR.part" "$JAR"
fi
ACTUAL="$(sha1sum "$JAR" | awk '{print $1}')"
if [[ "$ACTUAL" != "$TLC_SHA1" ]]; then
  echo "tla2tools checksum mismatch: expected $TLC_SHA1, got $ACTUAL" >&2
  exit 2
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/beacon-watch-tla.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# TLC requires the module name to match the file name. The repo file uses a
# hyphen, so check a copy under a valid TLA+ identifier.
cp "$MODEL" "$WORK/WatchStateMachine.tla"
cat > "$WORK/WatchStateMachine.cfg" <<'CFG'
INIT Init
NEXT Next
INVARIANT TypeOK NoDoubleMerge NoSilentVerdictChange BlockLiftedOnlyAfterResolve
CFG

if [[ -n "$CONTROL" ]]; then
  python3 - "$WORK/WatchStateMachine.tla" "$CONTROL" <<'PY'
import sys

path, control = sys.argv[1], sys.argv[2]
patches = {
    "double-merge": ("  /\\ NotAlreadyMerged", "  /\\ TRUE"),
    "silent-verdict": ("NewRev == revision + 1", "NewRev == revision"),
    "early-unblock": ("  /\\ resolveCount >= blockEpoch", "  /\\ TRUE"),
}
if control not in patches:
    sys.exit("unknown control patch: " + control)
old, new = patches[control]
text = open(path).read()
if text.count(old) != 1:
    sys.exit("control patch target count for %s is %d, want 1" % (control, text.count(old)))
open(path, "w").write(text.replace(old, new))
PY
  echo "CONTROL MODE: $CONTROL (this run is expected to fail)"
fi

echo "tla2tools v${TLC_VERSION} sha1=${ACTUAL}"
set +e
(
  cd "$WORK" && java -cp "$JAR" tlc2.TLC -workers 1 \
    -config WatchStateMachine.cfg WatchStateMachine
) > "$WORK/tlc.log" 2>&1
STATUS=$?
set -e

echo "--- TLC summary ---"
grep -E 'states generated|distinct states found|Invariant|Error:' "$WORK/tlc.log" | head -40 || true
if [[ $STATUS -ne 0 ]]; then
  echo "--- counterexample ---"
  if grep -q '^Error:' "$WORK/tlc.log"; then
    sed -n '/^Error:/,$p' "$WORK/tlc.log" | head -80
  else
    tail -80 "$WORK/tlc.log"
  fi
fi
echo "TLC exit: $STATUS"
exit "$STATUS"
