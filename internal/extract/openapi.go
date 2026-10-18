package extract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/convidera/impact-graph/internal/graph"
)

// An OpenAPI document (JSON or YAML with a top-level `openapi` or `swagger` key) is where a service's HTTP contract is
// written down and where every other side of it is generated from: the Data class or Go struct a schema is named after, the
// TypeScript type and client generated from it, the hand-written client of another language. Without nodes for it nothing
// connects those, so a change to a request class never reaches `integration/api.json` or the client that derives from it.
//
// The document yields
//   - a `schema` node per component schema and an `operation` node per verb and path, both pointing at the line of the
//     document, so `impact review` lists the document itself as a contract the change reaches;
//   - `uses` edges: an operation uses the schemas of its parameters, request body and responses, a schema the schemas it
//     refers to (a change to the used one affects the user);
//   - `maps_to` from an operation to the route that serves it (verb and path, a server prefix such as /api accounted for);
//   - `maps_to` from a schema to the class, struct or type of the same name: a PHP Data class, a Go struct and a generated
//     TypeScript interface that share the name. When a language has several types of that name the edge is a guess (0.6).
//   - the path of an operation as a vocabulary value, so any function that names it as a string (a hand-written HTTP client,
//     a router registration) is linked to the operation like an event type is to the code that emits it.
type openAPIDoc struct {
	file    string
	lines   []string
	prefix  string
	schemas map[string]int // name -> line
	ops     []openAPIOp
	refs    map[string][]string // schema -> schemas it refers to
}

type openAPIOp struct {
	id, verb, path string
	line           int
	schemas        []string
}

var (
	refRe      = regexp.MustCompile(`^#/(?:components/schemas|definitions)/(.+)$`)
	openAPIHdr = regexp.MustCompile(`(?m)"(openapi|swagger)"\s*:|^(openapi|swagger)\s*:`)
)

// loadOpenAPI reads the documents and registers their paths as vocabulary values. It runs before the language extractors,
// which mention those values; linkOpenAPI joins the documents to routes and types once every symbol exists.
func loadOpenAPI(ctx *Ctx) []*openAPIDoc {
	var docs []*openAPIDoc
	for _, f := range ctx.Files {
		if f.Ext != ".json" && f.Ext != ".yaml" && f.Ext != ".yml" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ctx.Root, f.Path))
		if err != nil {
			continue
		}
		if !openAPIHdr.Match(b) { // generated documents sort their keys: `openapi` is not necessarily near the top
			continue
		}
		var raw map[string]any
		if f.Ext == ".json" {
			err = json.Unmarshal(b, &raw)
		} else {
			err = yaml.Unmarshal(b, &raw)
		}
		if err != nil || (raw["openapi"] == nil && raw["swagger"] == nil) {
			continue
		}
		d := parseOpenAPI(f.Path, b, raw)
		d.addNodes(ctx)
		docs = append(docs, d)
	}
	return docs
}

func parseOpenAPI(file string, b []byte, raw map[string]any) *openAPIDoc {
	d := &openAPIDoc{file: file, lines: strings.Split(string(b), "\n"), schemas: map[string]int{}, refs: map[string][]string{}}
	if servers, ok := raw["servers"].([]any); ok && len(servers) > 0 {
		if u, ok := asMapAny(servers[0])["url"].(string); ok {
			d.prefix = "/" + strings.Trim(originRe.ReplaceAllString(u, ""), "/")
			if d.prefix == "/" {
				d.prefix = ""
			}
		}
	}
	schemas := asMapAny(asMapAny(raw["components"])["schemas"])
	if len(schemas) == 0 {
		schemas = asMapAny(raw["definitions"])
	}
	for name, s := range schemas {
		d.schemas[name] = d.lineOf(`"`+name+`"`, name+":", 0)
		d.refs[name] = collectRefs(s)
	}
	paths := asMapAny(raw["paths"])
	pnames := make([]string, 0, len(paths))
	for p := range paths {
		pnames = append(pnames, p)
	}
	sort.Strings(pnames)
	for _, p := range pnames {
		pl := d.lineOf(`"`+p+`"`, p+":", 0)
		for verb, op := range asMapAny(paths[p]) {
			v, ok := httpVerbs[strings.ToLower(verb)]
			if !ok {
				continue
			}
			o := openAPIOp{verb: v, path: p, line: d.lineOf(`"`+verb+`"`, verb+":", pl), schemas: collectRefs(op)}
			o.id = "op:" + file + "#" + v + " " + p
			d.ops = append(d.ops, o)
		}
		// parameters declared once for the path item
		for _, r := range collectRefs(asMapAny(paths[p])["parameters"]) {
			for i := range d.ops {
				if d.ops[i].path == p {
					d.ops[i].schemas = append(d.ops[i].schemas, r)
				}
			}
		}
	}
	return d
}

