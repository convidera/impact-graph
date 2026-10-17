// Package report renders query results as text for a terminal or an agent.
package report

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/convidera/impact-graph/internal/graph"
)

// Where formats the position of a node.
func Where(n *graph.Node) string {
	if n == nil || n.Path == "" {
		return ""
	}
	if n.Line > 0 {
		return fmt.Sprintf("%s:%d", n.Path, n.Line)
	}
	return n.Path
}

// Find lists the nodes that match text.
func Find(g *graph.Graph, text string, limit int) string { return FindIn(g, "", text, limit) }

// FindIn is Find restricted to a repository ("" for all of them).
func FindIn(g *graph.Graph, repo, text string, limit int) string {
	var b strings.Builder
	for _, n := range g.FindIn(text, limit, repo) {
		fmt.Fprintf(&b, "%-9s %s  %s\n", n.Type, n.ID, Where(n))
	}
	return b.String()
}

// Group sorts the hits of a walk into contracts, code, tests and docs.
func Group(g *graph.Graph, hits map[string]*graph.Hit, start, only string) map[string][]*graph.Hit {
	allow := map[string]bool{}
	for _, s := range strings.Split(only, ",") {
		if s = strings.TrimSpace(s); s != "" {
			allow[s] = true
		}
	}
	out := map[string][]*graph.Hit{}
	for id, h := range hits {
		if id == start {
			continue
		}
		n := g.Nodes[id]
		if n == nil || n.Type == graph.TypeFile && (n.Props["file_kind"] == "code" || n.Props["file_kind"] == "generated") {
			continue
		}
		grp := g.Group(id)
		if len(allow) > 0 && !allow[grp] {
			continue
		}
		out[grp] = append(out[grp], h)
	}
	for _, l := range out {
		sort.Slice(l, func(i, j int) bool {
			if l[i].Cost != l[j].Cost {
				return l[i].Cost < l[j].Cost
			}
			return l[i].ID < l[j].ID
		})
	}
	return out
}

// Impact renders a walk from start.
func Impact(g *graph.Graph, start string, hits map[string]*graph.Hit, hubs map[string]int, o graph.Options, limit int, paths, evidence bool, only string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d nodes reached (%s, %s, cost <= %g, confidence >= %g)\n", start, len(hits)-1, o.Mode, o.Dir, o.MaxCost, o.MinConf)
	if c, ok := communityOf(g, start); ok {
		fmt.Fprintf(&b, "community c%d: %s\n", c.ID, c.Label)
	}
	res := Group(g, hits, start, only)
	for _, grp := range []string{"contracts", "code", "tests", "docs"} {
		list := res[grp]
		if len(list) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n## %s (%d)\n", grp, len(list))
		for i, h := range list {
			if limit > 0 && i >= limit {
				fmt.Fprintf(&b, "  ... %d more (--limit)\n", len(list)-limit)
				break
			}
			fmt.Fprintf(&b, "  %4.1f %s  %s  %s\n", h.Cost, h.ID, Where(g.Nodes[h.ID]), edgeTag(h.Kind, h.Conf, h.Prov))
			if paths {
				fmt.Fprintf(&b, "       path: %s\n", strings.Join(graph.PathTo(hits, h.ID), " -> "))
			}
			if evidence && h.Evidence != "" {
				fmt.Fprintf(&b, "       at %s\n", h.Evidence)
			}
		}
	}
	if len(hubs) > 0 {
		b.WriteString("\nhubs not expanded (more than the hub limit of edges):\n")
		ids := make([]string, 0, len(hubs))
		for id := range hubs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			fmt.Fprintf(&b, "  %s: %d edges\n", id, hubs[id])
		}
	}
	return b.String()
}

// Path renders one route found by a trace.
func Path(g *graph.Graph, mode string, path []*graph.Hit, evidence bool) string {
	var b strings.Builder
	weakest := 1.0
	cost := 0.0
	for _, h := range path[1:] {
		if h.EdgeConf < weakest {
			weakest = h.EdgeConf
		}
	}
	for _, h := range path {
		cost += h.Step
	}
	fmt.Fprintf(&b, "%s path, cost %.1f, weakest edge %.2f\n", mode, cost, weakest)
	for i, h := range path {
		n := g.Nodes[h.ID]
		if i == 0 {
			fmt.Fprintf(&b, "  %s  %s\n", h.ID, Where(n))
			continue
		}
		arrow := h.Kind
		if h.Walked == "against" {
			arrow += " (against)"
		}
		fmt.Fprintf(&b, "   | %s [%s]\n  %s  %s\n", arrow, provTag(h.EdgeConf, h.Prov), h.ID, Where(n))
		if evidence && h.Evidence != "" {
			fmt.Fprintf(&b, "     at %s\n", h.Evidence)
		}
	}
	return b.String()
}

