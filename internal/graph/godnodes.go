package graph

import "sort"

// GodOptions tunes GodNodes.
type GodOptions struct {
	Types     []string // restrict to these node types; empty = all eligible
	MinDegree int      // drop nodes below this degree; default 1
}

// GodNode is a node many other symbols are connected to: a place where a change is likely to spread.
type GodNode struct {
	ID, Label, Type, Path string
	Line                  int
	Degree, In, Out       int    // distinct neighbours; In = neighbours with an edge into the node, Out = neighbours it has an edge to
	Bridges               int    // distinct communities (other than its own) among its neighbours
	Community             string // Props["community"] of the node, "" if none
}

// godSkipped are the kinds that say how a node is organised or documented rather than what it calls or uses.
var godSkipped = map[string]bool{
	MemberOf: true, DocumentedIn: true, CoChanged: true, Implements: true, Refines: true, DerivedFrom: true,
}

const (
	godIn  = 1 // the neighbour has an edge into the node
	godOut = 2 // the node has an edge to the neighbour
)

// GodNodes ranks the eligible nodes (code and contract symbols, see Cluster) by their number of distinct eligible
// neighbours, most connected first; ties go to more bridges, then to the smaller ID. Generated symbols are neighbours
// but never candidates. top <= 0 returns all of them.
func (g *Graph) GodNodes(top int, o GodOptions) []GodNode {
	if o.MinDegree <= 0 {
		o.MinDegree = 1
	}
	types := map[string]bool{}
	for _, t := range o.Types {
		types[t] = true
	}

	nodes := g.sortedNodes()
	pos := make(map[string]int, len(nodes))
	eligible := make([]bool, len(nodes))
	for i, x := range nodes {
		pos[x.ID] = i
		eligible[i] = clusterEligible(x)
	}

	type neighbour struct{ to, dir int }
	adj := make([][]neighbour, len(nodes))
	for i := range g.Edges {
		e := &g.Edges[i]
		a, okA := pos[e.Src]
		b, okB := pos[e.Dst]
		if !okA || !okB || a == b || godSkipped[e.Kind] || !eligible[a] || !eligible[b] {
			continue
		}
		adj[a] = append(adj[a], neighbour{b, godOut})
		adj[b] = append(adj[b], neighbour{a, godIn})
	}

	var out []GodNode
	for i, x := range nodes {
		if !eligible[i] || x.Props["file_kind"] == "generated" || (len(types) > 0 && !types[x.Type]) {
			continue
		}
		list := adj[i]
		sort.Slice(list, func(p, q int) bool { return list[p].to < list[q].to })
		gn := GodNode{
			ID: x.ID, Label: x.Label, Type: x.Type, Path: x.Path, Line: x.Line, Community: x.Props["community"],
		}
		communities := map[string]bool{}
		for j := 0; j < len(list); {
			to, dir := list[j].to, 0
			for ; j < len(list) && list[j].to == to; j++ {
				dir |= list[j].dir
			}
			gn.Degree++
			if dir&godIn != 0 {
				gn.In++
			}
			if dir&godOut != 0 {
				gn.Out++
			}
			if c := nodes[to].Props["community"]; c != "" && c != gn.Community {
				communities[c] = true
			}
		}
		if gn.Degree < o.MinDegree {
			continue
		}
		gn.Bridges = len(communities)
		out = append(out, gn)
	}

	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Degree != b.Degree {
			return a.Degree > b.Degree
		}
		if a.Bridges != b.Bridges {
			return a.Bridges > b.Bridges
		}
		return a.ID < b.ID
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}
