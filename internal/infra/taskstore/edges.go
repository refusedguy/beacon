package taskstore

import (
	"encoding/json"
	"sort"
	"strings"
)

// EdgeKind задаёт тип ребра в графе карточек. Все рёбра направленные.
type EdgeKind string

const (
	EdgeBlocks           EdgeKind = "blocks"
	EdgeDependsOn        EdgeKind = "depends-on"
	EdgeSupersedes       EdgeKind = "supersedes"
	EdgeRelated          EdgeKind = "related"
	EdgeWaitsForExternal EdgeKind = "waits-for-external"
)

// MaxEdgeKindLen задаёт предел длины типа ребра. Значение выведено из ширины
// колонки VARCHAR(32) в схемах миграций: тип длиннее колонки стал бы фильтром,
// который ничего не находит.
const MaxEdgeKindLen = 32

// ValidEdgeKinds возвращает встроенные типы рёбер. Возвращается копия,
// изменение которой не меняет канонический набор. Проекты могут добавлять
// свои типы через EdgeKind, не расширяя этот список.
func ValidEdgeKinds() []EdgeKind {
	return []EdgeKind{EdgeBlocks, EdgeDependsOn, EdgeSupersedes, EdgeRelated, EdgeWaitsForExternal}
}

// IsValidEdgeKind сообщает, можно ли записать тип ребра. Как и в beads,
// принимается любая непустая строка не длиннее MaxEdgeKindLen. Проверка
// длины происходит до записи в граф.
func IsValidEdgeKind(kind EdgeKind) bool {
	if kind == "" {
		return false
	}
	return len(kind) <= MaxEdgeKindLen
}

// IsWellKnownEdgeKind сообщает, входит ли тип во встроенный набор. Он нужен в
// пользовательских командах, где произвольный тип намеренно запрещён.
func IsWellKnownEdgeKind(kind EdgeKind) bool {
	for _, known := range ValidEdgeKinds() {
		if kind == known {
			return true
		}
	}
	return false
}

// IsExternalGateTarget проверяет цель внешнего гейта. Первая версия принимает
// gh:pr:<номер>, gh:run:<id>, timer:<имя>, human:<имя> и bead:<id>. Цель не
// является ID карточки, поэтому IsID её не проверяет.
func IsExternalGateTarget(target string) bool {
	parts := strings.SplitN(target, ":", 3)
	switch parts[0] {
	case "gh":
		if len(parts) != 3 || parts[2] == "" {
			return false
		}
		switch parts[1] {
		case "pr":
			return allDigits(parts[2])
		case "run":
			return true
		}
		return false
	case "timer", "human", "bead":
		return len(parts) == 2 && parts[1] != "" && !strings.Contains(parts[1], ":")
	}
	return false
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// Edge это одно направленное ребро. From — ID карточки любого семейства. Для
// четырёх внутренних типов To тоже ID карточки. Для waits-for-external To —
// цель внешнего гейта вида gh:pr:42 или gh:run:ID.
type Edge struct {
	From string
	To   string
	Kind EdgeKind
}

// Traversal это результат обхода графа. Visited содержит каждый узел один раз
// в порядке обнаружения. Edges содержит рёбра, по которым обход вошёл в узел.
type Traversal struct {
	Start     string
	Visited   []string
	Edges     []Edge
	Truncated bool
}

// Предел обхода по умолчанию для TraverseAll. Защищает от очень большого графа.
const DefaultTraversalLimit = 256

// Graph это типизированный граф карточек. Рёбра сгруппированы по узлу,
// поэтому обход и проверка циклов не сканируют весь граф.
type Graph struct {
	out   map[string][]Edge
	count int
}

// NewGraph создаёт пустой граф.
func NewGraph() *Graph {
	return &Graph{out: make(map[string][]Edge)}
}

// Size возвращает число рёбер в графе.
func (g *Graph) Size() int { return g.count }

// AddEdge добавляет ребро. ID проверяются через IsID, поэтому граф принимает
// все семейства: T3.17, S1.001, BS.1, RS-MCP-02, WIRING-485 и UI-31.
// Для waits-for-external вместо IsID проверяется IsExternalGateTarget.
// Пустой или слишком длинный тип, петля и ребро, которое замыкает цикл,
// отклоняются с кодом 1. Повтор того же ребра ничего не меняет.
func (g *Graph) AddEdge(from, to string, kind EdgeKind) error {
	if !IsValidEdgeKind(kind) {
		return Validationf("тип ребра %q пустой или длиннее %d символов", kind, MaxEdgeKindLen)
	}
	if !IsID(from) {
		return Validationf("неверный ID карточки %q", from)
	}
	if kind == EdgeWaitsForExternal {
		if !IsExternalGateTarget(to) {
			return Validationf("неверная цель внешнего гейта %q", to)
		}
	} else if !IsID(to) {
		return Validationf("неверный ID карточки %q", to)
	}
	if from == to {
		return Validationf("петля запрещена: %s -> %s", from, to)
	}
	for _, e := range g.out[from] {
		if e.To == to && e.Kind == kind {
			return nil
		}
	}
	if g.reaches(to, from) {
		return Validationf("цикл: %s -> %s замыкает путь", from, to)
	}
	e := Edge{From: from, To: to, Kind: kind}
	g.out[from] = append(g.out[from], e)
	g.count++
	return nil
}

// reaches сообщает, достижим ли target из start по существующим рёбрам.
// Посещённые узлы хранятся в множестве, поэтому цикл в графе не вызовет зацикливание.
func (g *Graph) reaches(start, target string) bool {
	if start == target {
		return true
	}
	seen := map[string]bool{start: true}
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.out[cur] {
			if e.To == target {
				return true
			}
			if !seen[e.To] {
				seen[e.To] = true
				queue = append(queue, e.To)
			}
		}
	}
	return false
}