// lineOf finds the line of a key at or after from; 1 when the document is not laid out one key per line.
func (d *openAPIDoc) lineOf(jsonKey, yamlKey string, from int) int {
	for i := from; i < len(d.lines); i++ {
		t := strings.TrimSpace(d.lines[i])
		if strings.HasPrefix(t, jsonKey+":") || strings.HasPrefix(t, jsonKey+" :") || t == yamlKey || strings.HasPrefix(t, yamlKey+" ") {
			return i + 1
		}
	}
	if from > 0 {
		return from
	}
	return 1
}

func asMapAny(v any) map[string]any { m, _ := v.(map[string]any); return m }

// collectRefs lists the schemas a JSON value refers to with $ref, each once, in order of appearance.
func collectRefs(v any) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(any)
	walk = func(v any) {
		switch t := v.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if k == "$ref" {
					if s, ok := t[k].(string); ok {
						if m := refRe.FindStringSubmatch(s); m != nil && !seen[m[1]] {
							seen[m[1]] = true
							out = append(out, m[1])
						}
					}
					continue
				}
				walk(t[k])
			}
		case []any:
			for _, x := range t {
				walk(x)
			}
		}
	}
	walk(v)
	return out
}

func schemaID(file, name string) string { return "schema:" + file + "#" + name }

func (d *openAPIDoc) addNodes(ctx *Ctx) {
	g := ctx.G
	names := make([]string, 0, len(d.schemas))
	for n := range d.schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		g.AddNode(graph.Node{ID: schemaID(d.file, n), Type: graph.TypeSchema, Label: n, Path: d.file, Line: d.schemas[n]})
	}
	for _, n := range names {
		for _, r := range d.refs[n] {
			if _, ok := d.schemas[r]; ok {
				g.AddEdge(schemaID(d.file, n), schemaID(d.file, r), graph.Uses, 0.9, d.file+":"+itoa(d.schemas[n])+" $ref "+r)
			}
		}
	}
	for _, o := range d.ops {
		g.AddNode(graph.Node{ID: o.id, Type: graph.TypeOp, Label: o.verb + " " + d.prefix + o.path, Path: d.file, Line: o.line})
		for _, r := range o.schemas {
			if _, ok := d.schemas[r]; ok {
				g.AddEdge(o.id, schemaID(d.file, r), graph.Uses, 0.9, d.file+":"+itoa(o.line)+" $ref "+r)
			}
		}
	}
	if ctx.Vocab != nil {
		// a static prefix ('/v1/permissions/' . $id) names the operation only when no other operation shares it
		for _, o := range d.ops {
			for _, lit := range opLiterals(d.prefix, o.path) {
				if strings.Contains(o.path, "{") && !strings.HasSuffix(lit, "}") && prefixShared(d, lit) {
					continue
				}
				ctx.Vocab.add(lit, o.id, "openapi-path", nil, nil, true)
			}
		}
	}
}

// prefixShared reports whether more than one path of the document starts with the static prefix lit.
func prefixShared(d *openAPIDoc, lit string) bool {
	n := 0
	seen := map[string]bool{}
	for _, o := range d.ops {
		if strings.HasPrefix(d.prefix+o.path, lit) && !seen[o.path] {
			seen[o.path] = true
			n++
		}
	}
	return n > 1
}

