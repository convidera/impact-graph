package graph

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// repoMeta is Meta["repos"] of a merged graph: each repository's commit and build time, keyed by owner/name.
func (g *Graph) repoMeta() map[string]map[string]string {
	if g.Meta["repos"] == "" {
		return nil
	}
	var meta map[string]map[string]string
	if err := json.Unmarshal([]byte(g.Meta["repos"]), &meta); err != nil {
		return nil
	}
	return meta
}

// Repos lists the repositories of a merged graph, sorted by owner/name; nil for a snapshot of one repository.
func (g *Graph) Repos() []string {
	meta := g.repoMeta()
	out := make([]string, 0, len(meta))
	for r := range meta {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// ResolveRepo turns a repository argument into the owner/name of a repository of a merged graph: the exact name, or a
// name without the owner that only one repository has. "" means no restriction. On a snapshot of one repository there is
// nothing to restrict to, so the argument is accepted and ignored.
func (g *Graph) ResolveRepo(s string) (string, error) {
	repos := g.Repos()
	if s == "" || len(repos) == 0 {
		return "", nil
	}
	var found []string
	for _, r := range repos {
		if r == s {
			return r, nil
		}
		if strings.HasSuffix(r, "/"+s) {
			found = append(found, r)
		}
	}
	if len(found) == 1 {
		return found[0], nil
	}
	known := strings.Join(repos, ", ")
	if len(found) == 0 {
		return "", fmt.Errorf("unknown repository %q; this graph has %s", s, known)
	}
	return "", fmt.Errorf("repository %q is ambiguous; use owner/name, this graph has %s", s, known)
}

// InRepo reports whether a node is listed for a repository: every node when repo is "", otherwise the nodes of that
// repository. Shared values (no repository) are only in the lists when no repository is asked for.
func (g *Graph) InRepo(n *Node, repo string) bool {
	return repo == "" || n.Repo == repo
}

// repoOf is the repository of a node by id; "" for a shared value or an unknown node.
func (g *Graph) repoOf(id string) string {
	if n := g.Nodes[id]; n != nil {
		return n.Repo
	}
	return ""
}

// RepoOf is the repository of a node by id; "" for a shared value or an unknown node.
func (g *Graph) RepoOf(id string) string { return g.repoOf(id) }

// RepoInfo is the commit and build time recorded for one repository of a merged graph.
func (g *Graph) RepoInfo(repo string) map[string]string { return g.repoMeta()[repo] }

// RepoStat is what a merged graph holds of one repository.
type RepoStat struct {
	Repo   string
	Commit string
	Nodes  int // nodes of the repository
	// Edges are those with both ends in the repository, or with the other end a shared value. An edge from one
	// repository straight to another is in neither count (see Links).
	Edges int
}

// RepoStats counts the nodes and edges of each repository of a merged graph, sorted by name; nil for a snapshot.
func (g *Graph) RepoStats() []RepoStat {
	meta := g.repoMeta()
	var out []RepoStat
	idx := make(map[string]int, len(meta))
	for _, r := range g.Repos() {
		idx[r] = len(out)
		out = append(out, RepoStat{Repo: r, Commit: meta[r]["commit"]})
	}
	for _, n := range g.Nodes {
		if i, ok := idx[n.Repo]; ok {
			out[i].Nodes++
		}
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		a, b := g.repoOf(e.Src), g.repoOf(e.Dst)
		r := a
		if r == "" {
			r = b
		}
		if r == "" || (a != "" && b != "" && a != b) {
			continue
		}
		if j, ok := idx[r]; ok {
			out[j].Edges++
		}
	}
	return out
}

// SharedValuesOf lists the shared values (nodes without a repository) that an edge of the repository reaches. It is nil
// for repo "".
func (g *Graph) SharedValuesOf(repo string) map[string]bool {
	if repo == "" {
		return nil
	}
	out := map[string]bool{}
	for i := range g.Edges {
		e := &g.Edges[i]
		a, b := g.repoOf(e.Src), g.repoOf(e.Dst)
		switch {
		case a == repo && b == "":
			out[e.Dst] = true
		case b == repo && a == "":
			out[e.Src] = true
		}
	}
	return out
}

// RepoPair is two repositories that connect: by an edge from one's own nodes to the other's (Cross), or through shared
// values that both reach (Values).
type RepoPair struct {
	A, B   string
	Cross  int
	Values int
}

// Links describes how the repositories of a merged graph connect. Shared is the number of shared value nodes, Through the
// number of those that reach at least two repositories, and Cross the number of edges between two repositories' own
// nodes. Pairs lists the connected pairs, sorted.
type Links struct {
	Shared  int
	Through int
	Cross   int
	Pairs   []RepoPair
}

// Links counts how the repositories of a merged graph connect (see Links).
func (g *Graph) Links() Links {
	var l Links
	for _, n := range g.Nodes {
		if n.Repo == "" {
			l.Shared++
		}
	}
	reach := map[string]map[string]bool{}
	pairs := map[[2]string]*RepoPair{}
	pair := func(a, b string) *RepoPair {
		if a > b {
			a, b = b, a
		}
		p := pairs[[2]string{a, b}]
		if p == nil {
			p = &RepoPair{A: a, B: b}
			pairs[[2]string{a, b}] = p
		}
		return p
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		a, b := g.repoOf(e.Src), g.repoOf(e.Dst)
		switch {
		case a == "" && b != "", b == "" && a != "":
			v, r := e.Src, b
			if a != "" {
				v, r = e.Dst, a
			}
			if reach[v] == nil {
				reach[v] = map[string]bool{}
			}
			reach[v][r] = true
		case a != "" && b != "" && a != b:
			l.Cross++
			pair(a, b).Cross++
		}
	}
	for _, repos := range reach {
		if len(repos) < 2 {
			continue
		}
		l.Through++
		list := make([]string, 0, len(repos))
		for r := range repos {
			list = append(list, r)
		}
		for i := range list {
			for j := i + 1; j < len(list); j++ {
				pair(list[i], list[j]).Values++
			}
		}
	}
	for _, p := range pairs {
		l.Pairs = append(l.Pairs, *p)
	}
	sort.Slice(l.Pairs, func(i, j int) bool {
		if l.Pairs[i].A != l.Pairs[j].A {
			return l.Pairs[i].A < l.Pairs[j].A
		}
		return l.Pairs[i].B < l.Pairs[j].B
	})
	return l
}
