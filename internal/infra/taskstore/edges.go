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

// ValidEdgeKinds возвращает допустимые типы рёбер. Возвращается копия,
// изменение которой не меняет канонический набор.
func ValidEdgeKinds() []EdgeKind {
	return []EdgeKind{EdgeBlocks, EdgeDependsOn, EdgeSupersedes, EdgeRelated, EdgeWaitsForExternal}
}

// IsValidEdgeKind сообщает, известен ли тип ребра.
func IsValidEdgeKind(kind EdgeKind) bool {
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
// Неизвестный тип, петля и ребро, которое замыкает цикл, отклоняются с кодом 1.
// Повтор того же ребра ничего не меняет.
func (g *Graph) AddEdge(from, to string, kind EdgeKind) error {
	if !IsValidEdgeKind(kind) {
		return Validationf("неизвестный тип ребра %q", kind)
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
