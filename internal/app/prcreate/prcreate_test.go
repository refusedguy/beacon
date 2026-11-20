package prcreate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeProvider struct {
	name          string
	createCalls   int
	lastCreate    CreateRequest
	created       CreatedPR
	stateCalls    int
	commentsCalls int
	mergeCalls    int
	err           error
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Create(_ context.Context, in CreateRequest) (CreatedPR, error) {
	f.createCalls++
	f.lastCreate = in
	return f.created, f.err
}

func (f *fakeProvider) PRState(_ context.Context, repo string, number int) (PullState, error) {
	f.stateCalls++
	return PullState{State: "open", HeadSHA: "abc", BaseRef: "main"}, nil
}

func (f *fakeProvider) ListComments(_ context.Context, repo string, number int) ([]Comment, error) {
	f.commentsCalls++
	return []Comment{{ID: 1, Author: "bot", Body: "hi"}}, nil
}

func (f *fakeProvider) Merge(_ context.Context, repo string, number int) error {
	f.mergeCalls++
	return nil
}

type fakeGit struct {
	repo, ref, sha string
	err            error
}

func (g fakeGit) HeadRef(context.Context) (string, error)  { return g.ref, g.err }
func (g fakeGit) HeadSHA(context.Context) (string, error)  { return g.sha, g.err }
func (g fakeGit) RepoSlug(context.Context) (string, error) { return g.repo, g.err }

type fakeCards struct {
	card Card
	err  error
}

func (c fakeCards) Card(_ context.Context, id string) (Card, error) {
	out := c.card
	out.ID = id
	return out, c.err
}

type fakeWatcher struct {
	calls int
	last  WatchInput
	out   WatchResult
	err   error
}

func (w *fakeWatcher) Register(_ context.Context, in WatchInput) (WatchResult, error) {
	w.calls++
	w.last = in
	return w.out, w.err
}

type fakeWaker struct {
	calls int
	last  Notice
	err   error
}

func (w *fakeWaker) Deliver(_ context.Context, n Notice) error {
	w.calls++
	w.last = n
	return w.err
}

func newTestService(t *testing.T) (*Service, *fakeProvider, *fakeWatcher, *fakeWaker) {
	t.Helper()
	provider := &fakeProvider{
		name:    "github",
		created: CreatedPR{Number: 42, URL: "https://github.com/o/r/pull/42", HeadSHA: "deadbeef"},
	}
	watcher := &fakeWatcher{out: WatchResult{
		WatchID: "watch:o/r:42:deadbeef", Revision: 3, Status: "watching", Verdict: "watching",
	}}
	waker := &fakeWaker{}
	svc := New(
		fakeGit{repo: "refusedguy/beacon", ref: "feat/w336", sha: "deadbeef"},
		fakeCards{card: Card{Title: "T3.329. PR из одной команды", Body: "Цель: одна команда"}},
		watcher, waker,
	)
	svc.WithProvider(provider)
	return svc, provider, watcher, waker
}

func TestRunCreatesPRWatchAndWake(t *testing.T) {
	svc, provider, watcher, waker := newTestService(t)

	got, err := svc.Run(context.Background(), Input{
		CardID: "T3.329", Target: "sess-1", Adapter: "opencode",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if provider.createCalls != 1 {
		t.Fatalf("create calls = %d, want 1", provider.createCalls)
	}
	wantCreate := CreateRequest{
		Repo: "refusedguy/beacon", HeadRef: "feat/w336", BaseRef: "main",
		Title: "T3.329. PR из одной команды", Body: "Цель: одна команда",
		HeadSHA: "deadbeef",
	}
	if provider.lastCreate != wantCreate {
		t.Fatalf("create request = %+v, want %+v", provider.lastCreate, wantCreate)
	}
	if watcher.calls != 1 {
		t.Fatalf("watch calls = %d, want 1", watcher.calls)
	}
	wantWatch := WatchInput{
		Repo: "refusedguy/beacon", PR: 42, HeadSHA: "deadbeef", Role: RoleExecutor,
		SubscriberID: "sess-1", Adapter: "opencode", Target: "sess-1",
	}
	if watcher.last != wantWatch {
		t.Fatalf("watch input = %+v, want %+v", watcher.last, wantWatch)
	}
	if waker.calls != 1 {
		t.Fatalf("wake calls = %d, want 1", waker.calls)
	}
	wantNotice := Notice{Adapter: "opencode", SessionID: "sess-1", WatchID: "watch:o/r:42:deadbeef", Revision: 3, Verdict: "watching"}
	if waker.last != wantNotice {
		t.Fatalf("notice = %+v, want %+v", waker.last, wantNotice)
	}
	if got.PR != 42 || got.WatchID != "watch:o/r:42:deadbeef" {
		t.Fatalf("result = %+v", got)
	}
	if got.URL != "https://github.com/o/r/pull/42" || got.Provider != "github" {
		t.Fatalf("result url/provider = %q/%q", got.URL, got.Provider)
	}
}

func TestRunDryRunHasNoSideEffects(t *testing.T) {
	svc, provider, watcher, waker := newTestService(t)

	got, err := svc.Run(context.Background(), Input{CardID: "T3.329", Target: "sess-1", DryRun: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !got.DryRun || got.Repo != "refusedguy/beacon" || got.HeadRef != "feat/w336" || got.Title == "" {
		t.Fatalf("dry-run result = %+v", got)
	}
	if provider.createCalls != 0 || watcher.calls != 0 || waker.calls != 0 {
		t.Fatalf("dry-run side effects: create=%d watch=%d wake=%d", provider.createCalls, watcher.calls, waker.calls)
	}
	if got.WatchID != "" || got.PR != 0 {
		t.Fatalf("dry-run returned PR/watch: %+v", got)
	}
}

func TestRunFlagsOverrideCard(t *testing.T) {
	svc, provider, _, _ := newTestService(t)

	_, err := svc.Run(context.Background(), Input{
		CardID: "T3.329", Title: "Свой заголовок", Body: "Своё тело", Target: "sess-1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.lastCreate.Title != "Свой заголовок" || provider.lastCreate.Body != "Своё тело" {
		t.Fatalf("flags did not override card: %+v", provider.lastCreate)
	}
}

func TestRunFillsBodyFromCardWhenOnlyTitleGiven(t *testing.T) {
	svc, provider, _, _ := newTestService(t)

	_, err := svc.Run(context.Background(), Input{
		CardID: "T3.329", Title: "Свой заголовок", Target: "sess-1",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if provider.lastCreate.Title != "Свой заголовок" || provider.lastCreate.Body != "Цель: одна команда" {
		t.Fatalf("create = %+v", provider.lastCreate)
	}
}

func TestRunUsesHeadSHAFromCreatedPR(t *testing.T) {
	svc, provider, watcher, _ := newTestService(t)
	provider.created.HeadSHA = ""

	_, err := svc.Run(context.Background(), Input{CardID: "T3.329", Target: "sess-1"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if watcher.last.HeadSHA != "deadbeef" {
		t.Fatalf("watch headSHA = %q, want local HEAD", watcher.last.HeadSHA)
	}
}

func TestRunRejectsUnknownProvider(t *testing.T) {
	svc, provider, _, _ := newTestService(t)

	_, err := svc.Run(context.Background(), Input{CardID: "T3.329", Target: "sess-1", Provider: "gitlab"})
	if !errors.Is(err, ErrProviderNotRegistered) {
		t.Fatalf("error = %v, want ErrProviderNotRegistered", err)
	}
	if provider.createCalls != 0 {
		t.Fatal("unknown provider still created a PR")
	}
}

func TestRunRejectsMissingInputs(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		code ErrorCode
	}{
		{name: "no title or card", in: Input{Target: "sess"}, code: CodeInvalidInput},
		{name: "no target", in: Input{CardID: "T3.329"}, code: CodeInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, watcher, _ := newTestService(t)
			_, err := svc.Run(context.Background(), tc.in)
			if err == nil {
				t.Fatal("want error")
			}
			var appErr *Error
			if !errors.As(err, &appErr) || appErr.Code != tc.code {
				t.Fatalf("error = %v, want code %s", err, tc.code)
			}
			if watcher.calls != 0 {
				t.Fatal("watch registered on invalid input")
			}
		})
	}
}

func TestRunRecordsWakeFailure(t *testing.T) {
	svc, _, _, waker := newTestService(t)
	waker.err = errors.New("wake: unsupported adapter")

	got, err := svc.Run(context.Background(), Input{CardID: "T3.329", Target: "sess-1"})
	if err != nil {
		t.Fatalf("wake failure must not fail the command: %v", err)
	}
	if got.WakeError != "wake: unsupported adapter" {
		t.Fatalf("WakeError = %q", got.WakeError)
	}
}

func TestRunReportsCreateFailure(t *testing.T) {
	svc, provider, watcher, _ := newTestService(t)
	provider.err = errors.New("401")

	_, err := svc.Run(context.Background(), Input{CardID: "T3.329", Target: "sess-1"})
	if err == nil {
		t.Fatal("want error")
	}
	var appErr *Error
	if !errors.As(err, &appErr) || appErr.Code != CodeCreateFailed {
		t.Fatalf("error = %v, want CREATE_FAILED", err)
	}
	if watcher.calls != 0 {
		t.Fatal("watch registered after failed PR creation")
	}
}

func TestPRProviderPortIsComplete(t *testing.T) {
	provider := &fakeProvider{name: "other"}
	var _ PRProvider = provider
	ctx := context.Background()
	if _, err := provider.PRState(ctx, "o/r", 1); err != nil || provider.stateCalls != 1 {
		t.Fatalf("PRState: %v, calls=%d", err, provider.stateCalls)
	}
	if _, err := provider.ListComments(ctx, "o/r", 1); err != nil || provider.commentsCalls != 1 {
		t.Fatalf("ListComments: %v, calls=%d", err, provider.commentsCalls)
	}
	if err := provider.Merge(ctx, "o/r", 1); err != nil || provider.mergeCalls != 1 {
		t.Fatalf("Merge: %v, calls=%d", err, provider.mergeCalls)
	}
}

func TestResolveProviderPrecedence(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "beacon.config.json")
	if err := os.WriteFile(config, []byte(`{"prProvider":"from-file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	lookup := func(string) string { return "from-env" }

	readFile := os.ReadFile
	if got := ResolveProvider("from-flag", dir, lookup, readFile); got != "from-flag" {
		t.Fatalf("flag precedence = %q", got)
	}
	if got := ResolveProvider("", dir, lookup, readFile); got != "from-env" {
		t.Fatalf("env precedence = %q", got)
	}
	if got := ResolveProvider("", dir, nil, readFile); got != "from-file" {
		t.Fatalf("config precedence = %q", got)
	}
	if got := ResolveProvider("", t.TempDir(), nil, readFile); got != DefaultProvider {
		t.Fatalf("default = %q", got)
	}
}

func TestFileCardSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docs", "tasks")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# T3.329. PR из одной команды\nСтатус: todo\n\nЦель: одна команда\n"
	if err := os.WriteFile(filepath.Join(path, "T3.329.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	card, err := (FileCardSource{Dir: dir, ReadFile: os.ReadFile}).Card(context.Background(), "T3.329")
	if err != nil {
		t.Fatalf("Card: %v", err)
	}
	if card.Title != "T3.329. PR из одной команды" {
		t.Fatalf("title = %q", card.Title)
	}
	if !strings.Contains(card.Body, "Цель: одна команда") {
		t.Fatalf("body = %q", card.Body)
	}
}

func TestNewRegistersFirstProviderAsDefault(t *testing.T) {
	svc := New(nil, nil, nil, nil)
	if svc.fallback != DefaultProvider {
		t.Fatalf("fallback = %q", svc.fallback)
	}
	svc.WithProvider(&fakeProvider{name: "gitlab"})
	if svc.fallback != "gitlab" {
		t.Fatalf("fallback after first provider = %q", svc.fallback)
	}
	svc.WithProvider(&fakeProvider{name: "github"})
	if svc.fallback != "gitlab" {
		t.Fatalf("later provider changed fallback to %q", svc.fallback)
	}
	svc.WithDefault("github")
	if svc.fallback != "github" {
		t.Fatalf("WithDefault = %q", svc.fallback)
	}
	if svc.providers["github"].Name() != "github" {
		t.Fatal("provider map lost the github adapter")
	}
}
