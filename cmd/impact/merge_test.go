package main

import (
	"path/filepath"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func snapshot(t *testing.T, dir, repo string) string {
	t.Helper()
	g := graph.New()
	g.Meta["repo"] = repo
	// a ring of eight functions with chords is one community of at least the minimum size
	ids := []string{"a", "b", "c", "d", "e", "f", "g", "h"}
	for _, id := range ids {
		g.AddNode(graph.Node{ID: "go:" + id, Type: graph.TypeFunction, Label: id, Path: "a.go", Repo: repo})
	}
	for i, id := range ids {
		g.AddEdge("go:"+id, "go:"+ids[(i+1)%len(ids)], graph.Calls, 0.9, "a.go:1")
		g.AddEdge("go:"+id, "go:"+ids[(i+3)%len(ids)], graph.Calls, 0.9, "a.go:2")
	}
	g.Index()
	path := filepath.Join(dir, filepath.Base(repo)+".db")
	if err := graph.Save(g, path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMergeClustersTheMergedGraph(t *testing.T) {
	dir := t.TempDir()
	a, b := snapshot(t, dir, "acme/one"), snapshot(t, dir, "acme/two")
	out := filepath.Join(dir, "m.db")
	if err := cmdMerge([]string{"--out", out, a, b}); err != nil {
		t.Fatal(err)
	}
	m, err := graph.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Communities) == 0 {
		t.Fatal("the merged snapshot must carry communities, impact_overview reads them")
	}
	plain := filepath.Join(dir, "plain.db")
	if err := cmdMerge([]string{"--no-cluster", "--out", plain, a, b}); err != nil {
		t.Fatal(err)
	}
	if p, _ := graph.Load(plain); len(p.Communities) != 0 {
		t.Fatalf("--no-cluster must skip clustering, got %d communities", len(p.Communities))
	}
}
