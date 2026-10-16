package mcp

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func testGraph() *graph.Graph {
	g := graph.New()
	g.AddNode(graph.Node{ID: "php:A::run", Type: graph.TypeMethod, Label: "A::run", Path: "a.php", Line: 3})
	g.AddNode(graph.Node{ID: "column:t.c", Type: graph.TypeColumn, Label: "t.c", Path: "m.php", Line: 1})
	g.AddNode(graph.Node{ID: "php:B::read", Type: graph.TypeMethod, Label: "B::read", Path: "b.php", Line: 5})
	g.AddEdge("php:A::run", "column:t.c", graph.Writes, 0.95, "a.php:4")
	g.AddEdge("php:B::read", "column:t.c", graph.Reads, 0.95, "b.php:6")
	g.Index()
	return g
}

func call(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad JSON line %q: %v", l, err)
		}
		res = append(res, m)
	}
	return res
}

func text(r map[string]any) string {
	c := r["result"].(map[string]any)["content"].([]any)
	return c[0].(map[string]any)["text"].(string)
}

func TestServeToolsAndTrace(t *testing.T) {
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return testGraph(), nil }, Reindex: func() (string, error) { return "ok", nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_trace","arguments":{"from":"A::run","to":"B::read"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_impact","arguments":{"node":"nothing-here"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	if len(res) != 5 {
		t.Fatalf("a notification gets no reply: got %d replies", len(res))
	}
	tools := res[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, x := range tools {
		names[x.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"impact_find", "impact_impact", "impact_trace", "impact_review", "impact_reindex"} {
		if !names[n] {
			t.Errorf("tool %s is not listed", n)
		}
	}
	if got := text(res[2]); !strings.Contains(got, "column:t.c") || !strings.Contains(got, "php:B::read") {
		t.Errorf("trace through the column expected, got:\n%s", got)
	}
	if res[3]["result"].(map[string]any)["isError"] != true {
		t.Errorf("an unknown node is a tool error, not a protocol error: %v", res[3])
	}
	if res[4]["error"] == nil {
		t.Errorf("an unknown method is a protocol error")
	}
}

func TestServeCommunityTools(t *testing.T) {
	g := testGraph()
	// the test graph has three symbols, below the default MinSize of 5
	g.Cluster(graph.ClusterOptions{MinSize: 2})
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return g, nil }, Reindex: func() (string, error) { return "ok", nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"impact_overview","arguments":{"communities":2,"gods":3}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_community","arguments":{"id":0}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_community","arguments":{"id":99}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"impact_god_nodes","arguments":{"top":2,"type":"method"}}}`,
	)
	names := map[string]bool{}
	for _, x := range res[0]["result"].(map[string]any)["tools"].([]any) {
		names[x.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"impact_overview", "impact_community", "impact_god_nodes"} {
		if !names[n] {
			t.Errorf("tool %s is not listed", n)
		}
	}
	if got := text(res[1]); !strings.Contains(got, "communities (") || !strings.Contains(got, "god nodes:") {
		t.Errorf("overview:\n%s", got)
	}
	if got := text(res[2]); !strings.Contains(got, "members by type") {
		t.Errorf("community 0:\n%s", got)
	}
	if got := text(res[3]); got != "no community 99" {
		t.Errorf("unknown community = %q", got)
	}
	if got := text(res[4]); !strings.Contains(got, "degree") || strings.Contains(got, "table") {
		t.Errorf("god nodes of type method:\n%s", got)
	}
}
