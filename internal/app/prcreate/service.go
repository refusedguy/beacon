package prcreate

import (
	"context"
	"fmt"
	"strings"
)

// RoleExecutor is the Watch role this command registers. It is an untyped
// constant so the cmd adapter can pass it straight to the domain role without
// this package importing domain (ADR 0005).
const RoleExecutor = "executor"

// Port errors use stable codes so cmd can render them without string matching.
type ErrorCode string

const (
	CodeInvalidInput     ErrorCode = "INVALID_INPUT"
	CodeProviderNotBound ErrorCode = "PROVIDER_NOT_BOUND"
	CodeGitFailed        ErrorCode = "GIT_FAILED"
	CodeCardFailed       ErrorCode = "CARD_FAILED"
	CodeCreateFailed     ErrorCode = "CREATE_FAILED"
	CodeWatchFailed      ErrorCode = "WATCH_FAILED"
)

// Error is one workflow failure with a stable code.
type Error struct {
	Code    ErrorCode
	Message string
}

func (e *Error) Error() string { return "prcreate: " + e.Message }

func fail(code ErrorCode, format string, args ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// GitSource reads local git facts. It is local by construction: no network and
// no hosting-service process is involved (T3.329).
type GitSource interface {
	HeadRef(ctx context.Context) (string, error)
	HeadSHA(ctx context.Context) (string, error)
	RepoSlug(ctx context.Context) (string, error)
}

// Card is a task card resolved into PR text.
type Card struct {
	ID    string
	Title string
	Body  string
}

// CardSource resolves a card id such as T3.329 into title and body.
type CardSource interface {
	Card(ctx context.Context, id string) (Card, error)
}

// WatchInput is the neutral registration request. SubscriberID is the opaque
// session id from _meta or --target; Adapter names a wake adapter in the shared
// registry.
type WatchInput struct {
	Repo         string
	PR           int
	HeadSHA      string
	Role         string
	SubscriberID string
	Adapter      string
	Target       string
}

// WatchResult is the neutral registration result.
type WatchResult struct {
	WatchID  string
	Revision uint64
	Status   string
	Verdict  string
}

// Watcher registers the Watch through domain.Fanout. cmd adapts the existing
// beacon_watch_pr handler here so this package never names domain types.
type Watcher interface {
	Register(ctx context.Context, in WatchInput) (WatchResult, error)
}

// Notice is an identifier-only pointer handed to the wake registry after the
// Watch exists. No PR text crosses this boundary.
type Notice struct {
	Adapter   string
	SessionID string
	WatchID   string
	Revision  uint64
	Verdict   string
}

// Waker delivers one Notice through the shared adapter registry.
type Waker interface {
	Deliver(ctx context.Context, n Notice) error
}

// Input configures one create run. Empty values are filled from git, the card
// source and the default base ref where possible.
type Input struct {
	Repo    string
	HeadRef string
	BaseRef string
	HeadSHA string
	Title   string
	Body    string
	CardID  string
	Target  string
	Adapter string
	// Provider is the resolved provider name. An empty value uses the
	// registered default.
	Provider string
	Draft    bool
	DryRun   bool
}

// Result is the observable outcome of one create run.
type Result struct {
	Provider     string `json:"provider"`
	Repo         string `json:"repo"`
	HeadRef      string `json:"headRef"`
	HeadSHA      string `json:"headSHA"`
	BaseRef      string `json:"baseRef"`
	Title        string `json:"title"`
	Body         string `json:"body"`
	PR           int    `json:"pr,omitempty"`
	URL          string `json:"url,omitempty"`
	WatchID      string `json:"watchID,omitempty"`
	WatchStatus  string `json:"watchStatus,omitempty"`
	WatchVerdict string `json:"watchVerdict,omitempty"`
	Revision     uint64 `json:"revision,omitempty"`
	DryRun       bool   `json:"dryRun"`
	WakeError    string `json:"wakeError,omitempty"`
}

// Service composes the four ports and a set of named PR providers. A Service
// can be built once and reused for consecutive commands.
type Service struct {
	git       GitSource
	cards     CardSource
	watcher   Watcher
	waker     Waker
	providers map[string]PRProvider
	fallback  string
	diff      DiffSource
	models    []Model
}

// New builds a Service. A nil git or card source is allowed when the caller
// always supplies the corresponding fields in Input; a nil watcher or waker is
// reported when it is needed.
func New(git GitSource, cards CardSource, watcher Watcher, waker Waker) *Service {
	return &Service{
		git:       git,
		cards:     cards,
		watcher:   watcher,
		waker:     waker,
		providers: make(map[string]PRProvider),
		fallback:  DefaultProvider,
	}
}

// WithProvider registers one implementation under its own Name(). The first
// provider also becomes the default; later calls do not change the default.
func (s *Service) WithProvider(p PRProvider) *Service {
	if s == nil || p == nil || strings.TrimSpace(p.Name()) == "" {
		return s
	}
	if s.providers == nil {
		s.providers = make(map[string]PRProvider)
	}
	if _, exists := s.providers[p.Name()]; !exists {
		s.providers[p.Name()] = p
		if len(s.providers) == 1 {
			s.fallback = p.Name()
		}
	}
	return s
}

// WithDefault sets the provider used for an empty Input.Provider.
func (s *Service) WithDefault(name string) *Service {
	if s != nil && strings.TrimSpace(name) != "" {
		s.fallback = strings.TrimSpace(name)
	}
	return s
}

// WithDiffSource binds the local diff source used to describe PRs. Without it
// the service keeps resolving text from the card only.
func (s *Service) WithDiffSource(d DiffSource) *Service {
	if s != nil {
		s.diff = d
	}
	return s
}

// WithModels sets the ordered model chain. The first model whose window fits
// the prompt and that answers successfully wins; the card stays the fallback.
func (s *Service) WithModels(models ...Model) *Service {
	if s != nil {
		s.models = append([]Model(nil), models...)
	}
	return s
}

// resolveGeneratedText tries the model chain over the local diff and fills the
// still-empty PR fields. It returns without changes when generation is not
// wired, the diff is empty, or every model was skipped or failed, so the caller
// keeps the card as fallback. Explicit flags are never overwritten.
func (s *Service) resolveGeneratedText(ctx context.Context, out *Result, card Card) {
	if s.diff == nil || len(s.models) == 0 {
		return
	}
	d, err := s.diff.Diff(ctx, out.BaseRef, out.HeadRef)
	if err != nil || strings.TrimSpace(d.Patch) == "" {
		return
	}
	text, ok := GenerateFromDiff(ctx, DiffPrompt(d, card), s.models)
	if !ok {
		return
	}
	if out.Title == "" {
		out.Title = text.Title
	}
	if out.Body == "" {
		out.Body = text.Body
	}
}

// Run performs one create-and-subscribe pass. DryRun stops after all inputs
// are resolved, before any provider, Watch or wake side effect.
func (s *Service) Run(ctx context.Context, in Input) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	out := Result{DryRun: in.DryRun, BaseRef: strings.TrimSpace(in.BaseRef)}
	if out.BaseRef == "" {
		out.BaseRef = "main"
	}

	name := strings.TrimSpace(in.Provider)
	if name == "" {
		name = s.fallback
	}
	if name == "" {
		name = DefaultProvider
	}
	out.Provider = name
	provider, ok := s.providers[name]
	if !ok || provider == nil {
		return out, fmt.Errorf("%w: %s", ErrProviderNotRegistered, name)
	}

	if err := s.resolveGit(ctx, &in, &out); err != nil {
		return out, err
	}
	if err := s.resolveText(ctx, &in, &out); err != nil {
		return out, err
	}

	if in.DryRun {
		return out, nil
	}
	if in.Target == "" {
		return out, fail(CodeInvalidInput, "пустой target: нужен sessionID из _meta или --target")
	}

	created, err := provider.Create(ctx, CreateRequest{
		Repo:    out.Repo,
		HeadRef: out.HeadRef,
		BaseRef: out.BaseRef,
		Title:   out.Title,
		Body:    out.Body,
		HeadSHA: out.HeadSHA,
		Draft:   in.Draft,
	})
	if err != nil {
		return out, fail(CodeCreateFailed, "%s: %v", name, err)
	}
	if created.Number <= 0 {
		return out, fail(CodeCreateFailed, "%s вернул некорректный номер PR: %d", name, created.Number)
	}
	if created.HeadSHA == "" {
		created.HeadSHA = out.HeadSHA
	}
	if created.BaseRef == "" {
		created.BaseRef = out.BaseRef
	}
	out.PR = created.Number
	out.URL = created.URL
	out.HeadSHA = created.HeadSHA
	out.BaseRef = created.BaseRef

	if s.watcher == nil {
		return out, fail(CodeWatchFailed, "watcher не подключён")
	}
	watch, err := s.watcher.Register(ctx, WatchInput{
		Repo:         out.Repo,
		PR:           created.Number,
		HeadSHA:      created.HeadSHA,
		Role:         RoleExecutor,
		SubscriberID: in.Target,
		Adapter:      in.Adapter,
		Target:       in.Target,
	})
	if err != nil {
		return out, fail(CodeWatchFailed, "%v", err)
	}
	if watch.WatchID == "" {
		return out, fail(CodeWatchFailed, "регистрация вернула пустой watchID")
	}
	out.WatchID = watch.WatchID
	out.WatchStatus = watch.Status
	out.WatchVerdict = watch.Verdict
	out.Revision = watch.Revision

	// Wake is best effort: the Watch is already durable and the command's
	// primary result is watchID plus the PR URL. A failure is reported, not
	// hidden, and never rolls back the PR.
	if s.waker != nil {
		if err := s.waker.Deliver(ctx, Notice{
			Adapter:   in.Adapter,
			SessionID: in.Target,
			WatchID:   watch.WatchID,
			Revision:  watch.Revision,
			Verdict:   watch.Verdict,
		}); err != nil {
			out.WakeError = err.Error()
		}
	}
	return out, nil
}

