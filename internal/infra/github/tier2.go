// Package github holds the GitHub adapter for Beacon.
//
// This file implements the tier-2 half of the GraphQL batch from ADR 0004 and
// T2.14. It reads statusCheckRollup.contexts(first:50) with inline fragments
// on CheckRun and StatusContext kept strictly inside contexts.nodes, follows
// pageInfo until the last page, and maps the nodes into domain.Check. A spread
// directly on StatusCheckRollup is rejected by GitHub with cannotSpreadFragment,
// so the query builder never emits one and the response parser reports that
// code as a malformed-query error.
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

	"github.com/refusedguy/beacon/internal/domain"
)

const (
	// Tier2PageSize is the connection page size from WORKER-PROMPT 4.6 and T0.2.
	Tier2PageSize = 50
	// maxTier2Pages bounds a runaway hasNextPage chain.
	maxTier2Pages = 100
	// codeCannotSpreadFragment is the GraphQL error code for a fragment spread
	// directly on StatusCheckRollup (spike T0.2, negative control).
	codeCannotSpreadFragment = "cannotSpreadFragment"
)

// ContextSuite pairs one parsed context with the id of its checkSuite. T2.15
// builds the suite digest from these pairs, so the association survives the
// mapping into domain.Check, which has no suite field.
type ContextSuite struct {
	Name    string
	SuiteID string
}

// Tier2PR is the parsed tier-2 data for one pull request.
type Tier2PR struct {
	Alias  string
	Owner  string
	Repo   string
	PR     int
	Checks map[string]domain.Check // keyed by CheckRun.name or StatusContext.context
	Suites []ContextSuite          // in node order across all pages
}

// Key identifies the watch this result belongs to.
func (p Tier2PR) Key() string {
	return p.Owner + "/" + p.Repo + "#" + strconv.Itoa(p.PR)
}

// Tier2Batch holds parsed results for all pull requests in a batch and the sum
// of rateLimit.cost over the first-page chunks and every continuation request.
type Tier2Batch struct {
	PRs  []Tier2PR
	Cost int
}

// FragmentSpreadError reports a malformed tier-2 query: GitHub refuses a
// fragment spread on StatusCheckRollup with code cannotSpreadFragment. It is a
// client bug, not a retriable transport error.
type FragmentSpreadError struct {
	Code    string
	Message string
}

func (e *FragmentSpreadError) Error() string {
	return "tier2 graphql " + e.Code + ": " + e.Message
}

// tier2Node is one element of contexts.nodes. Fields from both inline
// fragments live in the same struct because the JSON response is flat;
// __typename decides which subset is meaningful.
type tier2Node struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	DetailsURL string `json:"detailsUrl"`
	Context    string `json:"context"`
	State      string `json:"state"`
	TargetURL  string `json:"targetUrl"`
	IsRequired bool   `json:"isRequired"`
	CheckSuite struct {
		ID string `json:"id"`
	} `json:"checkSuite"`
}

type tier2PageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type tier2Contexts struct {
	Nodes    []tier2Node   `json:"nodes"`
	PageInfo tier2PageInfo `json:"pageInfo"`
}

type tier2Rollup struct {
	Contexts *tier2Contexts `json:"contexts"`
}

type tier2PullRequest struct {
	StatusCheckRollup *tier2Rollup `json:"statusCheckRollup"`
}

// tier2Body is one GraphQL response. It mirrors graphQLResponse so the tier-2
// parser can read errors and cost from a continuation response as well.
type tier2Body struct {
	Data      map[string]json.RawMessage `json:"data"`
	Errors    []GraphQLError             `json:"errors"`
	RateLimit rateLimitCost              `json:"rateLimit"`
}

// BuildTier2PageQuery renders a continuation query for one alias. It reuses
// tier2Fields from T2.11, which keeps both fragments inside contexts.nodes,
// and only injects the cursor into the same first:50 connection.
func BuildTier2PageQuery(a Alias, cursor string) string {
	fields := tier2Fields(a.PR)
	if cursor != "" {
		connection := "contexts(first:" + strconv.Itoa(Tier2PageSize) + ")"
		withCursor := "contexts(first:" + strconv.Itoa(Tier2PageSize) + ", after:" + strconv.Quote(cursor) + ")"
		fields = strings.Replace(fields, connection, withCursor, 1)
	}

	var b strings.Builder
	b.WriteString("query {\n")
	fmt.Fprintf(&b, "  %s: repository(owner:%s, name:%s) {\n", a.Name, strconv.Quote(a.Owner), strconv.Quote(a.Repo))
	fmt.Fprintf(&b, "    pullRequest(number:%d) {\n", a.PR)
	b.WriteString(fields)
	b.WriteString("    }\n  }\n}\n")
	return b.String()
}

