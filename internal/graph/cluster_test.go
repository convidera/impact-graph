package graph

import (
	"fmt"
	"math/rand"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func addFunc(g *Graph, id, file string) {
	g.AddNode(Node{ID: id, Type: TypeFunction, Label: id, Path: file})
}

// addClique adds n functions prefix0..prefix(n-1) in one file and links every pair with a Calls edge.
func addClique(g *Graph, prefix, file string, n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s%d", prefix, i)
		addFunc(g, ids[i], file)
	}
	for i := range ids {
		for j := i + 1; j < n; j++ {
			g.AddEdge(ids[i], ids[j], Calls, 0.95, "")
		}
	}
	return ids
}

// threeGroups builds one clique per size in directories a, b and c, chained by a single Calls edge each.
func threeGroups(sizes ...int) (*Graph, [][]string) {
	g := New()
	files := []string{"a/x.php", "b/y.php", "c/z.php"}
	prefixes := []string{"a", "b", "c"}
	var groups [][]string
	for i, n := range sizes {
		groups = append(groups, addClique(g, prefixes[i], files[i], n))
	}
	for i := 0; i+1 < len(groups); i++ {
		g.AddEdge(groups[i][len(groups[i])-1], groups[i+1][0], Calls, 0.95, "")
	}
	return g, groups
}

func communityOf(t *testing.T, g *Graph, id string) string {
	t.Helper()
	n := g.Nodes[id]
	if n == nil {
		t.Fatalf("no node %s", id)
	}
	return n.Props["community"]
}

func communityProps(g *Graph) map[string]string {
	out := map[string]string{}
	for id, n := range g.Nodes {
		if c, ok := n.Props["community"]; ok {
			out[id] = c
		}
	}
	return out
}

func TestClusterRecoversGroups(t *testing.T) {
	g, groups := threeGroups(8, 8, 8)
	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 3 {
		t.Fatalf("want 3 communities, got %d: %+v", len(comms), comms)
	}
	dirs := []string{"a", "b", "c"}
	seen := map[string]bool{}
	for gi, ids := range groups {
		id := communityOf(t, g, ids[0])
		if id == "" || seen[id] {
			t.Fatalf("group %s has community %q, empty or shared with another group", dirs[gi], id)
		}
		seen[id] = true
		for _, x := range ids {
			if got := communityOf(t, g, x); got != id {
				t.Fatalf("%s is in community %q, want %q", x, got, id)
			}
		}
		k, err := strconv.Atoi(id)
		if err != nil {
			t.Fatal(err)
		}
		c := comms[k]
		if c.Size != len(ids) || c.Dir != dirs[gi] || !strings.HasPrefix(c.Label, dirs[gi]+" ·") {
			t.Fatalf("community %d = %+v, want size %d in %s", k, c, len(ids), dirs[gi])
		}
		if c.Cohesion <= 0 || c.Cohesion > 1 {
			t.Fatalf("cohesion of %d = %v, want (0,1]", k, c.Cohesion)
		}
	}
}

func TestClusterCommunitiesOrderedBySize(t *testing.T) {
	g, _ := threeGroups(5, 12, 8)
	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 3 {
		t.Fatalf("want 3 communities, got %d", len(comms))
	}
	for i, c := range comms {
		if c.ID != i {
			t.Fatalf("community %d has id %d, ids must be dense", i, c.ID)
		}
		if i > 0 && c.Size > comms[i-1].Size {
			t.Fatalf("sizes not descending: %+v", comms)
		}
		if got := len(g.CommunityMembers(c.ID)); got != c.Size {
			t.Fatalf("community %d: %d members, size %d", c.ID, got, c.Size)
		}
	}
	if comms[0].Size != 12 || comms[2].Size != 5 {
		t.Fatalf("sizes = %d,%d,%d", comms[0].Size, comms[1].Size, comms[2].Size)
	}
}

func TestClusterExcludesHubAndAssignsIt(t *testing.T) {
	// 120 symbols in three groups plus a hub that calls all of them: 121 eligible nodes, so the 99th percentile
	// (nearest rank) is the second highest degree and only the hub lies above it.
	g, groups := threeGroups(40, 40, 40)
	addFunc(g, "hub", "h/hub.php")
	for _, ids := range groups {
		for _, x := range ids {
			g.AddEdge("hub", x, Calls, 0.95, "")
		}
	}
	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 3 {
		t.Fatalf("the hub must not glue the groups: got %d communities", len(comms))
	}
	seen := map[string]bool{}
	for _, ids := range groups {
		id := communityOf(t, g, ids[0])
		for _, x := range ids {
			if communityOf(t, g, x) != id {
				t.Fatalf("%s left its group", x)
			}
		}
		seen[id] = true
	}
	if len(seen) != 3 {
		t.Fatalf("groups share communities: %v", seen)
	}
	if communityOf(t, g, "hub") == "" {
		t.Fatalf("the excluded hub must be assigned to a community")
	}
}

