package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/convidera/impact-graph/internal/build"
	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/mcp"
)

func cmdMCP(args []string) error {
	var c common
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	c.flags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, db, err := c.load()
	if err != nil {
		return err
	}
	reindex := func() (string, error) {
		g, st, err := build.Build(c.root, cfg)
		if err != nil {
			return "", err
		}
		if err := graph.Save(g, db); err != nil {
			return "", err
		}
		return st.String(), nil
	}
	load := func() (*graph.Graph, error) {
		if _, err := os.Stat(db); err != nil {
			if _, err := reindex(); err != nil {
				return nil, err
			}
		}
		return graph.Load(db)
	}
	srv := mcp.New(mcp.Source{Root: c.root, Load: load, Reindex: reindex})
	fmt.Fprintln(os.Stderr, "impact: MCP server on stdio for", c.root)
	return srv.Serve(os.Stdin, os.Stdout)
}
