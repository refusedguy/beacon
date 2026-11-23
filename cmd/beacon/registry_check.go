package main

// T5.54: local registry check (ADR 0089, ADR 0086).
//
// The check reads a decoded registry config and reports every problem it finds
// through a narrow, read-only seam. It never writes state: no registration, no
// index rebuild, no session calls and no config rewrite. Schema, limits, the
// capability index and the content digest are checked with the validators in
// internal/infra/registry. Every issue names the config field and the reason
// (T5.54 Проверка), and a clean result carries the digest so the same input can
// be compared across runs.
//
// Dispatch registration in cmd/beacon/main.go is outside these OwnerFiles:
// this file owns the check and its output seam, not the command dispatch.

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"

	"github.com/refusedguy/beacon/internal/infra/registry"
)

// registryCheckIssue is one field-named problem found in the config.
type registryCheckIssue struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (i registryCheckIssue) Error() string {
	return fmt.Sprintf("%s: %s", i.Field, i.Reason)
}

// registryCheckResult is the outcome of one local check pass. A clean pass has
// no issues; Digest is set for every decodable config.
type registryCheckResult struct {
	Digest string               `json:"digest"`
	Issues []registryCheckIssue `json:"issues"`
}

// OK reports whether the config passed every check.
func (r registryCheckResult) OK() bool { return len(r.Issues) == 0 }

// issueFrom converts a validator error into a field-named issue. A
// ValidationError already carries its field; other validators put the field in
// their message, so those are reported under the narrow field they check.
func issueFrom(err error, fallbackField string) registryCheckIssue {
	var ve *registry.ValidationError
	if errors.As(err, &ve) {
		return registryCheckIssue{Field: ve.Field, Reason: ve.Reason}
	}
	return registryCheckIssue{Field: fallbackField, Reason: err.Error()}
}

// checkGuard runs the secret guard on the decoded config before a validator
// can echo a field. A ConfigError names the marker that matched, never its
// value, so the issue is safe to print.
func checkGuard(config map[string]any, out *[]registryCheckIssue) {
	if err := registry.GuardDocument(config); err != nil {
		var ce *registry.ConfigError
		if errors.As(err, &ce) {
			*out = append(*out, registryCheckIssue{Field: ce.Field, Reason: ce.Reason})
			return
		}
		*out = append(*out, registryCheckIssue{Field: "config", Reason: err.Error()})
	}
}

// checkVersionSchema applies the migration rules to the decoded document so an
// unknown version or a foreign schema cannot pass the local check.
func checkVersionSchema(config map[string]any, out *[]registryCheckIssue) {
	if err := registry.CheckVersionSchema(config); err != nil {
		*out = append(*out, issueFrom(err, "config"))
	}
}

// checkSchema runs the authored-config schema validator.
func checkSchema(config map[string]any, out *[]registryCheckIssue) {
	if err := registry.Validate(config); err != nil {
		*out = append(*out, issueFrom(err, "config"))
	}
}

// checkLimits validates the optional limits block. Absent limits use the
// documented defaults and pass.
func checkLimits(config map[string]any, out *[]registryCheckIssue) {
	raw, ok := config["limits"]
	if !ok {
		return
	}
	mapping, ok := raw.(map[string]any)
	if !ok {
		*out = append(*out, registryCheckIssue{Field: "limits", Reason: "must be a mapping"})
		return
	}
	if _, err := registry.ParseLimits(mapping); err != nil {
		*out = append(*out, registryCheckIssue{Field: "limits", Reason: err.Error()})
	}
}

// registryCheckStrings accepts the shapes a decoded config can take for a
// string list: []any from a JSON/YAML decoder or []string from a hand-built
// test config.
func registryCheckStrings(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...), true
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, s)
		}
		return out, true
	default:
		return nil, false
	}
}

// rosterEntry is one roster record reduced to what the index check needs.
type rosterEntry struct {
	ID           string
	Capabilities []string
}

