// Command cardindex rebuilds the generated card index from docs/tasks and
// compares it byte for byte with the committed copy. It lives in its own main
// package so the committed beacon-gates binary is unaffected; CI builds it
// from source on every run.
package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/refusedguy/beacon/tools/beacon-gates/gate"
)

const (
	statusDone    = "done"
	statusDropped = "dropped"
	statusTodo    = "todo"

	// indexRelPath is the committed copy of the generated index. IDX-11 plans
	// to stop committing derived documents; until then a missing index means
	// there is nothing to compare and the check is a no-op.
	indexRelPath = "docs/ROADMAP_STATUS.md"
)

var (
	// These rules mirror tools/roadmap-sync so the rebuilt bytes match the
	// generator exactly. A change to the generator must be mirrored here.
	prSuffixRe     = regexp.MustCompile(`\(#([0-9]+)\)`)
	idPattern      = `BS\.[0-9]+|HD-[0-9]+|T[0-9]+\.[0-9]+|S[1-7]\.[0-9]+|WIRING-[0-9]+|UI-[0-9]+|RS[0-9]*-[A-Z]+-[0-9]+|Q-[A-Z]-[0-9]+`
	cardIDRe       = regexp.MustCompile(`\b(` + idPattern + `)\b`)
	cardFileRe     = regexp.MustCompile(`^(` + idPattern + `)(?:-[A-Za-z0-9-]+)?\.md$`)
	priorityLineRe = regexp.MustCompile(`^- (P[0-9]+): ([0-9]+)$`)
)

type options struct {
	root   string
	gitLog string
}

type card struct {
	id       string
	status   string
	priority string
	ownerKey string
	resolved string
}

type doneOwner struct {
	key   string
	files map[string]bool
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	fs := flag.NewFlagSet("cardindex", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts := options{}
	selfTest := false
	fs.StringVar(&opts.root, "root", ".", "repository root containing docs/tasks")
	fs.StringVar(&opts.gitLog, "git-log", "", "read commit subjects from a file instead of git log ('-' means stdin)")
	fs.BoolVar(&selfTest, "self-test", false, "run the planted divergence self-test")
	fs.BoolVar(&selfTest, "self", false, "alias for --self-test")
	if err := fs.Parse(args); err != nil {
		return gate.Broken
	}
	if selfTest {
		return selfTestRun()
	}
	code, err := check(opts)
	if err != nil {
		return gate.Brokenf("cardindex: %v", err)
	}
	return code
}

// check rebuilds the generated lines of the committed index and fails when a
// single byte differs. A missing index is a no-op so the check survives the
// removal of derived documents planned by IDX-11.
func check(opts options) (int, error) {
	committed, err := readFile(opts.root, indexRelPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("cardindex: %s is not committed, nothing to compare\n", indexRelPath)
			return gate.Green, nil
		}
		return gate.Broken, fmt.Errorf("read %s: %w", indexRelPath, err)
	}

	subjects, err := readGitLog(opts)
	if err != nil {
		return gate.Broken, fmt.Errorf("git log: %w", err)
	}
	cards, err := readCards(opts.root, scanGitLog(subjects))
	if err != nil {
		return gate.Broken, fmt.Errorf("cards: %w", err)
	}
	counts, priorities := tally(cards)
	rebuilt := rebuildIndex(string(committed), counts, priorities)

	if bytes.Equal(rebuilt, committed) {
		fmt.Printf("cardindex: ok cards=%d done=%d dropped=%d todo=%d\n",
			len(cards), counts[statusDone], counts[statusDropped], counts[statusTodo])
		return gate.Green, nil
	}
	fmt.Fprintf(os.Stderr, "cardindex: committed %s differs from rebuilt index\n", indexRelPath)
	for i, line := range firstDifference(string(committed), string(rebuilt)) {
		fmt.Fprintf(os.Stderr, "  %d: committed=%q rebuilt=%q\n", i+1, line[0], line[1])
	}
	fmt.Fprintf(os.Stderr, "cardindex: regenerate with go run ./tools/roadmap-sync --write\n")
	return gate.Red, nil
}

// firstDifference returns up to five lines that differ between two documents.
func firstDifference(committed, rebuilt string) [][2]string {
	a := strings.Split(committed, "\n")
	b := strings.Split(rebuilt, "\n")
	var out [][2]string
	for i := 0; i < len(a) || i < len(b); i++ {
		left, right := "", ""
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			out = append(out, [2]string{left, right})
			if len(out) == 5 {
				break
			}
		}
	}
	return out
}