// Routes renders the routes of a trace; several routes get a heading each.
func Routes(g *graph.Graph, mode string, routes [][]*graph.Hit, evidence bool) string {
	var b strings.Builder
	for i, r := range routes {
		if len(routes) > 1 {
			fmt.Fprintf(&b, "\n== route %d ==\n", i+1)
		}
		b.WriteString(Path(g, mode, r, evidence))
	}
	return b.String()
}

// edgeTag formats the kind, confidence and provenance of the last edge of a hit: `[calls 0.95 ast]`.
func edgeTag(kind string, conf float64, prov string) string {
	return fmt.Sprintf("[%s %s]", kind, provTag(conf, prov))
}

// provTag formats the confidence and provenance of an edge: `0.95 ast`, or `0.95` when the provenance is unknown.
func provTag(conf float64, prov string) string {
	if prov == "" {
		return fmt.Sprintf("%.2f", conf)
	}
	return fmt.Sprintf("%.2f %s", conf, prov)
}

// Meet renders the contracts two nodes share.
func Meet(g *graph.Graph, a, b string, ms []graph.Meeting, limit int) string {
	var bld strings.Builder
	fmt.Fprintf(&bld, "%s and %s meet in %d contracts\n", a, b, len(ms))
	label := func(id string) string {
		if n := g.Nodes[id]; n != nil {
			return n.Label
		}
		return id
	}
	for i, m := range ms {
		if limit > 0 && i >= limit {
			fmt.Fprintf(&bld, "  ... %d more (--limit)\n", len(ms)-limit)
			break
		}
		n := g.Nodes[m.ID]
		fmt.Fprintf(&bld, "\n%s  %s\n", m.ID, Where(n))
		var down, up []string
		for _, id := range m.Down {
			down = append(down, label(id))
		}
		for _, id := range m.Up {
			up = append(up, label(id))
		}
		fmt.Fprintf(&bld, "   in : %s\n   out: %s\n", strings.Join(down, " -> "), strings.Join(up, " -> "))
	}
	return bld.String()
}

// Communities lists the communities of the graph, largest first; top <= 0 lists all of them.
func Communities(g *graph.Graph, top int) string { return CommunitiesIn(g, "", top) }

// CommunitiesIn is Communities restricted to the communities that belong to a repository ("" for all of them).
func CommunitiesIn(g *graph.Graph, repo string, top int) string {
	var b strings.Builder
	ranked := ownedBy(g, repo, sortedCommunities(g))
	fmt.Fprintf(&b, "communities (%d):\n", len(ranked))
	for i, c := range ranked {
		if top > 0 && i >= top {
			fmt.Fprintf(&b, "  ... %d more\n", len(ranked)-top)
			break
		}
		fmt.Fprintf(&b, "c%d  %d nodes  cohesion %.2f  %s\n", c.ID, c.Size, c.Cohesion, c.Label)
	}
	return b.String()
}

// Community describes one community: its label, directory, size, most connected members and its members by type. limit
// caps the ids listed per type; limit <= 0 lists them all. An unknown id is reported as text, not as an error.
func Community(g *graph.Graph, id, limit int) string { return CommunityIn(g, "", id, limit) }

