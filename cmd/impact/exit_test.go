package main

import (
	"errors"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func TestAnArgumentThatNamesNothingIsTheCallersError(t *testing.T) {
	g := graph.New()
	g.AddNode(graph.Node{ID: "php:A::run", Type: graph.TypeMethod, Label: "A::run"})
	g.AddNode(graph.Node{ID: "php:B::run", Type: graph.TypeMethod, Label: "B::run"})
	g.Index()
	var bad invalidArg
	if _, err := resolve(g, "nope"); !errors.As(err, &bad) {
		t.Errorf("no match must exit %d: %v", exitInvalidArgument, err)
	}
	if _, err := resolve(g, "run"); !errors.As(err, &bad) {
		t.Errorf("an ambiguous name must exit %d: %v", exitInvalidArgument, err)
	}
	if id, err := resolve(g, "php:A::run"); err != nil || id != "php:A::run" {
		t.Errorf("an exact id resolves: %q %v", id, err)
	}
}
