package tasksource

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"

	"github.com/refusedguy/beacon/pkg/core"
)

// CardStore owns one card directory. PatchStatus is its one deep operation:
// callers name a card and a status, and the store handles lookup, parsing,
// format detection and the write. Card paths and status-line rules stay inside
// this module instead of spreading across its callers.
type CardStore struct {
	Dir string
}

// card is one located card with the parsed task and the raw text. Carrying all
// three lets callers work from a single read instead of re-reading the file.
type card struct {
	path    string
	task    core.Task
	content string
	hasFM   bool
}

// cards returns the markdown paths under the store directory.
func (s CardStore) cards() ([]string, error) {
	var paths []string
	err := filepath.WalkDir(s.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != s.Dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

// find locates a card by parsed ID, not by filename, and returns the text it
// was parsed from so callers need no second read.
func (s CardStore) find(id core.TaskID) (card, error) {
	paths, err := s.cards()
	if err != nil {
		return card{}, err
	}
	for _, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(b)
		t, ok, hasFM := parseCardFields(content, path)
		if !ok || t.ID != id {
			continue
		}
		return card{path: path, task: t, content: content, hasFM: hasFM}, nil
	}
	return card{}, os.ErrNotExist
}

// PatchStatus finds a card by id and rewrites its status. It owns the whole
// chain, so Claim, Complete and workspace marking share one implementation.
func (s CardStore) PatchStatus(ctx context.Context, id core.TaskID, status string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c, err := s.find(id)
	if err != nil {
		return err
	}
	updated := rewriteStatus(c.content, status, c.hasFM)
	if err := ctx.Err(); err != nil {
		return err
	}
	return writeCardAtomic(c.path, updated)
}

// stringList decodes YAML scalars and sequences into a string slice.
type stringList []string

// UnmarshalYAML decodes a scalar or sequence into a stringList.
func (l *stringList) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		*l = splitList(s)
		return nil
	}
	var parts []string
	if err := node.Decode(&parts); err != nil {
		return err
	}
	for _, p := range parts {
		for _, v := range splitList(p) {
			*l = append(*l, v)
		}
	}
	return nil
}

// cardSchema is the wire shape shared by markdown frontmatter and the fenced
// YAML block in issue bodies. Both decoders accept every alias, so task_id and
// owner_files read the same in either format.
//
// Schema note. Six groups carry the load: identity (id, task_id); display
// and lifecycle (title, status, priority, kind, base_branch, require_review);
// ownership (scope, owner_files); dependencies (depends, depends_on); epic
// planning (plan, whose children carry title, body, scope, depends and
// priority); and perf closure (gate, with kind and key). The aliases are
// task_id for id, owner_files for scope and depends_on for depends. The
// flat-line reader also accepts the legacy keys Владелец файлов,
// Зависимости, Статус, Фаза, Ссылка and ParentTask.
//
// Keys outside these groups, including perf: true on existing cards, are
// ignored rather than rejected. Strict decoding with KnownFields stays off in
// this card: issue bodies still arrive as v1 flat text, and a strict decode
// would first need perf added here or removed from every card. Adding it here
// would turn a label into a load-bearing field, so leave it out.
type cardSchema struct {
	ID            string     `yaml:"id"`
	TaskID        string     `yaml:"task_id"`
	Title         string     `yaml:"title"`
	Status        string     `yaml:"status"`
	Parent        string     `yaml:"parent"`
	Scope         stringList `yaml:"scope"`
	OwnerFiles    stringList `yaml:"owner_files"`
	Depends       stringList `yaml:"depends"`
	DependsOn     stringList `yaml:"depends_on"`
	Priority      int        `yaml:"priority"`
	Kind          string     `yaml:"kind"`
	BaseBranch    string     `yaml:"base_branch"`
	RequireReview string     `yaml:"require_review"`
	SourceRef     string     `yaml:"source_ref"`
	Phase         string     `yaml:"phase"`
	WorkerAgent   string     `yaml:"worker_agent"`
	WorkerPrompt  string     `yaml:"worker_prompt"`
	Plan          []planSpec `yaml:"plan"`
	Gate          gateSpec   `yaml:"gate"`
}