// CommunityIn is Community for the members of one repository ("" for all of them). A community that belongs to another
// repository is reported as not found there.
func CommunityIn(g *graph.Graph, repo string, id, limit int) string {
	c, ok := findCommunity(g, id)
	if !ok || len(ownedBy(g, repo, []graph.Community{c})) == 0 {
		if repo != "" {
			return fmt.Sprintf("no community %d in %s", id, repo)
		}
		return fmt.Sprintf("no community %d", id)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "c%d  %s\n", c.ID, c.Label)
	if c.Dir != "" {
		fmt.Fprintf(&b, "dir       %s\n", c.Dir)
	}
	fmt.Fprintf(&b, "size      %d nodes\n", c.Size)
	fmt.Fprintf(&b, "cohesion  %.2f\n", c.Cohesion)
	if top := keptTop(g, repo, c.Top); len(top) > 0 {
		b.WriteString("\ntop members:\n")
		for _, id := range top {
			fmt.Fprintf(&b, "  %s  %s\n", id, Where(g.Nodes[id]))
		}
	}
	byType := map[string][]string{}
	for _, n := range g.CommunityMembers(c.ID) {
		if g.InRepo(n, repo) {
			byType[n.Type] = append(byType[n.Type], n.ID)
		}
	}
	b.WriteString("\nmembers by type:\n")
	for _, t := range sortedKeys(byType) {
		ids := byType[t]
		fmt.Fprintf(&b, "  %s (%d)\n", t, len(ids))
		for i, id := range ids {
			if limit > 0 && i >= limit {
				fmt.Fprintf(&b, "    ... %d more\n", len(ids)-limit)
				break
			}
			fmt.Fprintf(&b, "    %s\n", id)
		}
	}
	return b.String()
}

// GodNodes renders the most connected nodes as a table: how many distinct neighbours they have, how many communities
// those neighbours belong to, and where the node is.
func GodNodes(g *graph.Graph, nodes []graph.GodNode) string {
	if len(nodes) == 0 {
		return "no god nodes\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%6s %4s %4s %7s %-9s %-8s %s  %s\n", "degree", "in", "out", "bridges", "community", "type", "id", "path:line")
	for _, n := range nodes {
		community := "-"
		if _, ok := findCommunity(g, atoi(n.Community)); ok && n.Community != "" {
			community = "c" + n.Community
		}
		fmt.Fprintf(&b, "%6d %4d %4d %7d %-9s %-8s %s  %s\n", n.Degree, n.In, n.Out, n.Bridges, community, n.Type, n.ID, nodeWhere(n))
	}
	return b.String()
}

// Overview is the map of a repository an agent reads first: which snapshot this is, what it holds, its largest
// communities with their most connected members, its god nodes and the size of its contracts. communities and gods
// limit the lists; <= 0 lists them all.
func Overview(g *graph.Graph, communities, gods int) string {
	return OverviewIn(g, "", communities, gods)
}

// OverviewIn is Overview of one repository of a merged graph ("" for the whole graph): its nodes and edges, its communities
// and its god nodes, with the shared values it reaches counted apart.
func OverviewIn(g *graph.Graph, repo string, communities, gods int) string {
	var b strings.Builder
	b.WriteString(overviewHead(g, repo) + "\n")
	shared := g.SharedValuesOf(repo)
	types, layers := map[string]int{}, map[string]int{}
	for _, n := range g.Nodes {
		if !g.InRepo(n, repo) && !shared[n.ID] {
			continue
		}
		types[n.Type]++
		layers[graph.Layer(n.Type)]++
	}
	nodes, edges := len(g.Nodes), len(g.Edges)
	if repo != "" {
		for _, s := range g.RepoStats() {
			if s.Repo == repo {
				nodes, edges = s.Nodes, s.Edges
			}
		}
	}
	fmt.Fprintf(&b, "nodes %d, edges %d\n", nodes, edges)
	if repo != "" {
		fmt.Fprintf(&b, "shared values: %d (reached from this repository)\n", len(shared))
	}
	fmt.Fprintf(&b, "layers: %s\n", countsLine(layers, []string{graph.LayerFile, graph.LayerCode, graph.LayerContract, graph.LayerKnowledge}))
	fmt.Fprintf(&b, "types:  %s\n", countsLine(types, sortedKeys(types)))

	ranked := ownedBy(g, repo, sortedCommunities(g))
	fmt.Fprintf(&b, "\ncommunities (%d):\n", len(ranked))
	if communities > 0 && communities < len(ranked) {
		ranked = ranked[:communities]
	}
	for _, c := range ranked {
		fmt.Fprintf(&b, "c%d  %d nodes  cohesion %.2f  %s\n", c.ID, c.Size, c.Cohesion, c.Label)
		var labels []string
		top := keptTop(g, repo, c.Top)
		for _, id := range top[:min(3, len(top))] {
			labels = append(labels, labelOf(g, id))
		}
		if len(labels) > 0 {
			fmt.Fprintf(&b, "    top: %s\n", strings.Join(labels, ", "))
		}
	}

	b.WriteString("\ngod nodes:\n")
	b.WriteString(GodNodes(g, g.GodNodes(gods, graph.GodOptions{Repo: repo})))
	fmt.Fprintf(&b, "\ncontracts: tables=%d columns=%d routes=%d mcp-tools=%d values=%d\n",
		types[graph.TypeTable], types[graph.TypeColumn], types[graph.TypeRoute], types[graph.TypeTool], types[graph.TypeValue])
	return b.String()
}

