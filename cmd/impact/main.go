// Command impact indexes a repository into a graph and answers what a change influences.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strings"

	"github.com/convidera/impact-graph/internal/build"
	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/report"
)

const usage = `impact: ask a repository what a change influences.

  impact index                      build the graph (.impact/graph.db)
  impact stats                      node, edge and community counts, schema and engine versions
  impact overview                   the repository map: snapshot, layers, communities, god nodes, contracts (--repo owner/name: one repository of a merge)
  impact communities                the subsystems clustering found (--id N: one of them; --recluster: tune it in memory)
  impact find <text>                locate nodes by id or label (--repo owner/name: one repository of a merge)
  impact impact <node>              what a change to <node> can affect (-d up: what can affect it)
  impact trace <from> <to>          the cheapest path a value or a change takes from <from> to <to>
  impact meet <a> <b>               the columns, fields and events two things share: how <a> reaches <b>
  impact hubs                       god nodes: the most connected symbols and contracts, how many communities they bridge, where a change floods (alias: god-nodes)
  impact review                     what the working tree or branch changes, and what it leaves out, and the [[obligation]]s it does not meet (--strict: fail on those)
  impact suggest                    candidate [[obligation]] tables mined from git history, to confirm and paste into impact.toml
  impact merge <snapshot>...        combine the snapshots of several repositories into one graph (--out <file>)
  impact golden                     check the flows pinned in impact.toml
  impact check                      drift checks: lists kept by hand against the contracts that declare them
  impact mcp                        serve the queries as an MCP server on stdio
  impact version                    the engine version (also -v, --version)

Common flags: --root <repo> (default .), --db <file>, --config <file>
`

type common struct {
	root, db, cfgPath string
}

func (c *common) flags(fs *flag.FlagSet) {
	fs.StringVar(&c.root, "root", ".", "repository root")
	fs.StringVar(&c.db, "db", "", "graph snapshot (default <root>/.impact/graph.db)")
	fs.StringVar(&c.cfgPath, "config", "", "config file (default <root>/impact.toml)")
}

func (c *common) load() (*config.Config, string, error) {
	root, err := filepath.Abs(c.root)
	if err != nil {
		return nil, "", err
	}
	c.root = root
	cfg, err := config.Load(root, c.cfgPath)
	if err != nil {
		return nil, "", err
	}
	db := c.db
	if db == "" {
		db = filepath.Join(root, cfg.Index.Db)
	}
	return cfg, db, nil
}

func (c *common) graph() (*graph.Graph, *config.Config, error) {
	cfg, db, err := c.load()
	if err != nil {
		return nil, nil, err
	}
	g, err := graph.Load(db)
	if err != nil {
		var se *graph.SchemaError
		if errors.As(err, &se) {
			return nil, nil, err
		}
		return nil, nil, fmt.Errorf("%v (run `impact index` first)", err)
	}
	return g, cfg, nil
}

// parse allows flags after positional arguments.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" {
		fmt.Print(usage)
		return
	}
	if os.Args[1] == "version" || os.Args[1] == "-v" || os.Args[1] == "--version" {
		fmt.Println(versionLine())
		return
	}
	if f := os.Getenv("IMPACT_CPUPROFILE"); f != "" {
		if out, err := os.Create(f); err == nil {
			_ = pprof.StartCPUProfile(out)
			defer pprof.StopCPUProfile()
		}
	}
	var err error
	args := os.Args[2:]
	switch os.Args[1] {
	case "index":
		err = cmdIndex(args)
	case "stats":
		err = cmdStats(args)
	case "overview":
		err = cmdOverview(args)
	case "communities":
		err = cmdCommunities(args)
	case "merge":
		err = cmdMerge(args)
	case "find":
		err = cmdFind(args)
	case "impact":
		err = cmdImpact(args)
	case "trace":
		err = cmdTrace(args)
	case "meet":
		err = cmdMeet(args)
	case "hubs", "god-nodes":
		err = cmdHubs(args)
	case "review":
		err = cmdReview(args)
	case "suggest":
		err = cmdSuggest(args)
	case "golden":
		err = cmdGolden(args)
	case "check":
		err = cmdCheck(args)
	case "mcp":
		err = cmdMCP(args)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "impact:", err)
		pprof.StopCPUProfile()
		os.Exit(1)
	}
}