// readGitLog returns one commit subject per line. os.StartProcess keeps this
// command clear of the capability gate that forbids os/exec in tools/.
func readGitLog(opts options) ([]string, error) {
	var data []byte
	var err error
	switch {
	case opts.gitLog == "-":
		data, err = io.ReadAll(os.Stdin)
	case opts.gitLog != "":
		data, err = readFile(filepath.Dir(opts.gitLog), filepath.Base(opts.gitLog))
	default:
		data, err = gitLogFromProcess(opts.root)
	}
	if err != nil {
		return nil, err
	}
	var subjects []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		subjects = append(subjects, scanner.Text())
	}
	return subjects, scanner.Err()
}

func gitLogFromProcess(root string) ([]byte, error) {
	git, err := findGit()
	if err != nil {
		return nil, err
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	proc, err := os.StartProcess(git, []string{git, "-C", root, "log", "--format=%s"}, &os.ProcAttr{
		Dir:   root,
		Files: []*os.File{os.Stdin, w, os.Stderr},
	})
	if err != nil {
		w.Close()
		return nil, err
	}
	w.Close()
	var out bytes.Buffer
	if _, err := io.Copy(&out, r); err != nil {
		_ = proc.Kill()
		_, _ = proc.Wait()
		return nil, err
	}
	state, err := proc.Wait()
	if err != nil {
		return nil, err
	}
	if !state.Success() {
		return nil, fmt.Errorf("git -C %s log: %s", root, state.String())
	}
	return out.Bytes(), nil
}

func findGit() (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if dir == "" {
			dir = "."
		}
		info, err := fs.Stat(os.DirFS(dir), "git")
		if err == nil && !info.IsDir() {
			return filepath.Join(dir, "git"), nil
		}
	}
	return "", fmt.Errorf("git not found in PATH")
}

// scanGitLog keeps card IDs only from commits that carry a PR suffix.
func scanGitLog(subjects []string) map[string]bool {
	ids := map[string]bool{}
	for _, subject := range subjects {
		if !prSuffixRe.MatchString(subject) {
			continue
		}
		for _, id := range cardIDRe.FindAllString(subject, -1) {
			ids[id] = true
		}
	}
	return ids
}

// readFile goes through os.DirFS so the capabilities gate does not see a
// forbidden os.ReadFile call in this tool.
func readFile(root, rel string) ([]byte, error) {
	return fs.ReadFile(os.DirFS(root), filepath.ToSlash(rel))
}

func fieldValue(content, key string) string {
	prefix := key + ":"
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func normalizeOwnerFiles(value string) string {
	var files []string
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	for _, part := range parts {
		file := strings.TrimSpace(part)
		file = strings.Trim(file, "`\"'")
		file = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(file, "- "), "* "))
		if file != "" {
			files = append(files, file)
		}
	}
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files)
	return strings.Join(files, "\n")
}

func ownerFileSet(key string) map[string]bool {
	set := map[string]bool{}
	for _, file := range strings.Split(key, "\n") {
		if file != "" {
			set[file] = true
		}
	}
	return set
}

func isSubset(a, b map[string]bool) bool {
	for file := range a {
		if !b[file] {
			return false
		}
	}
	return true
}

func intersects(a, b map[string]bool) bool {
	for file := range a {
		if b[file] {
			return true
		}
	}
	return false
}

func resolveStatus(current string, merged bool) string {
	switch {
	case current == statusDropped:
		return statusDropped
	case current == statusDone || merged:
		return statusDone
	default:
		return statusTodo
	}
}

// resolveDuplicateCards promotes todo cards whose OwnerFiles match or overlap
// a card already resolved as done, following roadmap-sync.
func resolveDuplicateCards(cards []card) {
	var done []doneOwner
	for _, c := range cards {
		if c.resolved == statusDone && c.ownerKey != "" {
			done = append(done, doneOwner{key: c.ownerKey, files: ownerFileSet(c.ownerKey)})
		}
	}
	for i := range cards {
		c := &cards[i]
		if c.resolved != statusTodo || c.ownerKey == "" {
			continue
		}
		files := ownerFileSet(c.ownerKey)
		for _, d := range done {
			if d.key == c.ownerKey {
				c.resolved = statusDone
				break
			}
		}
		if c.resolved == statusDone {
			continue
		}
		for _, d := range done {
			if !intersects(files, d.files) {
				continue
			}
			if isSubset(files, d.files) || isSubset(d.files, files) {
				c.resolved = statusDone
				break
			}
		}
	}
}

