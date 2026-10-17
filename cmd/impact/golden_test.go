package main

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
)

func TestAGoldenFlowSaysWhichEndpointFailedAndWhy(t *testing.T) {
	g := graph.New()
	g.AddNode(graph.Node{ID: "php:A::run", Type: graph.TypeMethod, Label: "A::run"})
	g.AddNode(graph.Node{ID: "php:B::run", Type: graph.TypeMethod, Label: "B::run"})
	g.AddNode(graph.Node{ID: "php:C::go", Type: graph.TypeMethod, Label: "C::go"})
	g.Index()
	for _, c := range []struct {
		gd   config.Golden
		want []string
	}{
		{config.Golden{From: "gone", To: "php:C::go"}, []string{`from "gone" matches no node`, "impact find"}},
		{config.Golden{From: "php:C::go", To: "run"}, []string{`to "run" is ambiguous`, "php:A::run", "php:B::run"}},
		{config.Golden{From: "php:A::run", To: "php:C::go", Via: []string{"nowhere"}}, []string{`waypoint "nowhere" matches no node`}},
	} {
		ok, why := checkGolden(g, c.gd)
		if ok {
			t.Errorf("%+v must fail", c.gd)
		}
		for _, w := range c.want {
			if !strings.Contains(why, w) {
				t.Errorf("%+v: %q lacks %q", c.gd, why, w)
			}
		}
	}
}
