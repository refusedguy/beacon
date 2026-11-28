package app

import (
	"strings"

	"github.com/refusedguy/beacon/pkg/core"
)

// spawnVerification is the language-agnostic rule in the stock worker prompt.
// The concrete format and test commands belong to config: beacon init writes
// them for the detected stack, and the operator overrides them there. A stock
// block that names Go commands tells a Node or Python worker to run another
// stack's tools, so it must stay free of them.
const spawnVerification = "Before opening a pull request, run the local format and targeted test checks for this stack. " + core.WeakMachineVerificationRule + "\n"

func spawnPrompt(t core.Task, id core.RunID, ws, instructions string) string {
	var b strings.Builder
	// Reserve the fixed text plus the variable fields in one allocation. The
	// stock prompt is a few kilobytes, so growing from zero reallocates several
	// times for every dispatch.
	b.Grow(len(spawnVerification) + len(core.OpenPRAndWatchInstruction) + len(instructions) +
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
	b.WriteString(spawnVerification)
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
