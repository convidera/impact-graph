package extract

import (
	"strings"

	"github.com/convidera/impact/internal/graph"
)

// runLinks adds the edges declared in impact.toml: connections that no extractor can see, such as a value that leaves
// the code through a configuration file for an external program and comes back in its output. Each link carries its
// reason as evidence. An endpoint that does not exist is a warning, and a golden flow that depends on it fails.
func runLinks(ctx *Ctx) {
	for _, l := range ctx.Cfg.Links {
		from, to := resolveNode(ctx.G, l.From), resolveNode(ctx.G, l.To)
		if from == "" || to == "" {
			ctx.warn("link %s -> %s: endpoint not found (%s)", l.From, l.To, strings.TrimSpace(l.Why))
			continue
		}
		kind := l.Kind
		if kind == "" {
			kind = graph.Link
		}
		ctx.G.AddEdge(from, to, kind, 1, "impact.toml: "+strings.TrimSpace(l.Why))
	}
}

// resolveNode finds one node by id, repo path or unique substring.
func resolveNode(g *graph.Graph, text string) string {
	id, _ := g.Resolve(text)
	return id
}
