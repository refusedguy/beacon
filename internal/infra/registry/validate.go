// Package registry validates the author-owned agent registry config before any
// registration happens. It rejects unknown keys, unsafe paths and empty
// identifiers at the boundary so a bad config never reaches registry state.
package registry

import (
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ValidationError names the config field that failed, so the author can be
// pointed at the exact key instead of a generic "invalid config" message.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}

func invalid(at, format string, args ...any) error {
	return &ValidationError{Field: at, Reason: fmt.Sprintf(format, args...)}
}

// Unknown keys are refused rather than ignored: a typo must fail loudly
// instead of silently changing which agents get registered. The sets list
// every key that belongs to the beacon.agent-registry/v1 schema.
var (
	rootKeys          = keySet("schema", "version", "project", "roster", "configurations", "limits")
	rosterKeys        = keySet("id", "runtime", "roles", "capabilities", "path")
	configurationKeys = keySet("id", "members", "alias", "recommended")
)

func keySet(keys ...string) map[string]struct{} {
	out := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

func field(path, name string) string {
	switch {
	case name == "":
		return path
	case path == "":
		return name
	default:
		return path + "." + name
	}
}

// Validate checks a decoded agents.yaml document. The config arrives as a
// generic map because the YAML decoder belongs to the CLI boundary; this
// package only needs the structure and the field names to enforce the schema.
func Validate(config map[string]any) error {
	if err := validateKeys(config, rootKeys, ""); err != nil {
		return err
	}
	for _, name := range []string{"schema", "project"} {
		if v, ok := config[name]; ok {
			if _, ok := v.(string); !ok {
				return invalid(name, "must be a string")
			}
		}
	}
	if v, ok := config["version"]; ok {
		if err := validateVersion("version", v); err != nil {
			return err
		}
	}
	if raw, ok := config["roster"]; ok {
		entries, err := decodeEntries(raw, "roster")
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := validateKeys(e.obj, rosterKeys, e.at); err != nil {
				return err
			}
			if err := validateID(field(e.at, "id"), entryID(e)); err != nil {
				return err
			}
			roles, ok := e.obj["roles"]
			if !ok {
				return invalid(field(e.at, "roles"), "must not be empty")
			}
			if err := validateRoles(field(e.at, "roles"), roles); err != nil {
				return err
			}
			if p, ok := e.obj["path"]; ok {
				if err := validatePath(field(e.at, "path"), p); err != nil {
					return err
				}
			}
		}
	}
	if raw, ok := config["configurations"]; ok {
		entries, err := decodeEntries(raw, "configurations")
		if err != nil {
			return err
		}
		ids := make(map[string]struct{}, len(entries))
		for _, e := range entries {
			if id, ok := entryID(e).(string); ok {
				ids[id] = struct{}{}
			}
		}
		for _, e := range entries {
			if err := validateKeys(e.obj, configurationKeys, e.at); err != nil {
				return err
			}
			if err := validateID(field(e.at, "id"), entryID(e)); err != nil {
				return err
			}
			if members, ok := e.obj["members"]; ok {
				if _, ok := toStringSlice(members); !ok {
					return invalid(field(e.at, "members"), "must be a list of configuration ids")
				}
			}
			if alias, ok := e.obj["alias"]; ok {
				if err := validateConfigRef(field(e.at, "alias"), alias, ids); err != nil {
					return err
				}
			}
			if recommended, ok := e.obj["recommended"]; ok {
				if err := validateConfigRef(field(e.at, "recommended"), recommended, ids); err != nil {
					return err
				}
			}
		}
	}
	if raw, ok := config["limits"]; ok {
		if _, ok := raw.(map[string]any); !ok {
			return invalid("limits", "must be a mapping")
		}
	}
	return nil
}

// entry is one roster or configuration record. at is its dotted field path;
// key is set when the schema uses a mapping, where the key carries the id.
type entry struct {
	at  string
	obj map[string]any
	key any
}

func decodeEntries(raw any, path string) ([]entry, error) {
	switch x := raw.(type) {
	case []any:
		out := make([]entry, 0, len(x))
		for i, item := range x {
			at := fmt.Sprintf("%s[%d]", path, i)
			obj, ok := item.(map[string]any)
			if !ok {
				return nil, invalid(at, "must be a mapping")
			}
			out = append(out, entry{at: at, obj: obj})
		}
		return out, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out := make([]entry, 0, len(keys))
		for _, k := range keys {
			obj, ok := x[k].(map[string]any)
			if !ok {
				return nil, invalid(field(path, k), "must be a mapping")
			}
			out = append(out, entry{at: field(path, k), obj: obj, key: k})
		}
		return out, nil
	default:
		return nil, invalid(path, "must be a list or mapping")
	}
}

func entryID(e entry) any {
	if id, ok := e.obj["id"]; ok {
		return id
	}
	return e.key
}

func validateKeys(obj map[string]any, allowed map[string]struct{}, prefix string) error {
	unknown := make([]string, 0)
	for k := range obj {
		if _, ok := allowed[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return invalid(field(prefix, unknown[0]), "unknown key")
}

func validateID(at string, v any) error {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return invalid(at, "must be a non-empty string")
	}
	return nil
}

// validateVersion accepts the integer shapes a YAML or JSON decoder produces
// and the numeric strings migrate.go also accepts. Only the versions published
// in migrate.go are known, so an unlisted number fails closed.
func validateVersion(at string, v any) error {
	var n int
	switch x := v.(type) {
	case int:
		n = x
	case int64:
		n = int(x)
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return invalid(at, "must be an integer")
		}
		n = int(i)
	case float64:
		if x != math.Trunc(x) {
			return invalid(at, "must be an integer without a fractional part")
		}
		n = int(x)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return invalid(at, "must be an integer")
		}
		n = parsed
	default:
		return invalid(at, "must be an integer")
	}
	if n != SchemaV1 && n != SchemaV2 {
		return invalid(at, "unknown schema version %d", n)
	}
	return nil
}

