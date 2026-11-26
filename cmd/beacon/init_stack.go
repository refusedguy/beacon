package main

import (
	"os"
	"path/filepath"

	"github.com/refusedguy/beacon/pkg/core"
)

// stackInfo identifies the project stack and its targeted test instruction.
type stackInfo struct {
	kind     string
	targeted string
}

// workerInstructions returns the test instruction for the given weak-PC setting.
// The verification rule itself is shared with config and the spawn prompt; only
// the language-specific targeted command is added here.
func (s stackInfo) workerInstructions(weakPC bool) string {
	if !weakPC || s.targeted == "" {
		return core.WeakMachineVerificationRule
	}
	return core.WeakMachineVerificationRule + " " + s.targeted
}

// detectStack identifies the project stack from marker files. Each targeted
// hint names one local check command; the weak-machine rule comes from core.
func detectStack(root string) stackInfo {
	if existsIn(root, "go.mod") {
		return stackInfo{"go", "For Go, run go test for the packages you changed."}
	}
	if existsIn(root, "package.json") {
		return stackInfo{"node", "For Node, run the targeted tests for the files you changed; do not run a full npm run build."}
	}
	if existsIn(root, "pyproject.toml") || existsIn(root, "requirements.txt") {
		return stackInfo{"python", "For Python, run pytest for the files you changed."}
	}
	if existsIn(root, "Cargo.toml") {
		return stackInfo{"rust", "For Rust, run cargo test for the module you changed."}
	}
	if hasMarker(root, "*.sln") || hasMarker(root, "*.csproj") {
		return stackInfo{"dotnet", "For .NET, run dotnet test for the project you changed."}
	}
	return stackInfo{"generic", ""}
}

// existsIn reports whether name exists under root.
func existsIn(root, name string) bool {
	_, err := os.Stat(filepath.Join(root, name))
	return err == nil
}

// hasMarker reports whether any file under root matches pattern.
func hasMarker(root, pattern string) bool {
	matches, err := filepath.Glob(filepath.Join(root, pattern))
	return err == nil && len(matches) > 0
}
