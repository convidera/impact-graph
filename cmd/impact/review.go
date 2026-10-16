package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/convidera/impact-graph/internal/check"
	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/review"
)

func cmdReview(args []string) error {
	var c common
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	c.flags(fs)
	base := fs.String("base", "", "commit to compare with (default: upstream or merge base with the default branch)")
	maxCost := fs.Float64("max-cost", review.DefaultOptions().MaxCost, "budget of the impact walk from each changed symbol")
	together := fs.Float64("together", review.DefaultOptions().Together, "minimum confidence of a co-change edge")
	limit := fs.Int("limit", review.DefaultOptions().PerGroup, "rows per group")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	b := *base
	if b == "" {
		b = review.DefaultBase(c.root)
	}
	changes, err := review.Changes(c.root, b)
	if err != nil {
		return err
	}
	o := review.DefaultOptions()
	o.MaxCost, o.Together = *maxCost, *together
	rep := review.Run(g, b, changes, o)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(rep)
	}
	fmt.Print(review.Text(rep, *limit))
	return nil
}

func cmdGolden(args []string) error {
	var c common
	fs := flag.NewFlagSet("golden", flag.ContinueOnError)
	c.flags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	g, cfg, err := c.graph()
	if err != nil {
		return err
	}
	failed := 0
	for _, gd := range cfg.Golden {
		ok, why := checkGolden(g, gd)
		if ok {
			fmt.Printf("ok   %s\n", gd.Name)
		} else {
			failed++
			fmt.Printf("FAIL %s: %s\n", gd.Name, why)
		}
	}
	if len(cfg.Golden) == 0 {
		fmt.Println("no [[golden]] flows in the configuration")
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d golden flows failed", failed, len(cfg.Golden))
	}
	return nil
}

func checkGolden(g *graph.Graph, gd config.Golden) (bool, string) {
	from, c1 := g.Resolve(gd.From)
	to, c2 := g.Resolve(gd.To)
	if from == "" || to == "" {
		_ = c1
		_ = c2
		return false, fmt.Sprintf("endpoint not found (%q or %q)", gd.From, gd.To)
	}
	var via []string
	for _, v := range gd.Via {
		id, _ := g.Resolve(v)
		if id == "" {
			return false, fmt.Sprintf("waypoint %q not found", v)
		}
		via = append(via, id)
	}
	o := graph.DefaultOptions()
	o.Mode = graph.ModeFlow
	o.MaxCost = gd.MaxCost
	if o.MaxCost == 0 {
		o.MaxCost = 30
	}
	if gd.Mode == "impact" {
		o.Mode = graph.ModeImpact
	}
	routes := g.TraceN(from, via, to, o, 1)
	switch {
	case gd.Absent && len(routes) > 0:
		return false, "a path exists but must not"
	case !gd.Absent && len(routes) == 0:
		return false, fmt.Sprintf("no %s path from %s to %s within cost %g", o.Mode, from, to, o.MaxCost)
	}
	return true, ""
}

func cmdCheck(args []string) error {
	var c common
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	c.flags(fs)
	asJSON := fs.Bool("json", false, "JSON output")
	verbose := fs.Bool("v", false, "list every check, not only the failures")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, _, err := c.load()
	if err != nil {
		return err
	}
	results, err := check.Run(c.root, cfg)
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(results)
	}
	failed := 0
	for _, r := range results {
		if r.OK {
			fmt.Printf("ok   %s (%d values)\n", r.Name, r.Found)
			continue
		}
		failed++
		fmt.Printf("FAIL %s\n", r.Name)
		for _, p := range r.Problems {
			fmt.Printf("       %s\n", p)
		}
	}
	_ = verbose
	if len(results) == 0 {
		fmt.Println("no [[check]] tables in the configuration")
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d drift checks failed", failed, len(results))
	}
	return nil
}
