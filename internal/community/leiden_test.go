package community

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

// ringOfCliques returns k cliques of size nodes each; clique c is nodes c*size..c*size+size-1 and consecutive
// cliques (cyclically) are joined by one edge.
func ringOfCliques(k, size int) []Edge {
	var edges []Edge
	for c := 0; c < k; c++ {
		base := c * size
		for i := 0; i < size; i++ {
			for j := i + 1; j < size; j++ {
				edges = append(edges, Edge{U: base + i, V: base + j, W: 1})
			}
		}
		next := ((c + 1) % k) * size
		edges = append(edges, Edge{U: base + size - 1, V: next, W: 1})
	}
	return edges
}

// planted returns a random graph of 4 groups of 25 nodes (intra-group edge probability 0.3, inter-group 0.02) and
// its planted partition.
func planted(seed int64) (int, []Edge, []int) {
	const groups, size = 4, 25
	n := groups * size
	rng := rand.New(rand.NewSource(seed))
	part := make([]int, n)
	var edges []Edge
	for v := range n {
		part[v] = v / size
	}
	for u := range n {
		for v := u + 1; v < n; v++ {
			p := 0.02
			if part[u] == part[v] {
				p = 0.3
			}
			if rng.Float64() < p {
				edges = append(edges, Edge{U: u, V: v, W: 1})
			}
		}
	}
	return n, edges, part
}

func randomGraph(n, m int, seed int64) []Edge {
	rng := rand.New(rand.NewSource(seed))
	edges := make([]Edge, 0, m)
	for range m {
		edges = append(edges, Edge{U: rng.Intn(n), V: rng.Intn(n), W: 1 + rng.Float64()})
	}
	return edges
}

func numCommunities(part []int) int {
	k := 0
	for _, c := range part {
		k = max(k, c+1)
	}
	return k
}

// sizesDescending reports whether community ids are ordered by size descending with ties by smallest member index.
func sizesDescending(part []int) bool {
	k := numCommunities(part)
	size := make([]int, k)
	first := make([]int, k)
	for i := range first {
		first[i] = -1
	}
	for v, c := range part {
		if first[c] < 0 {
			first[c] = v
		}
		size[c]++
	}
	for c := 1; c < k; c++ {
		if size[c] > size[c-1] || (size[c] == size[c-1] && first[c] < first[c-1]) {
			return false
		}
	}
	return true
}

func TestLeidenRecoversRingOfCliques(t *testing.T) {
	const k, size = 6, 5
	edges := ringOfCliques(k, size)
	cases := []struct {
		name string
		opts Options
	}{
		{"defaults", Options{}},
		{"resolution 1 seed 1", Options{Resolution: 1, Seed: 1}},
		{"resolution 1 seed 2", Options{Resolution: 1, Seed: 2}},
		{"resolution 1 seed 3", Options{Resolution: 1, Seed: 3}},
		{"resolution 1 seed 42", Options{Resolution: 1, Seed: 42}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			part := Leiden(k*size, edges, tc.opts)
			if got := numCommunities(part); got != k {
				t.Fatalf("communities = %d, want %d (%v)", got, k, part)
			}
			for c := range k {
				want := part[c*size]
				for i := 1; i < size; i++ {
					if part[c*size+i] != want {
						t.Fatalf("clique %d is split: %v", c, part)
					}
				}
			}
			if !Connected(k*size, edges, part) {
				t.Fatalf("communities are not connected: %v", part)
			}
		})
	}
}

