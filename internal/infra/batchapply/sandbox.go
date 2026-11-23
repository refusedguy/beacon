package batchapply

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// CCx-24: one violation metric line is emitted only when a write was
// refused by a sandbox. An ordinary file error adds no line.
const sandboxDenialsFormat = "SandboxDenials=%d\n"

// writeGate owns the seams that publish bytes and the state the write path
// needs. It holds no report lists and no output streams.
type writeGate struct {
	write func(path string, data []byte) (string, error)
	// commit is called once per chunk when Options.Commit is set.
	commit func(root string, cards, adrs, evals int) error
	// sandboxDenials counts writes classified as sandbox refusals. Run emits
	// it as a single violation metric line when the count is positive.
	sandboxDenials int
	now            func() time.Time
	yield          func(time.Duration)
}

// fillDefaults installs the production seams. Tests replace any of them on
// the runner they build before calling writeChunked.
func (g *writeGate) fillDefaults() {
	if g.now == nil {
		g.now = time.Now
	}
	if g.yield == nil {
		g.yield = func(d time.Duration) {
			if d > 0 {
				time.Sleep(d)
			}
		}
	}
	if g.commit == nil {
		g.commit = commitArtifacts
	}
}

func (r *runner) effectiveChunkSize() int {
	if r.opts.ChunkSize > 0 {
		return r.opts.ChunkSize
	}
	return chunkSize
}

func (r *runner) effectiveChunkPause() time.Duration {
	if r.opts.ChunkPause > 0 {
		return r.opts.ChunkPause
	}
	return chunkPause
}

// writeChunked writes the artifact set in bounded chunks. Each chunk is
// committed before the runner yields, so a concurrent reader sees a complete
// chunk and never half a write. The report records the chunk count and the
// longest wall time between the first write of a chunk and its commit.
func (r *runner) writeChunked(root string, entries []artifactEntry, cards, adrs, evals int) error {
	r.fillDefaults()
	chunks := chunkEntries(entries, r.effectiveChunkSize())
	pause := r.effectiveChunkPause()
	var maxHeld time.Duration
	for i, c := range chunks {
		started := r.now()
		for _, e := range c {
			if derivedArtifact(e.path) {
				// Keep the stdout listing unchanged while leaving the derived
				// document off disk.
				if !r.opts.Quiet {
					fmt.Fprintln(r.out, " ", e.path)
				}
				continue
			}
			// CBX-07: recheck in the same window that publishes the file.
			// A local edit that landed after the prefilter is caught here,
			// before any byte reaches disk.
			path := filepath.Join(root, e.path)
			local, statErr := os.Stat(path)
			exists := statErr == nil
			var localText string
			if exists && !e.updatedAt.IsZero() {
				if raw, err := os.ReadFile(path); err == nil {
					localText = string(raw)
				}
			}
			if exists && !r.opts.Force && !e.updatedAt.IsZero() {
				if staleAgainst(local.ModTime(), e.updatedAt) {
					r.recordStaleSkipped(e.id)
					continue
				}
				// CBX-08: an equal timestamp is never a write. Different text
				// is a tie: local bytes stay on disk and only auxiliary data
				// (facts and the ADR index, built from the entry set) merges.
				// Identical text is a no-op, so a repeat import reports nothing.
				if local.ModTime().Equal(e.updatedAt) {
					if localText != e.content {
						r.recordTieKeptLocal(e.id)
					}
					continue
				}
			}
			if !e.updatedAt.IsZero() {
				if err := r.writeFresh(root, e.path, e.content, e.updatedAt); err != nil {
					return err
				}
				if !r.opts.DryRun {
					if exists {
						r.recordUpdated(e.id, changedFields(localText, e.content))
						r.updatedIssues++
					} else {
						r.recordCreated(e.id)
					}
				}
				continue
			}
			if err := r.writeFile(root, e.path, e.content); err != nil {
				return err
			}
			if !r.opts.DryRun && !exists {
				r.recordCreated(e.id)
			}
		}
		held := r.now().Sub(started)
		if held > maxHeld {
			maxHeld = held
		}
		if r.opts.Commit && !r.opts.DryRun {
			if err := r.commit(root, cards, adrs, evals); err != nil {
				return err
			}
		}
		fmt.Fprintf(r.progress, chunkProgressFormat, i+1, len(chunks), len(c), held)
		if i+1 < len(chunks) {
			r.yield(pause)
		}
	}
	if len(chunks) > 0 {
		fmt.Fprintf(r.progress, chunkSummaryFormat, len(chunks), maxHeld)
	}
	return nil
}

