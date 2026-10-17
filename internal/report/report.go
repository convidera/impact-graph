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

// FindIn is Find restricted to a repository ("" for all of them). Hits of several repositories of a merged graph start with
// a line that counts them per repository.
func FindIn(g *graph.Graph, repo, text string, limit int) string {
	var b strings.Builder
	hits := g.FindIn(text, limit, repo)
	if line := hitsByRepo(hits); repo == "" && line != "" {
		b.WriteString(line + "\n")
	}
	for _, n := range hits {
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

// CommunitiesIn is Communities restricted to one repository of a merged graph ("" for all of them): the communities that
// have members in it, with that repository's share of each (see scoped).
func CommunitiesIn(g *graph.Graph, repo string, top int) string {
	var b strings.Builder
	ranked := scoped(g, repo, sortedCommunities(g))
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

// CommunityIn is Community for one repository of a merged graph ("" for all of them). For a repository the size, the top
// members and the cohesion are those of its own members (see scoped), so a community that spans repositories is never
// shown with the size or cohesion of the whole; its label still describes the whole community. A community that has no
// member in the repository is reported as not found there.
func CommunityIn(g *graph.Graph, repo string, id, limit int) string {
	whole, ok := findCommunity(g, id)
	c := whole
	if ok && repo != "" {
		share := scoped(g, repo, []graph.Community{whole})
		ok = len(share) > 0
		if ok {
			c = share[0]
		}
	}
	if !ok {
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
	if repo != "" {
		fmt.Fprintf(&b, "size      %d nodes in %s (the community has %d)\n", c.Size, repo, whole.Size)
	} else {
		fmt.Fprintf(&b, "size      %d nodes\n", c.Size)
	}
	fmt.Fprintf(&b, "cohesion  %.2f\n", c.Cohesion)
	if len(c.Top) > 0 {
		b.WriteString("\ntop members:\n")
		for _, id := range c.Top {
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

// RepoBlock lists the repositories of a merged graph with their size and commit, the shared values, and how the
// repositories connect: through an edge between their own nodes, or through the shared values both reach. Two repositories
// that connect to nothing get a note, and a graph where only some pairs connect lists the pairs that do not. It is empty for
// a snapshot and for a merge of one repository.
func RepoBlock(g *graph.Graph) string {
	stats := g.RepoStats()
	if len(stats) < 2 {
		return ""
	}
	var b strings.Builder
	for _, s := range stats {
		line := fmt.Sprintf("  %s  %s nodes  %s edges", s.Repo, groupDigits(s.Nodes), groupDigits(s.Edges))
		if c := s.Commit; c != "" {
			if len(c) > 7 {
				c = c[:7]
			}
			line += "  commit " + c
		}
		b.WriteString(line + "\n")
	}
	l := g.Links()
	fmt.Fprintf(&b, "  shared values: %s\n", groupDigits(l.Shared))
	fmt.Fprintf(&b, "  cross-repository edges: %s\n", groupDigits(l.Cross))
	fmt.Fprintf(&b, "  connected through shared values: %s\n", groupDigits(l.Through))
	if l.Cross == 0 && l.Through == 0 {
		b.WriteString("note: these repositories share no contract with each other; a query in one never reaches the other.\n")
	} else if un := unconnected(stats, l.Pairs); len(un) > 0 {
		fmt.Fprintf(&b, "not connected: %s\n", strings.Join(un, ", "))
	}
	return b.String()
}

// unconnected lists the pairs of repositories with no connection, as "a <-> b".
func unconnected(stats []graph.RepoStat, connected []graph.RepoPair) []string {
	linked := map[[2]string]bool{}
	for _, p := range connected {
		linked[[2]string{p.A, p.B}] = true
	}
	var out []string
	for i := range stats {
		for j := i + 1; j < len(stats); j++ {
			a, b := stats[i].Repo, stats[j].Repo
			if !linked[[2]string{a, b}] {
				out = append(out, a+" <-> "+b)
			}
		}
	}
	return out
}

// hitsByRepo counts search hits per repository, for a line that says where they come from. It is empty unless the hits
// come from two or more repositories.
func hitsByRepo(hits []*graph.Node) string {
	counts := map[string]int{}
	shared := 0
	for _, n := range hits {
		if n.Repo == "" {
			shared++
		} else {
			counts[n.Repo]++
		}
	}
	if len(counts) < 2 {
		return ""
	}
	var parts []string
	for _, r := range sortedKeys(counts) {
		parts = append(parts, fmt.Sprintf("%d in %s", counts[r], r))
	}
	if shared > 0 {
		parts = append(parts, fmt.Sprintf("%d shared values", shared))
	}
	return fmt.Sprintf("%d hits: %s (use repository to restrict)", len(hits), strings.Join(parts, ", "))
}

// groupDigits writes n with a comma every three digits: 12525 is 12,525.
func groupDigits(n int) string {
	s := strconv.Itoa(n)
	var out []byte
	for i := range len(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, s[i])
	}
	return string(out)
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
	} else {
		b.WriteString(RepoBlock(g))
	}
	fmt.Fprintf(&b, "layers: %s\n", countsLine(layers, []string{graph.LayerFile, graph.LayerCode, graph.LayerContract, graph.LayerKnowledge}))
	fmt.Fprintf(&b, "types:  %s\n", countsLine(types, sortedKeys(types)))

	ranked := scoped(g, repo, sortedCommunities(g))
	fmt.Fprintf(&b, "\ncommunities (%d):\n", len(ranked))
	if communities > 0 && communities < len(ranked) {
		ranked = ranked[:communities]
	}
	for _, c := range ranked {
		fmt.Fprintf(&b, "c%d  %d nodes  cohesion %.2f  %s\n", c.ID, c.Size, c.Cohesion, c.Label)
		var labels []string
		for _, id := range c.Top[:min(3, len(c.Top))] {
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

// scoped returns the communities that a repository has members in, each as a copy that carries the repository's share
// (graph.CommunityShares): its size, its most connected members and its cohesion. Largest share first, ties by id. repo ""
// returns the communities unchanged.
func scoped(g *graph.Graph, repo string, comms []graph.Community) []graph.Community {
	if repo == "" {
		return comms
	}
	shares := g.CommunityShares()
	var out []graph.Community
	for _, c := range comms {
		for _, sh := range shares[c.ID] {
			if sh.Repo == repo {
				c.Size, c.Top, c.Cohesion = sh.Size, sh.Top, sh.Cohesion
				out = append(out, c)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size > out[j].Size
		}
		return out[i].ID < out[j].ID
	})
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