func TestLeidenSmallGraphs(t *testing.T) {
	cases := []struct {
		name  string
		n     int
		edges []Edge
		want  []int
	}{
		{"empty graph", 0, nil, []int{}},
		{"single node", 1, nil, []int{0}},
		{"no edges", 3, nil, []int{0, 1, 2}},
		{"two disjoint triangles", 6, []Edge{
			{0, 1, 1}, {1, 2, 1}, {0, 2, 1},
			{3, 4, 1}, {4, 5, 1}, {3, 5, 1},
		}, []int{0, 0, 0, 1, 1, 1}},
		{"isolated nodes", 4, []Edge{{U: 0, V: 1, W: 1}}, []int{0, 0, 1, 2}},
		{"self loops and non positive weights are ignored", 3, []Edge{
			{U: 0, V: 0, W: 5}, {U: 1, V: 2, W: 0}, {U: 1, V: 2, W: -1},
		}, []int{0, 1, 2}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Leiden(tc.n, tc.edges, Options{})
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Leiden = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLeidenDeterministicAndOrdered(t *testing.T) {
	n, edges, _ := planted(7)
	first := Leiden(n, edges, Options{Seed: 7})
	second := Leiden(n, edges, Options{Seed: 7})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same seed gave different results:\n%v\n%v", first, second)
	}
	if !sizesDescending(first) {
		t.Fatalf("community ids are not ordered by size: %v", first)
	}
	if !sizesDescending(Leiden(6*5, ringOfCliques(6, 5), Options{Seed: 9})) {
		t.Fatal("ring of cliques: community ids are not ordered by size")
	}
}

func TestLeidenPlantedPartitions(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		n, edges, truth := planted(seed)
		part := Leiden(n, edges, Options{Seed: seed})
		if !Connected(n, edges, part) {
			t.Errorf("seed %d: community is not connected: %v", seed, part)
		}
		q := Modularity(n, edges, part, 1)
		qTruth := Modularity(n, edges, truth, 1)
		if q <= 0.4 || q < qTruth-0.02 {
			t.Errorf("seed %d: modularity %.4f, planted %.4f", seed, q, qTruth)
		}
	}
}

func TestLeidenHigherResolutionGivesMoreCommunities(t *testing.T) {
	n, edges, _ := planted(3)
	low := numCommunities(Leiden(n, edges, Options{Resolution: 0.3, Seed: 3}))
	high := numCommunities(Leiden(n, edges, Options{Resolution: 3, Seed: 3}))
	if high < low {
		t.Fatalf("resolution 3 gave %d communities, resolution 0.3 gave %d", high, low)
	}
}

func TestModularity(t *testing.T) {
	const k, size = 6, 5
	edges := ringOfCliques(k, size)
	truth := make([]int, k*size)
	for v := range truth {
		truth[v] = v / size
	}
	if q := Modularity(k*size, edges, truth, 1); q <= 0.7 {
		t.Errorf("ring of cliques modularity of the true partition = %.4f, want > 0.7", q)
	}
	one := make([]int, k*size)
	if q := Modularity(k*size, edges, one, 1); q < -1e-9 || q > 1e-9 {
		t.Errorf("all-in-one modularity = %g, want ~0", q)
	}
	if q := Modularity(3, nil, make([]int, 3), 1); q != 0 {
		t.Errorf("graph without edges modularity = %g, want 0", q)
	}
}

func TestConnected(t *testing.T) {
	edges := []Edge{{U: 0, V: 1, W: 1}, {U: 1, V: 2, W: 1}}
	if !Connected(4, edges, []int{0, 0, 0, 1}) {
		t.Error("path 0-1-2 with singleton 3 should be connected")
	}
	if Connected(3, edges, []int{0, 1, 0}) {
		t.Error("nodes 0 and 2 share a community but are not adjacent: want disconnected")
	}
	if !Connected(0, nil, nil) {
		t.Error("empty graph should be connected")
	}
}

func TestLeidenPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("performance test skipped in short mode")
	}
	const n, m = 20000, 200000
	edges := randomGraph(n, m, 11)
	start := time.Now()
	part := Leiden(n, edges, Options{Seed: 11})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("Leiden on %d nodes and %d edges took %s", n, m, elapsed)
	}
	if len(part) != n {
		t.Fatalf("len(part) = %d, want %d", len(part), n)
	}
}

// leidenWithin runs Leiden in a goroutine and fails the test when it does not return within d, so a regression
// that never terminates fails instead of hanging the run.
func leidenWithin(t *testing.T, d time.Duration, n int, edges []Edge, o Options) []int {
	t.Helper()
	done := make(chan []int, 1)
	go func() { done <- Leiden(n, edges, o) }()
	select {
	case part := <-done:
		return part
	case <-time.After(d):
		t.Fatalf("Leiden on %d nodes and %d edges did not terminate within %s", n, len(edges), d)
		return nil
	}
}

func TestLeidenLargeSparsePlantedTerminates(t *testing.T) {
	const n = 90000
	rng := rand.New(rand.NewSource(7))
	edges := make([]Edge, 0, 3*n)
	for range 3 * n {
		a := rng.Intn(n)
		b := rng.Intn(n)
		if rng.Intn(20) < 19 {
			b = a/50*50 + rng.Intn(50)
		}
		edges = append(edges, Edge{U: a, V: b, W: 0.9})
	}
	start := time.Now()
	part := leidenWithin(t, 20*time.Second, n, edges, Options{Seed: 7})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Leiden took %s", elapsed)
	}
	if len(part) != n {
		t.Fatalf("len(part) = %d, want %d", len(part), n)
	}
	k := numCommunities(part)
	seen := make([]bool, k)
	for _, c := range part {
		seen[c] = true
	}
	for c, ok := range seen {
		if !ok {
			t.Fatalf("community id %d is not used; ids are not dense", c)
		}
	}
	if !Connected(n, edges, part) {
		t.Fatal("communities are not connected")
	}
}

func TestLeidenEqualGainTiesTerminate(t *testing.T) {
	const k = 20
	var edges []Edge
	for u := range k {
		for v := u + 1; v < k; v++ {
			edges = append(edges, Edge{U: u, V: v, W: 1})
		}
	}
	edges = append(edges, Edge{U: k - 1, V: k, W: 1e-9}, Edge{U: k, V: k + 1, W: 1e-9})
	n := k + 2
	part := leidenWithin(t, 5*time.Second, n, edges, Options{Seed: 3})
	if len(part) != n {
		t.Fatalf("len(part) = %d, want %d", len(part), n)
	}
	if !Connected(n, edges, part) {
		t.Fatalf("communities are not connected: %v", part)
	}
}