// overviewHead is the first line of an overview: the repository, its commit, when it was built and the schema. For one
// repository of a merged graph it names that repository and its commit; for a merged graph as a whole, the number of them.
func overviewHead(g *graph.Graph, repo string) string {
	name := g.Meta["repo"]
	if name == "" {
		name = g.Meta["root"]
	}
	commit := g.Meta["commit"]
	if repo != "" {
		name = repo
		for _, s := range g.RepoStats() {
			if s.Repo == repo {
				commit = s.Commit
			}
		}
	} else if n := len(g.Repos()); n > 0 {
		name = fmt.Sprintf("merged %d repositories", n)
	}
	line := name
	if c := commit; c != "" {
		if len(c) > 8 {
			c = c[:8]
		}
		line += " @ " + c
	}
	if t := g.Meta["built_at"]; t != "" {
		line += " built " + t
	}
	var versions []string
	if v := g.Meta["schema_version"]; v != "" {
		versions = append(versions, "schema "+v)
	}
	if v := g.Meta["engine_version"]; v != "" {
		versions = append(versions, "engine "+v)
	}
	if len(versions) > 0 {
		line += " (" + strings.Join(versions, ", ") + ")"
	}
	if line == "" {
		line = "graph"
	}
	return line
}

// countsLine lists the non-zero counts in the given order as `key=n`, or "none".
func countsLine(counts map[string]int, order []string) string {
	var parts []string
	for _, k := range order {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, " ")
}

// sortedCommunities returns the communities of g, largest first, ties by id.
func sortedCommunities(g *graph.Graph) []graph.Community {
	out := append([]graph.Community(nil), g.Communities...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ownedBy keeps the communities that belong to a repository; "" keeps all of them.
func ownedBy(g *graph.Graph, repo string, comms []graph.Community) []graph.Community {
	if repo == "" {
		return comms
	}
	owner := communityRepos(g)
	var out []graph.Community
	for _, c := range comms {
		if owner[c.ID] == repo {
			out = append(out, c)
		}
	}
	return out
}

// keptTop keeps the member ids that belong to a repository; "" keeps all of them.
func keptTop(g *graph.Graph, repo string, ids []string) []string {
	if repo == "" {
		return ids
	}
	var out []string
	for _, id := range ids {
		if n := g.Nodes[id]; n != nil && g.InRepo(n, repo) {
			out = append(out, id)
		}
	}
	return out
}

// communityRepos names the repository each community belongs to: the one most of its members come from, ties going to
// the smaller name. Shared values belong to no repository and do not count.
func communityRepos(g *graph.Graph) map[int]string {
	counts := map[int]map[string]int{}
	for _, n := range g.Nodes {
		c, ok := n.Props["community"]
		if !ok || n.Repo == "" {
			continue
		}
		id := atoi(c)
		if counts[id] == nil {
			counts[id] = map[string]int{}
		}
		counts[id][n.Repo]++
	}
	out := make(map[int]string, len(counts))
	for id, m := range counts {
		best, bestN := "", 0
		for r, k := range m {
			if k > bestN || (k == bestN && r < best) {
				best, bestN = r, k
			}
		}
		out[id] = best
	}
	return out
}

func findCommunity(g *graph.Graph, id int) (graph.Community, bool) {
	for _, c := range g.Communities {
		if c.ID == id {
			return c, true
		}
	}
	return graph.Community{}, false
}

// communityOf returns the community a node belongs to.
func communityOf(g *graph.Graph, id string) (graph.Community, bool) {
	n := g.Nodes[id]
	if n == nil || n.Props["community"] == "" {
		return graph.Community{}, false
	}
	return findCommunity(g, atoi(n.Props["community"]))
}

// atoi reads a community id; a value that is not a number yields -1, which no community has.
func atoi(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return -1
	}
	return v
}

func labelOf(g *graph.Graph, id string) string {
	if n := g.Nodes[id]; n != nil {
		return n.Label
	}
	return id
}

func nodeWhere(n graph.GodNode) string {
	if n.Path == "" {
		return ""
	}
	if n.Line > 0 {
		return fmt.Sprintf("%s:%d", n.Path, n.Line)
	}
	return n.Path
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
