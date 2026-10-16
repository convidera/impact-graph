package graph

import (
	"encoding/json"
	"testing"
)

func TestMergeUnifiesValuesAndPrefixesRepos(t *testing.T) {
	a := New()
	a.Meta["commit"], a.Meta["built_at"] = "aaa", "t1"
	a.AddNode(Node{ID: "php:Foo", Type: TypeClass, Label: "Foo", Path: "a/Foo.php", Props: map[string]string{"community": "2"}})
	a.AddNode(Node{ID: "event:x", Type: TypeValue, Label: "x", Props: map[string]string{"role": "a"}})
	a.AddEdge("php:Foo", "event:x", Mentions, 1, "")
	a.AddEdge("php:Foo", "php:Missing", Uses, 1, "")
	b := New()
	b.Meta["commit"], b.Meta["built_at"] = "bbb", "t2"
	b.AddNode(Node{ID: "php:Foo", Type: TypeClass, Label: "Foo", Path: "b/Foo.php"})
	b.AddNode(Node{ID: "event:x", Type: TypeValue, Label: "x", Props: map[string]string{"schema": "1"}})
	b.AddEdgeProv("php:Foo", "event:x", Produces, 0.9, "ev", ProvHuman)
	m := Merge(map[string]*Graph{"a/x": a, "a/y": b})

	fa, fb := m.Nodes["a/x//php:Foo"], m.Nodes["a/y//php:Foo"]
	if fa == nil || fb == nil || m.Has("php:Foo") {
		t.Fatalf("same-named classes must stay distinct and prefixed: %v", m.Nodes)
	}
	if fa.Repo != "a/x" || fb.Repo != "a/y" || fa.Path != "a/Foo.php" {
		t.Fatalf("repo or path lost: %+v %+v", fa, fb)
	}
	if _, ok := fa.Props["community"]; ok {
		t.Fatalf("community is per snapshot and must be removed: %v", fa.Props)
	}
	v := m.Nodes["event:x"]
	if v == nil || v.Repo != "" || v.Props["role"] != "a" || v.Props["schema"] != "1" {
		t.Fatalf("value must be unified with merged props: %+v", v)
	}
	if n := len(m.Edges); n != 2 {
		t.Fatalf("want 2 edges (the edge to a missing node is dropped), got %+v", m.Edges)
	}
	if e := edgeOf(t, m, Produces); e.Src != "a/y//php:Foo" || e.Dst != "event:x" || e.Prov != ProvHuman || e.Conf != 0.9 {
		t.Fatalf("edge not rewritten or provenance lost: %+v", e)
	}
	if m.Communities != nil {
		t.Fatalf("communities must not be merged: %+v", m.Communities)
	}
	var repos map[string]map[string]string
	if err := json.Unmarshal([]byte(m.Meta["repos"]), &repos); err != nil {
		t.Fatal(err)
	}
	if repos["a/x"]["commit"] != "aaa" || repos["a/y"]["commit"] != "bbb" || repos["a/y"]["built_at"] != "t2" {
		t.Fatalf("repos meta wrong: %v", repos)
	}
	if m.Meta["schema_version"] != "2" || m.Meta["engine_version"] != EngineVersion {
		t.Fatalf("versions missing: %v", m.Meta)
	}
	if hits, _ := m.Walk("a/x//php:Foo", Options{Mode: ModeFlow, Dir: "down", MaxCost: 5, MinConf: 0.5}); hits["event:x"] == nil {
		t.Fatal("merged graph must be indexed")
	}
}

func TestMergeNilIsEmpty(t *testing.T) {
	m := Merge(nil)
	if m == nil || len(m.Nodes) != 0 || len(m.Edges) != 0 || m.Out("x") != nil {
		t.Fatalf("Merge(nil) must be an empty graph: %+v", m)
	}
}
