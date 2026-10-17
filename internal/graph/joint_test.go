package graph

import "testing"

// repoWith is a repository that has one file and one function, and the value id on the given side: produces (the file
// sets the value) or consumes (the function reads it). Both edges carry confidence 0.7, the default walk threshold.
func repoWith(commit, value string, produces bool) *Graph {
	g := New()
	g.Meta["commit"] = commit
	g.AddNode(Node{ID: value, Type: TypeValue, Label: value})
	g.AddNode(Node{ID: "file:deploy/values.yaml", Type: TypeFile, Label: "deploy/values.yaml", Path: "deploy/values.yaml"})
	g.AddNode(Node{ID: "php:Cache::host", Type: TypeMethod, Label: "Cache::host", Path: "app/Cache.php", Line: 3})
	if produces {
		g.AddEdge("file:deploy/values.yaml", value, Produces, 0.7, "deploy/values.yaml:4")
	} else {
		g.AddEdge("php:Cache::host", value, Consumes, 0.7, "app/Cache.php:4")
	}
	return g
}

// sharedNodes lists the value nodes that are still shared between repositories (no Repo).
func sharedNodes(g *Graph) []string {
	var out []string
	for id, n := range g.Nodes {
		if n.Type == TypeValue && n.Repo == "" {
			out = append(out, id)
		}
	}
	return out
}

func TestSharedNameWithConsumersOnlyIsSplit(t *testing.T) {
	const v = "env:MEMCACHED_HOST"
	m := Merge(map[string]*Graph{"acme/shop": repoWith("1111111", v, false), "acme/api": repoWith("2222222", v, false)})
	if len(sharedNodes(m)) != 0 || m.Has(v) {
		t.Fatalf("two repositories that only read a name must not share its node: %v", sharedNodes(m))
	}
	if l := m.Links(); l.Shared != 0 || l.Through != 0 || l.Cross != 0 || len(l.Pairs) != 0 {
		t.Fatalf("no connection between them: %+v", l)
	}
	for _, repo := range []string{"acme/shop", "acme/api"} {
		own := repoID(repo, v)
		if n := m.Nodes[own]; n == nil || n.Repo != repo || n.Type != TypeValue {
			t.Fatalf("%s needs its own copy of the name: %+v", repo, n)
		}
		if !hasEdgeTo(m, repoID(repo, "php:Cache::host"), own, Consumes) {
			t.Fatalf("the read of %s must still lead to its own copy", repo)
		}
	}
}

func TestNameProducedHereAndReadThereStaysShared(t *testing.T) {
	const v = "env:ADAPTER_CLONE_TIMEOUT"
	m := Merge(map[string]*Graph{"acme/app": repoWith("1111111", v, false), "acme/chart": repoWith("2222222", v, true)})
	if !m.Has(v) || m.Nodes[v].Repo != "" {
		t.Fatalf("a produced and a consumed name joins the repositories through one node: %v", sharedNodes(m))
	}
	l := m.Links()
	if l.Shared != 1 || l.Through != 1 || l.Cross != 0 {
		t.Fatalf("links = %+v", l)
	}
	if want := (RepoPair{A: "acme/app", B: "acme/chart", Values: 1}); len(l.Pairs) != 1 || l.Pairs[0] != want {
		t.Fatalf("pairs = %+v", l.Pairs)
	}
	if !hasEdgeTo(m, repoID("acme/app", "php:Cache::host"), v, Consumes) || !hasEdgeTo(m, repoID("acme/chart", "file:deploy/values.yaml"), v, Produces) {
		t.Fatal("both sides must keep their edges to the shared node")
	}
}

func TestNameProducedInBothRepositoriesIsSplit(t *testing.T) {
	const v = "env:MEMCACHED_HOST"
	m := Merge(map[string]*Graph{"acme/shop": repoWith("1111111", v, true), "acme/api": repoWith("2222222", v, true)})
	if len(sharedNodes(m)) != 0 {
		t.Fatalf("two producers and no consumer across repositories: %v", sharedNodes(m))
	}
	if l := m.Links(); l.Through != 0 || len(l.Pairs) != 0 {
		t.Fatalf("no connection: %+v", l)
	}
	if !hasEdgeTo(m, repoID("acme/shop", "file:deploy/values.yaml"), repoID("acme/shop", v), Produces) {
		t.Fatal("the producer keeps its edge to its own copy")
	}
}

func TestProducerAndConsumerInOneRepositoryStayConnectedAfterAMerge(t *testing.T) {
	const v = "env:ADAPTER_CLONE_TIMEOUT"
	p := repoWith("1111111", v, true)
	p.AddEdge("php:Cache::host", v, Consumes, 0.7, "app/Cache.php:4")
	unrelated := New()
	unrelated.Meta["commit"] = "3333333"
	unrelated.AddNode(Node{ID: "php:Lone", Type: TypeClass, Label: "Lone", Path: "Lone.php", Line: 1})
	m := Merge(map[string]*Graph{"acme/shop": p, "acme/other": unrelated})
	o := DefaultOptions()
	o.Dir = "up"
	hits, _ := m.Walk(repoID("acme/shop", "php:Cache::host"), o)
	if hits[repoID("acme/shop", "file:deploy/values.yaml")] == nil {
		t.Fatal("the read of a name must still reach the file that sets it in the same repository")
	}
}

func TestImageNameJoinsOnlyWhenPushedAndDeployed(t *testing.T) {
	const img = "image:org/app-backend"
	pushed := repoWith("1111111", img, true)
	deployed := repoWith("2222222", img, false)
	if m := Merge(map[string]*Graph{"acme/app": pushed, "acme/chart": deployed}); !m.Has(img) || m.Links().Through != 1 {
		t.Fatalf("a pushed image that another repository deploys joins them: %v", sharedNodes(m))
	}
	if m := Merge(map[string]*Graph{"acme/app": repoWith("1111111", img, false), "acme/chart": repoWith("2222222", img, false)}); m.Has(img) || m.Links().Through != 0 {
		t.Fatalf("two repositories that only deploy an image are not connected: %v", sharedNodes(m))
	}
}

func TestOtherValuesAreNeverSplit(t *testing.T) {
	const v = "event:order"
	m := Merge(map[string]*Graph{"acme/shop": repoWith("1111111", v, false), "acme/api": repoWith("2222222", v, false)})
	if !m.Has(v) || m.Nodes[v].Repo != "" {
		t.Fatal("an event type keeps one node whatever its producers and consumers")
	}
}

func hasEdgeTo(g *Graph, src, dst, kind string) bool {
	for _, e := range g.Edges {
		if e.Src == src && e.Dst == dst && e.Kind == kind {
			return true
		}
	}
	return false
}
