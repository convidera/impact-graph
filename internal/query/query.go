// Package query answers the structured questions of a graph browser as plain data: which repositories a snapshot holds and
// how they connect, which nodes match a search, and the neighbours of one node. Its types are the contract of
// `impact query ... --json`, so a service that shows the graph (the session explorer of the platform) reads that output
// instead of the snapshot's tables, which are an implementation detail of the engine.
package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/convidera/impact-graph/internal/graph"
)

// Limits of the answers.
const (
	MaxEdges     = 60  // edges per direction of a node
	MaxNodes     = 100 // cap of one search
	DefaultNodes = 30  // page size without a limit
)

// Node is one node of the graph.
type Node struct {
	ID         string `json:"id"`
	Type       string `json:"type"`
	Label      string `json:"label"`
	Repository string `json:"repository,omitempty"` // empty for a node shared by several repositories
	Path       string `json:"path,omitempty"`
	Line       int    `json:"line,omitempty"`
	Degree     int    `json:"degree"` // edges touching the node, in both directions
}

// TypeCount is how many nodes of one type a repository has.
type TypeCount struct {
	Type  string `json:"type"`
	Count int    `json:"count"`
}

// Repository is one repository of the graph.
type Repository struct {
	Name    string      `json:"name"` // owner/name
	Commit  string      `json:"commit"`
	BuiltAt string      `json:"built_at"`
	Nodes   int         `json:"nodes"`
	Edges   int         `json:"edges"` // edges with at least one end in the repository
	Types   []TypeCount `json:"types"` // most frequent first
}

// Connection says how two repositories are joined.
type Connection struct {
	From         string `json:"from"`
	To           string `json:"to"`
	SharedValues int    `json:"shared_values"` // shared nodes that both repositories have edges to
	DirectEdges  int    `json:"direct_edges"`  // edges between a node of one and a node of the other
}

// Repositories is the answer of `impact query repos`.
type Repositories struct {
	Repositories         []Repository `json:"repositories"`
	SharedValues         int          `json:"shared_values"` // nodes without a repository: event types, environment variable names, image names
	SharedTypes          []TypeCount  `json:"shared_types"`
	CrossRepositoryEdges int          `json:"cross_repository_edges"`
	Connections          []Connection `json:"connections"` // every pair; a pair with both numbers 0 is not connected
}

// Edge is an edge next to a node; Node is the other end.
type Edge struct {
	Kind       string  `json:"kind"`
	Confidence float64 `json:"confidence"`
	Provenance string  `json:"provenance,omitempty"`
	Evidence   string  `json:"evidence,omitempty"`
	Node       Node    `json:"node"`
}

// NodeDetail is the answer of `impact query node`.
type NodeDetail struct {
	Node          Node   `json:"node"`
	Outgoing      []Edge `json:"outgoing"` // best confidence first, at most MaxEdges
	Incoming      []Edge `json:"incoming"`
	OutgoingTotal int    `json:"outgoing_total"`
	IncomingTotal int    `json:"incoming_total"`
}

// Nodes is the answer of `impact query nodes`.
type Nodes struct {
	Nodes     []Node `json:"nodes"` // most connected first
	Truncated bool   `json:"truncated"`
}

// Index precomputes what the questions share: the degree of every node.
type Index struct {
	g      *graph.Graph
	degree map[string]int
}

// New prepares a graph for queries.
func New(g *graph.Graph) *Index {
	x := &Index{g: g, degree: make(map[string]int, len(g.Nodes))}
	for i := range g.Edges {
		e := &g.Edges[i]
		x.degree[e.Src]++
		x.degree[e.Dst]++
	}
	return x
}

func (x *Index) node(n *graph.Node) Node {
	return Node{ID: n.ID, Type: n.Type, Label: n.Label, Repository: n.Repo, Path: n.Path, Line: n.Line, Degree: x.degree[n.ID]}
}

func sortTypes(t []TypeCount) {
	sort.Slice(t, func(i, j int) bool {
		if t[i].Count != t[j].Count {
			return t[i].Count > t[j].Count
		}
		return t[i].Type < t[j].Type
	})
}

