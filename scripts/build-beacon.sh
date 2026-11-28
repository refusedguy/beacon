#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

# Print each file path followed by its hash. The argument list is optional, so
# a sorted source list and a fixed set of module files share the same loop.
hash_files() {
  local file
  for file in "$@"; do
    printf '%s\n' "$file"
    sha256sum "$file"
  done
}

# Hash the real build inputs: Go sources under cmd, internal, pkg, harnesses and
# ui/tui, plus the module files. HEAD is deliberately excluded so a commit that
# touches none of these cannot make the binary look stale.
compute_build_hash() {
  {
    local dirs=()
    for dir in cmd internal pkg harnesses ui/tui; do
      if [ -d "$dir" ]; then dirs+=("$dir"); fi
    done
    local sources=()
    local file
    if [ ${#dirs[@]} -gt 0 ]; then
      while IFS= read -r file; do
        sources+=("$file")
      done < <(find "${dirs[@]}" -type f -name '*.go' -print | LC_ALL=C sort)
    fi
    hash_files "${sources[@]}"

    local modules=()
    for file in go.mod go.sum go.work; do
      if [ -f "$file" ]; then modules+=("$file"); fi
    done
    hash_files "${modules[@]}"
  } | sha256sum | cut -d' ' -f1
}

# Self-contained shell test. Stub `go`, build twice with unchanged sources,
# then build once after a source change. The stub records every invocation.
self_test() {
  local tmp calls ldflags stub script first recorded computed second
  tmp="$(mktemp -d)"
  trap "rm -rf '$tmp'" EXIT

  mkdir -p "$tmp/scripts" "$tmp/cmd/beacon" "$tmp/internal" "$tmp/pkg" \
    "$tmp/harnesses" "$tmp/ui/tui" "$tmp/stubbin"
  cp "${BASH_SOURCE[0]}" "$tmp/scripts/build-beacon.sh"
  printf 'package main\n' > "$tmp/cmd/beacon/main.go"
  printf 'module example.com/fake\n' > "$tmp/go.mod"
  : > "$tmp/go.sum"
  printf 'go 1.22\n' > "$tmp/go.work"

  calls="$tmp/go-calls"
  : > "$calls"
  ldflags="$tmp/go-ldflags"
  : > "$ldflags"
  stub="$tmp/stubbin/go"
  cat > "$stub" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'called\n' >> "$BEACON_TEST_CALLS"
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    out="$2"
    shift 2
    continue
  fi
  if [ "$1" = "-ldflags" ]; then
    printf '%s\n' "$2" >> "$BEACON_TEST_LDFLAGS"
    shift 2
    continue
  fi
  shift
done
[ -n "$out" ] || { echo "stub go: no -o argument" >&2; exit 1; }
printf '#!/bin/sh\n' > "$out"
chmod +x "$out"
STUB
  chmod +x "$stub"

  export BEACON_TEST_CALLS="$calls"
  export BEACON_TEST_LDFLAGS="$ldflags"
  export PATH="$tmp/stubbin:$PATH"
  script="$tmp/scripts/build-beacon.sh"

  bash "$script" >/dev/null
  bash "$script" >/dev/null
  first="$(wc -l < "$calls" | tr -d '[:space:]')"
  if [ "$first" -ne 1 ]; then
    echo "self-test: expected 1 go invocation after two identical builds, got $first" >&2
    exit 1
  fi

  recorded="$(cat "$tmp/.opencode/bin/beacon.build-sha")"
  computed="$(bash "$script" --hash)"
  if [ "$recorded" != "$computed" ]; then
    echo "self-test: build-sha does not describe the computed hash" >&2
    exit 1
  fi
  if [ "$(wc -l < "$ldflags" | tr -d '[:space:]')" -ne 1 ]; then
    echo "self-test: expected one embedded hash after two identical builds" >&2
    exit 1
  fi
  if ! grep -Fq -- "main.sourceBuildHash=$computed" "$ldflags"; then
    echo "self-test: build did not embed the computed hash" >&2
    exit 1
  fi

  printf '// changed\n' >> "$tmp/cmd/beacon/main.go"
  bash "$script" >/dev/null
  second="$(wc -l < "$calls" | tr -d '[:space:]')"
  if [ "$second" -ne 2 ]; then
    echo "self-test: expected 2 go invocations after a source change, got $second" >&2
    exit 1
  fi

  recorded="$(cat "$tmp/.opencode/bin/beacon.build-sha")"
  computed="$(bash "$script" --hash)"
  if [ "$recorded" != "$computed" ]; then
    echo "self-test: build-sha is stale after a source change" >&2
    exit 1
  fi
  if [ "$(wc -l < "$ldflags" | tr -d '[:space:]')" -ne 2 ]; then
    echo "self-test: expected a second embedded hash after a source change" >&2
    exit 1
  fi
  if ! tail -n 1 "$ldflags" | grep -Fq -- "main.sourceBuildHash=$computed"; then
    echo "self-test: build did not embed the refreshed hash after a source change" >&2
    exit 1
  fi

  echo "self-test: ok"
}

case "${1:-}" in
  --hash)
    compute_build_hash
    exit 0
    ;;
  --self-test)
    self_test
    exit 0
    ;;
esac

mkdir -p .opencode/bin

# Nothing to do when the binary exists and its recorded hash matches the
# current sources. Print the hash so the fast path stays observable.
hash="$(compute_build_hash)"
if [ -f .opencode/bin/beacon ] &&
   [ -f .opencode/bin/beacon.build-sha ] &&
   [ "$(cat .opencode/bin/beacon.build-sha)" = "$hash" ]; then
  printf '%s\n' "$hash"
  exit 0
fi

# Build next to the destination and rename into place. Writing straight to
# .opencode/bin/beacon fails with ETXTBSY while that binary is running.
tmpdir="$(mktemp -d .opencode/bin/.build.XXXXXX)"
trap 'rm -rf "$tmpdir"' EXIT

go build -ldflags "-X main.sourceBuildHash=$hash" -o "$tmpdir/beacon" ./cmd/beacon
mv -f "$tmpdir/beacon" .opencode/bin/beacon

# Publish the hash only after the rename succeeds. A failed build or rename
# leaves the old hash, which still describes the binary on disk.
printf '%s\n' "$hash" > "$tmpdir/beacon.build-sha"
mv -f "$tmpdir/beacon.build-sha" .opencode/bin/beacon.build-sha

printf '%s\n' "$hash"
