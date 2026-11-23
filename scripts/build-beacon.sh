#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

BINARY="$ROOT/.opencode/bin/beacon"

stop_beacon_daemons() {
  local pid exe cwd killed=0
  for pid in $(pgrep -f 'beacon mcp' 2>/dev/null || true); do
    exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
    cwd="$(readlink "/proc/$pid/cwd" 2>/dev/null || true)"
    if [[ "$exe" == "$BINARY"* && "$cwd" == "$ROOT" ]]; then
      kill "$pid" 2>/dev/null || true
      killed=1
    fi
  done
  if [ "$killed" -eq 1 ]; then
    sleep 1
    for pid in $(pgrep -f 'beacon mcp' 2>/dev/null || true); do
      exe="$(readlink "/proc/$pid/exe" 2>/dev/null || true)"
      cwd="$(readlink "/proc/$pid/cwd" 2>/dev/null || true)"
      if [[ "$exe" == "$BINARY"* && "$cwd" == "$ROOT" ]]; then
        kill -9 "$pid" 2>/dev/null || true
      fi
    done
  fi
}

mkdir -p .opencode/bin
go build -o "$BINARY" ./cmd/beacon
git rev-parse HEAD > "$BINARY.build-sha"

stop_beacon_daemons
