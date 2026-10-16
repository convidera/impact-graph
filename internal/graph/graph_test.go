package graph

import (
	"path/filepath"
	"testing"
)

func fn(g *Graph, id string) { g.AddNode(Node{ID: id, Type: TypeFunction, Label: id}) }

func TestWalkCallReturnMatching(t *testing.T) {
	g := New()
	for _, id := range []string{"a", "b", "helper", "other", "col"} {
		fn(g, id)
	}
	g.AddNode(Node{ID: "col", Type: TypeColumn})
	// a and b both call helper; a writes col, other reads col
	g.AddEdge("a", "helper", Calls, 1, "")
	g.AddEdge("b", "helper", Calls, 1, "")
	g.AddEdge("a", "col", Writes, 1, "")
	g.AddEdge("other", "col", Reads, 1, "")
	g.Index()

	flow := Options{Mode: ModeFlow, Dir: "down", MaxCost: 10, MinConf: 0.5}
	if p := g.Trace("a", "b", flow); p != nil {
		t.Fatalf("a value must not flow from a through the shared helper into b: %v", p)
	}
	if p := g.Trace("a", "other", flow); p == nil || len(p) != 3 {
		t.Fatalf("a -> col -> other expected, got %v", p)
	}
	// a change to the helper affects both callers
	hits, _ := g.Walk("helper", Options{Mode: ModeImpact, Dir: "down", MaxCost: 3, MinConf: 0.5})
	if hits["a"] == nil || hits["b"] == nil {
		t.Fatalf("impact of helper must reach both callers: %v", hits)
	}
}

func TestWalkUpAndConfidence(t *testing.T) {
	g := New()
	for _, id := range []string{"x", "y", "z"} {
		fn(g, id)
	}
	g.AddEdge("x", "y", Calls, 0.95, "") // x calls y
	g.AddEdge("y", "z", Calls, 0.4, "")  // a guess
	g.Index()
	o := Options{Mode: ModeImpact, Dir: "down", MaxCost: 10, MinConf: 0.7}
	hits, _ := g.Walk("z", o)
	if hits["y"] != nil {
		t.Fatalf("an edge below the confidence threshold must not be walked")
	}
	o.MinConf = 0.3
	hits, _ = g.Walk("z", o)
	if hits["x"] == nil || hits["x"].Conf != 0.4 {
		t.Fatalf("path confidence is the weakest edge: %+v", hits["x"])
	}
	o.Dir = "up"
	o.MinConf = 0.7
	hits, _ = g.Walk("x", o)
	if hits["y"] == nil {
		t.Fatalf("up from x must reach what x calls (what can affect x)")
	}
}

func TestHubIsNotExpanded(t *testing.T) {
	g := New()
	fn(g, "start")
	fn(g, "hub")
	g.AddEdge("hub", "start", Calls, 1, "") // start's change reaches hub (caller)
	for i := 0; i < 5; i++ {
		id := string(rune('a' + i))
		fn(g, id)
		g.AddEdge(id, "hub", Calls, 1, "")
	}
	g.Index()
	hits, hubs := g.Walk("start", Options{Mode: ModeImpact, Dir: "down", MaxCost: 10, MinConf: 0.5, HubLimit: 3})
	if hits["hub"] == nil || hubs["hub"] == 0 || hits["a"] != nil {
		t.Fatalf("hub must be reached but not expanded: hits=%v hubs=%v", hits, hubs)
	}
}

func TestMatchArmTag(t *testing.T) {
	g := New()
	for _, id := range []string{"dispatch", "onA", "onB", "emitA"} {
		fn(g, id)
	}
	g.AddNode(Node{ID: "event:a", Type: TypeValue})
	g.AddNode(Node{ID: "event:b", Type: TypeValue})
	g.AddEdge("emitA", "event:a", Produces, 1, "")
	g.AddEdge("dispatch", "event:a", Consumes, 1, "")
	g.AddEdge("dispatch", "event:b", Consumes, 1, "")
	g.AddTagged("dispatch", "onA", Calls, 1, "", "event:a")
	g.AddTagged("dispatch", "onB", Calls, 1, "", "event:b")
	g.Index()
	hits, _ := g.Walk("event:a", Options{Mode: ModeFlow, Dir: "down", MaxCost: 10, MinConf: 0.5})
	if hits["onA"] == nil {
		t.Fatalf("the arm of event a must be reached")
	}
	if hits["onB"] != nil {
		t.Fatalf("the arm of event b must not be reached from event a")
	}
}

func TestTraceViaAndAlternatives(t *testing.T) {
	g := New()
	for _, id := range []string{"s", "m1", "m2", "t"} {
		fn(g, id)
	}
	g.AddEdge("s", "m1", Calls, 1, "")
	g.AddEdge("m1", "t", Calls, 1, "")
	g.AddEdge("s", "m2", Calls, 1, "")
	g.AddEdge("m2", "t", Calls, 1, "")
	g.Index()
	o := Options{Mode: ModeFlow, Dir: "down", MaxCost: 10, MinConf: 0.5}
	r := g.TraceN("s", []string{"m2"}, "t", o, 1)
	if len(r) != 1 || r[0][1].ID != "m2" {
		t.Fatalf("via must force the waypoint: %v", r)
	}
	r = g.TraceN("s", nil, "t", o, 2)
	if len(r) != 2 || r[0][1].ID == r[1][1].ID {
		t.Fatalf("two distinct routes expected: %v", r)
	}
}

func TestStoreRoundTrip(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "a", Type: TypeFunction, Label: "A", Path: "x.php", Line: 3, Props: map[string]string{"k": "v"}})
	fn(g, "b")
	g.AddTagged("a", "b", Calls, 0.9, "ev", "t1")
	g.Meta["built_at"] = "now"
	path := filepath.Join(t.TempDir(), "sub", "g.db")
	if err := Save(g, path); err != nil {
		t.Fatal(err)
	}
	h, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.Nodes) != 2 || len(h.Edges) != 1 || h.Edges[0].Tag != "t1" || h.Nodes["a"].Props["k"] != "v" || h.Meta["built_at"] != "now" {
		t.Fatalf("round trip lost data: %+v %+v", h.Nodes, h.Edges)
	}
}

func TestResolveAmbiguity(t *testing.T) {
	g := New()
	fn(g, "php:A::run")
	fn(g, "php:B::run")
	g.Index()
	if id, c := g.Resolve("run"); id != "" || len(c) != 2 {
		t.Fatalf("ambiguous text must list candidates, got %q %v", id, c)
	}
	if id, _ := g.Resolve("A::run"); id != "php:A::run" {
		t.Fatalf("unique substring must resolve, got %q", id)
	}
	fn(g, "ts:x/v.vue#save")
	fn(g, "ts:x/v.vue#saveRow")
	g.Index()
	if id, _ := g.Resolve("v.vue#save"); id != "ts:x/v.vue#save" {
		t.Fatalf("an id that ends with the text at a boundary must win over longer ids, got %q", id)
	}
}
