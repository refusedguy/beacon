// Package github holds the GitHub adapter for Beacon.
//
// This file implements the GraphQL half of the hybrid client from ADR 0004.
// It owns a dedicated http.Client, builds one unique alias per active Watch,
// splits aliases into measured chunks, and reads rateLimit.cost from every
// response into the shared T2.12 counter. At most two requests are in flight.
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// DefaultEndpoint is the GitHub GraphQL endpoint.
	DefaultEndpoint = "https://api.github.com/graphql"

	// Tier1ChunkSize is the measured safe chunk: 100 state-only aliases cost
	// one point, while 150 cost two (WORKER-PROMPT 4.6, report D 1.3, T0.2).
	ChunkSizeTier1 = 100
	// Tier2ChunkSize leaves headroom before the contexts jump at about 75
	// aliases and the measured jump at 80 (WORKER-PROMPT 4.6, T0.2).
	ChunkSizeTier2 = 50

	// ChunkSizeBatch is the safe chunk for the combined batch: PR aliases carry
	// contexts(first:50), so the tier-2 cap applies to base aliases as well.
	ChunkSizeBatch = 50

	// MaxParallel is the cap on concurrent requests to GitHub.
	MaxParallel = 2

	defaultAttempts = 3
)

// Tier selects the field set and therefore the safe chunk size.
type Tier int

const (
	// Tier1 reads state, merge fields and the nested rollup state.
	Tier1 Tier = 1
	// Tier2 reads statusCheckRollup contexts(first:50).
	Tier2 Tier = 2
	// TierBatch reads PR state, PR contexts and base-ref contexts in one
	// document. It is the tier used by BatchProvider.FetchBatch.
	TierBatch Tier = 3
)

// Alias is one repository binding inside a GraphQL batch. Name is the unique
// GraphQL alias, for example "p0". A PR alias sets PR; a base alias sets
// BaseRef instead and reads ref(qualifiedName) on the same repository.
type Alias struct {
	Name    string
	Owner   string
	Repo    string
	PR      int
	BaseRef string
}

// IsBase reports whether the alias reads a base branch instead of a PR.
func (a Alias) IsBase() bool { return a.BaseRef != "" }

// AliasesFromWatches builds deduplicated, uniquely named aliases from active
// watches. Closed watches are skipped. Two watches for the same PR collapse
// into one alias; names are assigned by position, so they cannot repeat.
func AliasesFromWatches(watches []domain.Watch) []Alias {
	aliases := make([]Alias, 0, len(watches))
	seen := make(map[string]bool, len(watches))
	for _, w := range watches {
		if w.Status == domain.StatusClosed {
			continue
		}
		owner, repo := splitRepo(w.Repo)
		key := owner + "/" + repo + "#" + strconv.Itoa(w.PR)
		if seen[key] {
			continue
		}
		seen[key] = true
		aliases = append(aliases, Alias{
			Name:  "p" + strconv.Itoa(len(aliases)),
			Owner: owner,
			Repo:  repo,
			PR:    w.PR,
		})
	}
	return aliases
}

// ChunkSize returns the measured chunk size for a tier. Unknown tiers use the
// conservative tier-1 size.
func ChunkSize(tier Tier) int {
	if tier == Tier2 || tier == TierBatch {
		return ChunkSizeTier2
	}
	return ChunkSizeTier1
}

// Chunk splits aliases into consecutive chunks that fit the tier's limit.
// It returns nil for no aliases and preserves input order.
func Chunk(aliases []Alias, tier Tier) [][]Alias {
	size := ChunkSize(tier)
	if len(aliases) == 0 {
		return nil
	}
	chunks := make([][]Alias, 0, (len(aliases)+size-1)/size)
	for start := 0; start < len(aliases); start += size {
		end := start + size
		if end > len(aliases) {
			end = len(aliases)
		}
		chunks = append(chunks, aliases[start:end])
	}
	return chunks
}

