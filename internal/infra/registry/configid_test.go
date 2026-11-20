package registry

import (
	"strings"
	"testing"
)

// TestConfigurationIDCanonical checks the ID against the examples fixed by
// the registry design and by the OpenRig identity vectors. Byte order puts
// uppercase ids first and a shorter prefix before its longer sibling.
func TestConfigurationIDCanonical(t *testing.T) {
	tests := []struct {
		name    string
		members []Member
		want    string
	}{
		{
			name: "project roster",
			members: []Member{
				{ID: "orchestrator", Runtime: "primary"},
				{ID: "executor", Runtime: "subagent"},
			},
			want: "executor=subagent,orchestrator=primary",
		},
		{
			name: "openrig example",
			members: []Member{
				{ID: "check.review", Runtime: "codex"},
				{ID: "build.lead", Runtime: "claude-code"},
				{ID: "check.qa", Runtime: "codex"},
				{ID: "build.impl", Runtime: "claude-code"},
			},
			want: "build.impl=claude-code,build.lead=claude-code,check.qa=codex,check.review=codex",
		},
		{
			name: "byte order",
			members: []Member{
				{ID: "b.x", Runtime: "pi"},
				{ID: "B.y", Runtime: "codex"},
				{ID: "a.z", Runtime: "claude-code"},
			},
			want: "B.y=codex,a.z=claude-code,b.x=pi",
		},
		{
			name: "prefix order",
			members: []Member{
				{ID: "a.b-c", Runtime: "pi"},
				{ID: "a.b", Runtime: "codex"},
			},
			want: "a.b=codex,a.b-c=pi",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ConfigurationID(tt.members)
			if err != nil {
				t.Fatalf("ConfigurationID: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ConfigurationID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestConfigurationIDIgnoresInputOrder(t *testing.T) {
	roster := []Member{
		{ID: "orch.lead", Runtime: "codex"},
		{ID: "dev.build", Runtime: "claude-code"},
		{ID: "dev.review", Runtime: "codex"},
		{ID: "dev.qa", Runtime: "claude-code"},
	}
	want := "dev.build=claude-code,dev.qa=claude-code,dev.review=codex,orch.lead=codex"

	got, err := ConfigurationID(roster)
	if err != nil {
		t.Fatalf("ConfigurationID: %v", err)
	}
	if got != want {
		t.Fatalf("ConfigurationID = %q, want %q", got, want)
	}

	for i, j := 0, len(roster)-1; i < j; i, j = i+1, j-1 {
		roster[i], roster[j] = roster[j], roster[i]
	}
	reversed, err := ConfigurationID(roster)
	if err != nil {
		t.Fatalf("ConfigurationID(reversed): %v", err)
	}
	if reversed != want {
		t.Fatalf("ConfigurationID(reversed) = %q, want %q", reversed, want)
	}
}

// TestConfigurationIDExcludesAlias covers the acceptance rule: a preset name
// sits beside the ID, never inside it.
func TestConfigurationIDExcludesAlias(t *testing.T) {
	got, err := ConfigurationID([]Member{
		{ID: "executor", Runtime: "subagent", Alias: "recommended"},
		{ID: "orchestrator", Runtime: "primary", Alias: "recommended"},
	})
	if err != nil {
		t.Fatalf("ConfigurationID: %v", err)
	}
	if got != "executor=subagent,orchestrator=primary" {
		t.Fatalf("ConfigurationID = %q, want ID without alias", got)
	}
	if strings.Contains(got, "recommended") {
		t.Fatalf("ConfigurationID %q contains alias", got)
	}
}

func TestConfigurationIDRejectsWhitespace(t *testing.T) {
	tests := []struct {
		name   string
		member Member
		field  string
	}{
		{"id space", Member{ID: "dev build", Runtime: "codex"}, "id"},
		{"runtime space", Member{ID: "dev.build", Runtime: "claude code"}, "runtime"},
		{"id tab", Member{ID: "dev\tbuild", Runtime: "codex"}, "id"},
		{"runtime newline", Member{ID: "dev.build", Runtime: "codex\n"}, "runtime"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConfigurationID([]Member{tt.member})
			if err == nil {
				t.Fatal("ConfigurationID accepted whitespace")
			}
			if !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("error %q does not name field %q", err, tt.field)
			}
		})
	}
}

func TestConfigurationIDRejectsInvalidMembers(t *testing.T) {
	tests := []struct {
		name    string
		members []Member
		want    string
	}{
		{"empty id", []Member{{ID: "", Runtime: "codex"}}, "id"},
		{"empty runtime", []Member{{ID: "dev.build", Runtime: ""}}, "runtime"},
		{"id separator", []Member{{ID: "dev,build", Runtime: "codex"}}, "id"},
		{"runtime separator", []Member{{ID: "dev.build", Runtime: "a=b"}}, "runtime"},
		{"duplicate id", []Member{{ID: "dev.build", Runtime: "codex"}, {ID: "dev.build", Runtime: "pi"}}, "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ConfigurationID(tt.members)
			if err == nil {
				t.Fatal("ConfigurationID accepted invalid members")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not report %q", err, tt.want)
			}
		})
	}
}

func TestConfigurationIDEmptyRoster(t *testing.T) {
	got, err := ConfigurationID(nil)
	if err != nil {
		t.Fatalf("ConfigurationID(nil): %v", err)
	}
	if got != "" {
		t.Fatalf("ConfigurationID(nil) = %q, want empty string", got)
	}
}
