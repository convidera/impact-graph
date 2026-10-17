package review

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func TestReviewOfOneRepositoryOfAMergedGraph(t *testing.T) {
	shop := graph.New()
	shop.AddNode(graph.Node{ID: "php:Cart::add", Type: graph.TypeMethod, Label: "Cart::add", Repo: "acme/shop", Path: "app/Cart.php", Line: 5, Props: map[string]string{"end": "9"}})
	shop.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	shop.AddNode(graph.Node{ID: "file:README.md", Type: graph.TypeDoc, Label: "README.md", Repo: "acme/shop", Path: "README.md", Props: map[string]string{"file_kind": "doc"}})
	shop.AddEdge("php:Cart::add", "event:order", graph.Produces, 0.9, "app/Cart.php:9")
	api := graph.New()
	api.AddNode(graph.Node{ID: "php:Order::apply", Type: graph.TypeMethod, Label: "Order::apply", Repo: "acme/api", Path: "app/Cart.php", Line: 2, Props: map[string]string{"end": "4"}})
	api.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	api.AddEdge("php:Order::apply", "event:order", graph.Consumes, 0.9, "app/Cart.php:3")
	g := graph.Merge(map[string]*graph.Graph{"acme/shop": shop, "acme/api": api})
	g.Index()

	rep := Run(g, "main", []Change{{File: "app/Cart.php", Ranges: [][2]int{{6, 6}}}}, Options{MaxCost: 5, MinConf: 0.7, Together: 0.6, PerGroup: 12, Repo: "acme/shop"})
	if len(rep.Nodes) != 1 || !strings.HasPrefix(rep.Nodes[0], "acme/shop//") {
		t.Fatalf("only the nodes of the reviewed repository match the changed file, though acme/api has a file of the same path: %v", rep.Nodes)
	}
	var consumer *Item
	for i := range rep.Items {
		if rep.Items[i].Repo == "acme/api" {
			consumer = &rep.Items[i]
		}
	}
	if consumer == nil || consumer.Path != "app/Cart.php" {
		t.Fatalf("the consumer in the other repository is found, with its repository, although its path equals a changed path: %+v", rep.Items)
	}
	if out := Text(rep, 5); !strings.Contains(out, "acme/api: app/Cart.php") {
		t.Errorf("an item of another repository is labelled:\n%s", out)
	}
}