// BuildQuery renders one GraphQL document for a chunk of aliases. A TierBatch
// document may mix PR aliases and base-ref aliases, so a broken main can be
// attributed in the same round trip as the PRs it blocks.
func BuildQuery(tier Tier, aliases []Alias) string {
	var b strings.Builder
	b.WriteString("query {\n")
	for _, a := range aliases {
		fmt.Fprintf(&b, "  %s: repository(owner:%s, name:%s) {\n", a.Name, strconv.Quote(a.Owner), strconv.Quote(a.Repo))
		if tier == TierBatch && a.IsBase() {
			b.WriteString(baseRefFields(a.BaseRef))
		} else {
			fmt.Fprintf(&b, "    pullRequest(number:%d) {\n", a.PR)
			switch tier {
			case Tier2:
				b.WriteString(tier2Fields(a.PR))
			case TierBatch:
				b.WriteString(tier1Fields)
				b.WriteString(tier2Fields(a.PR))
			default:
				b.WriteString(tier1Fields)
			}
			b.WriteString("    }\n")
		}
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

const tier1Fields = `      state
      isDraft
      headRefOid
      baseRefName
      mergeable
      mergeStateStatus
      reviewDecision
      commits(last:1) {
        nodes {
          commit {
            statusCheckRollup {
              state
            }
          }
        }
      }
`

// tier2Fields keeps fragments strictly inside contexts.nodes. A fragment
// spread directly on StatusCheckRollup returns cannotSpreadFragment.
func tier2Fields(pr int) string {
	required := "isRequired(pullRequestNumber:" + strconv.Itoa(pr) + ")"
	return `      statusCheckRollup {
        contexts(first:50) {
          nodes {
            __typename
            ... on CheckRun {
              name
              status
              conclusion
              detailsUrl
              ` + required + `
              checkSuite {
                id
              }
            }
            ... on StatusContext {
              context
              state
              targetUrl
              ` + required + `
            }
          }
          pageInfo {
            hasNextPage
            endCursor
          }
        }
      }
`
}

// baseRefFields reads the rollup contexts of a branch tip. The fragments stay
// inside contexts.nodes, as in tier2Fields, and there is no isRequired field
// because a base branch is not attached to a pull request. The oid identifies
// which main commit produced a failure, so a later green run can clear it.
func baseRefFields(baseRef string) string {
	return `    ref(qualifiedName:` + strconv.Quote("refs/heads/"+baseRef) + `) {
      target {
        ... on Commit {
          oid
          statusCheckRollup {
            contexts(first:50) {
              nodes {
                __typename
                ... on CheckRun {
                  name
                  status
                  conclusion
                  detailsUrl
                  checkSuite {
                    id
                  }
                }
                ... on StatusContext {
                  context
                  state
                  targetUrl
                }
              }
              pageInfo {
                hasNextPage
                endCursor
              }
            }
          }
        }
      }
    }
`
}

// GraphQLError is one error entry in a GraphQL response body.
type GraphQLError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
	} `json:"extensions"`
}

type rateLimitCost struct {
	Cost int `json:"cost"`
}

type graphQLResponse struct {
	Data      map[string]json.RawMessage `json:"data"`
	Errors    []GraphQLError             `json:"errors"`
	RateLimit rateLimitCost              `json:"rateLimit"`
}

// BatchResult merges the data of all chunks. Data is keyed by alias name.
type BatchResult struct {
	Data      map[string]json.RawMessage
	TotalCost int
}

// Client is the GraphQL client. It holds its own http.Client and never
// shares the REST client's transport, ETag cache or rate-limit headers.
type Client struct {
	endpoint    string
	token       string
	http        *http.Client
	counter     *Counter
	normalizer  *Normalizer
	attempts    int
	sleep       func(time.Duration)
	sem         chan struct{}
	semOnce     sync.Once
	counterOnce sync.Once
}

// NewClient builds a GraphQL client on a dedicated http.Client. When client is
// nil a fresh one is created; http.DefaultClient is never used. The counter
// may be nil, in which case costs are only reported in BatchResult.
func NewClient(endpoint, token string, client *http.Client, counter *Counter) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	c := &Client{
		endpoint:   endpoint,
		http:       client,
		counter:    counter,
		normalizer: NewNormalizer(),
		attempts:   defaultAttempts,
		sleep:      time.Sleep,
		sem:        make(chan struct{}, MaxParallel),
	}
	c.token = token
	return c
}

// HTTPClient returns the client used for GraphQL requests. It is exposed so
// callers can verify the GraphQL client is separate from the REST client.
func (c *Client) HTTPClient() *http.Client { return c.http }