// validateConfigRef checks that a preset reference names exactly one existing
// configuration id. A preset resolves to a single id, so a list is not a valid
// reference even when every item is known.
func validateConfigRef(at string, v any, ids map[string]struct{}) error {
	name, ok := v.(string)
	if !ok || strings.TrimSpace(name) == "" {
		return invalid(at, "must name exactly one configuration id")
	}
	if _, ok := ids[name]; !ok {
		return invalid(at, "must name an existing configuration id %q", name)
	}
	return nil
}

func validateRoles(at string, v any) error {
	items, ok := toStringSlice(v)
	if !ok {
		return invalid(at, "must be a non-empty list of role names")
	}
	if len(items) == 0 {
		return invalid(at, "must not be empty")
	}
	for i, role := range items {
		if strings.TrimSpace(role) == "" {
			return invalid(fmt.Sprintf("%s[%d]", at, i), "must be a non-empty string")
		}
	}
	return nil
}

// validatePath refuses absolute paths and any ".." segment. Both would let a
// config reach outside the project root, which the registry must not allow.
func validatePath(at string, v any) error {
	s, ok := v.(string)
	if !ok {
		return invalid(at, "must be a string")
	}
	if strings.TrimSpace(s) == "" {
		return invalid(at, "must not be empty")
	}
	if filepath.IsAbs(s) || strings.HasPrefix(s, "/") || strings.HasPrefix(s, "\\") || (len(s) > 1 && s[1] == ':') {
		return invalid(at, "must be relative, got absolute path %q", s)
	}
	for _, part := range strings.Split(filepath.ToSlash(s), "/") {
		if part == ".." {
			return invalid(at, "must not contain a '..' segment")
		}
	}
	return nil
}

func toStringSlice(v any) ([]string, bool) {
	switch x := v.(type) {
	case []string:
		return x, true
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
