package report_test

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/report"
)

// testGraph is two communities: c0 (the class and its two methods, in app/) and c1 (a column and its table).
func testGraph() *graph.Graph {
	g := graph.New()
	g.AddNode(graph.Node{ID: "php:A", Type: graph.TypeClass, Label: "A", Path: "app/a.php", Line: 1, Props: map[string]string{"community": "0"}})
	g.AddNode(graph.Node{ID: "php:A::run", Type: graph.TypeMethod, Label: "A::run", Path: "app/a.php", Line: 3, Props: map[string]string{"community": "0"}})
	g.AddNode(graph.Node{ID: "php:B::read", Type: graph.TypeMethod, Label: "B::read", Path: "app/b.php", Line: 5, Props: map[string]string{"community": "0"}})
	g.AddNode(graph.Node{ID: "column:t.c", Type: graph.TypeColumn, Label: "t.c", Path: "db/m.php", Line: 1, Props: map[string]string{"community": "1"}})
	g.AddNode(graph.Node{ID: "table:t", Type: graph.TypeTable, Label: "t", Path: "db/m.php", Line: 1, Props: map[string]string{"community": "1"}})
	g.AddNode(graph.Node{ID: "file:app/a.php", Type: graph.TypeFile, Label: "app/a.php", Path: "app/a.php"})
	g.AddEdge("php:A::run", "column:t.c", graph.Writes, 0.95, "app/a.php:4")
	g.AddEdge("php:B::read", "column:t.c", graph.Reads, 0.95, "app/b.php:6")
	g.AddEdge("php:A", "php:A::run", graph.MemberOf, 1, "")
	g.AddEdge("php:B::read", "php:A::run", graph.Calls, 0.9, "app/b.php:6")
	g.Communities = []graph.Community{
		{ID: 1, Label: "db · t", Dir: "db", Size: 2, Cohesion: 0.9, Top: []string{"column:t.c", "table:t"}},
		{ID: 0, Label: "app · A, A::run", Dir: "app", Size: 3, Cohesion: 0.5, Top: []string{"php:A::run", "php:A", "php:B::read"}},
	}
	g.Meta = map[string]string{
		"repo": "acme/shop", "commit": "0123456789abcdef", "built_at": "2026-10-16T10:00:00Z",
		"schema_version": "2", "engine_version": "dev",
	}
	g.Index()
	return g
}

func TestCommunitiesListsLargestFirst(t *testing.T) {
	out := report.Communities(testGraph(), 0)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if lines[0] != "communities (2):" {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 3 || !strings.HasPrefix(lines[1], "c0  3 nodes  cohesion 0.50  app · A") || !strings.HasPrefix(lines[2], "c1  2 nodes  cohesion 0.90  db · t") {
		t.Errorf("communities out of order or misformatted:\n%s", out)
	}
	if top := report.Communities(testGraph(), 1); strings.Contains(top, "c1  2 nodes") || !strings.Contains(top, "... 1 more") {
		t.Errorf("top 1 must show only the largest:\n%s", top)
	}
}

func TestCommunityDescribesMembers(t *testing.T) {
	g := testGraph()
	out := report.Community(g, 0, 10)
	for _, want := range []string{"c0  app · A, A::run", "dir       app", "size      3 nodes", "cohesion  0.50", "php:A::run  app/a.php:3", "method (2)", "    php:B::read", "class (1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("community output lacks %q:\n%s", want, out)
		}
	}
	if capped := report.Community(g, 0, 1); !strings.Contains(capped, "... 1 more") {
		t.Errorf("limit 1 must cap the ids of a type:\n%s", capped)
	}
	if got := report.Community(g, 9, 10); got != "no community 9" {
		t.Errorf("unknown community = %q", got)
	}
}

func TestGodNodesTable(t *testing.T) {
	g := testGraph()
	out := report.GodNodes(g, g.GodNodes(0, graph.GodOptions{}))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if !strings.HasPrefix(lines[0], "degree") || !strings.Contains(lines[0], "community") || !strings.HasSuffix(lines[0], "path:line") {
		t.Errorf("header = %q", lines[0])
	}
	if len(lines) != 4 || !strings.Contains(lines[1], "column:t.c") || !strings.Contains(lines[1], "db/m.php:1") || !strings.Contains(lines[1], "c1") {
		t.Errorf("the most connected node comes first (ties by id):\n%s", out)
	}
	if !strings.Contains(lines[2], "php:A::run") || !strings.Contains(lines[2], "app/a.php:3") {
		t.Errorf("the second row is php:A::run:\n%s", out)
	}
	if empty := report.GodNodes(g, nil); empty != "no god nodes\n" {
		t.Errorf("empty = %q", empty)
	}
}

func TestOverviewIsTheRepositoryMap(t *testing.T) {
	out := report.Overview(testGraph(), 8, 8)
	lines := strings.Split(out, "\n")
	if lines[0] != "acme/shop @ 01234567 built 2026-10-16T10:00:00Z (schema 2, engine dev)" {
		t.Errorf("first line = %q", lines[0])
	}
	for _, want := range []string{"nodes 6, edges 4", "layers: file=1 code=3 contract=2", "types:  class=1 column=1 file=1 method=2 table=1", "communities (2):", "    top: A::run, A, B::read", "god nodes:", "contracts: tables=1 columns=1 routes=0 mcp-tools=0 values=0"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview lacks %q:\n%s", want, out)
		}
	}
	if n := len(lines); n > 60 {
		t.Errorf("overview is %d lines, want at most 60", n)
	}
}

func TestOverviewOmitsMissingMeta(t *testing.T) {
	g := graph.New()
	g.Meta = map[string]string{"repo": "acme/shop"}
	g.Index()
	if first := strings.Split(report.Overview(g, 8, 8), "\n")[0]; first != "acme/shop" {
		t.Errorf("first line = %q, want only the repository", first)
	}
}

func TestImpactNamesTheCommunity(t *testing.T) {
	g := testGraph()
	opts := graph.DefaultOptions()
	hits, hubs := g.Walk("php:A::run", opts)
	out := report.Impact(g, "php:A::run", hits, hubs, opts, 10, false, false, "")
	if !strings.Contains(out, "\ncommunity c0: app · A, A::run\n") {
		t.Errorf("impact lacks the community line:\n%s", out)
	}
	hits, hubs = g.Walk("file:app/a.php", opts)
	if out := report.Impact(g, "file:app/a.php", hits, hubs, opts, 10, false, false, ""); strings.Contains(out, "community c") {
		t.Errorf("a file is in no community:\n%s", out)
	}
}
