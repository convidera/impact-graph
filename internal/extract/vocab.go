package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/convidera/impact-graph/internal/config"
	"github.com/convidera/impact-graph/internal/graph"
)

// Vocab joins the services of a repository on shared string values: event types, queue channels, tool names. A value is
// a node; every function or file that mentions it as a string literal (or through an enum case or constant that holds
// it) is linked to that node, so a Go emitter and a PHP handler meet in one place without sharing any code.
//
// A value that is a bare word (`error`, `plan`, `message`) is also an ordinary string in unrelated code, so a literal of it
// is not taken at its word: it joins the vocabulary only when the same function or file mentions another value of that
// vocabulary (a handler `match`es several event types, an emitter sits next to its siblings), or when it comes in through a
// constant or enum case. Otherwise the edge is kept at confidence 0.65, below the walk threshold, so a walk does not flood
// through every `"error"` in the repository but `--min-confidence 0.6` still shows it.
//
// Two things lift the rule: the entry of the vocabulary file says `"ambiguous": false`, or the vocabulary's roles
// (produce and consume) cover at most NarrowFiles files, so a bare word can only mean the vocabulary there.
type Vocab struct {
	mu      sync.Mutex
	byValue map[string][]vocabRef
	// corroborated holds from+"\x00"+vocabulary for every mention of a specific (not bare-word) value.
	corroborated map[string]bool
	pending      []pendingMention
}

type vocabRef struct {
	node    string
	vocab   string
	produce []string
	consume []string
	trusted bool // literals of this value are taken at their word, see Vocab
}

type pendingMention struct {
	from, vocab, dst, kind, evidence string
	conf                             float64
}

// AmbiguousConf is the confidence of a bare-word literal that nothing corroborates.
const AmbiguousConf = 0.65

// NarrowFiles is the most files the roles of a vocabulary may cover for its bare-word literals to be trusted.
const NarrowFiles = 5

var bareWord = regexp.MustCompile(`^[A-Za-z]+$`)

func newVocab() *Vocab {
	return &Vocab{byValue: map[string][]vocabRef{}, corroborated: map[string]bool{}}
}

// add registers a value under a node id.
func (v *Vocab) add(value, node, vocab string, produce, consume []string, trusted bool) {
	if value == "" {
		return
	}
	v.byValue[value] = append(v.byValue[value], vocabRef{node, vocab, produce, consume, trusted})
}

// flush adds the edges of bare-word literals: at full confidence where the same source also mentions a specific value of
// the vocabulary, at AmbiguousConf where it does not. Call it once after the last mention.
func (v *Vocab) flush(ctx *Ctx) {
	if v == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, p := range v.pending {
		conf := p.conf
		if !v.corroborated[p.from+"\x00"+p.vocab] && conf > AmbiguousConf {
			conf = AmbiguousConf
		}
		ctx.G.AddEdge(p.from, p.dst, p.kind, conf, p.evidence)
	}
	v.pending = nil
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
		trusted := map[string]bool{}
		narrow := rolesAreNarrow(ctx, voc)
		path := ""
		line := 0
		if voc.File != "" {
			path = voc.File
			b, err := os.ReadFile(filepath.Join(ctx.Root, voc.File))
			if err != nil {
				ctx.warn("vocabulary %s: %v", voc.Name, err)
			} else {
				values = append(values, jsonValues(b, voc.List, voc.Key)...)
				for _, val := range explicitlyUnambiguous(b, voc.List, voc.Key) {
					trusted[val] = true
				}
			}
		}
		sort.Strings(values)
		for _, val := range values {
			id := typ + ":" + val
			ctx.G.AddNode(graph.Node{ID: id, Type: graph.TypeValue, Label: id, Path: path, Line: line, Props: map[string]string{"vocabulary": voc.Name}})
			v.add(val, id, voc.Name, voc.Produce, voc.Consume, trusted[val] || narrow)
		}
	}
}

// rolesAreNarrow reports whether the produce and consume globs of a vocabulary together match at most NarrowFiles files.
func rolesAreNarrow(ctx *Ctx, voc config.Vocabulary) bool {
	globs := append(append([]string{}, voc.Produce...), voc.Consume...)
	if len(globs) == 0 {
		return false
	}
	n := 0
	for _, f := range ctx.Files {
		if config.Match(globs, f.Path) {
			if n++; n > NarrowFiles {
				return false
			}
		}
	}
	return true
}

// explicitlyUnambiguous lists the entries of a vocabulary file that say `"ambiguous": false`.
func explicitlyUnambiguous(b []byte, list, key string) []string {
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil
	}
	var cur any = doc
	if list != "" {
		cur = doc[list]
	}
	arr, _ := cur.([]any)
	if key == "" {
		key = "name"
	}
	var out []string
	for _, x := range arr {
		if m, ok := x.(map[string]any); ok {
			if name, ok := m[key].(string); ok && m["ambiguous"] == false {
				out = append(out, name)
			}
		}
	}
	return out
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
	literal := strings.HasSuffix(evidence, " literal "+value)
	bare := bareWord.MatchString(value)
	for _, ref := range v.byValue[value] {
		ambiguous := bare && !ref.trusted
		if !ctx.G.Has(ref.node) {
			continue
		}
		prod := len(ref.produce) > 0 && config.Match(ref.produce, filePath)
		cons := len(ref.consume) > 0 && config.Match(ref.consume, filePath)
		if (len(ref.produce) > 0 || len(ref.consume) > 0) && !prod && !cons {
			continue // a vocabulary with declared roles concerns only the files that have one
		}
		kind, c := fallback, conf-0.1
		switch {
		case prod && !cons:
			kind, c = graph.Produces, conf
		case cons && !prod:
			kind, c = graph.Consumes, conf
		}
		if !ambiguous {
			v.mu.Lock()
			v.corroborated[from+"\x00"+ref.vocab] = true
			v.mu.Unlock()
		} else if literal {
			v.mu.Lock()
			v.pending = append(v.pending, pendingMention{from, ref.vocab, ref.node, kind, evidence, c})
			v.mu.Unlock()
			continue
		}
		ctx.G.AddEdge(from, ref.node, kind, c, evidence)
	}
}