func TestClusterAssignsTestSymbolAndSkipsFiles(t *testing.T) {
	g, groups := threeGroups(8, 8, 8)
	g.AddNode(Node{ID: "tb_test", Type: TypeFunction, Label: "tb_test", Path: "tests/b_test.php", Props: map[string]string{"file_kind": "test"}})
	for _, x := range groups[1][:4] {
		g.AddEdge("tb_test", x, Calls, 0.95, "")
	}
	g.AddNode(Node{ID: "file:b/y.php", Type: TypeFile, Path: "b/y.php"})
	g.AddNode(Node{ID: "file:tests/b_test.php", Type: TypeTest, Path: "tests/b_test.php"})
	g.AddNode(Node{ID: "doc:b", Type: TypeDoc, Path: "docs/b.md"})
	g.AddEdge("file:b/y.php", groups[1][0], Calls, 0.95, "")
	g.AddEdge("file:tests/b_test.php", groups[1][1], Calls, 0.95, "")
	g.AddEdge("doc:b", groups[1][2], Mentions, 0.9, "")

	comms := g.Cluster(DefaultClusterOptions())
	bID := communityOf(t, g, groups[1][0])
	if got := communityOf(t, g, "tb_test"); got != bID {
		t.Fatalf("test symbol in community %q, want the one of group b (%q)", got, bID)
	}
	k, _ := strconv.Atoi(bID)
	if comms[k].Size != 9 {
		t.Fatalf("group b with its test symbol has size %d, want 9", comms[k].Size)
	}
	for _, id := range []string{"file:b/y.php", "file:tests/b_test.php", "doc:b"} {
		if got := communityOf(t, g, id); got != "" {
			t.Fatalf("%s is a file, test or doc and must not get a community, got %q", id, got)
		}
	}
}

func TestClusterDeterministicAndIdempotent(t *testing.T) {
	build := func() *Graph {
		g, _ := threeGroups(8, 12, 5)
		return g
	}
	g1 := build()
	first := g1.Cluster(DefaultClusterOptions())
	props := communityProps(g1)

	g2 := build()
	if second := g2.Cluster(DefaultClusterOptions()); !reflect.DeepEqual(first, second) {
		t.Fatalf("two runs differ:\n%+v\n%+v", first, second)
	}
	if !reflect.DeepEqual(props, communityProps(g2)) {
		t.Fatalf("two runs assign different communities")
	}

	if again := g1.Cluster(DefaultClusterOptions()); !reflect.DeepEqual(first, again) {
		t.Fatalf("re-clustering changed the result:\n%+v\n%+v", first, again)
	}
	if !reflect.DeepEqual(props, communityProps(g1)) {
		t.Fatalf("re-clustering changed the node communities")
	}
}

func TestClusterRemovesStaleCommunity(t *testing.T) {
	g, groups := threeGroups(8, 8, 8)
	g.Cluster(DefaultClusterOptions())

	lone := groups[0][3]
	kept := g.Edges[:0]
	for _, e := range g.Edges {
		if e.Src != lone && e.Dst != lone {
			kept = append(kept, e)
		}
	}
	g.Edges = kept

	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 3 {
		t.Fatalf("want 3 communities after the cut, got %d", len(comms))
	}
	if _, ok := g.Nodes[lone].Props["community"]; ok {
		t.Fatalf("%s has no edges any more and must lose its community id", lone)
	}
	if got := len(g.CommunityMembers(mustAtoi(t, communityOf(t, g, groups[0][0])))); got != 7 {
		t.Fatalf("group a has %d members, want 7", got)
	}
}

func TestClusterEmptyAndEdgeless(t *testing.T) {
	if got := New().Cluster(DefaultClusterOptions()); len(got) != 0 {
		t.Fatalf("empty graph: %+v", got)
	}
	g := New()
	for i := range 5 {
		addFunc(g, fmt.Sprintf("f%d", i), "x.php")
	}
	if got := g.Cluster(DefaultClusterOptions()); len(got) != 0 {
		t.Fatalf("graph without edges: %+v", got)
	}
	for id := range g.Nodes {
		if _, ok := g.Nodes[id].Props["community"]; ok {
			t.Fatalf("%s got a community without edges", id)
		}
	}
}

