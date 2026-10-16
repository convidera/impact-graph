package graph

import "testing"

func configNode(g *Graph, id, kind, path string) {
	g.AddNode(Node{ID: id, Type: TypeConfig, Label: path, Path: "values.yaml", Line: 3, Props: map[string]string{PropConfigKind: kind, PropConfigPath: path}})
}

func TestLinkConfigMatchesWholeSegmentsOnly(t *testing.T) {
	g := New()
	configNode(g, "config:a.*.spec.port", ConfigPattern, "a.*.spec.port")
	configNode(g, "config:v#a.x.spec.port", ConfigKey, "a.x.spec.port")
	configNode(g, "config:v#a.x.spec.portal", ConfigKey, "a.x.spec.portal")
	configNode(g, "config:v#a.x.spec", ConfigKey, "a.x.spec")
	configNode(g, "config:v#a.x.y.spec.port", ConfigKey, "a.x.y.spec.port")
	configNode(g, "config:v#b.x.spec.port", ConfigKey, "b.x.spec.port")
	LinkConfig(g)
	LinkConfig(g) // idempotent: a second run (after a merge) adds nothing
	var got []string
	for _, e := range g.Edges {
		if e.Kind != Configures {
			t.Fatalf("unexpected edge %+v", e)
		}
		got = append(got, e.Src)
		if e.Dst != "config:a.*.spec.port" || e.Prov != ProvDeclared {
			t.Errorf("edge = %+v", e)
		}
	}
	if len(got) != 1 || got[0] != "config:v#a.x.spec.port" {
		t.Fatalf("only the key with the same number of segments and the same names matches: %v", got)
	}
}

func TestListElementsAreOneSegment(t *testing.T) {
	g := New()
	configNode(g, "p", ConfigPattern, "a.*.tolerations.[].key")
	configNode(g, "k", ConfigKey, "a.default.tolerations.[].key")
	LinkConfig(g)
	if len(g.Edges) != 1 {
		t.Fatalf("edges = %+v", g.Edges)
	}
}

func TestAKeyIsNotClusteredButAPatternIs(t *testing.T) {
	key := &Node{Type: TypeConfig, Props: map[string]string{PropConfigKind: ConfigKey}}
	pat := &Node{Type: TypeConfig, Props: map[string]string{PropConfigKind: ConfigPattern}}
	if clusterEligible(key) || !clusterEligible(pat) {
		t.Fatalf("key %v pattern %v", clusterEligible(key), clusterEligible(pat))
	}
}
