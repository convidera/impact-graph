package graph

import (
	"strconv"
	"strings"
)

// Props of a TypeConfig node.
const (
	PropConfigKind = "config_kind" // ConfigKey or ConfigPattern
	PropConfigPath = "config_path" // dotted key path; "[]" is a list element, "*" in a pattern is any one key
	ConfigKey      = "key"         // a key that exists in a values file
	ConfigPattern  = "pattern"     // a path derived from a Go struct by a declared mount
)

// LinkConfig joins every key of a values file to the patterns of declared mounts it matches: the key
// agentEnvironments.default.spec.adapter.resources in one repository meets the pattern agentEnvironments.*.spec.adapter.resources
// that another repository declared for a Go struct, and through it the Go field. It reads nodes only, so it can run
// after an index and again after a merge (the same edge is added once), which is how a deployment repository without any
// configuration is connected to the service whose chart it feeds.
func LinkConfig(g *Graph) {
	type entry struct {
		id   string
		segs []string
	}
	byLen := map[int][]entry{}
	var keys []entry
	for id, n := range g.Nodes {
		if n.Type != TypeConfig {
			continue
		}
		e := entry{id, strings.Split(n.Props[PropConfigPath], ".")}
		switch n.Props[PropConfigKind] {
		case ConfigPattern:
			byLen[len(e.segs)] = append(byLen[len(e.segs)], e)
		case ConfigKey:
			keys = append(keys, e)
		}
	}
	for _, k := range keys {
		for _, p := range byLen[len(k.segs)] {
			if matchSegments(p.segs, k.segs) {
				kn := g.Nodes[k.id]
				g.AddEdge(k.id, p.id, Configures, 0.9, kn.Path+":"+strconv.Itoa(kn.Line)+" matches the mount pattern "+g.Nodes[p.id].Props[PropConfigPath])
			}
		}
	}
}

func matchSegments(pattern, key []string) bool {
	for i, p := range pattern {
		if p != "*" && p != key[i] {
			return false
		}
	}
	return true
}