// opLiterals are the strings a client or router names an operation by: the full path, and for a path with parameters the
// static part before the first one (`'/v1/permissions/' . $id`), when that is distinctive enough to mean this path.
func opLiterals(prefix, p string) []string {
	full := prefix + p
	if prefix == "" && !strings.HasPrefix(p, "/v") && strings.Count(p, "/") < 2 {
		return nil // `/sessions` alone is a router path, a translation key, a URL of another service: too common to join on
	}
	out := []string{full}
	if i := strings.Index(full, "{"); i > 0 {
		static := full[:i]
		if strings.Count(static, "/") >= 2 && len(static) > 6 {
			out = append(out, static)
		}
		out = out[1:] // a path with parameters is never written out in full in code
	}
	return out
}

// linkOpenAPI joins operations to routes and schemas to the types of the same name.
func linkOpenAPI(ctx *Ctx, docs []*openAPIDoc) {
	if len(docs) == 0 {
		return
	}
	g := ctx.G
	routes := map[string][]string{} // verb + normalised path -> route ids
	for id, n := range g.Nodes {
		if n.Type != graph.TypeRoute {
			continue
		}
		if verb, p, ok := strings.Cut(n.Label, " "); ok {
			routes[verb+" "+strings.Join(splitPath(p), "/")] = append(routes[verb+" "+strings.Join(splitPath(p), "/")], id)
		}
	}
	types := map[string][]string{} // short name -> class node ids
	for id, n := range g.Nodes {
		if n.Type == graph.TypeClass {
			types[shortName(id)] = append(types[shortName(id)], id)
		}
	}
	for _, d := range docs {
		for _, o := range d.ops {
			for _, id := range routes[o.verb+" "+strings.Join(splitPath(d.prefix+o.path), "/")] {
				g.AddEdge(o.id, id, graph.MapsTo, 0.95, d.file+":"+itoa(o.line)+" "+o.verb+" "+d.prefix+o.path)
			}
		}
		names := make([]string, 0, len(d.schemas))
		for n := range d.schemas {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			byLang := map[string][]string{}
			for _, id := range types[name] {
				if n := g.Nodes[id]; n != nil && n.Path == d.file {
					continue
				}
				byLang[lang(id)] = append(byLang[lang(id)], id)
			}
			for _, ids := range byLang {
				conf := 0.85
				if len(ids) > 1 {
					if pick := namedAfter(g, ids, d.file); pick != "" {
						ids, conf = []string{pick}, 0.8
					} else {
						conf = 0.6 // several types of this name in one language: which one the schema is, is a guess
					}
				}
				for _, id := range ids {
					g.AddEdge(schemaID(d.file, name), id, graph.MapsTo, conf, d.file+":"+itoa(d.schemas[name])+" schema "+name)
				}
			}
		}
	}
}

// namedAfter picks, among several types of one name, the single one whose path contains the document's name with the
// punctuation removed: adapter-api.json is generated from the types in .../adapterapi/.
func namedAfter(g *graph.Graph, ids []string, docFile string) string {
	stem := normalise(strings.TrimSuffix(filepath.Base(docFile), filepath.Ext(docFile)))
	if len(stem) < 4 {
		return ""
	}
	pick := ""
	for _, id := range ids {
		if n := g.Nodes[id]; n != nil && strings.Contains(normalise(filepath.Dir(n.Path)), stem) {
			if pick != "" {
				return ""
			}
			pick = id
		}
	}
	return pick
}

func normalise(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return -1
	}, s)
}

// shortName is the last segment of a class id: `php:App\Data\Foo` and `go:mod/pkg.Foo` are both Foo.
func shortName(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndex(id, "#"); i >= 0 {
		id = id[i+1:]
	}
	if i := strings.LastIndexAny(id, `\.`); i >= 0 {
		id = id[i+1:]
	}
	return id
}

func lang(id string) string {
	if i := strings.IndexByte(id, ':'); i >= 0 {
		return id[:i]
	}
	return ""
}
