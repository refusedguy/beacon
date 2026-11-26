package tasksource

import (
	"strconv"
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

const claimedLabel = "beacon/claimed"

// issueRecord is the provider-neutral view of a remote issue. Each provider
// adapts its own issue type to this shape before parsing.
type issueRecord struct {
	Number      int
	Title       string
	Body        string
	Labels      []string
	SourceRef   string
	PullRequest bool
}

// hasIssueLabel reports whether labels contains want, ignoring case.
func hasIssueLabel(labels []string, want string) bool {
	for _, label := range labels {
		if strings.EqualFold(label, want) {
			return true
		}
	}
	return false
}

// issueAvailable reports whether an issue can be claimed. GitHub lists pull
// requests as issues, so those are filtered here with the claimed label.
func issueAvailable(rec issueRecord) bool {
	return !rec.PullRequest && !hasIssueLabel(rec.Labels, claimedLabel)
}

// parseIssueRecord builds a task with one set of field rules for every
// provider. A body id wins over the provider number, so the same body yields
// the same id under GitHub and GitLab.
func parseIssueRecord(rec issueRecord, prefix string) core.Task {
	fields := ParseTaskFields(rec.Body, "", prefix)
	return core.Task{
		ID:        core.TaskID(core.FirstNonEmpty(strings.TrimSpace(fields.ID), prefix+"-"+strconv.Itoa(rec.Number))),
		Title:     rec.Title,
		Body:      rec.Body,
		Status:    core.TaskAvailable,
		Priority:  fields.Priority,
		Scope:     fields.Scopes(""),
		DependsOn: fields.Dependencies(),
		SourceRef: rec.SourceRef,
	}
}