// Traverse обходит ребра от start в ширину. UNION означает, что найденный узел
// добавляется в множество посещённых один раз: повторные пути не дают дублей и
// обрывают цикл. LIMIT ограничивает число посещённых узлов. При достижении
// предела обход останавливается и Truncated принимает значение true.
// limit <= 0 снимает ограничение.
func (g *Graph) Traverse(start string, limit int) Traversal {
	result := Traversal{Start: start}
	if !IsID(start) {
		return result
	}
	seen := map[string]bool{start: true}
	result.Visited = append(result.Visited, start)
	queue := []string{start}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range g.out[cur] {
			if seen[e.To] {
				continue
			}
			if limit > 0 && len(result.Visited) >= limit {
				result.Truncated = true
				return result
			}
			seen[e.To] = true
			result.Visited = append(result.Visited, e.To)
			result.Edges = append(result.Edges, e)
			queue = append(queue, e.To)
		}
	}
	return result
}

// TraverseAll обходит граф с пределом по умолчанию.
func (g *Graph) TraverseAll(start string) Traversal {
	return g.Traverse(start, DefaultTraversalLimit)
}

// CycleReport это канонический отчёт о простых циклах. Каждый цикл — список ID,
// повёрнутый так, что первым идёт минимальный ID. Список циклов отсортирован,
// поэтому порядок рёбер на входе не влияет на результат.
type CycleReport struct {
	Cycles [][]string `json:"cycles"`
}

// JSON возвращает байтовое представление отчёта для JSON-ответа. Порядок полей
// и циклов фиксирован, поэтому два отчёта одного графа совпадают побайтово.
func (r CycleReport) JSON() ([]byte, error) {
	return json.Marshal(r)
}

// CanonicalCycleReport строит канонический отчёт по списку рёбер. Функция
// чистая: не меняет вход и не зависит от порядка рёбер. Параллельные рёбра
// (одинаковые from/to) схлопываются, корни и соседи сортируются, каждый цикл
// поворачивается к минимальному ID и разворачивается в каноническое направление.
func CanonicalCycleReport(edges []Edge) CycleReport {
	adj := make(map[string]map[string]bool)
	nodes := make(map[string]bool)
	for _, e := range edges {
		nodes[e.From] = true
		nodes[e.To] = true
		if adj[e.From] == nil {
			adj[e.From] = make(map[string]bool)
		}
		adj[e.From][e.To] = true
	}

	roots := make([]string, 0, len(nodes))
	for id := range nodes {
		roots = append(roots, id)
	}
	sort.Strings(roots)

	neighbors := make(map[string][]string, len(roots))
	for _, from := range roots {
		list := make([]string, 0, len(adj[from]))
		for to := range adj[from] {
			list = append(list, to)
		}
		sort.Strings(list)
		neighbors[from] = list
	}

	cycles := make([][]string, 0)
	seen := make(map[string]bool)
	var walk func(start, cur string, path []string, onPath map[string]bool)
	walk = func(start, cur string, path []string, onPath map[string]bool) {
		for _, next := range neighbors[cur] {
			if next == start {
				cycle := canonicalCycle(path)
				key := strings.Join(cycle, "\x00")
				if !seen[key] {
					seen[key] = true
					cycles = append(cycles, cycle)
				}
				continue
			}
			// Начало пути должно быть минимальным ID в цикле.
			if next < start || onPath[next] {
				continue
			}
			onPath[next] = true
			walk(start, next, append(path, next), onPath)
			delete(onPath, next)
		}
	}
	for _, start := range roots {
		walk(start, start, []string{start}, map[string]bool{start: true})
	}
	sort.Slice(cycles, func(i, j int) bool {
		return compareIDs(cycles[i], cycles[j]) < 0
	})
	return CycleReport{Cycles: cycles}
}