// Do builds and sends all chunks for a tier. It returns merged data keyed by
// alias and the sum of rateLimit.cost over all responses. At most MaxParallel
// requests are in flight, including across concurrent Do calls.
func (c *Client) Do(ctx context.Context, tier Tier, aliases []Alias) (*BatchResult, error) {
	aliases, err := withUniqueNames(aliases)
	if err != nil {
		return nil, err
	}
	chunks := Chunk(aliases, tier)
	result := &BatchResult{Data: make(map[string]json.RawMessage)}
	if len(chunks) == 0 {
		return result, nil
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	errs := &observe.ErrorCollector{}
	for _, chunk := range chunks {
		wg.Add(1)
		go func(chunk []Alias) {
			defer wg.Done()
			select {
			case c.semaphore() <- struct{}{}:
				defer func() { <-c.semaphore() }()
			case <-ctx.Done():
				errs.Add(ctx.Err())
				return
			}
			data, cost, err := c.doChunk(ctx, tier, chunk)
			mu.Lock()
			defer mu.Unlock()
			result.TotalCost += cost
			if err != nil {
				errs.Add(err)
				return
			}
			for name, raw := range data {
				result.Data[name] = raw
			}
		}(chunk)
	}
	wg.Wait()
	if err := errs.Error(); err != nil {
		return result, err
	}
	return result, nil
}

// QueryTier1 runs the tier-1 state batch over the active watches.
func (c *Client) QueryTier1(ctx context.Context, watches []domain.Watch) (*BatchResult, error) {
	return c.Do(ctx, Tier1, AliasesFromWatches(watches))
}

// QueryTier2 runs the tier-2 contexts batch over the active watches.
func (c *Client) QueryTier2(ctx context.Context, watches []domain.Watch) (*BatchResult, error) {
	return c.Do(ctx, Tier2, AliasesFromWatches(watches))
}

func (c *Client) doChunk(ctx context.Context, tier Tier, chunk []Alias) (map[string]json.RawMessage, int, error) {
	payload, err := json.Marshal(struct {
		Query string `json:"query"`
	}{Query: BuildQuery(tier, chunk)})
	if err != nil {
		return nil, 0, fmt.Errorf("graphql query: %w", err)
	}

	attempts := c.maxAttempts()
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
		if err != nil {
			return nil, 0, fmt.Errorf("graphql request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}

		resp, err := c.http.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("graphql POST: %w", err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, 0, fmt.Errorf("graphql body: %w", readErr)
		}

		var parsed graphQLResponse
		parseErr := json.Unmarshal(body, &parsed)
		c.counterOr().Observe(resp, parsed.RateLimit.Cost)
		verdict := c.normalizerOr().Normalize(resp.StatusCode, resp.Header, body)
		if verdict.Err != nil {
			return nil, parsed.RateLimit.Cost, verdict.Err
		}
		if verdict.Retry {
			if attempt < attempts {
				c.sleepOr()(verdict.Backoff)
				continue
			}
			return nil, parsed.RateLimit.Cost, fmt.Errorf("graphql: server error after %d attempts", attempt)
		}
		if parseErr != nil {
			return nil, parsed.RateLimit.Cost, fmt.Errorf("graphql decode: %w", parseErr)
		}
		if len(parsed.Errors) > 0 {
			return nil, parsed.RateLimit.Cost, fmt.Errorf("graphql: %s", parsed.Errors[0].Message)
		}
		return parsed.Data, parsed.RateLimit.Cost, nil
	}
	return nil, 0, errors.New("graphql: no attempt made")
}

func (c *Client) semaphore() chan struct{} {
	c.semOnce.Do(func() {
		if c.sem == nil {
			c.sem = make(chan struct{}, MaxParallel)
		}
	})
	return c.sem
}

func (c *Client) counterOr() *Counter {
	c.counterOnce.Do(func() {
		if c.counter == nil {
			c.counter = &Counter{now: time.Now}
		}
	})
	return c.counter
}

func (c *Client) normalizerOr() *Normalizer {
	if c.normalizer == nil {
		return NewNormalizer()
	}
	return c.normalizer
}

func (c *Client) maxAttempts() int {
	if c.attempts < 1 {
		return 1
	}
	return c.attempts
}

func (c *Client) sleepOr() func(time.Duration) {
	if c.sleep == nil {
		return time.Sleep
	}
	return c.sleep
}

// unwrapPullRequestAlias returns the pullRequest object carried by one
// repository alias in a TierBatch response. The query binds the alias on
// repository, so a real reply is data.p0 = {"pullRequest": {...}}. Reading
// fields such as state or headRefOid straight off the alias yields zero values,
// which quietly turns a merged PR into a watching verdict. Payloads that are
// already a bare pullRequest object, as in older unit fixtures, pass through
// unchanged.
func unwrapPullRequestAlias(raw json.RawMessage) json.RawMessage {
	var envelope struct {
		PullRequest json.RawMessage `json:"pullRequest"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return raw
	}
	if len(envelope.PullRequest) == 0 || string(envelope.PullRequest) == "null" {
		return raw
	}
	return envelope.PullRequest
}

// withUniqueNames fills missing alias names and rejects explicit duplicates.
func withUniqueNames(aliases []Alias) ([]Alias, error) {
	out := make([]Alias, len(aliases))
	seen := make(map[string]bool, len(aliases))
	next := 0
	for i, a := range aliases {
		out[i] = a
		name := a.Name
		if name == "" {
			for {
				candidate := "p" + strconv.Itoa(next)
				next++
				if !seen[candidate] {
					name = candidate
					break
				}
			}
			out[i].Name = name
		}
		if seen[name] {
			return nil, fmt.Errorf("graphql: duplicate alias %q", name)
		}
		seen[name] = true
	}
	return out, nil
}

func splitRepo(repo string) (string, string) {
	owner, name, found := strings.Cut(repo, "/")
	if !found {
		return "", repo
	}
	return owner, name
}
