package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/app/prcreate"
	"github.com/refusedguy/beacon/internal/infra/github"
	"github.com/refusedguy/beacon/internal/infra/mcp"
	"github.com/refusedguy/beacon/internal/infra/wake"
)

// prcreateRunner holds the command so CLI dispatch stays a one-line case.
// RegisterPRCreate replaces it in tests, mirroring RegisterWatch.
var prcreateRunner = func(c *CLI, args []string) int { return runPRCreate(c, args) }

// RegisterPRCreate installs the pr command implementation.
func RegisterPRCreate(fn func(*CLI, []string) int) {
	prcreateRunner = fn
}

// sessionIDMetaKey is the opaque _meta key used by the existing watch_pr
// handler. A CLI caller has no MCP request, so the wiring builds one carrying
// the same key from --target or BEACON_PARENT_SESSION.
const sessionIDMetaKey = "ai.opencode/sessionID"

// parentSessionEnv is the same opaque session id spawn.go passes to a child.
const parentSessionEnv = "BEACON_PARENT_SESSION"

// prCreateReadFile is the file capability seam. Assigning os.ReadFile to a
// package variable keeps the composition root free of a direct os.ReadFile
// call, which the capabilities gate forbids (ADR 0005).
var prCreateReadFile = os.ReadFile

// localGitSource reads branch, SHA and origin slug with local git commands.
// It never calls a hosting-service CLI (T3.329).
type localGitSource struct {
	c *CLI
}

func (g localGitSource) git(args ...string) (string, error) {
	res := g.c.run("", "git", args, nil)
	if res.code != 0 {
		reason := strings.TrimSpace(string(res.stderr))
		if reason == "" {
			reason = strings.TrimSpace(string(res.stdout))
		}
		if reason == "" {
			reason = fmt.Sprintf("exit %d", res.code)
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), reason)
	}
	return strings.TrimSpace(string(res.stdout)), nil
}

func (g localGitSource) HeadRef(context.Context) (string, error) {
	return g.git("rev-parse", "--abbrev-ref", "HEAD")
}

func (g localGitSource) HeadSHA(context.Context) (string, error) {
	return g.git("rev-parse", "HEAD")
}

func (g localGitSource) RepoSlug(context.Context) (string, error) {
	remote, err := g.git("remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	return parseRepoSlug(remote)
}

// parseRepoSlug turns an origin URL into an owner/name slug. It runs the URL
// through github.CanonicalForComparison first, so HTTP credentials and host
// case do not change the slug, and then extracts the owner and name from the
// canonical form.
func parseRepoSlug(remote string) (string, error) {
	raw := strings.TrimSpace(remote)
	if raw == "" {
		return "", fmt.Errorf("git remote origin пустой")
	}
	path := github.CanonicalForComparison(raw)
	if i := strings.Index(path, "://"); i >= 0 {
		path = path[i+3:]
		if slash := strings.Index(path, "/"); slash >= 0 {
			path = path[slash+1:]
		}
	}
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("не удалось разобрать owner/name из %q", remote)
	}
	return parts[0] + "/" + parts[1], nil
}

// githubPRProvider is the GitHub implementation of prcreate.PRProvider in this
// wiring. It splits the neutral slug and delegates every operation to the
// existing infra/github client; no gh process is involved.
type githubPRProvider struct {
	client *github.PRClient
}

func (p githubPRProvider) Name() string { return github.PRProviderName }

func (p githubPRProvider) Create(ctx context.Context, in prcreate.CreateRequest) (prcreate.CreatedPR, error) {
	owner, repo, err := splitSlug(in.Repo)
	if err != nil {
		return prcreate.CreatedPR{}, err
	}
	created, err := p.client.Create(ctx, github.CreatePRInput{
		Owner: owner,
		Repo:  repo,
		Head:  in.HeadRef,
		Base:  in.BaseRef,
		Title: in.Title,
		Body:  in.Body,
		Draft: in.Draft,
	})
	if err != nil {
		return prcreate.CreatedPR{}, err
	}
	return prcreate.CreatedPR{
		Number:  created.Number,
		URL:     created.URL,
		HeadSHA: created.HeadSHA,
		BaseRef: created.BaseRef,
	}, nil
}

