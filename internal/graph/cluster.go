package graph

import (
	"math"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/convidera/impact-graph/internal/community"
)

// ClusterOptions tunes Cluster. A zero Resolution, MinSize or Seed takes its default; a zero HubPercentile disables the
// hub rule (use DefaultClusterOptions for the default).
type ClusterOptions struct {
	Resolution    float64 // modularity resolution, default 1
	HubPercentile float64 // nodes whose weighted degree is above this percentile (0..100) are left out of the clustering and assigned afterwards; 0 disables; default 99
	MinSize       int     // communities with fewer members are dissolved and their members absorbed by a neighbouring community; default 5
	Seed          int64   // default 1
}

// DefaultClusterOptions are the options of a typical clustering.
func DefaultClusterOptions() ClusterOptions {
	return ClusterOptions{Resolution: 1, HubPercentile: 99, MinSize: 5, Seed: 1}
}

// clusterKindWeight is how strongly an edge kind ties two symbols together; kinds that only describe documentation,
// history or the knowledge layer tie nothing and have weight 0.
func clusterKindWeight(kind string) float64 {
	switch kind {
	case Calls, Requests, Reads, Writes, Produces, Consumes, Exposes, Link, Extends:
		return 1
	case MapsTo:
		return 1.5
	case MemberOf:
		return 3
	case Uses, Registers, Mentions:
		return 0.5
	}
	return 0
}

// clusterWeight is the weight an edge adds to the clustering: its kind weight times its confidence. Edges below 0.5
// confidence count for nothing.
func clusterWeight(e *Edge) float64 {
	if e.Conf < 0.5 {
		return 0
	}
	return clusterKindWeight(e.Kind) * e.Conf
}

// clusterEligible says whether a node is clustered: code or contract symbols, not the ones of test or doc files.
func clusterEligible(n *Node) bool {
	if l := Layer(n.Type); l != LayerCode && l != LayerContract {
		return false
	}
	if n.Type == TypeConfig && n.Props[PropConfigKind] == ConfigKey {
		return false // a values file is a tree of keys; it would form a community of its own for every file
	}
	k := n.Props["file_kind"]
	return k != "test" && k != "doc"
}

// isTestSymbol says whether a node is a symbol declared in a test file; those are assigned after clustering.
func isTestSymbol(n *Node) bool {
	if l := Layer(n.Type); l != LayerCode && l != LayerContract {
		return false
	}
	return n.Props["file_kind"] == "test"
}

// clusterPreferred marks the node types that name a community best in its label.
func clusterPreferred(nodeType string) bool {
	switch nodeType {
	case TypeClass, TypeTable, TypeRoute, TypeTool, TypeFunction:
		return true
	}
	return false
}

