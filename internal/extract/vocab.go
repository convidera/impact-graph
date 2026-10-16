package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
)

// Vocab joins the services of a repository on shared string values: event types, queue channels, tool names. A value is
// a node; every function or file that mentions it as a string literal (or through an enum case or constant that holds
// it) is linked to that node, so a Go emitter and a PHP handler meet in one place without sharing any code.
type Vocab struct {
	byValue map[string][]vocabRef
}

type vocabRef struct {
	node    string
	produce []string
	consume []string
}

func newVocab() *Vocab { return &Vocab{byValue: map[string][]vocabRef{}} }

// add registers a value under a node id.
func (v *Vocab) add(value, node string, produce, consume []string) {
	if value == "" {
		return
	}
	v.byValue[value] = append(v.byValue[value], vocabRef{node, produce, consume})
}

// loadVocab creates the nodes of the configured vocabularies.
func loadVocab(ctx *Ctx) {
	v := newVocab()
	ctx.Vocab = v
	for _, voc := range ctx.Cfg.Vocabulary {
		typ := voc.Type
		if typ == "" {
			typ = voc.Name
		}
		if voc.Key == "" {
			voc.Key = voc.NameKey
		}
		values := append([]string{}, voc.Values...)
		path := ""
		line := 0
		if voc.File != "" {
			path = voc.File
			b, err := os.ReadFile(filepath.Join(ctx.Root, voc.File))
			if err != nil {
				ctx.warn("vocabulary %s: %v", voc.Name, err)
			} else {
				values = append(values, jsonValues(b, voc.List, voc.Key)...)
			}
		}
		sort.Strings(values)
		for _, val := range values {
			id := typ + ":" + val
			ctx.G.AddNode(graph.Node{ID: id, Type: graph.TypeValue, Label: id, Path: path, Line: line, Props: map[string]string{"vocabulary": voc.Name}})
			v.add(val, id, voc.Produce, voc.Consume)
		}
	}
}

func jsonValues(b []byte, list, key string) []string {
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}
	var cur any = doc
	if list != "" {
		cur = doc[list]
	}
	arr, _ := cur.([]any)
	var out []string
	if key == "" {
		key = "name"
	}
	for _, x := range arr {
		switch t := x.(type) {
		case string:
			out = append(out, t)
		case map[string]any:
			if s, ok := t[key].(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// mention links `from` (a function id or a file id) to every node that holds value.
func (v *Vocab) mention(ctx *Ctx, from, filePath, value, evidence string, conf float64) {
	v.mentionAs(ctx, from, filePath, value, evidence, conf, graph.Mentions)
}

// mentionAs is mention with the kind used when the vocabulary gives the file no role.
func (v *Vocab) mentionAs(ctx *Ctx, from, filePath, value, evidence string, conf float64, fallback string) {
	if v == nil {
		return
	}
	for _, ref := range v.byValue[value] {
		if !ctx.G.Has(ref.node) {
			continue
		}
		prod := len(ref.produce) > 0 && config.Match(ref.produce, filePath)
		cons := len(ref.consume) > 0 && config.Match(ref.consume, filePath)
		if (len(ref.produce) > 0 || len(ref.consume) > 0) && !prod && !cons {
			continue // a vocabulary with declared roles concerns only the files that have one
		}
		switch {
		case prod && !cons:
			ctx.G.AddEdge(from, ref.node, graph.Produces, conf, evidence)
		case cons && !prod:
			ctx.G.AddEdge(from, ref.node, graph.Consumes, conf, evidence)
		default:
			ctx.G.AddEdge(from, ref.node, fallback, conf-0.1, evidence)
		}
	}
}
