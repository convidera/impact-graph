package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/convidera/impact-graph/internal/query"
)

// cmdQuery prints the structured answers a graph browser needs as JSON: `repos`, `nodes [text]` and `node <id>`. The shape
// is the contract of internal/query; walks and traces have their own --json.
func cmdQuery(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: impact query repos | nodes [text] | node <id>  [--db file] [--repo owner/name] [--type t] [--limit n]")
	}
	what, args := args[0], args[1:]
	var c common
	fs := flag.NewFlagSet("query "+what, flag.ContinueOnError)
	c.flags(fs)
	repo := fs.String("repo", "", "nodes: only this repository (owner/name, or a unique name)")
	typ := fs.String("type", "", "nodes: only this node type")
	limit := fs.Int("limit", 0, "nodes: how many to return")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	x := query.New(g)
	enc := json.NewEncoder(os.Stdout)
	switch what {
	case "repos":
		return enc.Encode(x.Repositories())
	case "nodes":
		text := ""
		if len(pos) > 0 {
			text = pos[0]
		}
		out, err := x.Search(*repo, text, *typ, *limit)
		if err != nil {
			return err
		}
		return enc.Encode(out)
	case "node":
		if len(pos) != 1 {
			return errors.New("usage: impact query node <id>")
		}
		out, err := x.Detail(pos[0])
		if err != nil {
			return fmt.Errorf("%w: %s", err, pos[0])
		}
		return enc.Encode(out)
	}
	return fmt.Errorf("unknown query %q (repos, nodes, node)", what)
}
