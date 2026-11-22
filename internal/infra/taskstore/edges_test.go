package taskstore

import (
	"strings"
	"testing"
)

func testGraph(t *testing.T, edges ...Edge) *Graph {
	t.Helper()
	g := NewGraph()
	for _, e := range edges {
		if err := g.AddEdge(e.From, e.To, e.Kind); err != nil {
			t.Fatalf("AddEdge(%+v): %v", e, err)
		}
	}
	return g
}

func TestEdgeKinds(t *testing.T) {
	want := []EdgeKind{EdgeBlocks, EdgeDependsOn, EdgeSupersedes, EdgeRelated, EdgeWaitsForExternal}
	got := ValidEdgeKinds()
	if len(got) != len(want) {
		t.Fatalf("ValidEdgeKinds = %v, ждали %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("тип %d = %q, ждали %q", i, got[i], want[i])
		}
		if !IsValidEdgeKind(want[i]) {
			t.Errorf("IsValidEdgeKind отклонил %q", want[i])
		}
	}
	if IsValidEdgeKind("conflicts-with") {
		t.Error("IsValidEdgeKind принял неизвестный тип")
	}
}

func TestIsExternalGateTarget(t *testing.T) {
	cases := []struct {
		target string
		want   bool
	}{
		{"gh:pr:42", true},
		{"gh:pr:1", true},
		{"gh:run:12345678901", true},
		{"gh:run:abc-def", true},
		{"timer:5m", true},
		{"human:owner", true},
		{"bead:bd-a1b2", true},
		{"gh:pr:", false},
		{"gh:pr:abc", false},
		{"gh:pr:42:extra", false},
		{"gh:run:", false},
		{"gh:other:1", false},
		{"timer:", false},
		{"human:owner:extra", false},
		{"T3.1", false},
		{"gh:pr:42\n", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := IsExternalGateTarget(tc.target); got != tc.want {
			t.Errorf("IsExternalGateTarget(%q) = %t, ждали %t", tc.target, got, tc.want)
		}
	}
}

func TestAddEdgeExternalGate(t *testing.T) {
	cases := []struct{ from, target string }{
		{"T3.1", "gh:pr:42"},
		{"T3.2", "gh:run:ID"},
		{"T3.3", "timer:5m"},
		{"T3.4", "human:owner"},
		{"T3.5", "bead:bd-a1b2"},
	}
	g := NewGraph()
	for _, tc := range cases {
		if err := g.AddEdge(tc.from, tc.target, EdgeWaitsForExternal); err != nil {
			t.Fatalf("AddEdge(%s, %s): %v", tc.from, tc.target, err)
		}
	}
	if g.Size() != len(cases) {
		t.Fatalf("Size = %d, ждали %d", g.Size(), len(cases))
	}
	tr := g.TraverseAll("T3.1")
	if len(tr.Edges) != 1 || tr.Edges[0].Kind != EdgeWaitsForExternal || tr.Edges[0].To != "gh:pr:42" {
		t.Fatalf("обход внешнего гейта: %+v", tr.Edges)
	}
	if len(tr.Visited) != 2 || tr.Visited[1] != "gh:pr:42" {
		t.Fatalf("Visited = %v, ждали gh:pr:42", tr.Visited)
	}
}

func TestAddEdgeExternalGateRejectsBadTarget(t *testing.T) {
	for _, target := range []string{"T3.2", "gh:pr:", "gh:pr:x", "простое слово"} {
		g := NewGraph()
		err := g.AddEdge("T3.1", target, EdgeWaitsForExternal)
		if err == nil {
			t.Errorf("AddEdge(T3.1, %q) не отклонил", target)
			continue
		}
		if Code(err) != ExitValidation || !strings.Contains(err.Error(), "гейт") {
			t.Errorf("ошибка = %q, код = %d", err, Code(err))
		}
		if g.Size() != 0 {
			t.Errorf("граф изменён при ошибке: Size = %d", g.Size())
		}
	}
}

func TestAddEdgeExternalGateKeepsCycleRejection(t *testing.T) {
	g := testGraph(t,
		Edge{From: "T3.1", To: "gh:pr:42", Kind: EdgeWaitsForExternal},
		Edge{From: "T3.1", To: "RS-MCP-02", Kind: EdgeBlocks},
		Edge{From: "RS-MCP-02", To: "WIRING-485", Kind: EdgeDependsOn},
	)
	err := g.AddEdge("WIRING-485", "T3.1", EdgeRelated)
	if err == nil {
		t.Fatal("цикл через внутренние рёбра не отклонён")
	}
	if Code(err) != ExitValidation || !strings.Contains(err.Error(), "цикл") {
		t.Errorf("ошибка = %q, код = %d", err, Code(err))
	}
	if g.Size() != 3 {
		t.Fatalf("цикл изменил граф: Size = %d", g.Size())
	}
}

func TestAddEdgeAllIDFamilies(t *testing.T) {
	ids := []string{"T3.17", "S1.001", "BS.1", "RS-MCP-02", "WIRING-485", "UI-31", "Q-A-03"}
	for _, id := range ids {
		if !IsID(id) {
			t.Fatalf("IsID(%q) = false", id)
		}
	}
	g := NewGraph()
	for i := 0; i+1 < len(ids); i++ {
		if err := g.AddEdge(ids[i], ids[i+1], EdgeDependsOn); err != nil {
			t.Fatalf("AddEdge(%s, %s): %v", ids[i], ids[i+1], err)
		}
	}
	if g.Size() != len(ids)-1 {
		t.Fatalf("Size = %d, ждали %d", g.Size(), len(ids)-1)
	}
}

func TestAddEdgeRejectsBadInput(t *testing.T) {
	cases := []struct {
		from, to string
		kind     EdgeKind
		want     string
	}{
		{"T3.1", "T3.2", "conflicts-with", "тип ребра"},
		{"pages/readme", "T3.2", EdgeBlocks, "ID"},
		{"T3.1", "простое слово", EdgeBlocks, "ID"},
		{"T3.1", "T3.1", EdgeBlocks, "петля"},
	}
	for _, tc := range cases {
		g := NewGraph()
		err := g.AddEdge(tc.from, tc.to, tc.kind)
		if err == nil {
			t.Fatalf("AddEdge(%s, %s, %s) не отклонил", tc.from, tc.to, tc.kind)
		}
		if Code(err) != ExitValidation {
			t.Errorf("код = %d, ждали %d", Code(err), ExitValidation)
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ошибка %q не содержит %q", err.Error(), tc.want)
		}
		if g.Size() != 0 {
			t.Errorf("граф изменён при ошибке: Size = %d", g.Size())
		}
	}
}

func TestAddEdgeRejectsCycleOnWrite(t *testing.T) {
	g := testGraph(t,
		Edge{From: "T3.1", To: "RS-MCP-02", Kind: EdgeBlocks},
		Edge{From: "RS-MCP-02", To: "WIRING-485", Kind: EdgeDependsOn},
	)
	for _, kind := range []EdgeKind{EdgeSupersedes, EdgeRelated} {
		err := g.AddEdge("WIRING-485", "T3.1", kind)
		if err == nil {
			t.Fatalf("цикл через %s не отклонён", kind)
		}
		if Code(err) != ExitValidation || !strings.Contains(err.Error(), "цикл") {
			t.Errorf("ошибка = %q, код = %d", err, Code(err))
		}
	}
	if g.Size() != 2 {
		t.Fatalf("цикл изменил граф: Size = %d", g.Size())
	}
}

func TestAddEdgeDuplicateNoOp(t *testing.T) {
	g := testGraph(t, Edge{From: "BS.1", To: "S1.001", Kind: EdgeRelated})
	if err := g.AddEdge("BS.1", "S1.001", EdgeRelated); err != nil {
		t.Fatalf("повторное ребро: %v", err)
	}
	if g.Size() != 1 {
		t.Fatalf("дубль добавлен: Size = %d", g.Size())
	}
}

func TestTraverseAllIDFamilies(t *testing.T) {
	chain := []string{"T3.17", "RS-MCP-02", "WIRING-485", "BS.1", "S1.001", "UI-31"}
	g := NewGraph()
	for i := 0; i+1 < len(chain); i++ {
		if err := g.AddEdge(chain[i], chain[i+1], EdgeDependsOn); err != nil {
			t.Fatal(err)
		}
	}
	tr := g.TraverseAll(chain[0])
	if len(tr.Visited) != len(chain) {
		t.Fatalf("Visited = %v, ждали %v", tr.Visited, chain)
	}
	for i := range chain {
		if tr.Visited[i] != chain[i] {
			t.Errorf("узел %d = %q, ждали %q", i, tr.Visited[i], chain[i])
		}
	}
	if tr.Truncated {
		t.Error("короткий граф помечен как урезанный")
	}
}

func TestTraverseUnionDedupesDiamond(t *testing.T) {
	g := testGraph(t,
		Edge{From: "T9.1", To: "RS-MCP-02", Kind: EdgeBlocks},
		Edge{From: "T9.1", To: "BS.1", Kind: EdgeSupersedes},
		Edge{From: "RS-MCP-02", To: "S1.001", Kind: EdgeDependsOn},
		Edge{From: "BS.1", To: "S1.001", Kind: EdgeRelated},
	)
	tr := g.TraverseAll("T9.1")
	count := 0
	for _, id := range tr.Visited {
		if id == "S1.001" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("S1.001 посещён %d раз: %v", count, tr.Visited)
	}
	if len(tr.Visited) != 4 {
		t.Errorf("Visited = %v, ждали 4 узла", tr.Visited)
	}
}

func TestTraverseLimit(t *testing.T) {
	chain := []string{"T3.1", "RS-MCP-02", "WIRING-485", "BS.1"}
	g := NewGraph()
	for i := 0; i+1 < len(chain); i++ {
		if err := g.AddEdge(chain[i], chain[i+1], EdgeBlocks); err != nil {
			t.Fatal(err)
		}
	}
	tr := g.Traverse(chain[0], 2)
	if !tr.Truncated {
		t.Fatal("предел обхода не отмечен")
	}
	if len(tr.Visited) != 2 {
		t.Fatalf("Visited = %v, ждали 2 узла", tr.Visited)
	}
	full := g.Traverse(chain[0], 0)
	if full.Truncated || len(full.Visited) != len(chain) {
		t.Fatalf("обход без предела: %+v", full)
	}
}

func TestTraverseUnknownStart(t *testing.T) {
	g := testGraph(t, Edge{From: "T3.1", To: "RS-MCP-02", Kind: EdgeBlocks})
	tr := g.TraverseAll("нет-такого-id")
	if len(tr.Visited) != 0 || len(tr.Edges) != 0 || tr.Truncated {
		t.Fatalf("обход от неизвестного ID: %+v", tr)
	}
}

func cycleKey(cycles [][]string) string {
	parts := make([]string, len(cycles))
	for i, c := range cycles {
		parts[i] = "[" + strings.Join(c, ",") + "]"
	}
	return strings.Join(parts, "|")
}

func TestCanonicalCycleReportStableJSON(t *testing.T) {
	edges := []Edge{
		{From: "T3.1", To: "T3.2", Kind: EdgeBlocks},
		{From: "T3.2", To: "T3.3", Kind: EdgeDependsOn},
		{From: "T3.3", To: "T3.1", Kind: EdgeRelated},
		{From: "T3.1", To: "T3.2", Kind: EdgeSupersedes}, // параллельное ребро
		{From: "T3.4", To: "T3.5", Kind: EdgeBlocks},
		{From: "T3.5", To: "T3.6", Kind: EdgeDependsOn},
		{From: "T3.6", To: "T3.4", Kind: EdgeRelated},
		{From: "T3.7", To: "T3.8", Kind: EdgeBlocks},
		{From: "T3.8", To: "T3.9", Kind: EdgeDependsOn},
		{From: "T3.9", To: "T3.7", Kind: EdgeRelated},
		{From: "T3.2", To: "T3.5", Kind: EdgeSupersedes},
		{From: "T3.9", To: "T3.2", Kind: EdgeRelated},
	}
	if len(edges) != 12 {
		t.Fatalf("ожидали 12 рёбер, получили %d", len(edges))
	}
	reversed := make([]Edge, len(edges))
	for i, e := range edges {
		reversed[len(edges)-1-i] = e
	}

	a := CanonicalCycleReport(edges)
	b := CanonicalCycleReport(reversed)
	ja, err := a.JSON()
	if err != nil {
		t.Fatal(err)
	}
	jb, err := b.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) != string(jb) {
		t.Fatalf("JSON зависит от порядка рёбер:\n%s\n%s", ja, jb)
	}
	want := "[T3.1,T3.2,T3.3]|[T3.4,T3.5,T3.6]|[T3.7,T3.8,T3.9]"
	if got := cycleKey(a.Cycles); got != want {
		t.Fatalf("циклы = %s, ждали %s", got, want)
	}
}

