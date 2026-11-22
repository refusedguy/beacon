// Package registry reads and migrates the authored agent registry config.
//
// Authored files outlive the code that reads them, so every config carries an
// explicit schema version. Migrate accepts version 1 and the current version 2,
// upgrades a version 1 document to version 2 with a warning, and fails closed on
// any other version. A caller must never treat an unknown version as current.
package registry

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

const (
	// SchemaV1 is the first published config format.
	SchemaV1 = 1
	// SchemaV2 is the format this build reads and writes.
	SchemaV2 = 2
	// CurrentSchemaVersion is the newest version Migrate can return.
	CurrentSchemaVersion = SchemaV2
)

// SchemaName is the stable prefix of the schema field, for example
// "beacon.agent-registry/v2".
const SchemaName = "beacon.agent-registry"

// Warning is a non-fatal note from reading an old config. Code is stable and
// suitable for metrics; Message is for humans.
type Warning struct {
	Code    string
	Message string
}

// migratedWarningCode identifies the v1 to v2 upgrade note.
const migratedWarningCode = "schema-migrated"

// Config is a normalized document ready for the config validator. Data is a
// JSON object stamped with CurrentSchemaVersion. Warnings says what Migrate
// changed; an empty slice means the input was already current.
type Config struct {
	Version  int
	Schema   string
	Data     []byte
	Warnings []Warning
}

// Migrate reads config and returns a version 2 document. A version 1 config is
// upgraded and reported through Warnings. A version 2 config is returned
// unchanged. Any other version, a missing version, or a schema field that
// disagrees with the version is an error: the caller must not guess.
func Migrate(config []byte) (Config, error) {
	if len(bytes.TrimSpace(config)) == 0 {
		return Config{}, errors.New("registry: empty config")
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(config, &doc); err != nil {
		return Config{}, fmt.Errorf("registry: config must be a JSON object: %w", err)
	}
	if doc == nil {
		return Config{}, errors.New("registry: config must be a JSON object")
	}

	version, err := readVersion(doc)
	if err != nil {
		return Config{}, err
	}
	schema, err := checkSchema(doc, version)
	if err != nil {
		return Config{}, err
	}

	switch version {
	case SchemaV1:
		return migrateV1ToV2(doc)
	case SchemaV2:
		return Config{
			Version: SchemaV2,
			Schema:  schema,
			Data:    append([]byte(nil), config...),
		}, nil
	default:
		return Config{}, fmt.Errorf("registry: unsupported config schema version %d (current %d)",
			version, CurrentSchemaVersion)
	}
}

// CheckVersionSchema validates the version and schema markers of an already
// decoded registry document. It mirrors readVersion and checkSchema for the
// map shapes a YAML decoder produces, so a local check can refuse an unknown
// version or a foreign schema without reading JSON bytes. A numeric string is
// accepted because Validate still models version as a string; the parsed value
// decides acceptance.
func CheckVersionSchema(config map[string]any) error {
	version, err := decodedVersion(config)
	if err != nil {
		return err
	}
	switch version {
	case SchemaV1, SchemaV2:
	default:
		return &ValidationError{Field: "version", Reason: fmt.Sprintf("unsupported schema version %d (current %d)", version, CurrentSchemaVersion)}
	}
	return decodedSchema(config, version)
}

// decodedVersion reads a positive integer from the version marker. YAML gives
// int, JSON gives float64, and the existing decoder-shaped tests use string.
func decodedVersion(config map[string]any) (int, error) {
	raw, ok := config["version"]
	if !ok {
		return 0, &ValidationError{Field: "version", Reason: "schema version is required"}
	}

	var version int
	switch v := raw.(type) {
	case int:
		version = v
	case int64:
		version = int(v)
	case float64:
		if v != math.Trunc(v) {
			return 0, &ValidationError{Field: "version", Reason: "must be an integer"}
		}
		version = int(v)
	case json.Number:
		n, err := strconv.Atoi(v.String())
		if err != nil {
			return 0, &ValidationError{Field: "version", Reason: "must be an integer"}
		}
		version = n
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return 0, &ValidationError{Field: "version", Reason: "must be an integer"}
		}
		version = n
	default:
		return 0, &ValidationError{Field: "version", Reason: "must be an integer"}
	}
	if version <= 0 {
		return 0, &ValidationError{Field: "version", Reason: fmt.Sprintf("invalid schema version %d", version)}
	}
	return version, nil
}

