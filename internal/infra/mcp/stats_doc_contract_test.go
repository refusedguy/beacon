package mcp

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const (
	statsDocPath        = "docs/adrs/0094-seven-tools.md"
	statsDocMarkerBegin = "<!-- stats-fields:begin"
	statsDocMarkerEnd   = "stats-fields:end -->"
)

// TestStatsDocListsEveryField keeps the ADR field list and the StatsOutput type
// in step. The schema is derived from the type, so a new field reaches clients
// without an ADR edit unless this test fails the build.
func TestStatsDocListsEveryField(t *testing.T) {
	doc := readStatsADR(t)
	documented := documentedStatsFields(t, doc)
	actual := statsOutputFields()

	sort.Strings(documented)
	sort.Strings(actual)
	if !reflect.DeepEqual(documented, actual) {
		t.Errorf("stats fields differ from %s:\nADR:         %v\nStatsOutput: %v\nUpdate the marker block in the ADR in the same change as StatsOutput.",
			statsDocPath, documented, actual)
	}
}

func readStatsADR(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(file), "..", "..", "..", statsDocPath)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func documentedStatsFields(t *testing.T, doc string) []string {
	t.Helper()
	begin := strings.Index(doc, statsDocMarkerBegin)
	if begin < 0 {
		t.Fatalf("%s has no %s marker", statsDocPath, statsDocMarkerBegin)
	}
	rest := doc[begin+len(statsDocMarkerBegin):]
	end := strings.Index(rest, statsDocMarkerEnd)
	if end < 0 {
		t.Fatalf("%s has no %s marker", statsDocPath, statsDocMarkerEnd)
	}
	var fields []string
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			fields = append(fields, line)
		}
	}
	if len(fields) == 0 {
		t.Fatalf("%s stats marker block is empty", statsDocPath)
	}
	return fields
}

// statsOutputFields flattens the JSON tags of StatsOutput. A struct field is
// expanded to prefix.subfield so nested blocks are documented too.
func statsOutputFields() []string {
	var fields []string
	typ := reflect.TypeOf(StatsOutput{})
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := jsonFieldName(field)
		if name == "" {
			continue
		}
		if nested := structType(field.Type); nested != nil {
			for j := 0; j < nested.NumField(); j++ {
				subName := jsonFieldName(nested.Field(j))
				if subName != "" {
					fields = append(fields, name+"."+subName)
				}
			}
			continue
		}
		fields = append(fields, name)
	}
	return fields
}

func jsonFieldName(field reflect.StructField) string {
	name := strings.Split(field.Tag.Get("json"), ",")[0]
	if name == "-" {
		return ""
	}
	return name
}

func structType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct {
		return t
	}
	return nil
}
