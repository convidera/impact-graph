package report_test

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/report"
)

// spanningGraph merges two repositories that share the value event:order, and puts their six symbols and the value into one
// community: the whole has 7 members and cohesion 1.00, each repository 3 members and cohesion 2/3 on its own.
func spanningGraph(t *testing.T) *graph.Graph {
	t.Helper()
	shop, api := graph.New(), graph.New()
	shop.Meta = map[string]string{"repo": "acme/shop", "commit": "1111111aaaa"}
	api.Meta = map[string]string{"repo": "acme/api", "commit": "2222222bbbb"}
	shop.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3})
	for _, id := range []string{"php:Cart::add", "php:Cart::total"} {
		shop.AddNode(graph.Node{ID: id, Type: graph.TypeMethod, Label: id, Path: "app/Cart.php", Line: 3})
	}
	shop.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	shop.AddEdge("php:Cart::add", "php:Cart::total", graph.Calls, 0.9, "app/Cart.php:6")
	shop.AddEdge("php:Cart::add", "event:order", graph.Produces, 0.9, "app/Cart.php:9")
	api.AddNode(graph.Node{ID: "php:Order", Type: graph.TypeClass, Label: "Order", Path: "app/Order.php", Line: 3})
	for _, id := range []string{"php:Order::handle", "php:Order::save"} {
		api.AddNode(graph.Node{ID: id, Type: graph.TypeMethod, Label: id, Path: "app/Order.php", Line: 3})
	}
	api.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	api.AddEdge("php:Order::handle", "php:Order::save", graph.Calls, 0.9, "app/Order.php:6")
	api.AddEdge("php:Order::handle", "event:order", graph.Consumes, 0.9, "app/Order.php:7")

	g := graph.Merge(map[string]*graph.Graph{"acme/shop": shop, "acme/api": api})
	members := []string{"acme/shop//php:Cart", "acme/shop//php:Cart::add", "acme/shop//php:Cart::total",
		"acme/api//php:Order", "acme/api//php:Order::handle", "acme/api//php:Order::save", "event:order"}
	for _, id := range members {
		g.Nodes[id].Props = map[string]string{"community": "0"}
	}
	g.Communities = []graph.Community{{ID: 0, Label: "app · Cart, Order", Dir: "app", Size: len(members), Cohesion: 1,
		Top: []string{"acme/shop//php:Cart::add", "acme/api//php:Order::handle", "event:order"}}}
	g.Index()
	return g
}

func TestCommunitySpanningRepositoriesShowsEachRepositoryItsOwnShare(t *testing.T) {
	g := spanningGraph(t)

	whole := report.CommunityIn(g, "", 0, 10)
	for _, want := range []string{"size      7 nodes\n", "cohesion  1.00\n", "acme/api//php:Order::handle"} {
		if !strings.Contains(whole, want) {
			t.Errorf("unfiltered community lacks %q:\n%s", want, whole)
		}
	}

	for _, repo := range []string{"acme/shop", "acme/api"} {
		out := report.CommunityIn(g, repo, 0, 10)
		for _, want := range []string{"size      3 nodes in " + repo + " (the community has 7)\n", "cohesion  0.67\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: community lacks %q:\n%s", repo, want, out)
			}
		}
		other := "acme/api"
		if repo == "acme/api" {
			other = "acme/shop"
		}
		if strings.Contains(out, other+"//") || strings.Contains(out, "event:order") {
			t.Errorf("%s: the community lists members of %s or a shared value:\n%s", repo, other, out)
		}
		if !strings.Contains(out, "method (2)\n") || !strings.Contains(out, "class (1)\n") {
			t.Errorf("%s: members by type must count only its own members:\n%s", repo, out)
		}
	}
	if shop := report.CommunityIn(g, "acme/shop", 0, 10); !strings.Contains(shop, "acme/shop//php:Cart::add") || strings.Contains(shop, "acme/api//php:Order::handle") {
		t.Errorf("top members of acme/shop:\n%s", shop)
	}
}

func TestCommunitiesListsASpanningCommunityForEachRepository(t *testing.T) {
	g := spanningGraph(t)
	for _, repo := range []string{"acme/shop", "acme/api"} {
		out := report.CommunitiesIn(g, repo, 0)
		if !strings.HasPrefix(out, "communities (1):\n") || !strings.Contains(out, "c0  3 nodes  cohesion 0.67  app · Cart, Order") {
			t.Errorf("%s: a community is listed with the repository's own size and cohesion:\n%s", repo, out)
		}
	}
	if all := report.Communities(g, 0); !strings.Contains(all, "c0  7 nodes  cohesion 1.00") {
		t.Errorf("unfiltered communities:\n%s", all)
	}
	overview := report.OverviewIn(g, "acme/api", 8, 8)
	if !strings.Contains(overview, "communities (1):\nc0  3 nodes  cohesion 0.67") {
		t.Errorf("overview of acme/api:\n%s", overview)
	}
}