func cmdIndex(args []string) error {
	var c common
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	c.flags(fs)
	quiet := fs.Bool("q", false, "quiet")
	maxFiles := fs.Int("max-files", 0, "fail when more than this many files would be indexed (0: impact.toml, else no limit)")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	cfg, db, err := c.load()
	if err != nil {
		return err
	}
	if *maxFiles > 0 {
		cfg.Index.MaxFiles = *maxFiles
	}
	g, st, err := build.Build(c.root, cfg)
	if err != nil {
		return err
	}
	if err := graph.Save(g, db); err != nil {
		return err
	}
	if !*quiet {
		for _, w := range st.Warnings {
			fmt.Println("warning:", w)
		}
		fmt.Printf("%s (%s)\n", st, db)
	}
	return nil
}

func cmdStats(args []string) error {
	var c common
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	c.flags(fs)
	if _, err := parse(fs, args); err != nil {
		return err
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	types, kinds := map[string]int{}, map[string]int{}
	for _, n := range g.Nodes {
		types[n.Type]++
	}
	for _, e := range g.Edges {
		kinds[e.Kind]++
	}
	fmt.Printf("repo %s, commit %s\n", orDash(g.Meta["repo"]), orDash(g.Meta["commit"]))
	fmt.Printf("schema %s, engine %s\n", orDash(g.Meta["schema_version"]), orDash(g.Meta["engine_version"]))
	fmt.Printf("built %s: %d nodes, %d edges, %d communities\nnodes:", orDash(g.Meta["built_at"]), len(g.Nodes), len(g.Edges), len(g.Communities))
	for _, k := range sortedKeys(types) {
		fmt.Printf(" %s=%d", k, types[k])
	}
	fmt.Print("\nedges:")
	for _, k := range sortedKeys(kinds) {
		fmt.Printf(" %s=%d", k, kinds[k])
	}
	fmt.Println()
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func cmdFind(args []string) error {
	var c common
	fs := flag.NewFlagSet("find", flag.ContinueOnError)
	c.flags(fs)
	limit := fs.Int("limit", 30, "max results")
	asJSON := fs.Bool("json", false, "JSON output")
	repo := fs.String("repo", "", "only this repository of a merged graph, as owner/name")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return fmt.Errorf("usage: impact find <text> [--repo owner/name]")
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	r, err := g.ResolveRepo(*repo)
	if err != nil {
		return err
	}
	found := g.FindIn(pos[0], *limit, r)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(found)
	}
	fmt.Print(report.FindIn(g, r, pos[0], *limit))
	return nil
}

type walkFlags struct {
	dir      string
	flow     bool
	maxCost  float64
	minConf  float64
	hubLimit int
	history  bool
}

func (w *walkFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&w.dir, "d", "down", "direction: down (what this affects) or up (what affects this)")
	fs.BoolVar(&w.flow, "flow", false, "follow the data instead of the change (arguments, return values, columns)")
	fs.Float64Var(&w.maxCost, "max-cost", 7, "budget; structure edges (imports, inheritance) cost 3, data edges 1")
	fs.Float64Var(&w.minConf, "min-confidence", 0.7, "hide edges below this; 0 shows every guess")
	fs.IntVar(&w.hubLimit, "hub-limit", 60, "do not expand nodes with more edges than this (0 = expand all)")
	fs.BoolVar(&w.history, "history", false, "also follow files that change together in git history")
}

func (w *walkFlags) options() graph.Options {
	o := graph.DefaultOptions()
	o.Dir, o.MaxCost, o.MinConf, o.HubLimit, o.History = w.dir, w.maxCost, w.minConf, w.hubLimit, w.history
	if w.flow {
		o.Mode = graph.ModeFlow
	}
	return o
}

func resolve(g *graph.Graph, text string) (string, error) {
	id, cands := g.Resolve(text)
	if id != "" {
		return id, nil
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no node matches %q", text)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%q is ambiguous, pick one:", text)
	for _, n := range cands {
		fmt.Fprintf(&b, "\n  %s", n.ID)
	}
	return "", fmt.Errorf("%s", b.String())
}

