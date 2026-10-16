// Package review answers the question an author or reviewer forgets to ask: what does this change affect that it does
// not touch? It maps a git diff onto graph nodes, walks the impact from each, and reports what is related but absent
// from the diff: tests, documents, contracts, callers and files that usually change together.
package review

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/convidera/impact/internal/graph"
)

// Change is one changed file and the line ranges (new side) the diff touches. A file added or untracked has no ranges
// and counts as changed as a whole.
type Change struct {
	File   string
	Ranges [][2]int
	Whole  bool
}

// Item is something related to the change that the change does not contain.
type Item struct {
	Path    string  `json:"path"`
	Group   string  `json:"group"` // tests, docs, contracts, code, together
	Cost    float64 `json:"cost"`
	Conf    float64 `json:"conf"`
	Why     string  `json:"why"`
	Nodes   int     `json:"nodes"`
	Example string  `json:"example"`
}

// Area is a community (subsystem) that the changed nodes belong to, with how many of them it holds.
type Area struct {
	ID    int    `json:"id"`
	Label string `json:"label"`
	Nodes int    `json:"nodes"`
}

// Report is the result of a review.
type Report struct {
	Base    string   `json:"base"`
	Changed []string `json:"changed"`
	Nodes   []string `json:"changed_nodes"`
	Areas   []Area   `json:"areas"`
	Items   []Item   `json:"items"`
}

var hunk = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

func git(root string, args ...string) ([]byte, error) {
	return exec.Command("git", append([]string{"-C", root}, args...)...).Output()
}

