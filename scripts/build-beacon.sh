#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

mkdir -p .opencode/bin
go build -o .opencode/bin/beacon ./cmd/beacon
git rev-parse HEAD > .opencode/bin/beacon.build-sha