// gateSpec is the wire shape of the perf gate block. Key names the registry
// key whose verdict.json entry decides the card.
type gateSpec struct {
	Kind string `yaml:"kind"`
	Key  string `yaml:"key"`
}

// GateKind names the advisory measurement that closes a perf card. A timing
// gate reads a timing verdict, alloc and counter gates read their gain kinds,
// and a structural gate closes without waiting for a measured gain.
type GateKind string

const (
	GateKindTiming     GateKind = "timing"
	GateKindAlloc      GateKind = "alloc"
	GateKindCounter    GateKind = "counter"
	GateKindStructural GateKind = "structural"
)

// PerfGate is the normalized gate block. Kind is empty when the source has no
// gate or carries an unknown kind, so tooling can treat both as ungated.
type PerfGate struct {
	Kind GateKind
	Key  string
}

// normalizeGateKind maps s to a known gate kind.
func normalizeGateKind(s string) GateKind {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "timing":
		return GateKindTiming
	case "alloc":
		return GateKindAlloc
	case "counter":
		return GateKindCounter
	case "structural":
		return GateKindStructural
	default:
		return ""
	}
}

// combinedScope returns the scope field followed by its owner_files alias.
func (s cardSchema) combinedScope() []string {
	out := make([]string, 0, len(s.Scope)+len(s.OwnerFiles))
	out = append(out, s.Scope...)
	out = append(out, s.OwnerFiles...)
	return out
}

// scopePaths resolves raw scope values against dir. Frontmatter and fenced
// YAML both reach scope conversion through here.
func scopePaths(values []string, dir string) []core.ScopePath {
	var out []core.ScopePath
	for _, p := range values {
		out = append(out, scopeForPath(p, dir))
	}
	return out
}

// planSpec is the wire shape of one child in an epic's plan array.
type planSpec struct {
	Title    string     `yaml:"title"`
	Body     string     `yaml:"body,omitempty"`
	Scope    stringList `yaml:"scope"`
	Depends  stringList `yaml:"depends"`
	Priority int        `yaml:"priority"`
}

// epicFields holds the epic-only frontmatter written by Markdown.Create.
type epicFields struct {
	Kind          string     `yaml:"kind,omitempty"`
	BaseBranch    string     `yaml:"base_branch,omitempty"`
	RequireReview string     `yaml:"require_review,omitempty"`
	Plan          []planSpec `yaml:"plan,omitempty"`
}

// taskFields holds the normalized fields parsed from a card or an issue body.
// Both decoders fill it through the same alias rules, so a field written in
// frontmatter, in a fenced YAML block or on a flat line resolves the same way
// in either source.
type taskFields struct {
	ID            string
	Title         string
	Status        string
	Parent        string
	Scope         []string
	Depends       []string
	Priority      int
	Kind          core.TaskKind
	BaseBranch    string
	RequireReview core.ReviewMode
	Phase         string
	SourceRef     string
	WorkerAgent   string
	WorkerPrompt  string
	Plan          []core.ChildSpec
	Gate          PerfGate
}

// childSpecs converts plan specs into core child specs.
func childSpecs(specs []planSpec, dir string) []core.ChildSpec {
	out := make([]core.ChildSpec, 0, len(specs))
	for _, spec := range specs {
		child := core.ChildSpec{Title: strings.TrimSpace(spec.Title), Body: strings.TrimSpace(spec.Body), Priority: spec.Priority}
		for _, p := range spec.Scope {
			child.Scope = append(child.Scope, scopeForPath(p, dir))
		}
		for _, d := range spec.Depends {
			if d = strings.TrimSpace(d); d != "" {
				child.Depends = append(child.Depends, core.TaskID(d))
			}
		}
		out = append(out, child)
	}
	return out
}