func cmdImpact(args []string) error {
	var c common
	var w walkFlags
	fs := flag.NewFlagSet("impact", flag.ContinueOnError)
	c.flags(fs)
	w.register(fs)
	limit := fs.Int("limit", 25, "rows per group")
	paths := fs.Bool("paths", false, "print the path to each result")
	evidence := fs.Bool("evidence", false, "print file:line evidence of the last hop")
	only := fs.String("only", "", "comma list of groups: contracts, code, tests, docs")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return fmt.Errorf("usage: impact impact <node> [flags]")
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	id, err := resolve(g, pos[0])
	if err != nil {
		return err
	}
	opts := w.options()
	hits, hubs := g.Walk(id, opts)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"start": id, "mode": opts.Mode, "dir": opts.Dir, "groups": report.Group(g, hits, id, *only), "hubs": hubs})
	}
	fmt.Print(report.Impact(g, id, hits, hubs, opts, *limit, *paths, *evidence, *only))
	return nil
}

type viaFlag []string

func (v *viaFlag) String() string     { return strings.Join(*v, ",") }
func (v *viaFlag) Set(s string) error { *v = append(*v, s); return nil }

func cmdTrace(args []string) error {
	var c common
	var w walkFlags
	var via viaFlag
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	c.flags(fs)
	w.register(fs)
	impactMode := fs.Bool("impact", false, "follow the direction a change spreads instead of the data (default)")
	evidence := fs.Bool("evidence", true, "print the evidence of each hop")
	paths := fs.Int("paths", 1, "show up to N distinct routes")
	fs.Var(&via, "via", "a node the path must pass through (repeatable)")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("usage: impact trace <from> <to> [--via <node>] [--paths N] [flags]")
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	a, err := resolve(g, pos[0])
	if err != nil {
		return err
	}
	b, err := resolve(g, pos[1])
	if err != nil {
		return err
	}
	var waypoints []string
	for _, v := range via {
		id, err := resolve(g, v)
		if err != nil {
			return err
		}
		waypoints = append(waypoints, id)
	}
	opts := w.options()
	if !*impactMode {
		opts.Mode = graph.ModeFlow
	}
	if opts.MaxCost == 7 {
		opts.MaxCost = 30
	}
	routes := g.TraceN(a, waypoints, b, opts, *paths)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(routes)
	}
	if len(routes) == 0 {
		return fmt.Errorf("no %s path from %s to %s within cost %g at confidence >= %g (try --min-confidence 0 or --max-cost)", opts.Mode, a, b, opts.MaxCost, opts.MinConf)
	}
	fmt.Print(report.Routes(g, opts.Mode, routes, *evidence))
	return nil
}

func cmdHubs(args []string) error {
	var c common
	fs := flag.NewFlagSet("hubs", flag.ContinueOnError)
	c.flags(fs)
	top := fs.Int("top", 20, "rows")
	typ := fs.String("type", "", "only nodes of this type (class, method, table, route, ...)")
	repo := fs.String("repo", "", "only this repository of a merged graph, as owner/name")
	if _, err := parse(fs, args); err != nil {
		return err
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	r, err := g.ResolveRepo(*repo)
	if err != nil {
		return err
	}
	var types []string
	if *typ != "" {
		types = []string{*typ}
	}
	fmt.Print(report.GodNodes(g, g.GodNodes(*top, graph.GodOptions{Types: types, Repo: r})))
	return nil
}

func cmdMeet(args []string) error {
	var c common
	var w walkFlags
	fs := flag.NewFlagSet("meet", flag.ContinueOnError)
	c.flags(fs)
	w.register(fs)
	limit := fs.Int("limit", 15, "contracts to show")
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 2 {
		return fmt.Errorf("usage: impact meet <a> <b> [flags]")
	}
	g, _, err := c.graph()
	if err != nil {
		return err
	}
	a, err := resolve(g, pos[0])
	if err != nil {
		return err
	}
	b, err := resolve(g, pos[1])
	if err != nil {
		return err
	}
	o := w.options()
	if o.MaxCost == 7 {
		o.MaxCost = 12
	}
	ms := g.Meet(a, b, o)
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(ms)
	}
	fmt.Print(report.Meet(g, a, b, ms, *limit))
	return nil
}
