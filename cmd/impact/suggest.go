package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/convidera/impact-graph/internal/suggest"
)

func cmdSuggest(args []string) error {
	var c common
	fs := flag.NewFlagSet("suggest", flag.ContinueOnError)
	c.flags(fs)
	d := suggest.DefaultOptions()
	commits := fs.Int("commits", d.Commits, "how many commits of history to read")
	support := fs.Int("min-support", d.MinSupport, "commits in which trigger and companion changed together")
	ratio := fs.Float64("min-ratio", d.MinRatio, "share of the commits touching the trigger that also touch the companion")
	top := fs.Int("top", d.Top, "candidates to print")
	asJSON := fs.Bool("json", false, "JSON output")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, _, err := c.load()
	if err != nil {
		return err
	}
	cs, err := suggest.Mine(c.root, cfg, suggest.Options{Commits: *commits, MaxFiles: d.MaxFiles, MinSupport: *support, MinRatio: *ratio, Top: *top})
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(cs)
	}
	if len(cs) == 0 {
		fmt.Println("history suggests no obligations (try --min-support or --min-ratio lower)")
		return nil
	}
	fmt.Print(suggest.TOML(cs))
	return nil
}