// planSpecs converts core child specs into their wire shape.
func planSpecs(specs []core.ChildSpec) []planSpec {
	out := make([]planSpec, 0, len(specs))
	for _, spec := range specs {
		out = append(out, planSpec{
			Title:    spec.Title,
			Body:     spec.Body,
			Scope:    scopeValues(spec.Scope),
			Depends:  dependencyValues(spec.Depends),
			Priority: spec.Priority,
		})
	}
	return out
}

// normalizeKind maps s to a known task kind.
func normalizeKind(s string) core.TaskKind {
	return core.NormalizeTaskKind(core.TaskKind(s))
}

// normalizeReview maps s to a known review mode.
func normalizeReview(s string) core.ReviewMode {
	return core.NormalizeReviewMode(core.ReviewMode(s))
}

// PlanParser is the default core.EpicParser. It numbers children in plan order.
type PlanParser struct{}

var _ core.EpicParser = PlanParser{}

// Children expands the epic's plan into numbered child tasks.
func (PlanParser) Children(epic core.Task) []core.Task {
	return core.SetParent(core.ExpandChildren(epic, ""), epic.ID)
}

// fieldCacheLimit bounds the number of cached parses so a long-running daemon
// cannot pin every body it has ever seen. It sits above a large card corpus so
// one refresh of a whole repository fits in the shared cache. Entries are
// evicted in insertion order only once the bound is reached.
const fieldCacheLimit = 1 << 16

// parseKey identifies one parse. The source text is part of the key, so an
// edited card can never return a stale result and dir and depPrefix still
// produce their own values for the same text.
type parseKey struct {
	body      string
	dir       string
	depPrefix string
}

// fieldCache stores the parsed form of unchanged card text. A refresh parses
// the same bodies repeatedly, so one decode serves every later call for that
// text.
type fieldCache struct {
	mu      sync.RWMutex
	entries map[parseKey]taskFields
	order   []parseKey
	next    int
}

var cachedFields = fieldCache{entries: make(map[parseKey]taskFields)}

// clone gives each caller its own slices. A cached entry must never be handed
// out directly, since callers may append to or overwrite the fields they get.
func (t taskFields) clone() taskFields {
	out := t
	if t.Scope != nil {
		out.Scope = append([]string(nil), t.Scope...)
	}
	if t.Depends != nil {
		out.Depends = append([]string(nil), t.Depends...)
	}
	if t.Plan != nil {
		out.Plan = make([]core.ChildSpec, len(t.Plan))
		for i, child := range t.Plan {
			out.Plan[i] = child
			if child.Scope != nil {
				out.Plan[i].Scope = append([]core.ScopePath(nil), child.Scope...)
			}
			if child.Depends != nil {
				out.Plan[i].Depends = append([]core.TaskID(nil), child.Depends...)
			}
		}
	}
	return out
}

func (c *fieldCache) load(key parseKey) (taskFields, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	fields, ok := c.entries[key]
	return fields, ok
}

func (c *fieldCache) store(key parseKey, fields taskFields) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.entries[key]; !exists {
		if len(c.order) < fieldCacheLimit {
			c.order = append(c.order, key)
		} else {
			oldest := c.order[c.next]
			delete(c.entries, oldest)
			c.order[c.next] = key
			c.next = (c.next + 1) % fieldCacheLimit
		}
	}
	c.entries[key] = fields
}

// ParseTaskFields reads a fenced YAML block and flat Scope/Depends/Priority
// lines. Repeated calls with identical text reuse the first parse.
func ParseTaskFields(body, dir, depPrefix string) taskFields {
	key := parseKey{body: body, dir: dir, depPrefix: depPrefix}
	if fields, ok := cachedFields.load(key); ok {
		return fields.clone()
	}
	fields := parseTaskFields(body, dir, depPrefix)
	cachedFields.store(key, fields.clone())
	return fields
}