// rosterRecords reduces a decoded roster to id/capabilities pairs. Both the
// list form and the keyed mapping form are accepted, matching the schema.
func rosterRecords(config map[string]any) ([]rosterEntry, error) {
	raw, ok := config["roster"]
	if !ok {
		return nil, nil
	}
	var out []rosterEntry
	switch x := raw.(type) {
	case []any:
		for _, item := range x {
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, errors.New("roster: entry must be a mapping")
			}
			entry, err := rosterRecord(obj, "")
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
	case map[string]any:
		for _, key := range registryCheckSortedKeys(x) {
			obj, ok := x[key].(map[string]any)
			if !ok {
				return nil, fmt.Errorf("roster.%s: must be a mapping", key)
			}
			entry, err := rosterRecord(obj, key)
			if err != nil {
				return nil, err
			}
			out = append(out, entry)
		}
	default:
		return nil, errors.New("roster: must be a list or mapping")
	}
	return out, nil
}

func rosterRecord(obj map[string]any, key string) (rosterEntry, error) {
	id := key
	if v, ok := obj["id"]; ok {
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return rosterEntry{}, errors.New("roster.id: must be a non-empty string")
		}
		id = s
	}
	if strings.TrimSpace(id) == "" {
		return rosterEntry{}, errors.New("roster.id: must be a non-empty string")
	}
	var caps []string
	if v, ok := obj["capabilities"]; ok {
		parsed, ok := registryCheckStrings(v)
		if !ok {
			return rosterEntry{}, fmt.Errorf("roster.%s.capabilities: must be a list of strings", id)
		}
		caps = parsed
	}
	return rosterEntry{ID: id, Capabilities: caps}, nil
}

func registryCheckSortedKeys(m map[string]any) []string {
	out := slices.Sorted(maps.Keys(m))
	return out
}

// checkIndex rebuilds the derived index from the roster declarations and
// verifies both directions with the T5.24 check. Building from the same roster
// that is checked keeps the comparison deterministic and read-only: no stored
// index or session state is touched (ADR 0089). An empty registry has no index
// to verify; a non-empty roster is always reconciled, so executors without
// capabilities fail through CheckCapabilityIndex instead of passing silently.
func checkIndex(config map[string]any, out *[]registryCheckIssue) {
	records, err := rosterRecords(config)
	if err != nil {
		*out = append(*out, issueFrom(err, "roster"))
		return
	}
	if len(records) == 0 {
		return
	}
	declarations := make(map[string][]string, len(records))
	build := make([]registry.ExecutorRecord, 0, len(records))
	for _, r := range records {
		declarations[r.ID] = append([]string(nil), r.Capabilities...)
		build = append(build, registry.ExecutorRecord{ID: r.ID, Capabilities: r.Capabilities})
	}

	idx, err := registry.BuildCapabilityIndex(build)
	if err != nil {
		*out = append(*out, registryCheckIssue{Field: "capabilities", Reason: err.Error()})
		return
	}
	index := make(map[string][]string)
	for _, cap := range idx.Capabilities() {
		index[cap] = idx.Executors(cap)
	}
	if err := registry.CheckCapabilityIndex(declarations, index); err != nil {
		*out = append(*out, registryCheckIssue{Field: "agent_capabilities", Reason: err.Error()})
	}
}

// checkRegistryConfig runs every local check against a decoded config. It does
// not mutate config and performs no I/O, so a caller may run it again at any
// time without changing the answer.
func checkRegistryConfig(config map[string]any) registryCheckResult {
	res := registryCheckResult{Issues: []registryCheckIssue{}}
	res.Digest = registry.Digest(config)
	checkGuard(config, &res.Issues)
	checkSchema(config, &res.Issues)
	checkVersionSchema(config, &res.Issues)
	checkLimits(config, &res.Issues)
	checkIndex(config, &res.Issues)
	return res
}

// registryCheckRunner prints a check result as terse text, one issue per line.
// A clean result prints "ok" with the digest. It is the read-only output seam
// for the dispatch registration that lives outside these OwnerFiles.
func registryCheckRunner(res registryCheckResult, out io.Writer) int {
	if res.OK() {
		fmt.Fprintf(out, "ok %s\n", res.Digest)
		return 0
	}
	for _, issue := range res.Issues {
		fmt.Fprintln(out, issue.Error())
	}
	return 1
}
