package graph

import (
	"fmt"
	"reflect"
	"testing"
)

func godIDs(list []GodNode) []string {
	var out []string
	for _, n := range list {
		out = append(out, n.ID)
	}
	return out
}

func findGod(t *testing.T, list []GodNode, id string) GodNode {
	t.Helper()
	for _, n := range list {
		if n.ID == id {
			return n
		}
	}
	t.Fatalf("%s is not in %v", id, godIDs(list))
	return GodNode{}
}

// centerGraph: center is connected to five distinct functions (n1, n2 out; n3, n4 in; n5 both ways), and leaf calls
// n1 and n2.
func centerGraph() *Graph {
	g := New()
	for _, id := range []string{"center", "n1", "n2", "n3", "n4", "n5", "leaf", "other"} {
		addFunc(g, id, "x.php")
	}
	g.AddEdge("center", "n1", Calls, 0.9, "")
	g.AddEdge("center", "n2", Calls, 0.9, "")
	g.AddEdge("n3", "center", Calls, 0.9, "")
	g.AddEdge("n4", "center", Calls, 0.9, "")
	g.AddEdge("center", "n5", Calls, 0.9, "")
	g.AddEdge("n5", "center", Requests, 0.9, "")
	g.AddEdge("leaf", "n1", Calls, 0.9, "")
	g.AddEdge("leaf", "n2", Calls, 0.9, "")
	return g
}

func TestGodNodesRanksByDistinctNeighbours(t *testing.T) {
	got := centerGraph().GodNodes(0, GodOptions{})
	if got[0].ID != "center" {
		t.Fatalf("center must rank first, got %v", godIDs(got))
	}
	c := got[0]
	if c.Degree != 5 || c.In != 3 || c.Out != 3 {
		t.Fatalf("center: degree %d in %d out %d, want 5, 3, 3", c.Degree, c.In, c.Out)
	}
	if want := []string{"center", "leaf", "n1", "n2", "n3", "n4", "n5"}; !reflect.DeepEqual(godIDs(got), want) {
		t.Fatalf("order = %v, want %v (other has no neighbour and is dropped)", godIDs(got), want)
	}
}

func TestGodNodesIgnoreMemberOfAndNonCodeNeighbours(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "K", Type: TypeClass, Label: "K", Path: "k.php"})
	for i := range 10 {
		id := fmt.Sprintf("m%d", i)
		addFunc(g, id, "k.php")
		g.AddEdge(id, "K", MemberOf, 1, "")
	}
	addFunc(g, "api", "api.php")
	for i := range 3 {
		g.AddNode(Node{ID: fmt.Sprintf("file:f%d", i), Type: TypeFile, Path: "f.php"})
		g.AddEdge("api", fmt.Sprintf("file:f%d", i), Calls, 0.9, "")
	}
	g.AddNode(Node{ID: "t", Type: TypeFunction, Label: "t", Path: "t_test.php", Props: map[string]string{"file_kind": "test"}})
	g.AddEdge("api", "t", Calls, 0.9, "")
	g.AddNode(Node{ID: "doc:d", Type: TypeDoc, Path: "d.md"})
	g.AddEdge("api", "doc:d", DocumentedIn, 0.9, "")
	g.AddEdge("api", "m0", Calls, 0.9, "")

	got := g.GodNodes(0, GodOptions{})
	if want := []string{"api", "m0"}; !reflect.DeepEqual(godIDs(got), want) {
		t.Fatalf("got %v, want %v: K only has MemberOf edges, files, docs and tests do not count", godIDs(got), want)
	}
	if api := findGod(t, got, "api"); api.Degree != 1 {
		t.Fatalf("api degree = %d, want 1", api.Degree)
	}
}