func (r *runner) writeFile(root, rel, content string) error {
	return r.writeFileMode(root, rel, content, false)
}

// sandboxDenialError carries the writer output that named a sandbox refusal.
// Output stays on the error so a caller can report the denial without writing
// the file again. Err keeps the underlying write error for errors.Is and
// errors.As.
type sandboxDenialError struct {
	Output string
	Err    error
}

func (e *sandboxDenialError) Error() string {
	if e.Err == nil {
		return "sandbox denied write: " + e.Output
	}
	return "sandbox denied write: " + e.Err.Error() + ": " + e.Output
}

func (e *sandboxDenialError) Unwrap() error { return e.Err }

// sandboxDenialPattern needs a backend marker and a refusal word close together
// in the writer output. The gap cannot cross a path separator, so a plain
// "permission denied" for a path under /sandbox/ stays an ordinary error.
var sandboxDenialPattern = regexp.MustCompile(`(?i)(sandbox|landlock|seccomp|bwrap|apparmor)[^/\n]{0,40}(denied|denial|blocked|refused|violation|not permitted|eperm)|(denied|blocked|refused|violation)[^/\n]{0,40}(sandbox|landlock|seccomp|bwrap|apparmor)`)

// isLikelySandboxDenial reads the joined writer output. A plain permission
// error from os.WriteFile has no sandbox marker and stays unclassified.
func isLikelySandboxDenial(output string) bool {
	return sandboxDenialPattern.MatchString(output)
}

// classifyWriteError turns a failed write into a sandboxDenialError when its
// output names a sandbox refusal. Only that type increments the violation
// counter, and only that type reaches the caller as a classified error.
func (g *writeGate) classifyWriteError(output string, err error) error {
	if !isLikelySandboxDenial(output) {
		return err
	}
	g.sandboxDenials++
	return &sandboxDenialError{Output: output, Err: err}
}

// emitSandboxDenials writes one violation metric line when the run saw a
// sandbox refusal. A run with only ordinary file errors writes no line.
func (g *writeGate) emitSandboxDenials(out io.Writer) {
	if g.sandboxDenials > 0 {
		fmt.Fprintf(out, sandboxDenialsFormat, g.sandboxDenials)
	}
}

// emitSandboxDenials keeps the Run and test call site unchanged while the
// counter and the metric format live in the write gate.
func (r *runner) emitSandboxDenials() {
	r.writeGate.emitSandboxDenials(r.out)
}

// writeFileMode writes a generated file. overwrite is true only for card
// closures, which update an existing card in place. New cards keep the
// never-overwrite rule.
func (r *runner) writeFileMode(root, rel, content string, overwrite bool) error {
	path := filepath.Join(root, rel)
	if !r.opts.Quiet {
		fmt.Fprintln(r.out, " ", path)
	}
	if r.opts.DryRun {
		r.recordWritten(rel)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil // never overwrite an existing card
		}
	}
	data := []byte(content)
	if r.write != nil {
		output, err := r.write(path, data)
		if err != nil {
			return r.writeGate.classifyWriteError(output, err)
		}
		r.recordWritten(rel)
		return nil
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return r.writeGate.classifyWriteError(err.Error(), err)
	}
	r.recordWritten(rel)
	return nil
}

// writeFresh publishes a timestamped row. The freshness check has already
// passed, so the never-overwrite rule for untimestamped cards does not apply:
// a newer incoming row replaces the local file. The file mtime is set to the
// incoming timestamp so an unchanged row is a no-op on the next import instead
// of looking stale against the time of its own write.
func (r *runner) writeFresh(root, rel, content string, updatedAt time.Time) error {
	path := filepath.Join(root, rel)
	if !r.opts.Quiet {
		fmt.Fprintln(r.out, " ", path)
	}
	if r.opts.DryRun {
		r.recordWritten(rel)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	// The bytes are on disk now. Record them before Chtimes, so a timestamp
	// failure still leaves the delta describing the file that was written.
	r.recordWritten(rel)
	if !updatedAt.IsZero() {
		return os.Chtimes(path, updatedAt, updatedAt)
	}
	return nil
}
