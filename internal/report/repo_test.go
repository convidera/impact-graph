package report_test

import (
	"strconv"
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/report"
)

// mergedShopAndAPI merges two repositories that share one event value and both have an orders.total column, which must
// not join them.
func mergedShopAndAPI(t *testing.T) *graph.Graph {
	t.Helper()
	shop := graph.New()
	shop.Meta = map[string]string{"repo": "acme/shop", "commit": "1111111aaaa", "built_at": "2026-10-16T10:00:00Z"}
	shop.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3})
	shop.AddNode(graph.Node{ID: "php:Cart::add", Type: graph.TypeMethod, Label: "Cart::add", Path: "app/Cart.php", Line: 5})
	shop.AddNode(graph.Node{ID: "column:orders.total", Type: graph.TypeColumn, Label: "orders.total", Path: "db/m.php", Line: 1})
	shop.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	shop.AddEdge("php:Cart", "php:Cart::add", graph.MemberOf, 1, "")
	shop.AddEdge("php:Cart::add", "column:orders.total", graph.Writes, 0.95, "app/Cart.php:6")
	shop.AddEdge("php:Cart::add", "event:order", graph.Produces, 0.9, "app/Cart.php:9")

	api := graph.New()
	api.Meta = map[string]string{"repo": "acme/api", "commit": "2222222bbbb", "built_at": "2026-10-16T11:00:00Z"}
	api.AddNode(graph.Node{ID: "php:Order", Type: graph.TypeClass, Label: "Order", Path: "app/Order.php", Line: 3})
	api.AddNode(graph.Node{ID: "php:Order::handle", Type: graph.TypeMethod, Label: "Order::handle", Path: "app/Order.php", Line: 5})
	api.AddNode(graph.Node{ID: "column:orders.total", Type: graph.TypeColumn, Label: "orders.total", Path: "db/m.php", Line: 1})
	api.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	api.AddEdge("php:Order", "php:Order::handle", graph.MemberOf, 1, "")
	api.AddEdge("php:Order::handle", "column:orders.total", graph.Reads, 0.95, "app/Order.php:6")
	api.AddEdge("php:Order::handle", "event:order", graph.Consumes, 0.9, "app/Order.php:7")

	g := graph.Merge(map[string]*graph.Graph{"acme/shop": shop, "acme/api": api})
	g.Cluster(graph.ClusterOptions{MinSize: 1})
	return g
}

func TestFindListsOnlyTheRepositoryAsked(t *testing.T) {
	g := mergedShopAndAPI(t)
	restricted := report.FindIn(g, "acme/api", "order", 0)
	if !strings.Contains(restricted, "acme/api//php:Order") || strings.Contains(restricted, "acme/shop//") || strings.Contains(restricted, "event:order") {
		t.Errorf("find in acme/api:\n%s", restricted)
	}
	if all := report.Find(g, "order", 0); !strings.Contains(all, "event:order") || !strings.Contains(all, "acme/api//php:Order") {
		t.Errorf("find without a repository:\n%s", all)
	}
}

