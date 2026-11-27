// Package bench is a repeatable measurement harness for Beacon hot paths.
// It is not imported by production code and it changes no production file.
//
// Run the whole harness with one command:
//
//	go test -bench=. -benchmem -count=5 ./internal/bench/
//
// Each benchmark pass runs every hotspot once and prints a report with the
// median, the range, work counts, error counts, the named limiter and each
// hotspot's share of the end-to-end median.
//
// Four hotspots call production code directly:
//
//   - sqlite snapshot queries call internal/infra/sqlite
//   - task card parsing calls internal/infra/tasksource.ParseTaskFields
//   - JSON wire encoding calls pkg/core.NewWireRuns and json.Marshal
//   - redaction calls pkg/core/redact.Redact
//
// Two hotspots are unexported in other packages (opJournal in internal/infra/mcp
// and fanout in the main package of cmd/beacon). They are mirrored line for
// line in models.go so this package can measure the same algorithm without
// editing production files. The mirrors are annotated with their source
// locations. See docs/perf/dossier.md for the protocol and the captured runs.
package bench

// Baseline measurement run.