// Repositories describes the repositories of the graph and how they connect. A snapshot of one repository is a graph of one.
func (x *Index) Repositories() Repositories {
	g := x.g
	out := Repositories{Repositories: []Repository{}, SharedTypes: []TypeCount{}, Connections: []Connection{}}
	stats := map[string]*Repository{}
	repo := func(name string) *Repository {
		r := stats[name]
		if r == nil {
			r = &Repository{Name: name, Types: []TypeCount{}}
			stats[name] = r
		}
		return r
	}
	for _, name := range g.Repos() {
		m := g.RepoInfo(name)
		r := repo(name)
		r.Commit, r.BuiltAt = m["commit"], m["built_at"]
	}
	if g.Meta["repos"] == "" && g.Meta["repo"] != "" { // a snapshot of one repository
		r := repo(g.Meta["repo"])
		r.Commit, r.BuiltAt = g.Meta["commit"], g.Meta["built_at"]
	}
	perType := map[string]map[string]int{}
	shared := map[string]int{}
	for _, n := range g.Nodes {
		if n.Repo == "" {
			out.SharedValues++
			shared[n.Type]++
			continue
		}
		repo(n.Repo).Nodes++
		if perType[n.Repo] == nil {
			perType[n.Repo] = map[string]int{}
		}
		perType[n.Repo][n.Type]++
	}
	for t, c := range shared {
		out.SharedTypes = append(out.SharedTypes, TypeCount{t, c})
	}
	sortTypes(out.SharedTypes)
	for i := range g.Edges {
		e := &g.Edges[i]
		a, b := g.RepoOf(e.Src), g.RepoOf(e.Dst)
		if a != "" {
			repo(a).Edges++
		}
		if b != "" && b != a {
			repo(b).Edges++
		}
		if a != "" && b != "" && a != b {
			out.CrossRepositoryEdges++
		}
	}
	links := g.Links()
	names := make([]string, 0, len(stats))
	for n := range stats {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := stats[n]
		for t, c := range perType[n] {
			r.Types = append(r.Types, TypeCount{t, c})
		}
		sortTypes(r.Types)
		out.Repositories = append(out.Repositories, *r)
	}
	pairs := map[[2]string]graph.RepoPair{}
	for _, p := range links.Pairs {
		pairs[[2]string{p.A, p.B}] = p
	}
	for i := range names {
		for j := i + 1; j < len(names); j++ {
			p := pairs[[2]string{names[i], names[j]}]
			out.Connections = append(out.Connections, Connection{From: names[i], To: names[j], SharedValues: p.Values, DirectEdges: p.Cross})
		}
	}
	return out
}

// Search lists the nodes whose label or id (without the repository prefix) contains text, case-insensitively, or the most
// connected nodes when text is empty, most connected first. repo and typ narrow the search; limit is capped at MaxNodes.
func (x *Index) Search(repo, text, typ string, limit int) (Nodes, error) {
	out := Nodes{Nodes: []Node{}}
	if limit <= 0 {
		limit = DefaultNodes
	}
	if limit > MaxNodes {
		limit = MaxNodes
	}
	r, err := x.g.ResolveRepo(repo)
	if err != nil {
		return out, err
	}
	text = strings.ToLower(strings.TrimSpace(text))
	var hits []*graph.Node
	for _, n := range x.g.Nodes {
		if (r != "" && n.Repo != r) || (typ != "" && n.Type != typ) {
			continue
		}
		if text != "" && !strings.Contains(strings.ToLower(n.Label), text) && !strings.Contains(strings.ToLower(strings.TrimPrefix(n.ID, n.Repo+"//")), text) {
			continue
		}
		hits = append(hits, n)
	}
	sort.Slice(hits, func(i, j int) bool {
		if x.degree[hits[i].ID] != x.degree[hits[j].ID] {
			return x.degree[hits[i].ID] > x.degree[hits[j].ID]
		}
		return hits[i].ID < hits[j].ID
	})
	if len(hits) > limit {
		hits, out.Truncated = hits[:limit], true
	}
	for _, n := range hits {
		out.Nodes = append(out.Nodes, x.node(n))
	}
	return out, nil
}

// ErrNodeNotFound is returned by Detail for an id that is not in the graph.
var ErrNodeNotFound = fmt.Errorf("node not found")

// Detail returns a node and up to MaxEdges edges per direction, best confidence first.
func (x *Index) Detail(id string) (NodeDetail, error) {
	out := NodeDetail{Outgoing: []Edge{}, Incoming: []Edge{}}
	n := x.g.Nodes[id]
	if n == nil {
		return out, ErrNodeNotFound
	}
	out.Node = x.node(n)
	collect := func(idx []int, other func(*graph.Edge) string) ([]Edge, int) {
		type cand struct {
			e *graph.Edge
			o *graph.Node
		}
		var cs []cand
		for _, i := range idx {
			e := &x.g.Edges[i]
			if o := x.g.Nodes[other(e)]; o != nil {
				cs = append(cs, cand{e, o})
			}
		}
		sort.Slice(cs, func(i, j int) bool {
			a, b := cs[i], cs[j]
			if a.e.Conf != b.e.Conf {
				return a.e.Conf > b.e.Conf
			}
			if a.e.Kind != b.e.Kind {
				return a.e.Kind < b.e.Kind
			}
			return a.o.ID < b.o.ID
		})
		total := len(cs)
		if len(cs) > MaxEdges {
			cs = cs[:MaxEdges]
		}
		edges := make([]Edge, 0, len(cs))
		for _, c := range cs {
			edges = append(edges, Edge{Kind: c.e.Kind, Confidence: c.e.Conf, Provenance: c.e.Prov, Evidence: c.e.Evidence, Node: x.node(c.o)})
		}
		return edges, total
	}
	out.Outgoing, out.OutgoingTotal = collect(x.g.Out(id), func(e *graph.Edge) string { return e.Dst })
	out.Incoming, out.IncomingTotal = collect(x.g.In(id), func(e *graph.Edge) string { return e.Src })
	return out, nil
}

// Lookup returns the nodes with the given ids, with their degree; an id that is not in the graph is left out.
func (x *Index) Lookup(ids []string) Nodes {
	out := Nodes{Nodes: []Node{}}
	seen := map[string]bool{}
	for _, id := range ids {
		if n := x.g.Nodes[id]; n != nil && !seen[id] {
			seen[id] = true
			out.Nodes = append(out.Nodes, x.node(n))
		}
	}
	return out
}