func TestGodNodesCountsBridges(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "hub", Type: TypeFunction, Label: "hub", Props: map[string]string{"community": "1"}})
	g.AddNode(Node{ID: "a", Type: TypeFunction, Label: "a", Props: map[string]string{"community": "1"}})
	g.AddNode(Node{ID: "b", Type: TypeFunction, Label: "b", Props: map[string]string{"community": "2"}})
	g.AddNode(Node{ID: "c", Type: TypeFunction, Label: "c", Props: map[string]string{"community": "2"}})
	g.AddNode(Node{ID: "d", Type: TypeFunction, Label: "d"})
	for _, x := range []string{"a", "b", "c", "d"} {
		g.AddEdge("hub", x, Calls, 0.9, "")
	}
	g.AddNode(Node{ID: "solo", Type: TypeFunction, Label: "solo"})
	g.AddEdge("solo", "a", Calls, 0.9, "")
	g.AddEdge("solo", "b", Calls, 0.9, "")

	got := g.GodNodes(0, GodOptions{})
	hub := findGod(t, got, "hub")
	if hub.Bridges != 1 || hub.Community != "1" || hub.Degree != 4 {
		t.Fatalf("hub = %+v, want degree 4, one foreign community (2), its own 1 and unclustered d ignored", hub)
	}
	if solo := findGod(t, got, "solo"); solo.Bridges != 2 || solo.Community != "" {
		t.Fatalf("solo = %+v, want two foreign communities and no community of its own", solo)
	}
}

func TestGodNodesSkipGeneratedButCountThemAsNeighbours(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "gen", Type: TypeFunction, Label: "gen", Path: "src/generated/api.ts", Props: map[string]string{"file_kind": "generated"}})
	addFunc(g, "leaf", "leaf.ts")
	for i := range 5 {
		id := fmt.Sprintf("n%d", i+1)
		addFunc(g, id, "n.ts")
		g.AddEdge("gen", id, Calls, 0.9, "")
		g.AddEdge("leaf", id, Calls, 0.9, "")
	}

	got := g.GodNodes(0, GodOptions{})
	if want := []string{"leaf", "n1", "n2", "n3", "n4", "n5"}; !reflect.DeepEqual(godIDs(got), want) {
		t.Fatalf("got %v, want %v: the generated gen is not a candidate", godIDs(got), want)
	}
	if n1 := findGod(t, got, "n1"); n1.Degree != 2 {
		t.Fatalf("n1 degree = %d, want 2: the generated neighbour still counts", n1.Degree)
	}
}

func TestGodNodesTypesTopAndMinDegree(t *testing.T) {
	g := centerGraph()
	g.AddNode(Node{ID: "Svc", Type: TypeClass, Label: "Svc", Path: "svc.php"})
	for _, id := range []string{"n1", "n2", "n3"} {
		g.AddEdge(id, "Svc", Calls, 0.9, "")
	}

	classes := g.GodNodes(0, GodOptions{Types: []string{TypeClass}})
	if len(classes) != 1 || classes[0].ID != "Svc" || classes[0].Degree != 3 {
		t.Fatalf("class filter = %+v", classes)
	}

	all := g.GodNodes(0, GodOptions{})
	if top := g.GodNodes(2, GodOptions{}); !reflect.DeepEqual(godIDs(top), godIDs(all)[:2]) {
		t.Fatalf("top 2 = %v, want the first two of %v", godIDs(top), godIDs(all))
	}

	if dense := g.GodNodes(0, GodOptions{MinDegree: 3}); !reflect.DeepEqual(godIDs(dense), []string{"center", "Svc", "n1", "n2"}) {
		t.Fatalf("MinDegree 3 = %v", godIDs(dense))
	}
}

func TestGodNodesDeterministicTieOrder(t *testing.T) {
	g := New()
	for _, id := range []string{"beta", "alpha", "y", "x"} {
		addFunc(g, id, "x.php")
	}
	for _, from := range []string{"alpha", "beta"} {
		g.AddEdge(from, "x", Calls, 0.9, "")
		g.AddEdge(from, "y", Calls, 0.9, "")
	}
	want := []string{"alpha", "beta", "x", "y"}
	for range 3 {
		if got := g.GodNodes(0, GodOptions{}); !reflect.DeepEqual(godIDs(got), want) {
			t.Fatalf("order = %v, want %v (equal degree and bridges: smaller id first)", godIDs(got), want)
		}
	}
}
