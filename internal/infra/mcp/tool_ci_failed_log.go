// Package mcp: beacon_ci_failed_log reads a failed check's full log in pages.
//
// beacon_get_result returns at most 2 KB of redacted annotations. When that
// excerpt is cut, an agent should be able to keep reading from the same CI
// source instead of shelling out to `gh run view --log-failed`. This tool adds
// a second call to that source with an explicit byte budget and an offset.
//
// The tool is opt-in, like t3_thread_read. The advertised tools/list response
// has a byte-pinned contract, so an eleventh name is attached only by a
// composition root that needs paginated logs. The source adds one method to
// envFailureExcerptSource from this file: there is no second HTTP client and
// no second set of credentials.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/refusedguy/beacon/internal/domain"
	"github.com/refusedguy/beacon/internal/infra/observe"
)

const (
	// CIFailedLogToolName is the MCP name of the paginated log reader.
	CIFailedLogToolName = "beacon_ci_failed_log"

	// ciFailedLogDefaultLimit is the byte budget used when limitBytes is zero.
	ciFailedLogDefaultLimit = 4096
	// ciFailedLogMaxLimit is the hard ceiling; a larger request is clamped.
	ciFailedLogMaxLimit = 16384
	// ciFailedLogSanitizedCeiling bounds the full cached text in memory. The
	// cache holds a sanitized string, never raw log bytes that may carry a token.
	ciFailedLogSanitizedCeiling = 1 << 20
	// ciFailedLogCacheSuffix keeps the full-log entry separate from the short
	// excerpt entry written by beacon_get_result for the same check.
	ciFailedLogCacheSuffix = "\x00full"
	// ciFailedLogAnnotationPages is the annotation count requested for a full
	// log. The short excerpt keeps failureExcerptAnnotationCount.
	ciFailedLogAnnotationPages = 100
)

// CIFailedLogInput selects one failed check and one byte page of its log.
type CIFailedLogInput struct {
	WatchID    string `json:"watchID" jsonschema:"Watch whose head SHA selects the check run; required"`
	CheckName  string `json:"checkName" jsonschema:"exact failed check name from failedChecks; required"`
	Offset     int    `json:"offset,omitempty" jsonschema:"byte offset in the sanitized log; zero starts from the beginning"`
	LimitBytes int    `json:"limitBytes,omitempty" jsonschema:"requested byte budget; zero uses 4096, ceiling 16384"`
}

// CIFailedLogOutput is one page of the sanitized log plus the cursor for the
// next call. TotalBytes is the length of the whole sanitized text.
type CIFailedLogOutput struct {
	WatchID    string `json:"watchID"`
	CheckName  string `json:"checkName"`
	Text       string `json:"text"`
	NextOffset int    `json:"nextOffset"`
	TotalBytes int    `json:"totalBytes"`
	Truncated  bool   `json:"truncated"`
	LogURL     string `json:"logUrl,omitempty"`
	FromCache  bool   `json:"fromCache"`
}

// CIFailedLogSource is the read port for one check's full log. offset and
// limit are advisory: the production source fetches the whole log with the
// existing GitHub client and the handler applies paging to the cached text.
// A source must never return raw bytes that can carry credentials.
type CIFailedLogSource interface {
	FailureLog(ctx context.Context, repo, headSHA, checkName string, offset, limit int) (text string, nextOffset int, totalBytes int, err error)
}