// decodeSchema maps the shared wire shape onto normalized fields. Frontmatter
// and fenced YAML both decode into cardSchema first, so this is their one
// alias authority.
func decodeSchema(yf cardSchema, dir string) taskFields {
	var out taskFields
	out.ID = core.FirstNonEmpty(strings.TrimSpace(yf.TaskID), strings.TrimSpace(yf.ID))
	out.Title = strings.TrimSpace(yf.Title)
	out.Status = strings.TrimSpace(yf.Status)
	out.Parent = strings.TrimSpace(yf.Parent)
	out.Scope = append(out.Scope, yf.combinedScope()...)
	out.Depends = append(out.Depends, yf.Depends...)
	out.Depends = append(out.Depends, yf.DependsOn...)
	out.Priority = yf.Priority
	out.Kind = normalizeKind(yf.Kind)
	out.BaseBranch = strings.TrimSpace(yf.BaseBranch)
	out.RequireReview = normalizeReview(yf.RequireReview)
	out.Phase = strings.TrimSpace(yf.Phase)
	out.SourceRef = strings.TrimSpace(yf.SourceRef)
	out.WorkerAgent = strings.TrimSpace(yf.WorkerAgent)
	out.WorkerPrompt = strings.TrimSpace(yf.WorkerPrompt)
	out.Plan = childSpecs(yf.Plan, dir)
	out.Gate = PerfGate{
		Kind: normalizeGateKind(yf.Gate.Kind),
		Key:  strings.TrimSpace(yf.Gate.Key),
	}
	return out
}

// decodeAliases applies the flat-line aliases to fields already decoded from
// YAML. Markdown cards and issue bodies call it with their own lines, so a
// TaskID, ParentTask or Russian legacy key resolves the same way in both.
func decodeAliases(lines []string, dir, depPrefix string, out *taskFields) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Scope:"), strings.HasPrefix(line, "Владелец файлов:"):
			out.Scope = append(out.Scope, splitList(afterColon(line))...)
		case strings.HasPrefix(line, "Depends:"), strings.HasPrefix(line, "Зависимости:"):
			out.Depends = append(out.Depends, splitList(afterColon(line))...)
		case strings.HasPrefix(line, "Priority:"):
			if n, err := strconv.Atoi(afterColon(line)); err == nil {
				out.Priority = n
			}
		case strings.HasPrefix(line, "Kind:"):
			out.Kind = normalizeKind(afterColon(line))
		case strings.HasPrefix(line, "BaseBranch:"), strings.HasPrefix(line, "Base branch:"):
			out.BaseBranch = afterColon(line)
		case strings.HasPrefix(line, "RequireReview:"):
			out.RequireReview = normalizeReview(afterColon(line))
		case strings.HasPrefix(line, "TaskID:"), strings.HasPrefix(line, "ID:"):
			out.ID = afterColon(line)
		case strings.HasPrefix(line, "Title:"):
			out.Title = afterColon(line)
		case strings.HasPrefix(line, "Status:"), strings.HasPrefix(line, "Статус:"):
			out.Status = afterColon(line)
		case strings.HasPrefix(line, "Parent:"), strings.HasPrefix(line, "ParentTask:"):
			out.Parent = afterColon(line)
		case strings.HasPrefix(line, "Фаза:"):
			out.Phase = afterColon(line)
		case strings.HasPrefix(line, "Ссылка:"):
			out.SourceRef = afterColon(line)
		}
	}
	out.Scope = unique(out.Scope)
	out.Depends = unique(out.Depends)
	if depPrefix != "" {
		for i, d := range out.Depends {
			out.Depends[i] = normalizeDep(d, depPrefix)
		}
	}
}

// task converts normalized fields into a core.Task resolved against dir.
// Callers still fill Body and SourceRef from the card itself.
func (t taskFields) task(dir string) core.Task {
	return core.Task{
		ID:            core.TaskID(t.ID),
		Title:         t.Title,
		Status:        normalizeStatus(t.Status),
		Parent:        core.TaskID(t.Parent),
		Priority:      t.Priority,
		Kind:          t.Kind,
		BaseBranch:    t.BaseBranch,
		RequireReview: t.RequireReview,
		WorkerAgent:   t.WorkerAgent,
		WorkerPrompt:  t.WorkerPrompt,
		Plan:          t.Plan,
		Scope:         t.Scopes(dir),
		DependsOn:     t.Dependencies(),
	}
}

