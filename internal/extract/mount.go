package extract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
)

const (
	maxMountDepth = 10
	maxMountNodes = 3000
)

// configID is the id of a pattern node: the dotted path, with * for any key and [] for a list element.
func configPatternID(path string) string { return "config:" + path }

// runMounts turns every declared [[mount]] into pattern nodes: one per field path of the Go struct, found by the json
// tags, each linked to the Go field it comes from. graph.LinkConfig then joins the keys of values files to them.
func (g *golang) runMounts() {
	for _, m := range g.ctx.Cfg.Mounts {
		if m.Path == "" || m.GoType == "" {
			g.ctx.warn("mount %q: path and go_type are required", m.Path)
			continue
		}
		t := g.findType(m.GoType)
		if t == nil {
			g.ctx.warn("mount %s: no struct %s found (%s)", m.Path, m.GoType, strings.TrimSpace(m.Reason))
			continue
		}
		if t.Kind != "struct" {
			g.ctx.warn("mount %s: %s is not a struct", m.Path, t.Key)
			continue
		}
		w := &mountWalk{g: g, m: m, nodes: 0}
		w.pattern(m.Path, t.File.src.file.Path, t.Line, "go:"+t.Key)
		w.walk(t, m.Path, map[string]bool{t.Key: true}, 0)
	}
}

// findType resolves a struct by its full key or, when unique, by its name.
func (g *golang) findType(name string) *goType {
	if t, ok := g.types[name]; ok {
		return t
	}
	var found *goType
	for _, t := range g.types {
		if t.Name == name {
			if found != nil {
				g.ctx.warn("mount: struct name %s is not unique (%s, %s); use the full key", name, found.Key, t.Key)
				return nil
			}
			found = t
		}
	}
	return found
}

type mountWalk struct {
	g     *golang
	m     config.Mount
	nodes int
}

func (w *mountWalk) pattern(path, file string, line int, goID string) {
	if w.nodes >= maxMountNodes {
		return
	}
	w.nodes++
	id := configPatternID(path)
	gr := w.g.ctx.G
	gr.AddNode(graph.Node{ID: id, Type: graph.TypeConfig, Label: path, Path: file, Line: line,
		Props: map[string]string{graph.PropConfigKind: graph.ConfigPattern, graph.PropConfigPath: path, "mount": w.m.Path}})
	gr.AddEdge(id, goID, graph.Configures, 0.9, fmt.Sprintf("impact.toml mount %s: %s (%s:%d)", w.m.Path, strings.TrimSpace(w.m.Reason), file, line))
}

// walk adds the fields of t below prefix. A field of a struct type declared in the repository is followed; a type from
// another module (corev1.ResourceRequirements) ends the pattern at that field.
func (w *mountWalk) walk(t *goType, prefix string, seen map[string]bool, depth int) {
	if depth >= maxMountDepth {
		return
	}
	names := make([]string, 0, len(t.Fields))
	for n := range t.Fields {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, fname := range names {
		f := t.Fields[fname]
		name := f.JSON
		if name == "-" {
			continue
		}
		if name == "" {
			name = fname
		}
		path := prefix + "." + name
		if f.Inline {
			path = prefix
		} else {
			w.pattern(path, t.File.src.file.Path, f.Line, f.ID)
		}
		elem, next := f.Type, path
		switch {
		case f.Map != "":
			elem, next = f.Map, path+".*"
		case strings.HasPrefix(f.Type, "[]"):
			elem, next = strings.TrimPrefix(f.Type, "[]"), path+".[]"
		}
		w.follow(elem, next, seen, depth+1)
	}
	for _, emb := range t.Embedded {
		w.follow(emb, prefix, seen, depth+1)
	}
}

func (w *mountWalk) follow(key, path string, seen map[string]bool, depth int) {
	sub := w.g.types[key]
	if sub == nil || sub.Kind != "struct" || seen[key] {
		return
	}
	seen[key] = true
	w.walk(sub, path, seen, depth)
	delete(seen, key)
}
