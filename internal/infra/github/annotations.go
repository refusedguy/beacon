// Package github holds the GitHub adapter for Beacon.
//
// This file reads failing check annotations through GraphQL. It is a second
// source for the MCP failure-excerpt port; the REST path in rest.go stays in
// place. The two paths share the annotation count and byte ceiling.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const annotationsQuery = `query FailureExcerpts($owner: String!, $repo: String!, $sha: GitObjectID!, $checkName: String!) {
  repository(owner: $owner, name: $repo) {
    object(oid: $sha) {
      ... on Commit {
        checkSuites(first: 20) {
          nodes {
            checkRuns(first: 50, filterBy: {checkName: $checkName, conclusions: [FAILURE, ERROR, TIMED_OUT, STARTUP_FAILURE, CANCELLED]}) {
              nodes {
                annotations(first: 8) {
                  nodes { path startLine title message }
                }
              }
            }
          }
        }
      }
    }
  }
}`

// GraphQLAnnotations fetches annotation text for one failed check. It has the
// same FailureExcerpt method as RestClient, so a composition root can inject
// either one into the MCP port without either infra package importing the
// other (ADR 0005).
type GraphQLAnnotations struct {
	token  string
	base   string
	client *http.Client
}

// NewGraphQLAnnotations builds a GraphQL source. An empty client uses a ten
// second timeout. An empty base uses the public GitHub GraphQL endpoint.
func NewGraphQLAnnotations(token, base string, client *http.Client) *GraphQLAnnotations {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	base = strings.TrimSpace(base)
	if base == "" {
		base = "https://api.github.com"
	}
	return &GraphQLAnnotations{
		token:  strings.TrimSpace(token),
		base:   strings.TrimRight(base, "/"),
		client: client,
	}
}

// FailureExcerpt returns raw annotation lines for a failed CheckRun on the
// Watch head SHA. An invalid repo, a missing run or an empty annotation list
// yields an empty string with a nil error. The MCP boundary redacts, escapes
// and bounds the text before it reaches a subscriber.
func (a *GraphQLAnnotations) FailureExcerpt(ctx context.Context, repo, headSHA, checkName string) (string, error) {
	if a == nil || a.client == nil || a.token == "" {
		return "", nil
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" || headSHA == "" || checkName == "" {
		return "", nil
	}
	body, err := json.Marshal(graphQLRequest{
		Query: annotationsQuery,
		Variables: map[string]string{
			"owner":     owner,
			"repo":      name,
			"sha":       headSHA,
			"checkName": checkName,
		},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/graphql", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("github graphql annotations: status %d", resp.StatusCode)
	}
	var payload graphQLAnnotationsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if len(payload.Errors) > 0 {
		return "", fmt.Errorf("github graphql annotations: %s", payload.Errors[0].Message)
	}
	return formatGraphQLAnnotations(payload.Data.Repository.Object.CheckSuites.Nodes), nil
}

type graphQLRequest struct {
	Query     string            `json:"query"`
	Variables map[string]string `json:"variables"`
}

type graphQLAnnotation struct {
	Path      string `json:"path"`
	StartLine int    `json:"startLine"`
	Title     string `json:"title"`
	Message   string `json:"message"`
}

type graphQLAnnotationsResponse struct {
	Data struct {
		Repository struct {
			Object struct {
				CheckSuites struct {
					Nodes []graphQLCheckSuite `json:"nodes"`
				} `json:"checkSuites"`
			} `json:"object"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

type graphQLCheckSuite struct {
	CheckRuns struct {
		Nodes []graphQLCheckRun `json:"nodes"`
	} `json:"checkRuns"`
}

type graphQLCheckRun struct {
	Annotations struct {
		Nodes []graphQLAnnotation `json:"nodes"`
	} `json:"annotations"`
}

// formatGraphQLAnnotations renders at most failureExcerptAnnotations lines as
// "path:line: title: message", followed by the marked source window when the
// annotated file is available. The byte check runs on every annotation so a
// large excerpt cannot push the result past the shared ceiling.
func formatGraphQLAnnotations(suites []graphQLCheckSuite) string {
	var b strings.Builder
	lines := 0
	for _, suite := range suites {
		for _, run := range suite.CheckRuns.Nodes {
			for _, annotation := range run.Annotations.Nodes {
				if lines >= failureExcerptAnnotations || b.Len() >= failureExcerptLimit {
					return clampExcerptBytes(b.String())
				}
				line := formatGraphQLAnnotation(annotation)
				if line == "" {
					continue
				}
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(line)
				if source := FormatAnnotationSource(strings.TrimSpace(annotation.Path), annotation.StartLine, ""); source != "" {
					b.WriteByte('\n')
					b.WriteString(source)
				}
				lines++
			}
		}
	}
	return clampExcerptBytes(b.String())
}

func formatGraphQLAnnotation(a graphQLAnnotation) string {
	message := strings.TrimSpace(a.Message)
	title := strings.TrimSpace(a.Title)
	if message == "" && title == "" {
		return ""
	}
	var b strings.Builder
	if p := strings.TrimSpace(a.Path); p != "" {
		b.WriteString(p)
		if a.StartLine > 0 {
			b.WriteByte(':')
			b.WriteString(strconv.Itoa(a.StartLine))
		}
		b.WriteString(": ")
	}
	if title != "" {
		b.WriteString(title)
		if message != "" {
			b.WriteString(": ")
		}
	}
	b.WriteString(message)
	return b.String()
}
