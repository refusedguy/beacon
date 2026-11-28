#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
INSTALL_DIR="${BEACON_INSTALL_DIR:-$HOME/.local/bin}"

mkdir -p "$INSTALL_DIR"
cd "$ROOT"

tmpdirs=()
cleanup() {
  if [ ${#tmpdirs[@]} -gt 0 ]; then
    rm -rf "${tmpdirs[@]}"
  fi
}
trap cleanup EXIT

# Build and install through a same-directory temporary file. Copying or
# building over an executing binary fails with ETXTBSY; renaming over it does
# not. The temporary directory also keeps a failed replace from publishing a
# hash that describes a binary which never reached the destination.
install_tmp="$(mktemp -d "$INSTALL_DIR/.beacon-install.XXXXXX")"
tmpdirs+=("$install_tmp")
go build -o "$install_tmp/beacon" ./cmd/beacon
mv -f "$install_tmp/beacon" "$INSTALL_DIR/beacon"
echo "installed $INSTALL_DIR/beacon"

if [ -f "$ROOT/go.mod" ] && [ -d "$ROOT/cmd/beacon" ]; then
  mkdir -p "$ROOT/.opencode/bin"
  local_tmp="$(mktemp -d "$ROOT/.opencode/bin/.beacon-install.XXXXXX")"
  tmpdirs+=("$local_tmp")
  cp "$INSTALL_DIR/beacon" "$local_tmp/beacon"
  mv -f "$local_tmp/beacon" "$ROOT/.opencode/bin/beacon"

  hash="$(bash "$ROOT/scripts/build-beacon.sh" --hash)"
  printf '%s\n' "$hash" > "$local_tmp/beacon.build-sha"
  mv -f "$local_tmp/beacon.build-sha" "$ROOT/.opencode/bin/beacon.build-sha"
  echo "refreshed $ROOT/.opencode/bin/beacon"
fi

case ":$PATH:" in
  *":$HOME/.local/bin:"*) ;;
  *) echo "warning: $HOME/.local/bin is not in PATH; add it before running beacon" >&2 ;;
esac
