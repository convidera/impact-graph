package graph

import "strings"

// isJoint says whether a node is an environment variable or an image name: the values repositories join through (see
// extract/joins.go). Other values, such as event types and channels, are always shared.
func isJoint(g *Graph, id string) bool {
	n := g.Nodes[id]
	return n != nil && n.Type == TypeValue && (strings.HasPrefix(id, "env:") || strings.HasPrefix(id, "image:"))
}

// jointEnd returns the joint value at one end of an edge and the repository of the other end, or "" when neither end
// is a joint value.
func jointEnd(g *Graph, e *Edge) (value, repo string) {
	switch {
	case isJoint(g, e.Dst) && g.repoOf(e.Src) != "":
		return e.Dst, g.repoOf(e.Src)
	case isJoint(g, e.Src) && g.repoOf(e.Dst) != "":
		return e.Src, g.repoOf(e.Dst)
	}
	return "", ""
}

// splitSharedNames keeps a shared env: or image: node only where data can flow between repositories: a repository
// produces the name and another one consumes it. A node that reaches two or more repositories without such a pair is
// split into one copy per repository, "<repo>//<id>", so the links inside each repository still hold and nothing joins
// across them. A node that reaches a single repository is left as it is.
func splitSharedNames(g *Graph) {
	touched := map[string]map[string]bool{}
	producers := map[string]map[string]bool{}
	consumers := map[string]map[string]bool{}
	add := func(m map[string]map[string]bool, v, repo string) {
		if m[v] == nil {
			m[v] = map[string]bool{}
		}
		m[v][repo] = true
	}
	for i := range g.Edges {
		e := &g.Edges[i]
		v, repo := jointEnd(g, e)
		if v == "" {
			continue
		}
		add(touched, v, repo)
		if e.Dst == v && e.Kind == Produces {
			add(producers, v, repo)
		}
		if e.Dst == v && e.Kind == Consumes {
			add(consumers, v, repo)
		}
	}
	split := map[string]bool{}
	for v, repos := range touched {
		if len(repos) < 2 || flows(producers[v], consumers[v]) {
			continue
		}
		split[v] = true
		for repo := range repos {
			// A copy of its own, props included: clustering writes the community label into the map, and copies that share
			// one map would write each other's.
			node := *g.Nodes[v]
			node.ID, node.Repo, node.Props = repoID(repo, v), repo, cloneProps(node.Props)
			g.AddNode(node)
		}
	}
	if len(split) == 0 {
		return
	}
	old := g.Edges
	g.Edges = nil
	g.seen = map[string]int{}
	for _, e := range old {
		srcRepo, dstRepo := g.repoOf(e.Src), g.repoOf(e.Dst)
		if split[e.Src] {
			e.Src = repoID(dstRepo, e.Src)
		}
		if split[e.Dst] {
			e.Dst = repoID(srcRepo, e.Dst)
		}
		g.add(e.Src, e.Dst, e.Kind, e.Conf, e.Evidence, e.Tag, e.Prov)
	}
	for v := range split {
		delete(g.Nodes, v)
	}
}

// flows says whether some repository produces a name and another one consumes it.
func flows(producers, consumers map[string]bool) bool {
	for p := range producers {
		for c := range consumers {
			if c != p {
				return true
			}
		}
	}
	return false
}

// cloneProps copies a props map; nil stays nil.
func cloneProps(props map[string]string) map[string]string {
	if props == nil {
		return nil
	}
	out := make(map[string]string, len(props))
	for k, v := range props {
		out[k] = v
	}
	return out
}
