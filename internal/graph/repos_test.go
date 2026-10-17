package graph

import (
	"strings"
	"testing"
)

// mergedRepos merges three small repositories: two named acme/shop and acme/api, and other/api, which makes "api" ambiguous.
func mergedRepos() *Graph {
	shop := New()
	shop.Meta["commit"] = "1111111"
	shop.AddNode(Node{ID: "php:Cart", Type: TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3})
	shop.AddNode(Node{ID: "event:order", Type: TypeValue, Label: "event:order"})
	shop.AddEdge("php:Cart", "event:order", Produces, 0.9, "app/Cart.php:9")
	api := New()
	api.Meta["commit"] = "2222222"
	api.AddNode(Node{ID: "php:Order", Type: TypeClass, Label: "Order", Path: "app/Order.php", Line: 3})
	api.AddNode(Node{ID: "event:order", Type: TypeValue, Label: "event:order"})
	api.AddEdge("php:Order", "event:order", Consumes, 0.9, "app/Order.php:7")
	other := New()
	other.AddNode(Node{ID: "php:Lone", Type: TypeClass, Label: "Lone", Path: "Lone.php", Line: 1})
	return Merge(map[string]*Graph{"acme/shop": shop, "acme/api": api, "other/api": other})
}

func TestResolveRepo(t *testing.T) {
	g := mergedRepos()
	for _, tc := range []struct {
		in, want, wantErr string
	}{
		{"", "", ""},
		{"acme/shop", "acme/shop", ""},
		{"other/api", "other/api", ""},
		{"shop", "acme/shop", ""},
		{"api", "", "ambiguous"},
		{"nope", "", "unknown repository"},
		{"acme/nope", "", "unknown repository"},
	} {
		got, err := g.ResolveRepo(tc.in)
		if tc.wantErr == "" {
			if err != nil || got != tc.want {
				t.Errorf("ResolveRepo(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("ResolveRepo(%q) = %q, %v; want an error containing %q", tc.in, got, err, tc.wantErr)
			continue
		}
		if !strings.Contains(err.Error(), "acme/api, acme/shop, other/api") {
			t.Errorf("ResolveRepo(%q): the error must list the known repositories: %v", tc.in, err)
		}
	}
}

func TestResolveRepoIgnoresTheArgumentOnASnapshot(t *testing.T) {
	g := New()
	g.Meta["repo"] = "acme/shop"
	for _, in := range []string{"acme/shop", "anything", "shop"} {
		if got, err := g.ResolveRepo(in); got != "" || err != nil {
			t.Errorf("ResolveRepo(%q) on a snapshot = %q, %v; want no restriction and no error", in, got, err)
		}
	}
}

func TestInRepoKeepsSharedValuesOutOfRepositoryLists(t *testing.T) {
	g := mergedRepos()
	shared, own := g.Nodes["event:order"], g.Nodes["acme/shop//php:Cart"]
	if !g.InRepo(shared, "") || !g.InRepo(own, "") {
		t.Fatal("no restriction lists every node")
	}
	if g.InRepo(shared, "acme/shop") || !g.InRepo(own, "acme/shop") || g.InRepo(own, "acme/api") {
		t.Fatalf("a repository lists its own nodes only, and not the shared values: %+v %+v", shared, own)
	}
}

func TestFindAndGodNodesRestrictToARepository(t *testing.T) {
	g := mergedRepos()
	if got := g.FindIn("order", 0, "acme/api"); len(got) != 1 || got[0].ID != "acme/api//php:Order" {
		t.Errorf("find in acme/api = %v", got)
	}
	if got := g.FindIn("order", 0, ""); len(got) != 2 {
		t.Errorf("find without a repository lists the shared value too: %v", got)
	}
	for _, n := range g.GodNodes(0, GodOptions{Repo: "acme/shop"}) {
		if !strings.HasPrefix(n.ID, "acme/shop//") {
			t.Errorf("god node of another repository: %s", n.ID)
		}
	}
	gods := g.GodNodes(0, GodOptions{Repo: "acme/shop"})
	if len(gods) != 1 || gods[0].Degree != 1 {
		t.Errorf("a god node of a repository keeps the degree of all its neighbours: %+v", gods)
	}
}

func TestRepoStatsAndLinks(t *testing.T) {
	g := mergedRepos()
	stats := g.RepoStats()
	want := []RepoStat{
		{Repo: "acme/api", Commit: "2222222", Nodes: 1, Edges: 1},
		{Repo: "acme/shop", Commit: "1111111", Nodes: 1, Edges: 1},
		{Repo: "other/api", Nodes: 1, Edges: 0},
	}
	if len(stats) != len(want) {
		t.Fatalf("stats = %+v", stats)
	}
	for i := range want {
		if stats[i] != want[i] {
			t.Errorf("stats[%d] = %+v, want %+v", i, stats[i], want[i])
		}
	}
	l := g.Links()
	if l.Shared != 1 || l.Through != 1 || l.Cross != 0 {
		t.Errorf("links = %+v", l)
	}
	if len(l.Pairs) != 1 || l.Pairs[0] != (RepoPair{A: "acme/api", B: "acme/shop", Values: 1}) {
		t.Errorf("pairs = %+v", l.Pairs)
	}
}

func TestRepoStatsOfASnapshotIsEmpty(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "php:A", Type: TypeClass, Label: "A", Repo: "acme/shop"})
	if s := g.RepoStats(); s != nil {
		t.Errorf("a snapshot has no repositories to count: %+v", s)
	}
	if l := g.Links(); l.Through != 0 || len(l.Pairs) != 0 {
		t.Errorf("a snapshot has no links: %+v", l)
	}
}
