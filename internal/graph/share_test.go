package graph

import (
	"fmt"
	"math"
	"testing"
)

// A community that lies wholly in one repository has, as its share, the size, top members and cohesion Cluster stored.
func TestShareOfAWholeCommunityIsTheClustererCohesion(t *testing.T) {
	g := New()
	g.Meta["commit"] = "1111111"
	ring := func(prefix string, n int) {
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("php:%s%d", prefix, i)
			g.AddNode(Node{ID: id, Type: TypeMethod, Label: id, Path: prefix + ".php", Line: i + 1, Repo: "acme/shop"})
		}
		for i := 0; i < n; i++ {
			g.AddEdge(fmt.Sprintf("php:%s%d", prefix, i), fmt.Sprintf("php:%s%d", prefix, (i+1)%n), Calls, 0.9, "")
			g.AddEdge(fmt.Sprintf("php:%s%d", prefix, i), fmt.Sprintf("php:%s%d", prefix, (i+3)%n), Calls, 0.8, "")
		}
	}
	ring("a", 8)
	ring("b", 6)
	comms := g.Cluster(ClusterOptions{MinSize: 3})
	if len(comms) < 2 {
		t.Fatalf("the fixture must give two communities, got %d", len(comms))
	}
	shares := g.CommunityShares()
	for _, c := range comms {
		s := shares[c.ID]
		if len(s) != 1 || s[0].Repo != "acme/shop" {
			t.Fatalf("community %d has one repository: %+v", c.ID, s)
		}
		if s[0].Size != c.Size || math.Abs(s[0].Cohesion-c.Cohesion) > 1e-9 {
			t.Errorf("community %d: share size %d cohesion %v, clusterer %d %v", c.ID, s[0].Size, s[0].Cohesion, c.Size, c.Cohesion)
		}
		if fmt.Sprint(s[0].Top) != fmt.Sprint(c.Top) {
			t.Errorf("community %d: top %v, clusterer %v", c.ID, s[0].Top, c.Top)
		}
	}
}