// DefaultBase picks the commit to compare with: the upstream of the branch, else the merge base with the default branch.
func DefaultBase(root string) string {
	if out, err := git(root, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		if b, err := git(root, "merge-base", "HEAD", strings.TrimSpace(string(out))); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	for _, ref := range []string{"origin/HEAD", "origin/main", "origin/develop", "origin/master", "main", "develop", "master"} {
		if b, err := git(root, "merge-base", "HEAD", ref); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return "HEAD"
}

// Changes lists what differs between base and the working tree, untracked files included.
func Changes(root, base string) ([]Change, error) {
	out, err := git(root, "diff", "-U0", "--no-color", "--no-ext-diff", base)
	if err != nil {
		return nil, fmt.Errorf("git diff %s: %w", base, err)
	}
	var res []Change
	var cur *Change
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		l := sc.Text()
		switch {
		case strings.HasPrefix(l, "+++ b/"):
			res = append(res, Change{File: strings.TrimPrefix(l, "+++ b/")})
			cur = &res[len(res)-1]
		case strings.HasPrefix(l, "+++ /dev/null"):
			cur = nil
		case strings.HasPrefix(l, "--- a/"):
			// a deleted file has no new side; the next +++ line says /dev/null
			name := strings.TrimPrefix(l, "--- a/")
			cur = nil
			_ = name
		case strings.HasPrefix(l, "@@") && cur != nil:
			if m := hunk.FindStringSubmatch(l); m != nil {
				start, _ := strconv.Atoi(m[1])
				n := 1
				if m[2] != "" {
					n, _ = strconv.Atoi(m[2])
				}
				if n == 0 {
					n = 1
				}
				cur.Ranges = append(cur.Ranges, [2]int{start, start + n - 1})
			}
		}
	}
	if un, err := git(root, "ls-files", "-o", "--exclude-standard"); err == nil {
		for _, f := range strings.Split(strings.TrimSpace(string(un)), "\n") {
			if f != "" {
				res = append(res, Change{File: f, Whole: true})
			}
		}
	}
	return res, nil
}

// Options tune a review.
type Options struct {
	MaxCost  float64
	MinConf  float64
	Together float64 // minimum confidence of a co-change edge
	PerGroup int
}

// DefaultOptions are what the CLI and the MCP tool use.
func DefaultOptions() Options { return Options{MaxCost: 5, MinConf: 0.7, Together: 0.6, PerGroup: 12} }

// Run reviews a set of changes against the graph.
func Run(g *graph.Graph, base string, changes []Change, o Options) Report {
	rep := Report{Base: base}
	changed := map[string]bool{}
	for _, c := range changes {
		changed[c.File] = true
		rep.Changed = append(rep.Changed, c.File)
	}
	sort.Strings(rep.Changed)

	byPath := map[string][]*graph.Node{}
	for _, n := range g.Nodes {
		if n.Path != "" && n.Type != graph.TypeFile && n.Type != graph.TypeTest && n.Type != graph.TypeDoc {
			byPath[n.Path] = append(byPath[n.Path], n)
		}
	}
	var starts []string
	for _, c := range changes {
		nodes := touched(byPath[c.File], c)
		for _, n := range nodes {
			starts = append(starts, n.ID)
		}
		if len(nodes) == 0 && g.Has("file:"+c.File) {
			starts = append(starts, "file:"+c.File)
		}
	}
	sort.Strings(starts)
	rep.Nodes = starts
	rep.Areas = areas(g, starts)

	found := map[string]*Item{}
	consider := func(path, group string, cost, conf float64, why, example string) {
		if path == "" || changed[path] {
			return
		}
		key := group + "\x00" + path
		it := found[key]
		if it == nil {
			found[key] = &Item{Path: path, Group: group, Cost: cost, Conf: conf, Why: why, Nodes: 1, Example: example}
			return
		}
		it.Nodes++
		if cost < it.Cost {
			it.Cost, it.Conf, it.Why, it.Example = cost, conf, why, example
		}
	}
	opts := graph.DefaultOptions()
	opts.MaxCost, opts.MinConf = o.MaxCost, o.MinConf
	for _, s := range starts {
		hits, _ := g.Walk(s, opts)
		for id, h := range hits {
			if id == s {
				continue
			}
			n := g.Nodes[id]
			if n == nil || n.Path == "" {
				continue
			}
			group := g.Group(id)
			if n.Type == graph.TypeFile {
				switch n.Props["file_kind"] {
				case "test":
					group = "tests"
				case "doc":
					group = "docs"
				default:
					continue
				}
			}
			path := PathString(g, hits, id)
			consider(n.Path, group, h.Cost, h.Conf, path, id)
		}
	}
	// files that usually change together with a changed file
	for _, c := range changes {
		for _, i := range g.Out("file:" + c.File) {
			e := g.Edges[i]
			if e.Kind == graph.CoChanged && e.Conf >= o.Together {
				consider(strings.TrimPrefix(e.Dst, "file:"), "together", 2, e.Conf, c.File+" "+e.Evidence, e.Dst)
			}
		}
		for _, i := range g.In("file:" + c.File) {
			e := g.Edges[i]
			if e.Kind == graph.CoChanged && e.Conf >= o.Together {
				consider(strings.TrimPrefix(e.Src, "file:"), "together", 2, e.Conf, c.File+" "+e.Evidence, e.Src)
			}
		}
	}
	for _, it := range found {
		rep.Items = append(rep.Items, *it)
	}
	sort.Slice(rep.Items, func(i, j int) bool {
		a, b := rep.Items[i], rep.Items[j]
		if a.Group != b.Group {
			return groupOrder(a.Group) < groupOrder(b.Group)
		}
		if a.Cost != b.Cost {
			return a.Cost < b.Cost
		}
		return a.Path < b.Path
	})
	return rep
}

// areas counts the changed nodes per community, largest first, ties by id.
func areas(g *graph.Graph, ids []string) []Area {
	counts := map[int]int{}
	for _, id := range ids {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		if c, err := strconv.Atoi(n.Props["community"]); err == nil {
			counts[c]++
		}
	}
	labels := map[int]string{}
	for _, c := range g.Communities {
		labels[c.ID] = c.Label
	}
	var out []Area
	for id, n := range counts {
		if label, ok := labels[id]; ok {
			out = append(out, Area{ID: id, Label: label, Nodes: n})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Nodes != out[j].Nodes {
			return out[i].Nodes > out[j].Nodes
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func groupOrder(g string) int {
	for i, x := range []string{"tests", "docs", "contracts", "code", "together"} {
		if x == g {
			return i
		}
	}
	return 9
}

// touched returns the nodes of a file that overlap the changed lines.
func touched(nodes []*graph.Node, c Change) []*graph.Node {
	var out []*graph.Node
	for _, n := range nodes {
		if c.Whole {
			out = append(out, n)
			continue
		}
		end := n.Line
		if e, err := strconv.Atoi(n.Props["end"]); err == nil && e >= n.Line {
			end = e
		}
		for _, r := range c.Ranges {
			if r[0] <= end && r[1] >= n.Line {
				out = append(out, n)
				break
			}
		}
	}
	// a changed class body that contains changed methods is represented by the methods
	if len(out) > 1 {
		var narrow []*graph.Node
		for _, n := range out {
			if n.Type == graph.TypeClass && n.Props["lang"] != "" {
				continue
			}
			narrow = append(narrow, n)
		}
		if len(narrow) > 0 {
			out = narrow
		}
	}
	return out
}

// PathString renders the path of a hit as `a -> b -> c` with short labels.
func PathString(g *graph.Graph, hits map[string]*graph.Hit, id string) string {
	var parts []string
	for _, p := range graph.PathTo(hits, id) {
		if n := g.Nodes[p]; n != nil {
			parts = append(parts, n.Label)
		} else {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " -> ")
}

// Text renders a report for a terminal or an agent.
func Text(r Report, perGroup int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "review against %s: %d changed files, %d changed symbols or contracts\n", r.Base, len(r.Changed), len(r.Nodes))
	if len(r.Areas) > 0 {
		b.WriteString("\n## changed areas\n")
		for _, a := range r.Areas {
			fmt.Fprintf(&b, "  c%d %s (%d changed)\n", a.ID, a.Label, a.Nodes)
		}
	}
	titles := map[string]string{
		"tests":     "tests that exercise the change and are not in it",
		"docs":      "documents that name the changed things and are not in the change",
		"contracts": "contracts (routes, tools, columns, events) the change reaches",
		"code":      "code that depends on the change and is not in it",
		"together":  "files that usually change together with the changed files (git history)",
	}
	for _, grp := range []string{"tests", "docs", "contracts", "code", "together"} {
		var items []Item
		for _, it := range r.Items {
			if it.Group == grp {
				items = append(items, it)
			}
		}
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (%d)\n", titles[grp], len(items))
		for i, it := range items {
			if perGroup > 0 && i >= perGroup {
				fmt.Fprintf(&b, "  ... %d more\n", len(items)-perGroup)
				break
			}
			fmt.Fprintf(&b, "  %s  [cost %.1f, confidence %.2f]\n      because %s\n", it.Path, it.Cost, it.Conf, it.Why)
		}
	}
	if len(r.Items) == 0 {
		b.WriteString("\nnothing related is missing from the change\n")
	}
	return b.String()
}
