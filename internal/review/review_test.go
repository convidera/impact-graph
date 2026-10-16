package review_test

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/review"
)

func areaGraph() *graph.Graph {
	g := graph.New()
	g.AddNode(graph.Node{ID: "php:A", Type: graph.TypeClass, Label: "A", Path: "app/a.php", Line: 1, Props: map[string]string{"community": "0"}})
	g.AddNode(graph.Node{ID: "php:A::run", Type: graph.TypeMethod, Label: "A::run", Path: "app/a.php", Line: 3, Props: map[string]string{"community": "0"}})
	g.AddNode(graph.Node{ID: "php:A::other", Type: graph.TypeMethod, Label: "A::other", Path: "app/a.php", Line: 10, Props: map[string]string{"community": "0"}})
	g.AddNode(graph.Node{ID: "php:C::go", Type: graph.TypeMethod, Label: "C::go", Path: "app/c.php", Line: 2, Props: map[string]string{"community": "1"}})
	g.AddNode(graph.Node{ID: "php:C::loose", Type: graph.TypeMethod, Label: "C::loose", Path: "app/c.php", Line: 8})
	g.Communities = []graph.Community{
		{ID: 1, Label: "c", Size: 2, Cohesion: 0.5},
		{ID: 0, Label: "app · A", Size: 3, Cohesion: 0.5},
	}
	g.Index()
	return g
}

func TestRunNamesTheChangedAreas(t *testing.T) {
	g := areaGraph()
	changes := []review.Change{
		{File: "app/a.php", Ranges: [][2]int{{3, 10}}},
		{File: "app/c.php", Ranges: [][2]int{{1, 2}}},
	}
	rep := review.Run(g, "HEAD", changes, review.DefaultOptions())
	if len(rep.Nodes) != 3 {
		t.Fatalf("changed nodes = %v, want the two methods of app/a.php and php:C::go", rep.Nodes)
	}
	want := []review.Area{{ID: 0, Label: "app · A", Nodes: 2}, {ID: 1, Label: "c", Nodes: 1}}
	if len(rep.Areas) != len(want) {
		t.Fatalf("areas = %+v, want %+v", rep.Areas, want)
	}
	for i := range want {
		if rep.Areas[i] != want[i] {
			t.Errorf("area %d = %+v, want %+v", i, rep.Areas[i], want[i])
		}
	}
	txt := review.Text(rep, 10)
	if !strings.HasPrefix(txt[strings.Index(txt, "\n## "):], "\n## changed areas\n  c0 app · A (2 changed)\n  c1 c (1 changed)\n") {
		t.Errorf("text report must start with the changed areas:\n%s", txt)
	}
}

func TestRunWithoutCommunitiesHasNoAreas(t *testing.T) {
	g := areaGraph()
	g.Communities = nil
	rep := review.Run(g, "HEAD", []review.Change{{File: "app/c.php", Ranges: [][2]int{{1, 2}}}}, review.DefaultOptions())
	if len(rep.Areas) != 0 {
		t.Errorf("areas = %+v, want none", rep.Areas)
	}
	if txt := review.Text(rep, 10); strings.Contains(txt, "changed areas") {
		t.Errorf("no section without areas:\n%s", txt)
	}
}
