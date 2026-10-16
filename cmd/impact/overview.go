package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/convidera/impact/internal/report"
)

func cmdOverview(args []string) error {
	var c common
	fs := flag.NewFlagSet("overview", flag.ContinueOnError)
	c.flags(fs)
	communities := fs.Int("communities", 8, "communities to list")
	gods := fs.Int("gods", 8, "god nodes to list")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	fmt.Print(report.Overview(g, *communities, *gods))
	return nil
}

func cmdCommunities(args []string) error {
	var c common
	fs := flag.NewFlagSet("communities", flag.ContinueOnError)
	c.flags(fs)
	top := fs.Int("top", 20, "communities to list")
	id := fs.Int("id", -1, "show the members of this community")
	limit := fs.Int("limit", 20, "ids to list per member type with --id")
	recluster := fs.Bool("recluster", false, "recompute the communities in memory with the options below; the snapshot is not written")
	resolution := fs.Float64("resolution", 0, "with --recluster: modularity resolution, higher gives smaller communities (0 = configured value)")
	hubPercentile := fs.Float64("hub-percentile", -1, "with --recluster: leave out nodes above this degree percentile, 0 disables (-1 = configured value)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	g, cfg, err := c.graph()
	if err != nil {
		return err
	}
	if *recluster {
		o := cfg.ClusterOptions()
		if *resolution > 0 {
			o.Resolution = *resolution
		}
		if *hubPercentile >= 0 {
			o.HubPercentile = *hubPercentile
		}
		g.Cluster(o)
		fmt.Fprintf(os.Stderr, "reclustered in memory: resolution %g, hub percentile %g; the snapshot is unchanged\n", o.Resolution, o.HubPercentile)
	}
	if *id >= 0 {
		fmt.Print(report.Community(g, *id, *limit))
		return nil
	}
	fmt.Print(report.Communities(g, *top))
	return nil
}
