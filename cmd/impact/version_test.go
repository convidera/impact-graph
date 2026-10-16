package main

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func TestVersionLineUsesEngineVersion(t *testing.T) {
	old := graph.EngineVersion
	t.Cleanup(func() { graph.EngineVersion = old })
	graph.EngineVersion = "v1.2.3"
	if got := versionLine(); !strings.HasPrefix(got, "impact v1.2.3 (") {
		t.Fatalf("versionLine() = %q", got)
	}
}
