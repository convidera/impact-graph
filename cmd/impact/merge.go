package main

import (
	"flag"
	"fmt"

	"github.com/convidera/impact/internal/graph"
)

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	out := fs.String("out", ".impact/merged.db", "snapshot to write")
	pos, err := parse(fs, args)
	if err != nil || len(pos) == 0 {
		return fmt.Errorf("usage: impact merge <snapshot>... --out <file>")
	}
	parts := map[string]*graph.Graph{}
	from := map[string]string{}
	for _, path := range pos {
		g, err := graph.Load(path)
		if err != nil {
			return err
		}
		repo := g.Meta["repo"]
		if repo == "" {
			return fmt.Errorf("snapshot %s has no repo in its metadata: run `impact index` on that repository again", path)
		}
		if prev, dup := from[repo]; dup {
			return fmt.Errorf("snapshots %s and %s both belong to %s: merge one of them out", prev, path, repo)
		}
		from[repo] = path
		parts[repo] = g
	}
	m := graph.Merge(parts)
	if err := graph.Save(m, *out); err != nil {
		return err
	}
	fmt.Printf("merged %d repositories -> %d nodes, %d edges\n", len(parts), len(m.Nodes), len(m.Edges))
	return nil
}
