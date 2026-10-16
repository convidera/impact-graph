// Package community detects communities in undirected weighted graphs with the Leiden algorithm (Traag, Waltman and
// van Eck, 2019). It is generic: it knows nothing about the impact graph and depends on the standard library only.
package community

import (
	"math/rand"
	"sort"
)

// Edge is an undirected weighted edge between node indexes U and V. Weights must be > 0 (edges with W <= 0 are
// ignored). Self loops (U == V) are ignored and parallel edges are summed.
type Edge struct {
	U, V int
	W    float64
}

// Options tunes Leiden. Zero values select the defaults.
type Options struct {
	Resolution float64 // modularity resolution gamma; 0 means 1.0
	Theta      float64 // randomness of the refinement phase; 0 means 0.01
	Seed       int64   // seed of the pseudo random generator; 0 means 1
}

// Leiden returns the community of every node 0..n-1. Community ids are dense (0..k-1), ordered by community size
// descending, ties broken by the smallest node index in the community. A node without any edge is its own community.
// The result is deterministic for a given graph and options. n == 0 returns an empty slice.
func Leiden(n int, edges []Edge, o Options) []int {
	if n == 0 {
		return []int{}
	}
	gamma := o.Resolution
	if gamma == 0 {
		gamma = 1
	}
	theta := o.Theta
	if theta == 0 {
		theta = 0.01
	}
	seed := o.Seed
	if seed == 0 {
		seed = 1
	}
	rng := rand.New(rand.NewSource(seed))

	g := buildGraph(n, edges)
	nodeOf := make([]int, n)
	comm := make([]int, n)
	for v := range n {
		nodeOf[v] = v
		comm[v] = v
	}

	for {
		moveNodes(g, comm, gamma, rng)
		if countLabels(comm) == g.n {
			break
		}
		refined := refine(g, comm, gamma, theta, rng)
		group, k := representatives(refined)
		if k == g.n {
			break
		}
		next := make([]int, k)
		for v := range comm {
			next[group[v]] = comm[v]
		}
		compact(next, g.n)
		for x := range nodeOf {
			nodeOf[x] = group[nodeOf[x]]
		}
		g = g.aggregate(group, k)
		comm = next
	}

	labels := make([]int, n)
	for x := range labels {
		labels[x] = comm[nodeOf[x]]
	}
	return denseBySize(labels)
}

// Modularity returns the (generalised) modularity of a partition: sum over communities of (inside weight / m) minus
// resolution*(community degree / 2m)^2, with m the total edge weight. It returns 0 for a graph without edges.
func Modularity(n int, edges []Edge, part []int, resolution float64) float64 {
	in := make(map[int]float64)
	tot := make(map[int]float64)
	m := 0.0
	for _, e := range edges {
		if !usable(e, n) {
			continue
		}
		m += e.W
		if part[e.U] == part[e.V] {
			in[part[e.U]] += e.W
		}
		tot[part[e.U]] += e.W
		tot[part[e.V]] += e.W
	}
	if m == 0 {
		return 0
	}
	keys := make([]int, 0, len(tot))
	for c := range tot {
		keys = append(keys, c)
	}
	sort.Ints(keys)
	q := 0.0
	for _, c := range keys {
		frac := tot[c] / (2 * m)
		q += in[c]/m - resolution*frac*frac
	}
	return q
}

// Connected reports whether every community of the partition induces a connected subgraph (a singleton counts as
// connected).
func Connected(n int, edges []Edge, part []int) bool {
	parent := make([]int, n)
	for v := range parent {
		parent[v] = v
	}
	find := func(v int) int {
		for parent[v] != v {
			parent[v] = parent[parent[v]]
			v = parent[v]
		}
		return v
	}
	for _, e := range edges {
		if !usable(e, n) || part[e.U] != part[e.V] {
			continue
		}
		a, b := find(e.U), find(e.V)
		if a != b {
			parent[a] = b
		}
	}
	root := make(map[int]int)
	for v := range n {
		r := find(v)
		if first, ok := root[part[v]]; !ok {
			root[part[v]] = r
		} else if first != r {
			return false
		}
	}
	return true
}

func usable(e Edge, n int) bool {
	return e.W > 0 && e.U != e.V && e.U >= 0 && e.V >= 0 && e.U < n && e.V < n
}

// countLabels returns the number of distinct labels of a partition whose labels lie in 0..len(labels)-1.
func countLabels(labels []int) int {
	seen := make([]bool, len(labels))
	k := 0
	for _, c := range labels {
		if !seen[c] {
			seen[c] = true
			k++
		}
	}
	return k
}

// compact renumbers labels densely in order of first appearance; bound is an exclusive upper bound of the labels.
func compact(labels []int, bound int) {
	id := make([]int, bound)
	for i := range id {
		id[i] = -1
	}
	k := 0
	for v, c := range labels {
		if id[c] < 0 {
			id[c] = k
			k++
		}
		labels[v] = id[c]
	}
}

// representatives maps every node to a dense id of its refined community; the id of a community is assigned to its
// representative, the member that carries the community's label. It returns the mapping and the number of communities.
func representatives(refined []int) ([]int, int) {
	id := make([]int, len(refined))
	k := 0
	for v, r := range refined {
		if r == v {
			id[v] = k
			k++
		}
	}
	group := make([]int, len(refined))
	for v, r := range refined {
		group[v] = id[r]
	}
	return group, k
}

// denseBySize renumbers labels so that the largest community gets id 0; ties go to the community with the smallest
// node index.
func denseBySize(labels []int) []int {
	bound := 0
	for _, c := range labels {
		if c+1 > bound {
			bound = c + 1
		}
	}
	size := make([]int, bound)
	first := make([]int, bound)
	for i := range first {
		first[i] = -1
	}
	present := make([]int, 0)
	for v, c := range labels {
		if size[c] == 0 {
			first[c] = v
			present = append(present, c)
		}
		size[c]++
	}
	sort.Slice(present, func(i, j int) bool {
		a, b := present[i], present[j]
		if size[a] != size[b] {
			return size[a] > size[b]
		}
		return first[a] < first[b]
	})
	rank := make([]int, bound)
	for i, c := range present {
		rank[c] = i
	}
	out := make([]int, len(labels))
	for v, c := range labels {
		out[v] = rank[c]
	}
	return out
}
