package graph

import "testing"

func TestDefaultProv(t *testing.T) {
	cases := []struct {
		name     string
		kind     string
		dst      string
		conf     float64
		evidence string
		want     string
	}{
		{"column read by convention", Reads, "column:t.c", 1, "", ProvConvention},
		{"column written by convention", Writes, "column:t.c", 1, "", ProvConvention},
		{"low confidence call", Calls, "f", 0.55, "", ProvGuess},
		{"framework call", Calls, "f", 0.95, "framework: laravel", ProvConvention},
		{"plain call", Calls, "f", 0.95, "", ProvAST},
		{"declared link", Link, "x", 1, "", ProvDeclared},
		{"history", CoChanged, "y", 0.5, "", ProvHistory},
		{"documented", DocumentedIn, "doc:x", 1, "", ProvDocs},
		{"produces", Produces, "event:x", 1, "", ProvContract},
		{"consumes", Consumes, "event:x", 1, "", ProvContract},
		{"mentions", Mentions, "event:x", 1, "", ProvContract},
		{"requests", Requests, "route:x", 1, "", ProvConvention},
		{"exposes", Exposes, "route:x", 1, "", ProvConvention},
		{"maps to", MapsTo, "table:t", 1, "", ProvConvention},
		{"registers", Registers, "class:x", 1, "", ProvConvention},
		{"implements", Implements, "req:x", 1, "", ProvHuman},
		{"refines", Refines, "req:y", 1, "", ProvHuman},
		{"derived from", DerivedFrom, "ticket:1", 1, "", ProvHuman},
	}
	for _, c := range cases {
		if got := DefaultProv(c.kind, c.dst, c.conf, c.evidence); got != c.want {
			t.Errorf("%s: DefaultProv(%s, %s, %v, %q) = %s, want %s", c.name, c.kind, c.dst, c.conf, c.evidence, got, c.want)
		}
	}
}

func TestAddEdgeProvKeepsExplicitValue(t *testing.T) {
	g := New()
	g.AddEdgeProv("a", "b", Calls, 0.95, "", ProvHuman)
	g.AddEdge("c", "d", Calls, 0.95, "")
	if g.Edges[0].Prov != ProvHuman || g.Edges[1].Prov != ProvAST {
		t.Fatalf("explicit provenance must be kept, derived one computed: %+v", g.Edges)
	}
}

func TestDuplicateEdgeKeepsStrongerProvenance(t *testing.T) {
	g := New()
	g.AddEdge("a", "b", Calls, 0.5, "")
	if g.Edges[0].Prov != ProvGuess {
		t.Fatalf("first edge: %+v", g.Edges[0])
	}
	g.AddEdge("a", "b", Calls, 0.95, "")
	if len(g.Edges) != 1 || g.Edges[0].Conf != 0.95 || g.Edges[0].Prov != ProvAST {
		t.Fatalf("stronger edge must win: %+v", g.Edges)
	}
	g.AddEdge("a", "b", Calls, 0.3, "")
	if g.Edges[0].Conf != 0.95 || g.Edges[0].Prov != ProvAST {
		t.Fatalf("weaker edge must not replace it: %+v", g.Edges)
	}
}

func TestLayerOfEveryNodeType(t *testing.T) {
	want := map[string]string{
		TypeFile:        LayerFile,
		TypeTest:        LayerFile,
		TypeDoc:         LayerFile,
		TypeClass:       LayerCode,
		TypeFunction:    LayerCode,
		TypeMethod:      LayerCode,
		TypeField:       LayerCode,
		TypeTable:       LayerContract,
		TypeColumn:      LayerContract,
		TypeRoute:       LayerContract,
		TypeTool:        LayerContract,
		TypeValue:       LayerContract,
		TypeRequirement: LayerKnowledge,
		TypeTicket:      LayerKnowledge,
		TypeDecision:    LayerKnowledge,
		TypeSection:     LayerKnowledge,
		TypeFeature:     LayerKnowledge,
	}
	for typ, layer := range want {
		if got := Layer(typ); got != layer {
			t.Errorf("Layer(%s) = %s, want %s", typ, got, layer)
		}
	}
}

func TestKnowledgeNodesEndWalks(t *testing.T) {
	g := New()
	fn(g, "fa")
	fn(g, "fb")
	g.AddNode(Node{ID: "req:billing", Type: TypeRequirement, Label: "Billing"})
	g.AddEdge("fa", "req:billing", Implements, 1, "")
	g.AddEdge("fb", "req:billing", Implements, 1, "")
	g.Index()
	o := Options{Mode: ModeImpact, Dir: "down", MaxCost: 10, MinConf: 0.5}
	hits, _ := g.Walk("fa", o)
	if hits["req:billing"] == nil {
		t.Fatal("the requirement must be reached from a function that implements it")
	}
	if hits["fb"] != nil {
		t.Fatal("two functions implementing one requirement must not be connected through it")
	}
	hits, _ = g.Walk("req:billing", o)
	if hits["fa"] == nil || hits["fb"] == nil {
		t.Fatalf("a walk starting at the requirement must reach both functions: %v", hits)
	}
	if g.Group("req:billing") != "knowledge" {
		t.Fatalf("requirement group = %s", g.Group("req:billing"))
	}
}