func (s *Service) resolveGit(ctx context.Context, in *Input, out *Result) error {
	out.Repo = strings.TrimSpace(in.Repo)
	out.HeadRef = strings.TrimSpace(in.HeadRef)
	out.HeadSHA = strings.TrimSpace(in.HeadSHA)
	if s.git == nil {
		if out.Repo == "" || out.HeadRef == "" || out.HeadSHA == "" {
			return fail(CodeGitFailed, "git-источник не подключён, а repo/headRef/headSHA не заданы")
		}
		return nil
	}
	if out.Repo == "" {
		repo, err := s.git.RepoSlug(ctx)
		if err != nil {
			return fail(CodeGitFailed, "repo из git remote: %v", err)
		}
		out.Repo = strings.TrimSpace(repo)
	}
	if out.HeadRef == "" {
		ref, err := s.git.HeadRef(ctx)
		if err != nil {
			return fail(CodeGitFailed, "HEAD branch: %v", err)
		}
		out.HeadRef = strings.TrimSpace(ref)
	}
	if out.HeadSHA == "" {
		sha, err := s.git.HeadSHA(ctx)
		if err != nil {
			return fail(CodeGitFailed, "HEAD SHA: %v", err)
		}
		out.HeadSHA = strings.TrimSpace(sha)
	}
	if out.Repo == "" {
		return fail(CodeInvalidInput, "repo slug пустой: проверьте git remote origin")
	}
	if !strings.Contains(out.Repo, "/") {
		return fail(CodeInvalidInput, "repo slug %q должен иметь вид owner/name", out.Repo)
	}
	if out.HeadRef == "" {
		return fail(CodeInvalidInput, "HEAD branch пустая")
	}
	if out.HeadSHA == "" {
		return fail(CodeInvalidInput, "HEAD SHA пустой")
	}
	return nil
}

