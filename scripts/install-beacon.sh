#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${BEACON_INSTALL_DIR:-$HOME/.local/bin}"

mkdir -p "$INSTALL_DIR"
cd "$ROOT"
go build -o "$INSTALL_DIR/beacon" ./cmd/beacon
echo "installed $INSTALL_DIR/beacon"

if [ -f "$ROOT/go.mod" ] && [ -d "$ROOT/cmd/beacon" ]; then
  mkdir -p "$ROOT/.opencode/bin"
  cp "$INSTALL_DIR/beacon" "$ROOT/.opencode/bin/beacon"
  bash "$ROOT/scripts/build-beacon.sh" --hash > "$ROOT/.opencode/bin/beacon.build-sha"
  echo "refreshed $ROOT/.opencode/bin/beacon"
fi

case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) echo "warning: $HOME/.local/bin is not in PATH; add it before running beacon" >&2 ;;
esac
