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
	want := []EdgeKind{EdgeBlocks, EdgeDependsOn, EdgeSupersedes, EdgeRelated}
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