// FailureLog extends envFailureExcerptSource with a full-log read. It reuses
// getJSON, the allowed-host guard and the path escaping of the short excerpt,
// so it adds no client and no environment variable. The full text is the
// formatted annotation log for the failed check run on the Watch's head SHA.
func (s *envFailureExcerptSource) FailureLog(ctx context.Context, repo, headSHA, checkName string, offset, limit int) (string, int, int, error) {
	if s == nil || s.client == nil {
		return "", 0, 0, errors.New("ci_failed_log: source is not configured")
	}
	if repo == "" {
		return "", 0, 0, errors.New("ci_failed_log: missing repo")
	}
	if headSHA == "" {
		return "", 0, 0, errors.New("ci_failed_log: missing headSHA")
	}
	if checkName == "" {
		return "", 0, 0, errors.New("ci_failed_log: missing checkName")
	}
	if offset < 0 || limit < 0 {
		return "", 0, 0, errors.New("ci_failed_log: negative offset or limit")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", 0, 0, fmt.Errorf("ci_failed_log: repo %q is not owner/name", repo)
	}

	var runs struct {
		CheckRuns []githubCheckRun `json:"check_runs"`
	}
	if err := s.getJSON(ctx, excerptCheckRunsURL(s.base, owner, name, headSHA), &runs); err != nil {
		return "", 0, 0, err
	}
	var id int64
	for _, run := range runs.CheckRuns {
		if run.Name == checkName && failedRunConclusion(run.Conclusion) {
			id = run.ID
			break
		}
	}
	if id == 0 {
		return "", 0, 0, nil
	}

	q := url.Values{}
	q.Set("per_page", fmt.Sprintf("%d", ciFailedLogAnnotationPages))
	annotationsPath := fmt.Sprintf("%s/repos/%s/%s/check-runs/%d/annotations?%s",
		s.base, url.PathEscape(owner), url.PathEscape(name), id, q.Encode())
	var annotations []githubAnnotation
	if err := s.getJSON(ctx, annotationsPath, &annotations); err != nil {
		return "", 0, 0, err
	}
	text := formatAllAnnotations(annotations)
	return text, len(text), len(text), nil
}

// formatAllAnnotations renders every annotation as a line of
// "path:line: title: message". The short excerpt keeps the eight-line cap;
// the paginated reader has a byte budget instead.
func formatAllAnnotations(annotations []githubAnnotation) string {
	var b strings.Builder
	for _, a := range annotations {
		msg := strings.TrimSpace(a.Message)
		title := strings.TrimSpace(a.Title)
		if msg == "" && title == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		if p := strings.TrimSpace(a.Path); p != "" {
			b.WriteString(p)
			if a.StartLine > 0 {
				b.WriteByte(':')
				b.WriteString(fmt.Sprintf("%d", a.StartLine))
			}
			b.WriteString(": ")
		}
		if title != "" {
			b.WriteString(title)
			if msg != "" {
				b.WriteString(": ")
			}
		}
		b.WriteString(msg)
	}
	return b.String()
}

// sanitizeCILog redacts secrets first, then escapes untrusted markup and
// finally bounds the text. Redaction runs on raw bytes so its patterns still
// match; the escaped string is what gets cached and returned.
func sanitizeCILog(raw string) string {
	if raw == "" {
		return ""
	}
	clean := observe.Redact([]byte(raw))
	return clampExcerpt(escapeUntrusted(string(clean)), ciFailedLogSanitizedCeiling)
}

// ciLogWindow selects a rune-safe byte window of s. Backing up to a rune start
// keeps an offset from splitting a multi-byte character; the returned next
// offset is always a valid start for the following call.
func ciLogWindow(s string, offset, limit int) (page string, next int, truncated bool) {
	if offset < 0 {
		offset = 0
	}
	if limit <= 0 {
		limit = ciFailedLogDefaultLimit
	}
	if offset >= len(s) {
		return "", len(s), false
	}
	start := offset
	for start > 0 && !utf8.RuneStart(s[start]) {
		start--
	}
	end := start + limit
	if end >= len(s) {
		return s[start:], len(s), false
	}
	for end > start && !utf8.RuneStart(s[end]) {
		end--
	}
	if end == start {
		end = start + limit
		for end < len(s) && !utf8.RuneStart(s[end]) {
			end++
		}
	}
	return s[start:end], end, true
}

// CIFailedLogHandler implements beacon_ci_failed_log over a read port, the
// existing failure-log source and the existing excerpt cache. It writes no
// Watch state and stamps no obligation.
type CIFailedLogHandler struct {
	store  resultStoreReader
	source CIFailedLogSource
	cache  failureExcerptCache
}

// NewCIFailedLogHandler builds a handler. A nil source falls back to the
// default env-backed source; a nil cache means no cross-call caching.
func NewCIFailedLogHandler(store resultStoreReader, source CIFailedLogSource) *CIFailedLogHandler {
	if source == nil {
		// The constructor returns the narrower FailureExcerptSource interface.
		// Assert the concrete type so the compiler sees FailureLog as well.
		if env, ok := newEnvFailureExcerptSource().(*envFailureExcerptSource); ok && env != nil {
			source = env
		}
	}
	return &CIFailedLogHandler{store: store, source: source}
}

// SetCache overrides the full-log cache. Tests inject an in-memory fake to
// prove the second page does not reach the source again.
func (h *CIFailedLogHandler) SetCache(cache failureExcerptCache) {
	if h != nil {
		h.cache = cache
	}
}

