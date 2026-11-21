package taskstore

// EdgeKind задаёт тип ребра в графе карточек. Все рёбра направленные.
type EdgeKind string

const (
	EdgeBlocks     EdgeKind = "blocks"
	EdgeDependsOn  EdgeKind = "depends-on"
	EdgeSupersedes EdgeKind = "supersedes"
	EdgeRelated    EdgeKind = "related"
)

// ValidEdgeKinds возвращает четыре допустимых типа рёбер. Возвращается копия,
// изменение которой не меняет канонический набор.
func ValidEdgeKinds() []EdgeKind {
	return []EdgeKind{EdgeBlocks, EdgeDependsOn, EdgeSupersedes, EdgeRelated}
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

// Edge это одно направленное ребро. From и To это ID карточек любого семейства.
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
// Неизвестный тип, петля и ребро, которое замыкает цикл, отклоняются с кодом 1.
// Повтор того же ребра ничего не меняет.
func (g *Graph) AddEdge(from, to string, kind EdgeKind) error {
	if !IsValidEdgeKind(kind) {
		return Validationf("неизвестный тип ребра %q", kind)
	}
	if !IsID(from) {
		return Validationf("неверный ID карточки %q", from)
	}
	if !IsID(to) {
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