// decodedSchema applies the checkSchema rule to a decoded document. An absent
// schema is allowed; a present one must carry the version prefix and agree
// with the version marker.
func decodedSchema(config map[string]any, version int) error {
	raw, ok := config["schema"]
	if !ok {
		return nil
	}
	schema, ok := raw.(string)
	if !ok {
		return &ValidationError{Field: "schema", Reason: "must be a string"}
	}
	prefix := SchemaName + "/v"
	if !strings.HasPrefix(schema, prefix) {
		return &ValidationError{Field: "schema", Reason: fmt.Sprintf("unexpected schema %q, want prefix %q", schema, prefix)}
	}
	fromSchema, err := strconv.Atoi(strings.TrimPrefix(schema, prefix))
	if err != nil {
		return &ValidationError{Field: "schema", Reason: fmt.Sprintf("schema %q has no integer version", schema)}
	}
	if fromSchema != version {
		return &ValidationError{Field: "schema", Reason: fmt.Sprintf("schema %q disagrees with version %d", schema, version)}
	}
	return nil
}

// readVersion requires an explicit positive integer. A config without a
// version is not assumed to be old: that would silently accept an unknown
// format.
func readVersion(doc map[string]json.RawMessage) (int, error) {
	raw, ok := doc["version"]
	if !ok {
		return 0, errors.New("registry: config has no schema version")
	}
	var version int
	if err := json.Unmarshal(raw, &version); err != nil {
		return 0, fmt.Errorf("registry: schema version must be an integer: %w", err)
	}
	if version <= 0 {
		return 0, fmt.Errorf("registry: invalid schema version %d", version)
	}
	return version, nil
}

// checkSchema returns the schema string, which is empty when the field is
// absent. A present schema must name the same version as the version field.
func checkSchema(doc map[string]json.RawMessage, version int) (string, error) {
	raw, ok := doc["schema"]
	if !ok {
		return "", nil
	}
	var schema string
	if err := json.Unmarshal(raw, &schema); err != nil {
		return "", fmt.Errorf("registry: schema must be a string: %w", err)
	}
	prefix := SchemaName + "/v"
	if !strings.HasPrefix(schema, prefix) {
		return "", fmt.Errorf("registry: unexpected schema %q, want prefix %q", schema, prefix)
	}
	fromSchema, err := strconv.Atoi(strings.TrimPrefix(schema, prefix))
	if err != nil {
		return "", fmt.Errorf("registry: schema %q has no integer version", schema)
	}
	if fromSchema != version {
		return "", fmt.Errorf("registry: schema %q disagrees with version %d", schema, version)
	}
	return schema, nil
}

// migrateV1ToV2 advances only the version markers. Payload fields are shared
// between v1 and v2, so every other key keeps its JSON value.
func migrateV1ToV2(doc map[string]json.RawMessage) (Config, error) {
	out := make(map[string]json.RawMessage, len(doc)+2)
	for key, value := range doc {
		out[key] = value
	}
	out["version"] = json.RawMessage(strconv.Itoa(SchemaV2))
	schema, err := json.Marshal(schemaFor(SchemaV2))
	if err != nil {
		return Config{}, fmt.Errorf("registry: encode migrated schema: %w", err)
	}
	out["schema"] = schema

	data, err := json.Marshal(out)
	if err != nil {
		return Config{}, fmt.Errorf("registry: encode migrated config: %w", err)
	}
	return Config{
		Version: SchemaV2,
		Schema:  schemaFor(SchemaV2),
		Data:    data,
		Warnings: []Warning{{
			Code:    migratedWarningCode,
			Message: "config schema version 1 was migrated to version 2",
		}},
	}, nil
}

func schemaFor(version int) string { return SchemaName + "/v" + strconv.Itoa(version) }