// Handle returns one page of the failed check's sanitized log. A checkName
// that is not a failed check on this Watch is refused with CodeInvalidInput.
func (h *CIFailedLogHandler) Handle(ctx context.Context, _ *mcpsdk.CallToolRequest, in CIFailedLogInput) (*mcpsdk.CallToolResult, CIFailedLogOutput, error) {
	if in.WatchID == "" {
		return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodeInvalidInput, "ci_failed_log: watchID is required")
	}
	if in.CheckName == "" {
		return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodeInvalidInput, "ci_failed_log: checkName is required")
	}
	if in.Offset < 0 || in.LimitBytes < 0 {
		return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodeInvalidInput, "ci_failed_log: offset and limitBytes must not be negative")
	}
	if h == nil || h.store == nil {
		return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodePersistFailed, "ci_failed_log: store is nil")
	}
	if h.source == nil {
		return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodePersistFailed, "ci_failed_log: source is nil")
	}

	w, err := h.store.Watch(in.WatchID)
	if err != nil {
		return nil, CIFailedLogOutput{}, domain.NewAppErrorWithCause(domain.CodeWatchNotFound, "ci_failed_log: watch: "+err.Error(), err)
	}

	failed := collectFailedChecks(w)
	var logURL string
	matched := false
	for i := range failed {
		if failed[i].Name == in.CheckName {
			matched = true
			logURL = failed[i].DetailsURL
			break
		}
	}
	if !matched {
		return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodeInvalidInput, "ci_failed_log: checkName is not a failed check on this Watch")
	}

	limit := in.LimitBytes
	if limit == 0 {
		limit = ciFailedLogDefaultLimit
	}
	if limit > ciFailedLogMaxLimit {
		limit = ciFailedLogMaxLimit
	}

	cache := h.cache
	cacheKey := in.CheckName + ciFailedLogCacheSuffix
	fromCache := false
	text, ok, cacheErr := cacheGet(ctx, cache, w.ID, w.HeadSHA, cacheKey)
	if cacheErr != nil {
		ok = false
	}
	if !ok {
		raw, _, _, fetchErr := h.source.FailureLog(ctx, w.Repo, w.HeadSHA, in.CheckName, 0, ciFailedLogSanitizedCeiling)
		if fetchErr != nil {
			observe.ActorError("ci_failed_log-source", w.ID, fetchErr)
			return nil, CIFailedLogOutput{}, domain.NewAppError(domain.CodePersistFailed, "ci_failed_log: source: "+fetchErr.Error())
		}
		text = sanitizeCILog(raw)
		if cacheErr == nil {
			if putErr := cachePut(ctx, cache, w.ID, w.HeadSHA, cacheKey, text); putErr != nil {
				observe.ActorError("ci_failed_log-cache", w.ID, putErr)
			}
		}
	} else {
		fromCache = true
	}

	page, next, windowTruncated := ciLogWindow(text, in.Offset, limit)
	out := CIFailedLogOutput{
		WatchID:    in.WatchID,
		CheckName:  in.CheckName,
		Text:       page,
		NextOffset: next,
		TotalBytes: len(text),
		Truncated:  windowTruncated || excerptWasTruncated(text),
		LogURL:     logURL,
		FromCache:  fromCache,
	}
	return &mcpsdk.CallToolResult{}, out, nil
}

// AttachCIFailedLog installs beacon_ci_failed_log as an opt-in tool. It stays
// out of toolDescriptors so the pinned tools/list contract does not change;
// a composition root that needs paginated logs calls this helper.
func AttachCIFailedLog(server *mcpsdk.Server, store resultStoreReader, source CIFailedLogSource) {
	if server == nil || store == nil {
		return
	}
	derived, err := OutputSchema[CIFailedLogOutput]()
	if err != nil || derived == nil {
		derived = &jsonschema.Schema{Type: "object"}
	}
	annotations := mcpsdk.ToolAnnotations{
		ReadOnlyHint:    true,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
	}
	server.RemoveTools(CIFailedLogToolName)
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name:         CIFailedLogToolName,
		Description:  "Read a failed check's full log in bounded byte pages with an explicit offset and continuation cursor.",
		OutputSchema: derived,
		Annotations:  &annotations,
	}, NewCIFailedLogHandler(store, source).Handle)
}
