package main

import (
	"errors"
	"fmt"
	"strings"
)

// rebuildCommand is the exact remedy printed for a stale binary. The doctor
// preflight and the serve and mcp startup guards share this text so every
// report names the same steps.
const rebuildCommand = "bash scripts/build-beacon.sh, then restart the MCP daemon and the TUI"

// unknownBuildHash marks a binary that did not come from scripts/build-beacon.sh.
const unknownBuildHash = "unknown"

// sourceBuildHash is the digest of the build inputs at link time.
// scripts/build-beacon.sh replaces it with -ldflags on every real build.
// A go run or an install outside that script leaves it as unknownBuildHash.
var sourceBuildHash = unknownBuildHash

// embeddedBuildHash returns the stamped hash without surrounding whitespace.
func embeddedBuildHash() string {
	return strings.TrimSpace(sourceBuildHash)
}

// staleBuildMessage names both hashes and the command that fixes the mismatch.
func staleBuildMessage(embedded, current string) string {
	return fmt.Sprintf("binary built from %s but sources hash to %s; fix: %s",
		doctorShortHash(embedded), doctorShortHash(current), rebuildCommand)
}

// buildHashMismatch compares the embedded hash with the sources under root.
// A binary with no stamped hash passes, so go run keeps working. A mismatch
// returns an error a startup guard can print as is.
func buildHashMismatch(root string) error {
	embedded := embeddedBuildHash()
	if embedded == "" || embedded == unknownBuildHash {
		return nil
	}
	current, err := doctorBuildHash(root)
	if err != nil {
		return fmt.Errorf("cannot compute the source hash: %w; fix: %s", err, rebuildCommand)
	}
	if embedded == strings.TrimSpace(current) {
		return nil
	}
	return errors.New(staleBuildMessage(embedded, current))
}
