package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"testing"

	"github.com/refusedguy/beacon/pkg/core"
)

var (
	pluginActionsBlock = regexp.MustCompile(`(?s)export const CONTROL_ACTIONS = Object\.freeze\(\[([^\]]*)\]\)`)
	quotedVerb         = regexp.MustCompile(`"([a-z]+)"`)
)

// readTUIVerbs resolves the TUI's actions array through its Action constants.
// The TUI is a separate module whose package is main, so the test reads its
// source instead of importing it.
func readTUIVerbs(t *testing.T, path string) []string {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parser.ParseFile(%q): %v", path, err)
	}
	constants := map[string]string{}
	var verbs []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			values, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			switch gen.Tok {
			case token.CONST:
				for i, name := range values.Names {
					if i >= len(values.Values) {
						continue
					}
					lit, ok := values.Values[i].(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					value, err := strconv.Unquote(lit.Value)
					if err != nil {
						t.Fatalf("%s: unquote %s: %v", path, lit.Value, err)
					}
					constants[name.Name] = value
				}
			case token.VAR:
				if len(values.Names) != 1 || values.Names[0].Name != "actions" {
					continue
				}
				if len(values.Values) != 1 {
					t.Fatalf("%s: actions has %d values", path, len(values.Values))
				}
				array, ok := values.Values[0].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("%s: actions is not a literal array", path)
				}
				for _, element := range array.Elts {
					id, ok := element.(*ast.Ident)
					if !ok {
						t.Fatalf("%s: actions has a non-constant element", path)
					}
					verb, ok := constants[id.Name]
					if !ok {
						t.Fatalf("%s: action %s has no constant value", path, id.Name)
					}
					verbs = append(verbs, verb)
				}
			}
		}
	}
	if len(verbs) == 0 {
		t.Fatalf("%s: TUI vocabulary is empty", path)
	}
	return verbs
}

// readPluginVerbs pulls the quoted verbs from the plugin's CONTROL_ACTIONS
// declaration. The plugin is JavaScript, so the test reads its source.
func readPluginVerbs(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q): %v", path, err)
	}
	match := pluginActionsBlock.FindSubmatch(data)
	if match == nil {
		t.Fatalf("%s: CONTROL_ACTIONS declaration not found", path)
	}
	found := quotedVerb.FindAllSubmatch(match[1], -1)
	verbs := make([]string, 0, len(found))
	for _, item := range found {
		verbs = append(verbs, string(item[1]))
	}
	if len(verbs) == 0 {
		t.Fatalf("%s: plugin vocabulary is empty", path)
	}
	return verbs
}

func TestWireControlVocabulariesMatchCore(t *testing.T) {
	want := core.ControlActionNames()
	cases := []struct {
		name string
		got  []string
	}{
		{"TUI", readTUIVerbs(t, "../../ui/tui/api.go")},
		{"plugin", readPluginVerbs(t, "../../.opencode/plugins/beacon-project/ui/ui_model.js")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !reflect.DeepEqual(tc.got, want) {
				t.Fatalf("%s verbs = %v, core = %v", tc.name, tc.got, want)
			}
		})
	}
}
