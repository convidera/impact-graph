package graph

import (
	"encoding/json"
	"sort"
	"strconv"
)

// Merge combines the snapshots of several repositories (keyed by owner/name) into one indexed graph. The nodes of each
// repository get the id "<repo>//<id>" and their Repo; values (TypeValue, such as an event type) keep their id and are
// unified across repositories. Edges are rewritten the same way; an edge whose endpoint is missing from its own snapshot
// is dropped. Communities are per snapshot and are not merged. Meta["repos"] records each repository's commit.
func Merge(parts map[string]*Graph) *Graph {
	out := New()
	repos := make([]string, 0, len(parts))
	for repo, g := range parts {
		if g != nil {
			repos = append(repos, repo)
		}
	}
	sort.Strings(repos)
	repoMeta := map[string]map[string]string{}
	for _, repo := range repos {
		g := parts[repo]
		repoMeta[repo] = map[string]string{"commit": g.Meta["commit"], "built_at": g.Meta["built_at"]}
		ids := make(map[string]string, len(g.Nodes))
		for id, n := range g.Nodes {
			node := *n
			node.Props = withoutCommunity(n.Props)
			node.ID = repoID(repo, id)
			node.Repo = repo
			if n.Type == TypeValue {
				node.ID, node.Repo = id, ""
			}
			ids[id] = node.ID
			out.AddNode(node)
		}
		for _, e := range g.Edges {
			src, okSrc := ids[e.Src]
			dst, okDst := ids[e.Dst]
			if okSrc && okDst {
				out.add(src, dst, e.Kind, e.Conf, e.Evidence, e.Tag, e.Prov)
			}
		}
	}
	splitSharedNames(out)
	LinkConfig(out)
	repoJSON, _ := json.Marshal(repoMeta)
	out.Meta["repos"] = string(repoJSON)
	out.Meta["schema_version"] = strconv.Itoa(SchemaVersion)
	out.Meta["engine_version"] = EngineVersion
	out.Index()
	return out
}

// repoID is the id of a node of one repository in a merged graph.
func repoID(repo, id string) string { return repo + "//" + id }

// withoutCommunity copies props minus the community label, which is only meaningful inside one snapshot.
func withoutCommunity(props map[string]string) map[string]string {
	if props == nil {
		return nil
	}
	out := make(map[string]string, len(props))
	for k, v := range props {
		if k != "community" {
			out[k] = v
		}
	}
	return out
}