// FetchTier2 runs the tier-2 batch for the active watches, follows pageInfo
// for every pull request and maps all nodes into domain checks. First-page
// chunks are sent sequentially, which stays within the two-request limit from
// T2.11 and keeps page order deterministic.
func (c *Client) FetchTier2(ctx context.Context, watches []domain.Watch) (*Tier2Batch, error) {
	aliases, err := withUniqueNames(AliasesFromWatches(watches))
	if err != nil {
		return nil, err
	}
	batch := &Tier2Batch{PRs: make([]Tier2PR, 0, len(aliases))}
	for _, chunk := range Chunk(aliases, Tier2) {
		body, err := c.postTier2Query(ctx, BuildQuery(Tier2, chunk))
		if err != nil {
			return batch, err
		}
		batch.Cost += body.RateLimit.Cost
		for _, a := range chunk {
			checks, suites, page, err := parseTier2Payload(body.Data[a.Name])
			if err != nil {
				return batch, fmt.Errorf("tier2 %s: %w", a.Name, err)
			}
			pages := 1
			for page.HasNextPage {
				if page.EndCursor == "" {
					return batch, fmt.Errorf("tier2 %s: hasNextPage without endCursor", a.Name)
				}
				if pages >= maxTier2Pages {
					return batch, fmt.Errorf("tier2 %s: exceeded %d pages", a.Name, maxTier2Pages)
				}
				next, err := c.postTier2Query(ctx, BuildTier2PageQuery(a, page.EndCursor))
				if err != nil {
					return batch, err
				}
				batch.Cost += next.RateLimit.Cost
				nextChecks, nextSuites, nextPage, err := parseTier2Payload(next.Data[a.Name])
				if err != nil {
					return batch, fmt.Errorf("tier2 %s next page: %w", a.Name, err)
				}
				for name, check := range nextChecks {
					checks[name] = check
				}
				suites = append(suites, nextSuites...)
				page = nextPage
				pages++
			}
			batch.PRs = append(batch.PRs, Tier2PR{
				Alias:  a.Name,
				Owner:  a.Owner,
				Repo:   a.Repo,
				PR:     a.PR,
				Checks: checks,
				Suites: suites,
			})
		}
	}
	return batch, nil
}

// postTier2Query sends one raw query through the GraphQL client's dedicated
// http.Client. It shares the counter, error normalizer and retry policy with
// T2.11, but accepts the cursor query that BuildQuery cannot express.
func (c *Client) postTier2Query(ctx context.Context, query string) (*tier2Body, error) {
	payload, err := json.Marshal(struct {
		Query string `json:"query"`
	}{Query: query})
	if err != nil {
		return nil, fmt.Errorf("tier2 query: %w", err)
	}

	attempts := c.maxAttempts()
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("tier2 request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("tier2 POST: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("tier2 body: %w", readErr)
		}

		parsed, parseErr := parseTier2Body(body)
		cost := 0
		if parsed != nil {
			cost = parsed.RateLimit.Cost
		}
		c.counterOr().Observe(resp, cost)
		verdict := c.normalizerOr().Normalize(resp.StatusCode, resp.Header, body)
		if verdict.Err != nil {
			return nil, verdict.Err
		}
		if verdict.Retry {
			if attempt < attempts {
				c.sleepOr()(verdict.Backoff)
				continue
			}
			return nil, fmt.Errorf("tier2: server error after %d attempts", attempt)
		}
		if parseErr != nil {
			return nil, parseErr
		}
		if err := tier2ResponseError(parsed); err != nil {
			return nil, err
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("tier2: no attempt made")
}

func parseTier2Body(body []byte) (*tier2Body, error) {
	var parsed tier2Body
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("tier2 decode: %w", err)
	}
	return &parsed, nil
}

func tier2ResponseError(parsed *tier2Body) error {
	if len(parsed.Errors) == 0 {
		return nil
	}
	first := parsed.Errors[0]
	if first.Extensions.Code == codeCannotSpreadFragment {
		return &FragmentSpreadError{Code: first.Extensions.Code, Message: first.Message}
	}
	code := first.Extensions.Code
	if code == "" {
		code = "UNKNOWN"
	}
	return fmt.Errorf("tier2 graphql %s: %s", code, first.Message)
}

// parseTier2Payload maps one alias's pullRequest payload into checks, suite
// pairs and connection state. A null rollup means the PR has no contexts.
func parseTier2Payload(raw json.RawMessage) (map[string]domain.Check, []ContextSuite, tier2PageInfo, error) {
	checks := make(map[string]domain.Check)
	var suites []ContextSuite
	if len(raw) == 0 || string(raw) == "null" {
		return checks, suites, tier2PageInfo{}, nil
	}

	var pr tier2PullRequest
	if err := json.Unmarshal(raw, &pr); err != nil {
		return nil, nil, tier2PageInfo{}, fmt.Errorf("tier2 payload: %w", err)
	}
	if pr.StatusCheckRollup == nil || pr.StatusCheckRollup.Contexts == nil {
		return checks, suites, tier2PageInfo{}, nil
	}
	contexts := pr.StatusCheckRollup.Contexts
	for _, node := range contexts.Nodes {
		name, check, ok := mapTier2Node(node)
		if !ok {
			continue
		}
		checks[name] = check
		suites = append(suites, ContextSuite{Name: name, SuiteID: node.CheckSuite.ID})
	}
	return checks, suites, contexts.PageInfo, nil
}

// mapTier2Node converts one inline fragment into a domain.Check. CheckRun
// contributes status/conclusion/detailsUrl, StatusContext contributes
// state/targetUrl; both carry the boolean from isRequired.
func mapTier2Node(node tier2Node) (string, domain.Check, bool) {
	switch node.Typename {
	case "CheckRun":
		if node.Name == "" {
			return "", domain.Check{}, false
		}
		return node.Name, domain.Check{
			State:      node.Status,
			Conclusion: node.Conclusion,
			Required:   node.IsRequired,
			DetailsURL: node.DetailsURL,
		}, true
	case "StatusContext":
		if node.Context == "" {
			return "", domain.Check{}, false
		}
		return node.Context, domain.Check{
			State:      node.State,
			Required:   node.IsRequired,
			DetailsURL: node.TargetURL,
		}, true
	default:
		return "", domain.Check{}, false
	}
}