func TestOverviewOfOneRepositoryCountsItsOwnNodesAndEdges(t *testing.T) {
	g := mergedShopAndAPI(t)
	out := report.OverviewIn(g, "acme/api", 8, 8)
	for _, want := range []string{"acme/api @ 2222222b", "nodes 3, edges 3", "shared values: 1 (reached from this repository)", "god nodes:", "contracts: tables=0 columns=1 routes=0 mcp-tools=0 values=1"} {
		if !strings.Contains(out, want) {
			t.Errorf("overview of acme/api lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "acme/shop") || strings.Contains(out, "Cart") {
		t.Errorf("overview of acme/api lists the other repository:\n%s", out)
	}
}

func TestCommunitiesBelongToOneRepositoryEach(t *testing.T) {
	g := mergedShopAndAPI(t)
	count := func(repo string) int {
		lines := strings.Split(strings.TrimSpace(report.CommunitiesIn(g, repo, 0)), "\n")
		return len(lines) - 1 // the header

	}
	if shop, api := count("acme/shop"), count("acme/api"); shop+api != len(g.Communities) {
		t.Errorf("every community is listed for exactly one repository: %d + %d, want %d", shop, api, len(g.Communities))
	}
	shopOnly, apiOnly := 0, 0
	for id := range g.Communities {
		if !strings.Contains(report.CommunityIn(g, "acme/shop", id, 10), "no community") {
			shopOnly++
		}
		if !strings.Contains(report.CommunityIn(g, "acme/api", id, 10), "no community") {
			apiOnly++
		}
	}
	if shopOnly == 0 || apiOnly == 0 {
		t.Errorf("both repositories own communities: shop %d, api %d", shopOnly, apiOnly)
	}
}

func TestCommunityOfAnotherRepositoryIsNotFound(t *testing.T) {
	g := mergedShopAndAPI(t)
	for id := range g.Communities {
		if !strings.Contains(report.CommunityIn(g, "acme/shop", id, 10), "no community") {
			continue
		}
		if got := report.CommunityIn(g, "acme/shop", id, 10); got != "no community "+strconv.Itoa(id)+" in acme/shop" {
			t.Errorf("community %d of another repository = %q", id, got)
		}
		return
	}
	t.Fatal("no community of the test graph belongs to acme/api only")
}

func TestRepoBlockSummarisesTheMerge(t *testing.T) {
	g := mergedShopAndAPI(t)
	want := strings.Join([]string{
		"  acme/api  3 nodes  3 edges  commit 2222222",
		"  acme/shop  3 nodes  3 edges  commit 1111111",
		"  shared values: 1",
		"  cross-repository edges: 0",
		"  connected through shared values: 1",
		"",
	}, "\n")
	if got := report.RepoBlock(g); got != want {
		t.Errorf("RepoBlock =\n%s\nwant\n%s", got, want)
	}
	if out := report.Overview(g, 8, 8); !strings.HasPrefix(out, "merged 2 repositories") || !strings.Contains(out, want) {
		t.Errorf("the overview of a merge starts with the block:\n%s", out)
	}
	if out := report.OverviewIn(g, "acme/api", 8, 8); strings.Contains(out, "cross-repository edges") {
		t.Errorf("a restricted overview has no summary block:\n%s", out)
	}
}

func TestRepoBlockSaysWhenNothingConnects(t *testing.T) {
	a := graph.New()
	a.Meta = map[string]string{"repo": "acme/shop", "commit": "1111111aaaa"}
	a.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3})
	b := graph.New()
	b.Meta = map[string]string{"repo": "acme/api", "commit": "2222222bbbb"}
	b.AddNode(graph.Node{ID: "php:Order", Type: graph.TypeClass, Label: "Order", Path: "app/Order.php", Line: 3})
	g := graph.Merge(map[string]*graph.Graph{"acme/shop": a, "acme/api": b})
	want := "note: these repositories share no contract with each other; a query in one never reaches the other.\n"
	if got := report.RepoBlock(g); !strings.HasSuffix(got, "  connected through shared values: 0\n"+want) || !strings.Contains(got, "  cross-repository edges: 0\n") {
		t.Errorf("RepoBlock of two unconnected repositories:\n%s", got)
	}
}

func TestRepoBlockListsTheUnconnectedPairs(t *testing.T) {
	shop, api, lone := graph.New(), graph.New(), graph.New()
	shop.Meta = map[string]string{"repo": "acme/shop"}
	api.Meta = map[string]string{"repo": "acme/api"}
	lone.Meta = map[string]string{"repo": "other/lone", "commit": "3333333ccc"}
	shop.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	shop.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3})
	shop.AddEdge("php:Cart", "event:order", graph.Produces, 0.9, "app/Cart.php:9")
	api.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	api.AddNode(graph.Node{ID: "php:Order", Type: graph.TypeClass, Label: "Order", Path: "app/Order.php", Line: 3})
	api.AddEdge("php:Order", "event:order", graph.Consumes, 0.9, "app/Order.php:7")
	lone.AddNode(graph.Node{ID: "php:Lone", Type: graph.TypeClass, Label: "Lone", Path: "Lone.php", Line: 1})
	g := graph.Merge(map[string]*graph.Graph{"acme/shop": shop, "acme/api": api, "other/lone": lone})
	out := report.RepoBlock(g)
	if !strings.Contains(out, "\n  connected through shared values: 1\nnot connected: acme/api <-> other/lone, acme/shop <-> other/lone\n") {
		t.Errorf("unconnected pairs:\n%s", out)
	}
	if strings.Contains(out, "note:") {
		t.Errorf("a partly connected merge gets the pairs, not the note:\n%s", out)
	}
}

func TestRepoBlockIsEmptyForASnapshotAndForOneRepository(t *testing.T) {
	if got := report.RepoBlock(testGraph()); got != "" {
		t.Errorf("a snapshot has no block: %q", got)
	}
	one := graph.Merge(map[string]*graph.Graph{"acme/shop": testGraph()})
	if got := report.RepoBlock(one); got != "" {
		t.Errorf("a merge of one repository has no block: %q", got)
	}
}

func TestFindSaysWhereTheHitsComeFrom(t *testing.T) {
	g := mergedShopAndAPI(t)
	out := report.Find(g, "orders", 0)
	if !strings.HasPrefix(out, "2 hits: 1 in acme/api, 1 in acme/shop (use repository to restrict)\n") {
		t.Errorf("find across two repositories:\n%s", out)
	}
	if restricted := report.FindIn(g, "acme/api", "orders", 0); strings.Contains(restricted, "hits:") {
		t.Errorf("a restricted find has no count line:\n%s", restricted)
	}
	if single := report.Find(g, "Cart", 0); strings.Contains(single, "hits:") {
		t.Errorf("hits of one repository have no count line:\n%s", single)
	}
	if snapshot := report.Find(testGraph(), "A", 0); strings.Contains(snapshot, "hits:") {
		t.Errorf("a snapshot has no count line:\n%s", snapshot)
	}
}
