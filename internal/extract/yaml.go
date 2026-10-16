package extract

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/convidera/impact-graph/internal/graph"
)

const (
	maxYAMLBytes = 512 << 10
	maxYAMLKeys  = 5000
	maxYAMLDepth = 12
)

var valuesFile = regexp.MustCompile(`(?i)(^|[.-])values([.-].*)?\.ya?ml$`)

// runYAML makes a node of every key path of a Helm values file (values.yaml, values-stage.yaml, stage.values.yaml): the
// place where a deployment sets a value of a service it does not contain. Without a declared mount (see
// config.Mount) nothing connects a key to code, so the nodes only mark where the values are; graph.LinkConfig joins them to
// the patterns of a mount, here or after a merge in the repository that declares it. A list element is the segment "[]",
// so a key found under every element is one node.
func runYAML(ctx *Ctx) {
	for _, f := range ctx.Files {
		if (f.Ext != ".yaml" && f.Ext != ".yml") || !valuesFile.MatchString(path.Base(f.Path)) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ctx.Root, f.Path))
		if err != nil || len(b) > maxYAMLBytes {
			continue
		}
		dec := yaml.NewDecoder(strings.NewReader(string(b)))
		w := &yamlWalk{ctx: ctx, file: f.Path, seen: map[string]bool{}}
		for {
			var doc yaml.Node
			if dec.Decode(&doc) != nil {
				break // a templated or broken document: what was read stays
			}
			if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
				w.walk(doc.Content[0], nil, "", 0)
			}
		}
	}
}

type yamlWalk struct {
	ctx  *Ctx
	file string
	seen map[string]bool
}

// walk visits a node; path is the key path above it and parent the id of the nearest key node.
func (w *yamlWalk) walk(n *yaml.Node, segs []string, parent string, depth int) {
	if depth > maxYAMLDepth || len(w.seen) > maxYAMLKeys {
		return
	}
	switch n.Kind {
	case yaml.AliasNode:
		if n.Alias != nil && depth < maxYAMLDepth {
			w.walk(n.Alias, segs, parent, depth+1)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.Value == "<<" {
				if k.Value == "<<" {
					w.walk(v, segs, parent, depth+1) // a merge key: its keys sit at this level
				}
				continue
			}
			next := append(append([]string{}, segs...), k.Value)
			id := w.add(next, k.Line, parent)
			w.walk(v, next, id, depth+1)
		}
	case yaml.SequenceNode:
		for _, item := range n.Content {
			w.walk(item, append(append([]string{}, segs...), "[]"), parent, depth+1)
		}
	}
}

func (w *yamlWalk) add(segs []string, line int, parent string) string {
	dotted := strings.Join(segs, ".")
	id := "config:" + w.file + "#" + dotted
	if !w.seen[id] {
		w.seen[id] = true
		w.ctx.G.AddNode(graph.Node{ID: id, Type: graph.TypeConfig, Label: dotted, Path: w.file, Line: line,
			Props: map[string]string{graph.PropConfigKind: graph.ConfigKey, graph.PropConfigPath: dotted}})
		if parent != "" {
			w.ctx.G.AddEdge(id, parent, graph.MemberOf, 1, w.file)
		}
	}
	return id
}
