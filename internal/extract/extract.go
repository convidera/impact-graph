// Package extract turns source files into graph facts with tree-sitter. It is the only package that needs cgo.
//
// Extraction runs in phases so that every file can be resolved against every other file: parse, declare symbols,
// resolve bodies into edges, then the framework packs (schema, routes, tools), vocabularies, docs and history.
package extract

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/scan"
)

// Ctx is shared by every extractor of one run.
type Ctx struct {
	Root  string
	Cfg   *config.Config
	G     *graph.Graph
	Files []scan.File
	Vocab *Vocab
	Warn  []string

	mu sync.Mutex
}

func (c *Ctx) warn(format string, a ...any) {
	c.mu.Lock()
	c.Warn = append(c.Warn, fmt.Sprintf(format, a...))
	c.mu.Unlock()
}

// source is a parsed file.
type source struct {
	file scan.File
	src  []byte
	tree *ts.Tree
	root *ts.Node
}

func (s *source) close() {
	if s.tree != nil {
		s.tree.Close()
	}
}

func parseFiles(files []scan.File, root string, lang *ts.Language) []*source {
	out := make([]*source, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f scan.File) {
			defer wg.Done()
			defer func() { <-sem }()
			b, err := os.ReadFile(filepath.Join(root, f.Path))
			if err != nil {
				return
			}
			p := ts.NewParser()
			defer p.Close()
			if err := p.SetLanguage(lang); err != nil {
				return
			}
			t := p.Parse(b, nil)
			out[i] = &source{file: f, src: b, tree: t, root: t.RootNode()}
		}(i, f)
	}
	wg.Wait()
	var res []*source
	for _, s := range out {
		if s != nil {
			res = append(res, s)
		}
	}
	return res
}

// ---- tree-sitter helpers ----

func (s *source) text(n *ts.Node) string {
	if n == nil {
		return ""
	}
	return n.Utf8Text(s.src)
}

func line(n *ts.Node) int { return int(n.StartPosition().Row) + 1 }

func endLine(n *ts.Node) int { return int(n.EndPosition().Row) + 1 }

func kids(n *ts.Node) []*ts.Node {
	out := make([]*ts.Node, 0, n.NamedChildCount())
	for i := uint(0); i < n.NamedChildCount(); i++ {
		out = append(out, n.NamedChild(i))
	}
	return out
}

func field(n *ts.Node, name string) *ts.Node { return n.ChildByFieldName(name) }

// firstKid returns the first named child of one of the given kinds.
func firstKid(n *ts.Node, kinds ...string) *ts.Node {
	for _, k := range kids(n) {
		for _, kind := range kinds {
			if k.Kind() == kind {
				return k
			}
		}
	}
	return nil
}

// evidence formats the position of a node for an edge.
func (s *source) evidence(n *ts.Node, note string) string {
	e := fmt.Sprintf("%s:%d", s.file.Path, line(n))
	if note != "" {
		e += " " + note
	}
	return e
}

func fileID(path string) string { return "file:" + path }

func (c *Ctx) addFileNodes() {
	for _, f := range c.Files {
		t := graph.TypeFile
		switch f.Kind {
		case "test":
			t = graph.TypeTest
		case "doc":
			t = graph.TypeDoc
		}
		c.G.AddNode(graph.Node{ID: fileID(f.Path), Type: t, Label: f.Path, Path: f.Path, Props: map[string]string{"file_kind": f.Kind}})
	}
}

// ---- small string utilities shared by the extractors ----

var (
	camelBoundary = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	camelAcronym  = regexp.MustCompile(`([A-Z]+)([A-Z][a-z])`)
)

func snake(s string) string {
	s = camelAcronym.ReplaceAllString(s, "${1}_${2}")
	s = camelBoundary.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(s)
}

func kebab(s string) string { return strings.ReplaceAll(snake(s), "_", "-") }

// plural is the English plural Laravel uses for table names, enough for model names.
func plural(w string) string {
	switch {
	case strings.HasSuffix(w, "y") && len(w) > 1 && !strings.ContainsRune("aeiou", rune(w[len(w)-2])):
		return w[:len(w)-1] + "ies"
	case strings.HasSuffix(w, "s"), strings.HasSuffix(w, "x"), strings.HasSuffix(w, "z"), strings.HasSuffix(w, "ch"), strings.HasSuffix(w, "sh"):
		return w + "es"
	}
	return w + "s"
}

func lastSegment(s string, sep string) string {
	if i := strings.LastIndex(s, sep); i >= 0 {
		return s[i+len(sep):]
	}
	return s
}

type scanFile = scan.File

// Run extracts every file of the context into the graph.
func Run(ctx *Ctx) {
	ctx.addFileNodes()
	loadVocab(ctx)
	openAPI := loadOpenAPI(ctx)
	var phpFiles, goFiles, tsFiles, pyFiles []scan.File
	for _, f := range ctx.Files {
		if f.Kind == "doc" {
			continue
		}
		switch f.Ext {
		case ".php":
			phpFiles = append(phpFiles, f)
		case ".go":
			goFiles = append(goFiles, f)
		case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".mts", ".vue":
			tsFiles = append(tsFiles, f)
		case ".py":
			pyFiles = append(pyFiles, f)
		}
	}
	runPHP(ctx, phpFiles)
	runGo(ctx, goFiles)
	runTS(ctx, tsFiles) // after the backend: the HTTP calls of a client are joined to the routes found there
	runPython(ctx, pyFiles)
	ctx.Vocab.flush(ctx)
	linkOpenAPI(ctx, openAPI)
	runYAML(ctx)
	runEnv(ctx)
	graph.LinkConfig(ctx.G) // after the Go mounts (runGo) and the values files
	runDocs(ctx)
	runHistory(ctx)
	runLinks(ctx)
}