func (g *Graph) sortedNodes() []*Node {
	out := make([]*Node, 0, len(g.Nodes))
	for _, n := range g.Nodes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Cluster finds the communities of the code and contract symbols with Leiden. It writes each member's Props["community"]
// (its dense id), removes that key from every other node, stores the communities in g.Communities and returns them. The
// ids are ordered by size, largest first. Run it again to recompute: the result does not depend on earlier runs.
func (g *Graph) Cluster(o ClusterOptions) []Community {
	d := DefaultClusterOptions()
	if o.Resolution == 0 {
		o.Resolution = d.Resolution
	}
	if o.MinSize == 0 {
		o.MinSize = d.MinSize
	}
	if o.Seed == 0 {
		o.Seed = d.Seed
	}

	nodes := g.sortedNodes()
	n := len(nodes)
	pos := make(map[string]int, n)
	eligible := make([]bool, n)
	testSym := make([]bool, n)
	nEligible := 0
	for i, x := range nodes {
		pos[x.ID] = i
		eligible[i] = clusterEligible(x)
		testSym[i] = isTestSymbol(x)
		if eligible[i] {
			nEligible++
		}
	}

	type link struct {
		a, b int
		w    float64
	}
	var links []link
	deg := make([]float64, n) // weighted degree towards eligible nodes
	for i := range g.Edges {
		e := &g.Edges[i]
		a, okA := pos[e.Src]
		b, okB := pos[e.Dst]
		w := clusterWeight(e)
		if !okA || !okB || a == b || w == 0 {
			continue
		}
		links = append(links, link{a, b, w})
		if eligible[b] {
			deg[a] += w
		}
		if eligible[a] {
			deg[b] += w
		}
	}

	hub := make([]bool, n)
	if nEligible >= 50 && o.HubPercentile > 0 && o.HubPercentile < 100 {
		degs := make([]float64, 0, nEligible)
		for i := range nodes {
			if eligible[i] {
				degs = append(degs, deg[i])
			}
		}
		sort.Float64s(degs)
		rank := int(math.Ceil(o.HubPercentile * float64(len(degs)) / 100)) // nearest rank, 1..len
		limit := degs[rank-1]
		for i := range nodes {
			hub[i] = eligible[i] && deg[i] > limit
		}
	}

	comm := make([]int, n)
	local := make([]int, n)
	var clustered []int
	for i := range nodes {
		comm[i], local[i] = -1, -1
		if eligible[i] && !hub[i] {
			local[i] = len(clustered)
			clustered = append(clustered, i)
		}
	}
	var edges []community.Edge
	for _, l := range links {
		if la, lb := local[l.a], local[l.b]; la >= 0 && lb >= 0 {
			edges = append(edges, community.Edge{U: la, V: lb, W: l.w})
		}
	}
	// members of communities below MinSize are left out like the hubs and attached below
	detached := make([]bool, n)
	if len(clustered) > 0 {
		labels := community.Leiden(len(clustered), edges, community.Options{Resolution: o.Resolution, Seed: o.Seed})
		size := make([]int, len(clustered))
		for _, c := range labels {
			size[c]++
		}
		for li, gi := range clustered {
			if size[labels[li]] >= o.MinSize {
				comm[gi] = labels[li]
			} else {
				detached[gi] = true
			}
		}
	}

	// hubs, test symbols and detached members join the kept community they weigh most towards; ties go to the lower id
	score := map[int]map[int]float64{}
	attach := func(x, y int, w float64) {
		if (hub[x] || testSym[x] || detached[x]) && comm[y] >= 0 {
			if score[x] == nil {
				score[x] = map[int]float64{}
			}
			score[x][comm[y]] += w
		}
	}
	for _, l := range links {
		attach(l.a, l.b, l.w)
		attach(l.b, l.a, l.w)
	}
	for x, m := range score {
		best, bestW := -1, 0.0
		for c, w := range m {
			if w > bestW || (w == bestW && c < best) {
				best, bestW = c, w
			}
		}
		comm[x] = best
	}

	sizes := make([]int, n)
	first := make([]int, n)
	for c := range first {
		first[c] = -1
	}
	for i := range nodes {
		c := comm[i]
		if c < 0 {
			continue
		}
		sizes[c]++
		if first[c] < 0 {
			first[c] = i
		}
	}
	var used []int
	for c := range sizes {
		if sizes[c] > 0 {
			used = append(used, c)
		}
	}
	sort.Slice(used, func(a, b int) bool {
		if sizes[used[a]] != sizes[used[b]] {
			return sizes[used[a]] > sizes[used[b]]
		}
		return first[used[a]] < first[used[b]]
	})
	newID := make([]int, n)
	for r, c := range used {
		newID[c] = r
	}
	members := make([][]int, len(used))
	for i, x := range nodes {
		if c := comm[i]; c >= 0 {
			comm[i] = newID[c]
			members[comm[i]] = append(members[comm[i]], i)
			if x.Props == nil {
				x.Props = map[string]string{}
			}
			x.Props["community"] = strconv.Itoa(comm[i])
		} else {
			delete(x.Props, "community")
		}
	}

	inside := make([]float64, len(used))
	for _, l := range links {
		if eligible[l.a] && eligible[l.b] && comm[l.a] >= 0 && comm[l.a] == comm[l.b] {
			inside[comm[l.a]] += l.w
		}
	}
	comms := make([]Community, len(used))
	for c, idx := range members {
		comms[c] = describeCommunity(c, idx, nodes, deg)
		denom := 0.0
		for _, i := range idx {
			denom += deg[i]
		}
		if denom > 0 {
			comms[c].Cohesion = 2 * inside[c] / denom
		}
	}
	g.Communities = comms
	return comms
}

// describeCommunity names a community after the directory most of its code members live in (all members when it has
// no code member, so a community of migrated columns is not named after the migrations) and the two most connected
// members (class, table, route, tool or function first). idx are indexes into nodes, sorted by ID.
func describeCommunity(id int, idx []int, nodes []*Node, deg []float64) Community {
	c := Community{ID: id, Size: len(idx)}
	code := 0
	for _, i := range idx {
		if Layer(nodes[i].Type) == LayerCode {
			code++
		}
	}
	dirs := map[string]int{}
	for _, i := range idx {
		if p := nodes[i].Path; p != "" && (code == 0 || Layer(nodes[i].Type) == LayerCode) {
			dirs[path.Dir(p)]++
		}
	}
	bestN := 0
	for d, k := range dirs {
		if k > bestN || (k == bestN && d < c.Dir) {
			c.Dir, bestN = d, k
		}
	}

	ranked := append([]int(nil), idx...)
	sort.Slice(ranked, func(a, b int) bool {
		x, y := ranked[a], ranked[b]
		if deg[x] != deg[y] {
			return deg[x] > deg[y]
		}
		return nodes[x].ID < nodes[y].ID
	})
	for _, i := range ranked[:min(5, len(ranked))] {
		c.Top = append(c.Top, nodes[i].ID)
	}

	named := append([]int(nil), idx...)
	sort.Slice(named, func(a, b int) bool {
		x, y := named[a], named[b]
		px, py := clusterPreferred(nodes[x].Type), clusterPreferred(nodes[y].Type)
		if px != py {
			return px
		}
		if deg[x] != deg[y] {
			return deg[x] > deg[y]
		}
		return nodes[x].ID < nodes[y].ID
	})
	var names []string
	for _, i := range named[:min(2, len(named))] {
		names = append(names, nodes[i].Label)
	}
	c.Label = c.Dir
	if c.Label == "" {
		c.Label = "(no path)"
	}
	if len(names) > 0 {
		c.Label += " · " + strings.Join(names, ", ")
	}
	return c
}

// CommunityMembers lists the nodes whose Props["community"] is id, sorted by ID.
func (g *Graph) CommunityMembers(id int) []*Node {
	key := strconv.Itoa(id)
	var out []*Node
	for _, n := range g.sortedNodes() {
		if n.Props["community"] == key {
			out = append(out, n)
		}
	}
	return out
}