func TestClusterDefaultMinSizeIsFive(t *testing.T) {
	if got := DefaultClusterOptions().MinSize; got != 5 {
		t.Fatalf("default MinSize = %d, want 5", got)
	}
	g := New()
	addClique(g, "a", "a/x.php", 8)
	addClique(g, "p", "p/y.php", 4)
	addFunc(g, "s", "s/z.php")

	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 1 || comms[0].Size != 8 {
		t.Fatalf("default MinSize 5: %+v", comms)
	}
	for _, id := range []string{"p0", "p3", "s"} {
		if got := communityOf(t, g, id); got != "" {
			t.Fatalf("%s is in a group below MinSize with no link to a kept community, got community %q", id, got)
		}
	}
	if comms = g.Cluster(ClusterOptions{MinSize: 3}); len(comms) != 2 || comms[1].Size != 4 {
		t.Fatalf("MinSize 3 keeps the group of 4: %+v", comms)
	}
}

func TestClusterAbsorbsSmallGroupIntoStrongestNeighbour(t *testing.T) {
	g := New()
	a := addClique(g, "a", "a/x.php", 8)
	b := addClique(g, "b", "b/y.php", 8)
	p := addClique(g, "p", "p/z.php", 3)
	g.AddEdge(p[0], a[0], Calls, 0.95, "")
	g.AddEdge(p[1], a[0], Calls, 0.95, "")
	g.AddEdge(p[2], b[0], Calls, 0.95, "")

	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 2 {
		t.Fatalf("want 2 communities, the group of 3 is dissolved: %+v", comms)
	}
	// each member goes to the community it weighs most towards: p0 and p1 to a, p2 to b
	aID, bID := communityOf(t, g, a[0]), communityOf(t, g, b[0])
	for _, id := range []string{p[0], p[1]} {
		if got := communityOf(t, g, id); got != aID {
			t.Fatalf("%s is in community %q, want the one of a (%q)", id, got, aID)
		}
	}
	if got := communityOf(t, g, p[2]); got != bID {
		t.Fatalf("%s is in community %q, want the one of b (%q)", p[2], got, bID)
	}
	if comms[mustAtoi(t, aID)].Size != 10 || comms[mustAtoi(t, bID)].Size != 9 {
		t.Fatalf("sizes after absorption = %+v, want a 10 and b 9", comms)
	}
}

func TestClusterIsolatedPairStaysUnassigned(t *testing.T) {
	g := New()
	addClique(g, "a", "a/x.php", 8)
	addClique(g, "p", "p/y.php", 2)

	comms := g.Cluster(DefaultClusterOptions())
	if len(comms) != 1 || comms[0].Size != 8 {
		t.Fatalf("the pair has no link to a kept community: %+v", comms)
	}
	for _, id := range []string{"p0", "p1"} {
		if got := communityOf(t, g, id); got != "" {
			t.Fatalf("%s has no weight to any kept community and must stay unassigned, got %q", id, got)
		}
	}
}

func TestCommunityDirIgnoresContractsWhenCodeMembersExist(t *testing.T) {
	nodes := []*Node{
		{ID: "c1", Type: TypeColumn, Label: "c1", Path: "database/migrations/a.php"},
		{ID: "c2", Type: TypeColumn, Label: "c2", Path: "database/migrations/a.php"},
		{ID: "f1", Type: TypeFunction, Label: "f1", Path: "app/Services/S.php"},
	}
	deg := []float64{1, 1, 1}
	if c := describeCommunity(0, []int{0, 1, 2}, nodes, deg); c.Dir != "app/Services" || !strings.HasPrefix(c.Label, "app/Services ·") {
		t.Fatalf("community with code = %+v, want the directory of the code member", c)
	}
	if c := describeCommunity(0, []int{0, 1}, nodes, deg); c.Dir != "database/migrations" {
		t.Fatalf("contracts only = %+v, want the directory of the contracts", c)
	}
}

func TestClusterLargeGraphIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("large graph")
	}
	const n, m, group = 100000, 300000, 50
	g := New()
	for i := range n {
		g.AddNode(Node{ID: fmt.Sprintf("n%d", i), Type: TypeFunction, Path: fmt.Sprintf("d%d/f.php", i/group)})
	}
	rng := rand.New(rand.NewSource(7))
	for range m {
		a := rng.Intn(n)
		b := rng.Intn(n)
		if rng.Intn(20) < 19 {
			b = a/group*group + rng.Intn(group)
		}
		g.AddEdge(fmt.Sprintf("n%d", a), fmt.Sprintf("n%d", b), Calls, 0.9, "")
	}
	start := time.Now()
	comms := g.Cluster(DefaultClusterOptions())
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("clustering took %v", elapsed)
	}
	// modularity has a resolution limit: neighbouring planted groups merge, so expect far fewer than n/group communities,
	// but never a handful and never one per node
	if len(comms) < 100 || len(comms) > n/group {
		t.Fatalf("planted groups of %d give a few hundred communities, got %d", group, len(comms))
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	k, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