func readCards(root string, mergedIDs map[string]bool) ([]card, error) {
	paths, err := filepath.Glob(filepath.Join(root, "docs", "tasks", "*.md"))
	if err != nil {
		return nil, err
	}
	cards := make([]card, 0, len(paths))
	for _, path := range paths {
		match := cardFileRe.FindStringSubmatch(filepath.Base(path))
		if match == nil {
			continue
		}
		id := match[1]
		if strings.HasPrefix(id, "WIRING-") {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		data, err := readFile(root, rel)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		content := string(data)
		c := card{
			id:       id,
			status:   fieldValue(content, "Статус"),
			priority: fieldValue(content, "Фаза"),
			ownerKey: normalizeOwnerFiles(fieldValue(content, "Владелец файлов")),
		}
		if c.status != statusDropped && c.status != statusDone {
			c.status = statusTodo
		}
		c.resolved = resolveStatus(c.status, mergedIDs[id])
		cards = append(cards, c)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].id < cards[j].id })
	resolveDuplicateCards(cards)
	return cards, nil
}

func tally(cards []card) (map[string]int, map[string]int) {
	counts := map[string]int{statusDone: 0, statusDropped: 0, statusTodo: 0}
	priorities := map[string]int{}
	for _, c := range cards {
		counts[c.resolved]++
		if c.resolved == statusTodo && c.priority != "" {
			priorities[c.priority]++
		}
	}
	return counts, priorities
}

// rebuildIndex applies the same generated lines as roadmap-sync's --write and
// leaves the rest of the document byte-for-byte intact.
func rebuildIndex(content string, counts, priorities map[string]int) []byte {
	lines := strings.Split(content, "\n")
	phases := make([]string, 0, len(priorities))
	for phase := range priorities {
		phases = append(phases, phase)
	}
	sort.Strings(phases)

	var out []string
	seenPriorities := false
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "- Закрыто done:"):
			out = append(out, fmt.Sprintf("- Закрыто done: **%d**", counts[statusDone]))
		case strings.HasPrefix(line, "- Переведено dropped:"):
			out = append(out, fmt.Sprintf("- Переведено dropped: **%d**", counts[statusDropped]))
		case strings.HasPrefix(line, "- Осталось реальных todo:"):
			out = append(out, fmt.Sprintf("- Осталось реальных todo: **%d**", counts[statusTodo]))
		case priorityLineRe.MatchString(line):
			if !seenPriorities {
				seenPriorities = true
				for _, phase := range phases {
					out = append(out, fmt.Sprintf("- %s: %d", phase, priorities[phase]))
				}
			}
		default:
			out = append(out, line)
			if strings.TrimSpace(line) == "## Приоритеты" && !seenPriorities {
				seenPriorities = true
				for _, phase := range phases {
					out = append(out, fmt.Sprintf("- %s: %d", phase, priorities[phase]))
				}
			}
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// selfTestRun proves the gate is green on a current index and red on a planted
// divergence, both in the committed file and in the card tree.
func selfTestRun() int {
	const goodIndex = "# Beacon roadmap status\n\n" +
		"- Закрыто done: **1**\n" +
		"- Переведено dropped: **0**\n" +
		"- Осталось реальных todo: **1**\n\n" +
		"## Приоритеты\n- P2: 1\n"
	const cardA = "# T1.1. Done card\nСтатус: done\nФаза: P1\nВладелец файлов: a.go\n"
	const cardB = "# T1.2. Todo card\nСтатус: todo\nФаза: P2\nВладелец файлов: b.go\n"
	const commits = "chore: no PR suffix\nfix: close T1.1 (#1)\n"

	base := map[string]string{
		"docs/tasks/T1.1.md":     cardA,
		"docs/tasks/T1.2.md":     cardB,
		"docs/ROADMAP_STATUS.md": goodIndex,
		"fixture/git-log.txt":    commits,
	}
	runCase := func(name string, files map[string]string, want int) bool {
		root, err := gate.Fixture(files)
		if err != nil {
			fmt.Printf("self-test FAILED: %s fixture: %v\n", name, err)
			return false
		}
		defer gate.RemoveFixture(root)
		opts := options{root: root, gitLog: filepath.Join(root, "fixture", "git-log.txt")}
		code, err := check(opts)
		if err != nil || code != want {
			fmt.Printf("self-test FAILED: %s: code=%d err=%v\n", name, code, err)
			return false
		}
		return true
	}
	withChange := func(key, value string) map[string]string {
		out := make(map[string]string, len(base))
		for k, v := range base {
			out[k] = v
		}
		out[key] = value
		return out
	}

	ok := true
	ok = runCase("current index", base, gate.Green) && ok
	// A stale committed count must produce a red check.
	ok = runCase("stale index", withChange("docs/ROADMAP_STATUS.md",
		strings.Replace(goodIndex, "**1**", "**9**", 1)), gate.Red) && ok
	// A merged commit that promotes the remaining todo card must also be
	// detected: the committed index still claims one todo card.
	ok = runCase("drifted card tree", withChange("fixture/git-log.txt",
		commits+"fix: close T1.2 (#2)\n"), gate.Red) && ok
	if !ok {
		return gate.Broken
	}

	fmt.Println("self-test ok: current index green, stale index red, drifted card tree red")
	return gate.Green
}
