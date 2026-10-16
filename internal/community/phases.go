package community

import (
	"math"
	"math/rand"
)

type nbr struct {
	to int
	w  float64
}

// graph is the working graph of one Leiden level. self holds the weight of the edges collapsed into a node by
// aggregation (each undirected edge once); it counts twice toward the degree but is not an edge for moving.
type graph struct {
	n    int
	adj  [][]nbr
	self []float64
	deg  []float64
	twoM float64
}

func buildGraph(n int, edges []Edge) *graph {
	raw := make([][]nbr, n)
	for _, e := range edges {
		if !usable(e, n) {
			continue
		}
		raw[e.U] = append(raw[e.U], nbr{e.V, e.W})
		raw[e.V] = append(raw[e.V], nbr{e.U, e.W})
	}
	return newGraph(combine(raw), make([]float64, n))
}

func newGraph(adj [][]nbr, self []float64) *graph {
	g := &graph{n: len(adj), adj: adj, self: self, deg: make([]float64, len(adj))}
	for v := range adj {
		g.deg[v] = 2 * self[v]
		for _, e := range adj[v] {
			g.deg[v] += e.w
		}
		g.twoM += g.deg[v]
	}
	return g
}

// combine sums parallel entries of every node's neighbour list, keeping the order of first appearance.
func combine(raw [][]nbr) [][]nbr {
	pos := make([]int, len(raw))
	seen := make([]int, len(raw))
	out := make([][]nbr, len(raw))
	for v := range raw {
		for _, e := range raw[v] {
			if seen[e.to] != v+1 {
				seen[e.to] = v + 1
				pos[e.to] = len(out[v])
				out[v] = append(out[v], nbr{e.to, 0})
			}
			out[v][pos[e.to]].w += e.w
		}
	}
	return out
}

// aggregate collapses every group of nodes (group[v] < k) into one node of the returned graph.
func (g *graph) aggregate(group []int, k int) *graph {
	self := make([]float64, k)
	raw := make([][]nbr, k)
	for v := 0; v < g.n; v++ {
		a := group[v]
		self[a] += g.self[v]
		for _, e := range g.adj[v] {
			b := group[e.to]
			if a == b {
				self[a] += e.w / 2
			} else {
				raw[a] = append(raw[a], nbr{b, e.w})
			}
		}
	}
	return newGraph(combine(raw), self)
}

// groupNodes returns the nodes sorted by label (stable in node index) and the start offset of every label.
func groupNodes(labels []int, k int) ([]int, []int) {
	start := make([]int, k+1)
	for _, c := range labels {
		start[c+1]++
	}
	for c := 0; c < k; c++ {
		start[c+1] += start[c]
	}
	next := append([]int(nil), start[:k]...)
	order := make([]int, len(labels))
	for v, c := range labels {
		order[next[c]] = v
		next[c]++
	}
	return order, start
}

// moveNodes is the local moving phase: it repeatedly moves single nodes to the neighbouring community (or an empty
// one) that increases modularity the most, until no node can move. Labels of comm lie in 0..g.n-1.
func moveNodes(g *graph, comm []int, gamma float64, rng *rand.Rand) {
	tot := make([]float64, g.n)
	cnt := make([]int, g.n)
	for v := 0; v < g.n; v++ {
		tot[comm[v]] += g.deg[v]
		cnt[comm[v]]++
	}
	var free []int
	for c := g.n - 1; c >= 0; c-- {
		if cnt[c] == 0 {
			free = append(free, c)
		}
	}
	wTo := make([]float64, g.n)
	mark := make([]bool, g.n)
	queued := make([]bool, g.n)
	queue := rng.Perm(g.n)
	for _, v := range queue {
		queued[v] = true
	}
	var touched []int
	// A move must gain more than eps: float64 rounding can make two moves look like gains of ±1 ulp, and without
	// the margin nodes would swap communities forever. maxPops is a hard stop for the same reason; the partition
	// stays valid when it is hit.
	eps := 1e-12 * g.twoM
	pops, maxPops := 0, 100*g.n+1000

	for head := 0; head < len(queue); head++ {
		if head > 1024 && 2*head > len(queue) {
			queue = queue[:copy(queue, queue[head:])]
			head = 0
		}
		pops++
		if pops > maxPops {
			return
		}
		v := queue[head]
		queued[v] = false
		own := comm[v]
		kv := g.deg[v]
		tot[own] -= kv
		cnt[own]--

		touched = append(touched[:0], own)
		mark[own] = true
		for _, e := range g.adj[v] {
			c := comm[e.to]
			if !mark[c] {
				mark[c] = true
				touched = append(touched, c)
			}
			wTo[c] += e.w
		}

		best := own
		bestGain := wTo[own] - gamma*kv*tot[own]/g.twoM
		for _, c := range touched[1:] {
			if gain := wTo[c] - gamma*kv*tot[c]/g.twoM; gain > bestGain+eps {
				best, bestGain = c, gain
			}
		}
		for len(free) > 0 && cnt[free[len(free)-1]] != 0 {
			free = free[:len(free)-1]
		}
		if len(free) > 0 && bestGain < -eps {
			best = free[len(free)-1]
			free = free[:len(free)-1]
		}

		for _, c := range touched {
			wTo[c] = 0
			mark[c] = false
		}
		tot[best] += kv
		cnt[best]++
		comm[v] = best
		if cnt[own] == 0 {
			free = append(free, own)
		}
		if best == own {
			continue
		}
		for _, e := range g.adj[v] {
			if comm[e.to] != best && !queued[e.to] {
				queued[e.to] = true
				queue = append(queue, e.to)
			}
		}
	}
}