func (s *Service) resolveText(ctx context.Context, in *Input, out *Result) error {
	out.Title = strings.TrimSpace(in.Title)
	out.Body = strings.TrimSpace(in.Body)
	if out.Title != "" && out.Body != "" {
		return nil
	}

	id := strings.TrimSpace(in.CardID)
	var card Card
	var cardErr error
	haveCard := false
	if id != "" && s.cards != nil {
		card, cardErr = s.cards.Card(ctx, id)
		haveCard = cardErr == nil
	}

	// The diff describes the actual change, so it is tried before the card.
	s.resolveGeneratedText(ctx, out, card)
	if out.Title != "" && out.Body != "" {
		return nil
	}

	if id == "" {
		if out.Title == "" {
			return fail(CodeInvalidInput, "нужен --title или --card")
		}
		return nil
	}
	if s.cards == nil {
		return fail(CodeCardFailed, "источник карточек не подключён")
	}
	if !haveCard {
		return fail(CodeCardFailed, "карточка %s: %v", id, cardErr)
	}
	if out.Title == "" {
		out.Title = strings.TrimSpace(card.Title)
	}
	if out.Body == "" {
		out.Body = strings.TrimSpace(card.Body)
	}
	if out.Title == "" {
		return fail(CodeCardFailed, "карточка %s не содержит заголовок", id)
	}
	return nil
}
