// Package registry guards the author-supplied registry config before it is
// validated (T5.12) and registered. An author config is untrusted input: it
// may carry a credential in a field name or reference a login file such as
// auth.json. This package refuses such a config before validation or
// registration can log it, and it never copies a secret value into an error.
package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// maxConfigBytes is the hard ceiling for an author config. OpenRig's bundle
// check refuses oversized files; the guard applies the same limit before the
// body is parsed so an unbounded file cannot reach the validator or a log.
const maxConfigBytes = 64 * 1024 // 64 KiB

// secretMarkers are the field names and login-file references the guard looks
// for. The order is the reporting priority when a config trips more than one
// marker, which keeps errors deterministic across map iterations.
var secretMarkers = []string{"token", "key", "password", "auth.json"}

// camelBoundaryRE splits a lower-to-upper transition so camelCase names such
// as apiKey are reduced to the same word segments as api_key.
var camelBoundaryRE = regexp.MustCompile(`([a-z0-9])([A-Z])`)

// ConfigError reports a rejected author config. Field holds only the marker
// that matched (or "size" for an oversized file), never the offending value,
// so formatting a ConfigError into a log line cannot disclose a secret.
type ConfigError struct {
	Field  string
	Reason string
}

func (e *ConfigError) Error() string {
	return fmt.Sprintf("registry: author config: %s %q", e.Reason, e.Field)
}

// GuardConfig checks author config bytes. It refuses a body larger than 64 KiB
// and any config carrying a token, key, password or auth.json field. The
// returned error names the string that matched, not the value behind it.
func GuardConfig(data []byte) error {
	if len(data) > maxConfigBytes {
		return &ConfigError{Field: "size", Reason: fmt.Sprintf("larger than %d bytes", maxConfigBytes)}
	}

	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		// Do not wrap the decoder error: its message can quote a fragment of
		// the offending value, which would put secret content in a log.
		return errors.New("registry: author config: invalid JSON")
	}

	found := make(map[string]bool, len(secretMarkers))
	collectMarkers(doc, found)
	for _, marker := range secretMarkers {
		if found[marker] {
			return &ConfigError{Field: marker, Reason: "secret field"}
		}
	}
	return nil
}

// GuardFile checks the config at path. It stats the file first so an oversized
// file is rejected without being read, then delegates the body to GuardConfig.
func GuardFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("registry: author config: stat %q: %w", filepath.Base(path), err)
	}
	if info.Size() > maxConfigBytes {
		return &ConfigError{Field: "size", Reason: fmt.Sprintf("larger than %d bytes", maxConfigBytes)}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("registry: author config: read %q: %w", filepath.Base(path), err)
	}
	return GuardConfig(data)
}

// collectMarkers walks the decoded config and records every marker it finds
// in found. It descends through objects and arrays and checks string values
// for auth.json references. It never retains a value, only marker names.
func collectMarkers(node any, found map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for key, child := range v {
			if marker, ok := markerInField(key); ok {
				found[marker] = true
			}
			collectMarkers(child, found)
		}
	case []any:
		for _, child := range v {
			collectMarkers(child, found)
		}
	case string:
		if strings.Contains(strings.ToLower(v), "auth.json") {
			found["auth.json"] = true
		}
	}
}

// markerInField reports the marker a field name matches. Matching is
// case-insensitive and splits camelCase, snake_case and kebab-case names into
// words, so api_key, apiKey and AUTH_TOKEN all match "key" or "token" while
// unrelated words such as "keyboard" do not.
func markerInField(name string) (string, bool) {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "auth.json") {
		return "auth.json", true
	}
	split := strings.ToLower(camelBoundaryRE.ReplaceAllString(name, "${1}_${2}"))
	for _, word := range strings.FieldsFunc(split, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		switch word {
		case "token", "key", "password":
			return word, true
		}
	}
	return "", false
}