// parseTaskFields decodes issue text without the cache. It keeps field aliases
// and scope conversion in one place so cached and fresh parses stay identical.
func parseTaskFields(body, dir, depPrefix string) taskFields {
	var out taskFields
	var yf cardSchema
	lines, block, hasBlock := splitFieldLines(body)
	if hasBlock {
		if err := yaml.Unmarshal([]byte(block), &yf); err != nil {
			slog.Warn("tasksource: malformed YAML field block", "error", err)
		} else {
			out = decodeSchema(yf, dir)
		}
	}
	decodeAliases(lines, dir, depPrefix, &out)
	return out
}

// Scopes resolves the parsed scope strings against dir.
func (t taskFields) Scopes(dir string) []core.ScopePath {
	return scopePaths(t.Scope, dir)
}

// ParentID returns the parsed parent as a task ID. It is empty when the source
// card has no epic relation.
func (t taskFields) ParentID() core.TaskID {
	return core.TaskID(strings.TrimSpace(t.Parent))
}

// Dependencies returns the parsed dependency ids.
func (t taskFields) Dependencies() []core.TaskID {
	var out []core.TaskID
	for _, d := range t.Depends {
		if d = strings.TrimSpace(d); d != "" {
			out = append(out, core.TaskID(d))
		}
	}
	return out
}

// normalizeDep adds prefix to a bare numeric dependency id.
func normalizeDep(d, prefix string) string {
	d = strings.TrimSpace(strings.Trim(d, "` "))
	if d == "" || strings.Contains(d, "-") {
		return d
	}
	return prefix + "-" + d
}

// unique trims values and drops empty or repeated entries.
func unique(values []string) []string {
	seen := make(map[string]bool, len(values))
	var out []string
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// afterColon returns the trimmed text after the first colon.
func afterColon(line string) string {
	if i := strings.Index(line, ":"); i >= 0 {
		return strings.TrimSpace(line[i+1:])
	}
	return ""
}

// splitFieldLines splits card text once and returns both the lines and the
// contents of the first fenced YAML block. Callers that need both would
// otherwise walk the same text twice.
func splitFieldLines(body string) (lines []string, yamlText string, ok bool) {
	lines = strings.Split(strings.ReplaceAll(body, "\r", ""), "\n")
	var block []string
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```yaml" || trimmed == "```yml" {
				inBlock = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			break
		}
		block = append(block, line)
	}
	return lines, strings.Join(block, "\n"), len(block) > 0
}

// splitList splits a comma or semicolon separated list into sorted items.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' }) {
		p = strings.Trim(strings.TrimSpace(p), "` ")
		if p != "" && p != "нет" && p != "-" {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// scopeForPath classifies p as a pattern, directory or file scope.
func scopeForPath(p, dir string) core.ScopePath {
	p = strings.TrimSpace(strings.Trim(p, "` "))
	if strings.Contains(p, "*") {
		return core.ScopePattern(p)
	}
	if strings.HasSuffix(p, "/") || strings.HasSuffix(p, "\\") {
		return core.ScopeDir(p)
	}
	if dir != "" {
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p))); err == nil && info.IsDir() {
			return core.ScopeDir(p)
		}
	}
	return core.ScopeFile(p)
}

// normalizeStatus maps s to a known task status.
func normalizeStatus(s string) core.TaskStatus {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "done", "готово", "closed", "merged", "выполнено":
		return core.TaskDone
	case "dropped", "отменено", "отклонено":
		return core.TaskDropped
	case "in_progress", "in progress", "в работе", "claimed":
		return core.TaskClaimed
	default:
		return core.TaskAvailable
	}
}
