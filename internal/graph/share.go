package graph

import (
	"sort"
	"strconv"
)

// Share is the part of a community that one repository of a merged graph holds. A community can span repositories through a
// shared value; each repository sees only its own members.
type Share struct {
	Repo string
	// Size is the number of members of the community that belong to the repository. Shared values are members of the
	// community but belong to no repository, so they are not counted.
	Size int
	// Top is the members of Community.Top that belong to the repository, in the same order.
	Top []string
	// Cohesion is the share of the repository's members' edge weight that stays between them. It is the formula Cluster
	// uses (2 * inside / degree), applied to the repository's members on their own.
	Cohesion float64
}

// communityIDOf returns the community id stored in a node's props, or -1 when it has none.
func communityIDOf(n *Node) int {
	c, ok := n.Props["community"]
	if !ok {
		return -1
	}
	v, err := strconv.Atoi(c)
	if err != nil {
		return -1
	}
	return v
}

// CommunityShares lists, for each community id, the repositories that have members in it, sorted by name. The shares are
// computed from the members' props and the edges, with the same weights and eligibility as Cluster.
func (g *Graph) CommunityShares() map[int][]Share {
	type key struct {
		comm int
		repo string
	}
	size := map[key]int{}
	inside := map[key]float64{}
	degree := map[key]float64{}
	for _, n := range g.Nodes {
		if c := communityIDOf(n); c >= 0 && n.Repo != "" {
			size[key{c, n.Repo}]++
		}
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		w := clusterWeight(e)
		a, b := g.Nodes[e.Src], g.Nodes[e.Dst]
		if w == 0 || a == nil || b == nil {
			continue
		}
		ca, cb := communityIDOf(a), communityIDOf(b)
		if ca >= 0 && a.Repo != "" && clusterEligible(b) {
			degree[key{ca, a.Repo}] += w
		}
		if cb >= 0 && b.Repo != "" && clusterEligible(a) {
			degree[key{cb, b.Repo}] += w
		}
		if ca >= 0 && ca == cb && a.Repo == b.Repo && a.Repo != "" && clusterEligible(a) && clusterEligible(b) {
			inside[key{ca, a.Repo}] += w
		}
	}
	out := map[int][]Share{}
	for k, n := range size {
		s := Share{Repo: k.repo, Size: n}
		if d := degree[k]; d > 0 {
			s.Cohesion = 2 * inside[k] / d
		}
		out[k.comm] = append(out[k.comm], s)
	}
	for _, c := range g.Communities {
		for i := range out[c.ID] {
			for _, id := range c.Top {
				if n := g.Nodes[id]; n != nil && n.Repo == out[c.ID][i].Repo {
					out[c.ID][i].Top = append(out[c.ID][i].Top, id)
				}
			}
		}
	}
	for c := range out {
		sort.Slice(out[c], func(i, j int) bool { return out[c][i].Repo < out[c][j].Repo })
	}
	return out
}
