package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestCommandFormatsAcceptDeclaredValues(t *testing.T) {
	for name, allowed := range commandFormats {
		for _, format := range allowed {
			t.Run(name+"/"+format, func(t *testing.T) {
				var errOut bytes.Buffer
				if !parseCommandFormat(name, format, &errOut, false) {
					t.Fatalf("формат %q отклонён: %q", format, errOut.String())
				}
				if errOut.Len() != 0 {
					t.Fatalf("формат %q записал в stderr: %q", format, errOut.String())
				}
			})
		}
	}
}

func TestCommandFormatsRejectUnknownValue(t *testing.T) {
	for name := range commandFormats {
		t.Run(name, func(t *testing.T) {
			var errOut bytes.Buffer
			if parseCommandFormat(name, "yaml", &errOut, false) {
				t.Fatal("неизвестный формат принят")
			}
			if !strings.Contains(errOut.String(), "yaml") {
				t.Fatalf("в ошибке нет отклонённого значения: %q", errOut.String())
			}
		})
	}
}

func TestLogsAcceptsFormatFlagAndJSONAlias(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"table", []string{"logs", "--format", "table"}},
		{"json", []string{"logs", "--format", "json"}},
		{"alias", []string{"logs", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(beaconStateDirEnv, t.TempDir())
			c, _, errOut, _ := t06NewCLI(t, nil)
			if code := c.Run(tc.args); code != 0 {
				t.Fatalf("code = %d, stderr: %s", code, errOut.String())
			}
		})
	}
}

func TestInspectAcceptsFormatFlagAndJSONAlias(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantJSON bool
	}{
		{"table", []string{"inspect", "--format", "table"}, false},
		{"json", []string{"inspect", "--format", "json"}, true},
		{"alias", []string{"inspect", "--json"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, out, errOut, _ := t06NewCLI(t, nil)
			installFakeInspectStore(t, boundInspectStore())
			if code := c.Run(tc.args); code != 0 {
				t.Fatalf("code = %d, stderr: %s", code, errOut.String())
			}
			var view inspectView
			err := json.Unmarshal(out.Bytes(), &view)
			if tc.wantJSON && err != nil {
				t.Fatalf("ожидался JSON: %v; вывод %q", err, out.String())
			}
			if !tc.wantJSON && err == nil {
				t.Fatalf("ожидалась таблица, получен JSON: %q", out.String())
			}
		})
	}
}

func TestMetricsFormatValuesAndJSONAlias(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"json", []string{"metrics", "--format", "json"}},
		{"prometheus", []string{"metrics", "--format", "prometheus"}},
		{"alias", []string{"metrics", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _, errOut, _ := t06NewCLI(t, nil)
			if code := c.Run(tc.args); code != 0 {
				t.Fatalf("code = %d, stderr: %s", code, errOut.String())
			}
		})
	}
}

// advertisedFormats extracts the values after --format from a catalog Usage
// string. The catalog writes them as "[--format table|json]".
func advertisedFormats(usage string) []string {
	const marker = "--format "
	i := strings.Index(usage, marker)
	if i < 0 {
		return nil
	}
	rest := usage[i+len(marker):]
	if end := strings.IndexByte(rest, ']'); end >= 0 {
		rest = rest[:end]
	}
	return strings.Split(rest, "|")
}

func TestCatalogFormatsMatchRegistry(t *testing.T) {
	advertised := map[string][]string{}
	for _, c := range catalog {
		if values := advertisedFormats(c.Usage); values != nil {
			advertised[c.Name] = values
		}
	}

	if len(advertised) != len(commandFormats) {
		t.Fatalf("каталог advertise-ит --format у %d команд, commandFormats содержит %d", len(advertised), len(commandFormats))
	}
	for name, values := range advertised {
		got, ok := commandFormats[name]
		if !ok {
			t.Errorf("команда %q advertise-ит --format, но отсутствует в commandFormats", name)
			continue
		}
		if strings.Join(values, ",") != strings.Join(got, ",") {
			t.Errorf("команда %q: в каталоге %v, в commandFormats %v", name, values, got)
		}
	}
	for name := range commandFormats {
		if _, ok := advertised[name]; !ok {
			t.Errorf("commandFormats содержит %q, но каталог не advertise-ит --format", name)
		}
	}
}