func TestCanonicalCycleReportCollapsesParallelEdges(t *testing.T) {
	edges := []Edge{
		{From: "T3.1", To: "T3.2", Kind: EdgeBlocks},
		{From: "T3.1", To: "T3.2", Kind: EdgeDependsOn},
		{From: "T3.2", To: "T3.1", Kind: EdgeRelated},
	}
	r := CanonicalCycleReport(edges)
	if got, want := cycleKey(r.Cycles), "[T3.1,T3.2]"; got != want {
		t.Fatalf("циклы = %s, ждали %s", got, want)
	}
}

func TestCanonicalCycleReportRotationIsOneEntry(t *testing.T) {
	forward := []Edge{
		{From: "T3.1", To: "T3.2", Kind: EdgeBlocks},
		{From: "T3.2", To: "T3.3", Kind: EdgeBlocks},
		{From: "T3.3", To: "T3.1", Kind: EdgeBlocks},
	}
	rotated := []Edge{
		{From: "T3.2", To: "T3.3", Kind: EdgeBlocks},
		{From: "T3.3", To: "T3.1", Kind: EdgeBlocks},
		{From: "T3.1", To: "T3.2", Kind: EdgeBlocks},
	}
	both := append(append([]Edge(nil), forward...), rotated...)
	for _, edges := range [][]Edge{forward, rotated, both} {
		r := CanonicalCycleReport(edges)
		if got, want := cycleKey(r.Cycles), "[T3.1,T3.2,T3.3]"; got != want {
			t.Fatalf("циклы = %s, ждали %s", got, want)
		}
	}
}