// canonicalCycle приводит путь к канонической форме: первый ID минимален
// (гарантируется обходом), между прямым и обратным направлением берётся
// лексикографически меньшее.
func canonicalCycle(path []string) []string {
	forward := append([]string(nil), path...)
	backward := make([]string, len(path))
	backward[0] = path[0]
	for i := 1; i < len(path); i++ {
		backward[i] = path[len(path)-i]
	}
	if compareIDs(backward, forward) < 0 {
		return backward
	}
	return forward
}

// compareIDs сравнивает списки ID лексикографически.
func compareIDs(a, b []string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// Edges возвращает все рёбра графа. Порядок результата не определён: рёбра
// приходят из карты. Для отчёта используйте CycleReport, который сортирует.
func (g *Graph) Edges() []Edge {
	out := make([]Edge, 0, g.count)
	for _, list := range g.out {
		out = append(out, list...)
	}
	return out
}

// CycleReport возвращает канонический отчёт о циклах графа. Граф, построенный
// через AddEdge, ацикличен, поэтому отчёт пуст; функция остаётся удобной точкой
// входа для JSON-ответа и для графов, загруженных из внешнего источника.
func (g *Graph) CycleReport() CycleReport {
	return CanonicalCycleReport(g.Edges())
}

// ---- CBX-01: post-commit recheck of dependent blocked flags ----
//
// A dependent card holds a "blocked" status while any of its dependencies is
// unresolved. Two writers that read the same snapshot can both leave the flag
// set after their commits. The helpers below read a fresh card set and compute
// the flag again. They never run inside a close transaction.

// resolvedStatus reports whether a status means a card no longer blocks the
// cards that depend on it.
func resolvedStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "done", "dropped", "duplicate":
		return true
	}
	return false
}

// BlockedGraph builds a graph with an edge from each dependency to the card
// that depends on it. Walking from a changed ID therefore reaches every
// dependent that the change can affect. Unusable edges are skipped so a bad ID
// in one card does not hide the rest of the graph.
func BlockedGraph(cards []Card) *Graph {
	g := NewGraph()
	known := make(map[string]bool, len(cards))
	for _, c := range cards {
		known[c.ID] = true
	}
	for _, c := range cards {
		for _, raw := range c.Dependencies {
			dep := strings.TrimSpace(raw)
			if dep == "" || !known[dep] {
				continue
			}
			_ = g.AddEdge(dep, c.ID, EdgeBlocks)
		}
	}
	return g
}

// DependentsOf returns every card that transitively depends on one of ids.
// The result is sorted and has no duplicates. The start IDs themselves are not
// included.
func DependentsOf(g *Graph, ids []string) []string {
	seen := map[string]bool{}
	for _, raw := range ids {
		start := strings.TrimSpace(raw)
		if start == "" {
			continue
		}
		for _, visited := range g.TraverseAll(start).Visited {
			if visited != start {
				seen[visited] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// BlockedDecision is one card's current status and the status it should hold
// after a recheck against the current graph.
type BlockedDecision struct {
	ID      string
	Current string
	Want    string
}

// RecheckDecisions computes the desired status for each card in scope. Only
// todo and blocked cards take part, so in-progress and closed cards keep their
// meaning. The decision works both ways: unresolved dependencies want blocked,
// and a blocked card with only resolved dependencies wants todo.
func RecheckDecisions(cards []Card, scope map[string]bool) []BlockedDecision {
	byID := make(map[string]Card, len(cards))
	for _, c := range cards {
		byID[c.ID] = c
	}
	decisions := make([]BlockedDecision, 0, len(scope))
	for _, c := range cards {
		if !scope[c.ID] || (c.Status != "todo" && c.Status != "blocked") {
			continue
		}
		want := "todo"
		for _, raw := range c.Dependencies {
			dep := strings.TrimSpace(raw)
			if dep == "" {
				continue
			}
			blocker, ok := byID[dep]
			if !ok || !resolvedStatus(blocker.Status) {
				want = "blocked"
				break
			}
		}
		decisions = append(decisions, BlockedDecision{ID: c.ID, Current: c.Status, Want: want})
	}
	return decisions
}

// StaleBlocked returns IDs of cards whose status disagrees with the current
// graph. An empty result means every blocked flag agrees with its blockers.
func StaleBlocked(cards []Card) []string {
	scope := make(map[string]bool, len(cards))
	for _, c := range cards {
		scope[c.ID] = true
	}
	var out []string
	for _, d := range RecheckDecisions(cards, scope) {
		if d.Current != d.Want {
			out = append(out, d.ID)
		}
	}
	return out
}

// SetCardStatus returns raw with its Статус line replaced by status. CRLF line
// endings are preserved. A card without a Статус line is left unchanged.
func SetCardStatus(raw, status string) string {
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		lineCR := ""
		if strings.HasSuffix(line, "\r") {
			lineCR = "\r"
			line = strings.TrimSuffix(line, "\r")
		}
		if !strings.HasPrefix(line, "Статус:") {
			continue
		}
		lines[i] = "Статус: " + status + lineCR
		return strings.Join(lines, "\n")
	}
	return raw
}