func (p githubPRProvider) PRState(ctx context.Context, repo string, number int) (prcreate.PullState, error) {
	owner, name, err := splitSlug(repo)
	if err != nil {
		return prcreate.PullState{}, err
	}
	state, err := p.client.PRState(ctx, owner, name, number)
	if err != nil {
		return prcreate.PullState{}, err
	}
	return prcreate.PullState{
		State:     state.State,
		HeadSHA:   state.HeadSHA,
		BaseRef:   state.BaseRef,
		Mergeable: state.Mergeable,
	}, nil
}

func (p githubPRProvider) ListComments(ctx context.Context, repo string, number int) ([]prcreate.Comment, error) {
	owner, name, err := splitSlug(repo)
	if err != nil {
		return nil, err
	}
	comments, err := p.client.ListComments(ctx, owner, name, number)
	if err != nil {
		return nil, err
	}
	out := make([]prcreate.Comment, 0, len(comments))
	for _, c := range comments {
		out = append(out, prcreate.Comment{ID: c.ID, Author: c.Author, Body: c.Body})
	}
	return out, nil
}

// Merge lands a pull request after checking its head SHA and the merge reply.
// The provider port keeps its single-error contract; the GitHub reason travels
// inside the error so the caller prints why the merge did not land.
func (p githubPRProvider) Merge(ctx context.Context, repo string, number int) error {
	owner, name, err := splitSlug(repo)
	if err != nil {
		return err
	}
	state, err := p.client.PRState(ctx, owner, name, number)
	if err != nil {
		return fmt.Errorf("github pr merge %s#%d: head SHA: %w", repo, number, err)
	}
	result, err := p.client.MergePR(ctx, owner, name, number, state.HeadSHA)
	if err != nil {
		if result.Reason != "" {
			return fmt.Errorf("github pr merge %s#%d: %s: %w", repo, number, result.Reason, err)
		}
		return fmt.Errorf("github pr merge %s#%d: %w", repo, number, err)
	}
	return nil
}

func splitSlug(repo string) (string, string, error) {
	owner, name, ok := strings.Cut(strings.TrimSpace(repo), "/")
	if !ok || owner == "" || name == "" {
		return "", "", fmt.Errorf("repo slug %q должен иметь вид owner/name", repo)
	}
	return owner, name, nil
}

// watchRegistrar registers through the existing beacon_watch_pr handler, which
// owns domain.Fanout. The command calls Register directly, so no MCP tool call
// or gh subprocess is made from outside this process.
type watchRegistrar struct {
	deps mcp.Dependencies
}

func (r watchRegistrar) Register(ctx context.Context, in prcreate.WatchInput) (prcreate.WatchResult, error) {
	if r.deps.Store == nil {
		return prcreate.WatchResult{}, fmt.Errorf("beacon pr create: store не подключён")
	}
	handler := mcp.NewWatchPRHandler(r.deps.Store, r.deps.CI, r.deps.Clock)
	req := &mcpsdk.CallToolRequest{Params: &mcpsdk.CallToolParamsRaw{
		Meta: mcpsdk.Meta{sessionIDMetaKey: in.SubscriberID},
	}}
	input := mcp.WatchPRInput{
		Repo:    in.Repo,
		PR:      in.PR,
		HeadSHA: in.HeadSHA,
		Notify: &mcp.WatchPRNotify{
			Adapter: in.Adapter,
			Target:  in.Target,
		},
	}
	// The role field is a domain.Role. String literals stay untyped here, so
	// cmd never has to import domain (ADR 0005).
	if in.Role == "orchestrator" {
		input.Role = "orchestrator"
	} else {
		input.Role = "executor"
	}
	out, err := handler.Register(ctx, req, input)
	if err != nil {
		return prcreate.WatchResult{}, err
	}
	return prcreate.WatchResult{
		WatchID:  out.WatchID,
		Revision: out.Revision,
		Status:   out.Status,
		Verdict:  out.Verdict,
	}, nil
}

// registryWaker reuses the shared wake adapter registry. The domain payload is
// built in infra/github so this file never names domain types.
type registryWaker struct {
	registry *wake.Registry
}

func (w registryWaker) Deliver(ctx context.Context, n prcreate.Notice) error {
	return github.DeliverWakePointer(ctx, w.registry, n.Adapter, n.SessionID, n.WatchID, n.Verdict, n.Revision)
}

// prcreateDeps is the composed service and the store closer.
type prcreateDeps struct {
	Service *prcreate.Service
	Close   func() error
}

type prcreateDepsFactory func(*CLI) (prcreateDeps, error)

var makePRCreateDeps prcreateDepsFactory = defaultPRCreateDeps

