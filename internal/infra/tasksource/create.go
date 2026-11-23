package tasksource

import (
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/pkg/core"
)

var (
	_ core.TaskSource = (*Markdown)(nil)
	_ core.TaskSource = (*GitHubIssues)(nil)
)

type cardFields struct {
	ID       string   `yaml:"id,omitempty"`
	Title    string   `yaml:"title,omitempty"`
	Status   string   `yaml:"status,omitempty"`
	Scope    []string `yaml:"scope,omitempty"`
	Depends  []string `yaml:"depends,omitempty"`
	Priority int      `yaml:"priority,omitempty"`
}

func scopeValues(scopes []core.ScopePath) []string {
	out := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		value := string(scope)
		kind := byte('f')
		if len(value) > 1 && value[1] == ':' {
			kind, value = value[0], value[2:]
		}
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		if kind == 'd' && !strings.HasSuffix(value, "/") {
			value += "/"
		}
		out = append(out, value)
	}
	return out
}

func dependencyValues(ids []core.TaskID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if value := strings.TrimSpace(string(id)); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func marshalFields(fields cardFields) (string, error) {
	b, err := yaml.Marshal(&fields)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// issueBody embeds the parsed fields in a fenced YAML block. ParseTaskFields
// reads the same block for issues coming back from the remote API.
func issueBody(task core.Task) (string, error) {
	fields, err := marshalFields(cardFields{
		Scope:    scopeValues(task.Scope),
		Depends:  dependencyValues(task.DependsOn),
		Priority: task.Priority,
	})
	if err != nil {
		return "", err
	}
	var b strings.Builder
	if body := strings.TrimSpace(task.Body); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	b.WriteString("```yaml\n")
	b.WriteString(fields)
	b.WriteString("```\n")
	return b.String(), nil
}
