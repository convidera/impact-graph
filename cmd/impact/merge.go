package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/report"
)

func cmdMerge(args []string) error {
	fs := flag.NewFlagSet("merge", flag.ContinueOnError)
	out := fs.String("out", ".impact/merged.db", "snapshot to write")
	noCluster := fs.Bool("no-cluster", false, "do not cluster the merged graph (communities are per snapshot and are not carried over)")
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
	if !*noCluster {
		m.Cluster(graph.DefaultClusterOptions())
	}
	if err := graph.Save(m, *out); err != nil {
		return err
	}
	fmt.Printf("merged %d repositories -> %d nodes, %d edges, %d communities\n", len(parts), len(m.Nodes), len(m.Edges), len(m.Communities))
	fmt.Fprint(os.Stderr, report.RepoBlock(m))
	return nil
}
