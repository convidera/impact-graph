package query

import (
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func merged() *graph.Graph {
	shop := graph.New()
	shop.Meta["repo"], shop.Meta["commit"], shop.Meta["built_at"] = "acme/shop", "1111111", "t1"
	shop.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Repo: "acme/shop", Path: "app/Cart.php", Line: 3})
	shop.AddNode(graph.Node{ID: "php:Cart::add", Type: graph.TypeMethod, Label: "Cart::add", Repo: "acme/shop", Path: "app/Cart.php", Line: 5})
	shop.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	shop.AddEdge("php:Cart::add", "event:order", graph.Produces, 0.9, "app/Cart.php:9")
	shop.AddEdge("php:Cart::add", "php:Cart", graph.MemberOf, 1, "app/Cart.php:5")
	api := graph.New()
	api.Meta["repo"], api.Meta["commit"], api.Meta["built_at"] = "acme/api", "2222222", "t2"
	api.AddNode(graph.Node{ID: "php:Order", Type: graph.TypeClass, Label: "Order", Repo: "acme/api", Path: "app/Order.php", Line: 3})
	api.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	api.AddEdge("php:Order", "event:order", graph.Consumes, 0.9, "app/Order.php:7")
	lone := graph.New()
	lone.Meta["repo"] = "acme/lone"
	lone.AddNode(graph.Node{ID: "php:Lone", Type: graph.TypeClass, Label: "Lone", Repo: "acme/lone", Path: "Lone.php", Line: 1})
	g := graph.Merge(map[string]*graph.Graph{"acme/shop": shop, "acme/api": api, "acme/lone": lone})
	g.Index()
	return g
}

func TestRepositoriesSummarisesAndConnects(t *testing.T) {
	r := New(merged()).Repositories()
	if len(r.Repositories) != 3 || r.Repositories[0].Name != "acme/api" || r.Repositories[1].Commit != "" && r.Repositories[1].Name == "acme/lone" {
		t.Fatalf("repositories sorted by name: %+v", r.Repositories)
	}
	var shop Repository
	for _, x := range r.Repositories {
		if x.Name == "acme/shop" {
			shop = x
		}
	}
	if shop.Commit != "1111111" || shop.BuiltAt != "t1" || shop.Nodes != 2 || shop.Edges != 2 {
		t.Errorf("shop: %+v", shop)
	}
	if shop.Types[0].Type != graph.TypeClass && shop.Types[0].Type != graph.TypeMethod {
		t.Errorf("types: %+v", shop.Types)
	}
	if r.SharedValues != 1 || r.SharedTypes[0] != (TypeCount{graph.TypeValue, 1}) {
		t.Errorf("shared: %d %+v", r.SharedValues, r.SharedTypes)
	}
	conn := map[[2]string]Connection{}
	for _, c := range r.Connections {
		conn[[2]string{c.From, c.To}] = c
	}
	if len(conn) != 3 || conn[[2]string{"acme/api", "acme/shop"}].SharedValues != 1 || conn[[2]string{"acme/api", "acme/lone"}].SharedValues != 0 {
		t.Errorf("connections: %+v", r.Connections)
	}
}

func TestRepositoriesOfASingleSnapshot(t *testing.T) {
	g := graph.New()
	g.Meta["repo"], g.Meta["commit"] = "acme/solo", "abc"
	g.AddNode(graph.Node{ID: "php:A", Type: graph.TypeClass, Label: "A", Repo: "acme/solo"})
	r := New(g).Repositories()
	if len(r.Repositories) != 1 || r.Repositories[0].Name != "acme/solo" || r.Repositories[0].Commit != "abc" || len(r.Connections) != 0 {
		t.Errorf("%+v", r)
	}
}

func TestSearchRanksByDegreeAndFilters(t *testing.T) {
	x := New(merged())
	all, err := x.Search("", "", "", 2)
	if err != nil || len(all.Nodes) != 2 || !all.Truncated {
		t.Fatalf("a limit truncates: %+v %v", all, err)
	}
	if all.Nodes[0].Degree != 2 || all.Nodes[1].Degree != 2 {
		t.Errorf("the most connected nodes first (ties by id): %+v", all.Nodes)
	}
	cart, _ := x.Search("shop", "CART", "", 0)
	if len(cart.Nodes) != 2 {
		t.Errorf("case-insensitive, narrowed to a repository (a unique name works): %+v", cart.Nodes)
	}
	if byType, _ := x.Search("", "cart", graph.TypeMethod, 0); len(byType.Nodes) != 1 || byType.Nodes[0].Label != "Cart::add" {
		t.Errorf("type filter: %+v", byType.Nodes)
	}
	// the repository prefix of an id is not searchable text
	if hit, _ := x.Search("", "acme", "", 0); len(hit.Nodes) != 0 {
		t.Errorf("the id prefix is not matched: %+v", hit.Nodes)
	}
	if _, err := x.Search("nope", "", "", 0); err == nil {
		t.Errorf("an unknown repository is an error")
	}
	if big, _ := x.Search("", "", "", 5000); len(big.Nodes) > MaxNodes {
		t.Errorf("the limit is capped")
	}
}

func TestDetailListsNeighboursBestFirstAndCaps(t *testing.T) {
	g := graph.New()
	g.AddNode(graph.Node{ID: "hub", Type: graph.TypeFunction, Label: "hub"})
	for i := 0; i < MaxEdges+5; i++ {
		id := string(rune('a'+i%26)) + string(rune('a'+i/26))
		g.AddNode(graph.Node{ID: id, Type: graph.TypeFunction, Label: id})
		g.AddEdge("hub", id, graph.Calls, 0.5+float64(i)/1000, "x:1")
	}
	g.AddNode(graph.Node{ID: "caller", Type: graph.TypeFunction, Label: "caller"})
	g.AddEdge("caller", "hub", graph.Calls, 0.95, "y:2")
	g.Index()
	d, err := New(g).Detail("hub")
	if err != nil {
		t.Fatal(err)
	}
	if d.OutgoingTotal != MaxEdges+5 || len(d.Outgoing) != MaxEdges || d.IncomingTotal != 1 || d.Incoming[0].Node.ID != "caller" {
		t.Errorf("totals and cap: out %d/%d in %d", len(d.Outgoing), d.OutgoingTotal, d.IncomingTotal)
	}
	if d.Outgoing[0].Confidence < d.Outgoing[1].Confidence || d.Outgoing[0].Evidence != "x:1" || d.Outgoing[0].Provenance == "" {
		t.Errorf("best confidence first, evidence and provenance carried: %+v", d.Outgoing[:2])
	}
	if _, err := New(g).Detail("missing"); err != ErrNodeNotFound {
		t.Errorf("missing node: %v", err)
	}
}