func TestCanonicalCycleReportEmptyForAcyclic(t *testing.T) {
	edges := []Edge{
		{From: "T3.1", To: "T3.2", Kind: EdgeBlocks},
		{From: "T3.2", To: "T3.3", Kind: EdgeDependsOn},
		{From: "T3.1", To: "T3.3", Kind: EdgeSupersedes},
	}
	a := CanonicalCycleReport(edges)
	b := CanonicalCycleReport([]Edge{edges[2], edges[0], edges[1]})
	ja, err := a.JSON()
	if err != nil {
		t.Fatal(err)
	}
	jb, err := b.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(ja) != string(jb) || string(ja) != `{"cycles":[]}` {
		t.Fatalf("пустой отчёт = %s и %s, ждали {\"cycles\":[]}", ja, jb)
	}
}

func TestGraphCycleReportKeepsAddEdgeRejection(t *testing.T) {
	g := testGraph(t,
		Edge{From: "T3.1", To: "T3.2", Kind: EdgeBlocks},
		Edge{From: "T3.2", To: "T3.3", Kind: EdgeDependsOn},
	)
	if err := g.AddEdge("T3.3", "T3.1", EdgeRelated); err == nil {
		t.Fatal("AddEdge должен отклонять цикл")
	}
	if got := g.CycleReport(); len(got.Cycles) != 0 {
		t.Fatalf("граф с запретом цикла дал отчёт: %+v", got)
	}
}
