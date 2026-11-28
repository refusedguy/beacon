package app

import (
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// prePRCheck is appended to the stock worker prompt before the task card's
// override. It makes a known formatting or test failure stop the worker before
// it opens a PR, so CI does not spend a fix round on it. The Go check commands
// are concrete; the verification rule they serve comes from pkg/core so the
// spawn prompt cannot contradict the config default or the init instructions.
const prePRCheck = `Before opening a pull request, run the local format and test check.

Run: gofmt -l <changed Go files>
The command must print nothing. If it prints a file, format that file and run it again.

Run: go test <changed Go packages>
The command must pass. If a test fails, fix it and run the command again.

If gofmt -l still prints a file or a targeted test still fails, stop. Do not open a pull request. Report the exact command you ran and its full output, then stop.

` + core.WeakMachineVerificationRule + `
The gofmt -l and targeted go test checks are required on every machine.
`

func spawnPrompt(t core.Task, id core.RunID, ws, instructions string) string {
	var b strings.Builder
	// Reserve the fixed text plus the variable fields in one allocation. The
	// stock prompt is a few kilobytes, so growing from zero reallocates several
	// times for every dispatch.
	b.Grow(len(prePRCheck) + len(core.OpenPRAndWatchInstruction) + len(instructions) +
		len(id) + len(t.ID) + len(t.Title) + len(ws) +
		len(t.BaseBranch) + len(t.WorkerPrompt) + 256)
	prependWorkerInstructions(&b, instructions)
	b.WriteString("Run ID: ")
	b.WriteString(string(id))
	b.WriteString("\nTask: ")
	b.WriteString(string(t.ID))
	b.WriteString(" (")
	b.WriteString(t.Title)
	b.WriteString(")\nWorkspace: ")
	b.WriteString(ws)
	b.WriteString("\n")
	if base := strings.TrimSpace(t.BaseBranch); base != "" {
		b.WriteString("BaseBranch: ")
		b.WriteString(base)
		b.WriteString("\nOpen the pull request against ")
		b.WriteString(base)
		b.WriteString(", not the default branch.\n")
	}
	b.WriteString("Implement the task, " + core.OpenPRAndWatchInstruction + "\n")
	b.WriteString(prePRCheck)
	appendWorkerPrompt(&b, t)
	return b.String()
}

// BuildSpawnPrompt builds the prompt a dispatched task receives. The bench
// harness calls it to time prompt construction without running a Dispatcher.
func BuildSpawnPrompt(t core.Task, id core.RunID, ws, instructions string) string {
	return spawnPrompt(t, id, ws, instructions)
}

// prependWorkerInstructions puts the operator's worker instructions at the
// start of a spawn prompt. An empty value leaves the stock prompt unchanged.
func prependWorkerInstructions(b *strings.Builder, instructions string) {
	text := strings.TrimSpace(instructions)
	if text == "" {
		return
	}
	b.WriteString(text)
	b.WriteString("\n\n")
}

// appendWorkerPrompt adds a card's worker_prompt after the global instructions.
// A task without an override keeps the stock prompt.
func appendWorkerPrompt(b *strings.Builder, t core.Task) {
	if override := strings.TrimSpace(t.WorkerPrompt); override != "" {
		b.WriteString("\nWorker prompt from task card:\n")
		b.WriteString(override)
		b.WriteString("\n")
	}
}