// refine splits every community of comm into well connected refined communities. Nodes that are still singletons
// merge, randomly weighted by modularity gain, into a well connected refined community of their own community.
// It returns the refined community of every node; each refined community lies inside one community of comm.
func refine(g *graph, comm []int, gamma, theta float64, rng *rand.Rand) []int {
	n := g.n
	m := g.twoM / 2
	refined := make([]int, n)
	size := make([]int, n)
	kr := make([]float64, n)
	swc := make([]float64, n)
	inw := make([]float64, n)
	wc := make([]float64, n)
	kvd := make([]float64, n)
	mark := make([]bool, n)
	for v := range refined {
		refined[v] = v
	}

	order, start := groupNodes(comm, n)
	var touched []int
	var cands []int
	var gains []float64
	for c := 0; c < n; c++ {
		mem := order[start[c]:start[c+1]]
		if len(mem) < 2 {
			continue
		}
		kc := 0.0
		for _, v := range mem {
			kc += g.deg[v]
			size[v] = 1
			kr[v] = g.deg[v]
			inw[v] = 0
			wc[v] = 0
			for _, e := range g.adj[v] {
				if comm[e.to] == c {
					wc[v] += e.w
				}
			}
			swc[v] = wc[v]
		}

		visit := append([]int(nil), mem...)
		rng.Shuffle(len(visit), func(i, j int) { visit[i], visit[j] = visit[j], visit[i] })
		for _, v := range visit {
			if size[refined[v]] != 1 {
				continue
			}
			if wc[v] < gamma*g.deg[v]*(kc-g.deg[v])/g.twoM {
				continue
			}

			touched = touched[:0]
			for _, e := range g.adj[v] {
				if comm[e.to] != c {
					continue
				}
				d := refined[e.to]
				if !mark[d] {
					mark[d] = true
					touched = append(touched, d)
				}
				kvd[d] += e.w
			}
			cands, gains = cands[:0], gains[:0]
			for _, d := range touched {
				gain := kvd[d] - gamma*g.deg[v]*kr[d]/g.twoM
				if gain >= 0 && swc[d]-2*inw[d] >= gamma*kr[d]*(kc-kr[d])/g.twoM {
					cands = append(cands, d)
					gains = append(gains, gain/m)
				}
			}

			target := -1
			if len(cands) > 0 {
				target = pickCandidate(cands, gains, theta, rng)
				refined[v] = target
				size[target]++
				kr[target] += g.deg[v]
				swc[target] += wc[v]
				inw[target] += kvd[target]
			}
			for _, d := range touched {
				kvd[d] = 0
				mark[d] = false
			}
		}
	}
	return refined
}

// pickCandidate draws one candidate with probability proportional to exp(gain/theta). It overwrites gains with the
// weights, so no buffer is allocated.
func pickCandidate(cands []int, gains []float64, theta float64, rng *rand.Rand) int {
	best := math.Inf(-1)
	for _, g := range gains {
		best = math.Max(best, g)
	}
	total := 0.0
	for i, g := range gains {
		gains[i] = math.Exp((g - best) / theta)
		total += gains[i]
	}
	r := rng.Float64() * total
	for i, w := range gains {
		r -= w
		if r < 0 {
			return cands[i]
		}
	}
	return cands[len(cands)-1]
}