func defaultPRCreateDeps(c *CLI) (prcreateDeps, error) {
	client, err := github.NewPRClient(context.Background())
	if err != nil {
		return prcreateDeps{}, err
	}
	deps, closeStore, err := openMCPDependencies()
	if err != nil {
		return prcreateDeps{}, err
	}
	svc := prcreate.New(
		localGitSource{c: c},
		prcreate.FileCardSource{Dir: c.root, ReadFile: prCreateReadFile},
		watchRegistrar{deps: deps},
		registryWaker{registry: wake.NewRegistry()},
	)
	svc.WithProvider(githubPRProvider{client: client})
	return prcreateDeps{Service: svc, Close: closeStore}, nil
}

// runPRCreate implements `beacon pr create [flags]`.
func runPRCreate(c *CLI, args []string) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintln(c.errOut, "beacon pr create: ожидается подкоманда create")
		writeUsage(c.errOut)
		return 2
	}
	fs := flag.NewFlagSet("pr create", flag.ContinueOnError)
	title := fs.String("title", "", "заголовок PR; иначе берётся из --card")
	body := fs.String("body", "", "тело PR; иначе берётся из --card")
	cardID := fs.String("card", "", "карточка из docs/tasks, например T3.329")
	base := fs.String("base", "main", "ветка назначения")
	target := fs.String("target", "", "sessionID подписчика из _meta; по умолчанию BEACON_PARENT_SESSION")
	adapter := fs.String("adapter", wake.AdapterOpenCode, "имя wake-адаптера из реестра")
	provider := fs.String("provider", "", "провайдер хостинга; по умолчанию github")
	draft := fs.Bool("draft", false, "создать черновик PR")
	dryRun := fs.Bool("dry-run", false, "показать план без создания PR и Watch")
	format := fs.String("format", "table", "формат вывода: table или json")
	if !parseFlags("pr create", args[1:], c.errOut, fs) {
		return 2
	}
	if !parseFormatFlag(*format, "beacon pr create: неизвестный формат %q\n", c.errOut, false) {
		return 2
	}

	subscriber := strings.TrimSpace(*target)
	if subscriber == "" {
		subscriber = strings.TrimSpace(os.Getenv(parentSessionEnv))
	}

	deps, err := makePRCreateDeps(c)
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon pr create: %v\n", err)
		return 2
	}
	if deps.Close != nil {
		defer func() { _ = deps.Close() }()
	}

	result, err := deps.Service.Run(context.Background(), prcreate.Input{
		Title:    *title,
		Body:     *body,
		CardID:   *cardID,
		BaseRef:  *base,
		Target:   subscriber,
		Adapter:  *adapter,
		Provider: prcreate.ResolveProvider(*provider, c.root, os.Getenv, prCreateReadFile),
		Draft:    *draft,
		DryRun:   *dryRun,
	})
	if err != nil {
		fmt.Fprintf(c.errOut, "beacon pr create: %v\n", err)
		return 2
	}
	if *format == "json" {
		if encErr := renderPRCreateJSON(c.out, result); encErr != nil {
			fmt.Fprintf(c.errOut, "beacon pr create: %v\n", encErr)
			return 2
		}
	} else {
		renderPRCreateTable(c.out, result)
	}
	return 0
}

func renderPRCreateJSON(w io.Writer, result prcreate.Result) error {
	return json.NewEncoder(w).Encode(result)
}

func renderPRCreateTable(w io.Writer, result prcreate.Result) {
	if result.DryRun {
		fmt.Fprintln(w, "РЕЖИМ: просмотр без создания PR и Watch")
	}
	fmt.Fprintf(w, "PROVIDER: %s\n", result.Provider)
	fmt.Fprintf(w, "REPO: %s\n", result.Repo)
	fmt.Fprintf(w, "BRANCH: %s -> %s\n", result.HeadRef, result.BaseRef)
	fmt.Fprintf(w, "HEAD: %s\n", result.HeadSHA)
	fmt.Fprintf(w, "TITLE: %s\n", result.Title)
	if result.URL != "" {
		fmt.Fprintf(w, "PR: #%d %s\n", result.PR, result.URL)
	}
	if result.WatchID != "" {
		fmt.Fprintf(w, "WATCH: %s revision=%d status=%s verdict=%s\n",
			result.WatchID, result.Revision, result.WatchStatus, result.WatchVerdict)
	}
	if result.WakeError != "" {
		fmt.Fprintf(w, "WAKE ERROR: %s\n", result.WakeError)
	}
}
