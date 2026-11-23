package batchapply

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
)

// ImportReport is the machine-readable result of one import. Each list is
// written even when empty, so a consumer can index it without a nil check.
// Changes maps an updated id to the field names whose values changed. Created
// ids carry no changes because their whole content is new. WrittenPaths is the
// CC-04 delta: the paths that reached disk before the run ended. It stays
// populated when a later write fails.
type ImportReport struct {
	CreatedIDs      []string            `json:"CreatedIDs"`
	UpdatedIDs      []string            `json:"UpdatedIDs"`
	StaleSkippedIDs []string            `json:"StaleSkippedIDs"`
	TieKeptLocalIDs []string            `json:"TieKeptLocalIDs"`
	WrittenPaths    []string            `json:"WrittenPaths"`
	Changes         map[string][]string `json:"Changes,omitempty"`
}

// runReport owns the accumulated result of one import. The text lines and the
// JSON object read this state, so the two views cannot disagree.
type runReport struct {
	// The four lists and the change map are filled by the freshness checks
	// and printed once by Run after the write pass.
	createdIDs      []string
	updatedIDs      []string
	staleSkippedIDs []string
	tieKeptLocalIDs []string
	changes         map[string][]string
	updatedIssues   int
	// writtenPaths is the CC-04 delta. It holds one relative path per file
	// written before the run ended, in write order. A later failed write does
	// not clear it, so the error path still reports what reached disk.
	writtenPaths []string
	// reportEmitted keeps the JSON report to one object per run. Error paths
	// emit it too, so the guard prevents a second object after a success.
	reportEmitted bool
}

// recordStaleSkipped keeps one id per artifact in encounter order so the
// report stays reproducible and a duplicate row cannot name an id twice.
func (p *runReport) recordStaleSkipped(id string) {
	for _, seen := range p.staleSkippedIDs {
		if seen == id {
			return
		}
	}
	p.staleSkippedIDs = append(p.staleSkippedIDs, id)
}

// recordWritten adds one relative path to the CC-04 delta. A duplicate write
// of the same path is recorded once, as with the id lists above.
func (p *runReport) recordWritten(rel string) {
	if !slices.Contains(p.writtenPaths, rel) {
		p.writtenPaths = append(p.writtenPaths, rel)
	}
}

func (p *runReport) recordCreated(id string) {
	if !slices.Contains(p.createdIDs, id) {
		p.createdIDs = append(p.createdIDs, id)
	}
}

func (p *runReport) recordTieKeptLocal(id string) {
	if !slices.Contains(p.tieKeptLocalIDs, id) {
		p.tieKeptLocalIDs = append(p.tieKeptLocalIDs, id)
	}
}

// recordUpdated stores one overwritten id and the field names that changed.
// updatedIssues keeps its CBX-07 count so the existing text line is unchanged.
func (p *runReport) recordUpdated(id string, fields []string) {
	if !slices.Contains(p.updatedIDs, id) {
		p.updatedIDs = append(p.updatedIDs, id)
	}
	if p.changes == nil {
		p.changes = map[string][]string{}
	}
	p.changes[id] = fields
}

// importReport snapshots the four lists and the change map. Empty lists stay
// empty rather than nil so a JSON consumer can index them directly.
func (p *runReport) importReport() ImportReport {
	empty := func(ids []string) []string {
		if ids == nil {
			return []string{}
		}
		return append([]string(nil), ids...)
	}
	return ImportReport{
		CreatedIDs:      empty(p.createdIDs),
		UpdatedIDs:      empty(p.updatedIDs),
		StaleSkippedIDs: empty(p.staleSkippedIDs),
		TieKeptLocalIDs: empty(p.tieKeptLocalIDs),
		WrittenPaths:    empty(p.writtenPaths),
		Changes:         p.changes,
	}
}

// emitReport writes the CC-04 delta line and, when the caller supplied a
// writer, one JSON object. It runs on success and on failure, so a partial
// batch still names the files it created. The guard keeps both outputs to one
// copy per run when an error path calls it after a success.
func (p *runReport) emitReport(out io.Writer, jsonReport io.Writer) {
	if p.reportEmitted {
		return
	}
	p.reportEmitted = true
	fmt.Fprintf(out, writtenPathsFormat, strings.Join(p.writtenPaths, ","))
	if jsonReport != nil {
		_ = json.NewEncoder(jsonReport).Encode(p.importReport())
	}
}

// emitReport keeps the Run and test call site unchanged while the report state
// and the JSON encoding live in runReport.
func (r *runner) emitReport() {
	r.runReport.emitReport(r.out, r.opts.JSONReport)
}
