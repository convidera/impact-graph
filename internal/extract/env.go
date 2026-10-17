package extract

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"
	tstypescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	"gopkg.in/yaml.v3"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/scan"
)

// joinConfidence is the confidence of an edge made by a shared name. A shared name is evidence of a link, not proof of one,
// so it is below the 0.7 that walks use by default: query with --min-confidence 0.6 to follow these edges.
const joinConfidence = 0.6

var vueEnv = regexp.MustCompile(`\b(?:import\.meta|process)\.env\.([A-Za-z_][A-Za-z0-9_]*)`)

// runEnv joins the repositories through the names of environment variables and images (joins.go). A compose or Kubernetes
// manifest and a Helm values file set a variable or deploy an image, and a GitHub workflow pushes an image: those are the
// producing sides. The code reads a variable and a dotenv file expects it: those are the consuming sides. Each name is one value node that every repository sharing it reaches, so graph.Merge joins them.
// A read is attributed to the function that makes it, else to its file.
func runEnv(ctx *Ctx) {
	e := &envScan{ctx: ctx, fns: functionsByLine(ctx.G)}
	for _, f := range ctx.Files {
		if f.Kind == "doc" {
			continue
		}
		switch {
		case scan.IsDotenv(f.Path):
			e.dotenv(f.Path)
		case f.Ext == ".yaml" || f.Ext == ".yml":
			if strings.HasPrefix(f.Path, ".github/workflows/") {
				e.workflow(f.Path)
			} else {
				e.manifest(f.Path)
			}
		}
	}
	e.code(ctx.Files)
}

type envScan struct {
	ctx *Ctx
	fns map[string]string // path and line of a function or method -> its node id
}

// functionsByLine indexes the function and method nodes by the file and line they start on. A module node is a function
// of the file that starts on line 1 too, so it is left out: a read at file level belongs to the file.
func functionsByLine(g *graph.Graph) map[string]string {
	out := map[string]string{}
	for id, n := range g.Nodes {
		if (n.Type == graph.TypeFunction || n.Type == graph.TypeMethod) && n.Path != "" && n.Props["kind"] != "module" {
			k := fnKey(n.Path, n.Line)
			if prev, ok := out[k]; !ok || id < prev {
				out[k] = id
			}
		}
	}
	return out
}

func fnKey(path string, line int) string { return path + "\x00" + strconv.Itoa(line) }

// link adds a value node and an edge from `from` to it, with its place in the source as evidence.
func (e *envScan) link(kind, from, id, file string, line int, note string) {
	e.ctx.G.AddNode(graph.Node{ID: id, Type: graph.TypeValue, Label: id})
	e.ctx.G.AddEdgeProv(from, id, kind, joinConfidence, fmt.Sprintf("%s:%d %s", file, line, note), graph.ProvConvention)
}

// defineEnv records that a deployment definition sets an environment variable: the producing side of the name.
func (e *envScan) defineEnv(file string, line int, name string) {
	if joinableEnv(name) {
		e.link(graph.Produces, fileID(file), "env:"+name, file, line, "env "+name)
	}
}

// expectEnv records that a file expects an environment variable, as a dotenv key does: the consuming side of the name.
// A .env.example says what the application reads, it does not set the value of a running service.
func (e *envScan) expectEnv(file string, line int, name string) {
	if joinableEnv(name) {
		e.link(graph.Consumes, fileID(file), "env:"+name, file, line, "env "+name)
	}
}

// joinImage links a file to the image repository a reference names.
func (e *envScan) joinImage(kind, from, file string, line int, ref string) {
	if repo := imageRepository(ref); repo != "" {
		e.link(kind, from, "image:"+repo, file, line, "image "+repo)
	}
}

// readFile returns the content of a repository file, or false when it is missing or larger than a values file may be.
func (e *envScan) readFile(file string) ([]byte, bool) {
	b, err := os.ReadFile(filepath.Join(e.ctx.Root, file))
	if err != nil || len(b) > maxYAMLBytes {
		return nil, false
	}
	return b, true
}

// dotenv reads the keys of a dotenv file. The values are not read into the graph.
func (e *envScan) dotenv(file string) {
	b, ok := e.readFile(file)
	if !ok {
		return
	}
	for i, raw := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(raw)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
		if key, _, ok := strings.Cut(t, "="); ok {
			e.expectEnv(file, i+1, strings.TrimSpace(key))
		}
	}
}

// manifest reads the environment variables and images of a compose, Kubernetes or Helm YAML file: `environment:` maps and
// lists, `env:` lists of `- name:` items, and `image:` values (a string, or a Helm `repository:`).
func (e *envScan) manifest(file string) {
	b, ok := e.readFile(file)
	if !ok {
		return
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var doc yaml.Node
		if dec.Decode(&doc) != nil {
			return
		}
		e.manifestNode(file, &doc, 0)
	}
}

func (e *envScan) manifestNode(file string, n *yaml.Node, depth int) {
	if depth > maxYAMLDepth {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			e.manifestNode(file, c, depth+1)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			switch {
			case k.Value == "environment" && v.Kind == yaml.MappingNode:
				for j := 0; j+1 < len(v.Content); j += 2 {
					e.defineEnv(file, v.Content[j].Line, v.Content[j].Value)
				}
			case k.Value == "environment" && v.Kind == yaml.SequenceNode:
				for _, item := range v.Content {
					name, _, _ := strings.Cut(item.Value, "=")
					e.defineEnv(file, item.Line, strings.TrimSpace(name))
				}
			case k.Value == "env" && v.Kind == yaml.SequenceNode:
				for _, item := range v.Content {
					if item.Kind == yaml.MappingNode {
						if name := yamlValue(item, "name"); name != nil {
							e.defineEnv(file, name.Line, name.Value)
						}
					}
				}
			case k.Value == "image":
				ref := v
				if v.Kind == yaml.MappingNode {
					ref = yamlValue(v, "repository")
				}
				if ref != nil && ref.Kind == yaml.ScalarNode {
					e.joinImage(graph.Consumes, fileID(file), file, ref.Line, ref.Value)
				}
			}
			e.manifestNode(file, v, depth+1)
		}
	}
}

// yamlValue returns the value of a key of a mapping node, or nil.
func yamlValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// workflow reads the images a GitHub workflow pushes: the `images:` and `tags:` inputs of a docker action, and the
// `docker push` and `docker build -t` commands of its `run:` steps.
func (e *envScan) workflow(file string) {
	b, ok := e.readFile(file)
	if !ok {
		return
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	for {
		var doc yaml.Node
		if dec.Decode(&doc) != nil {
			return
		}
		e.workflowNode(file, &doc, 0)
	}
}

func (e *envScan) workflowNode(file string, n *yaml.Node, depth int) {
	if depth > maxYAMLDepth {
		return
	}
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			e.workflowNode(file, c, depth+1)
		}
	case yaml.MappingNode:
		if uses := yamlValue(n, "uses"); uses != nil && strings.HasPrefix(uses.Value, "docker/") {
			if with := yamlValue(n, "with"); with != nil && with.Kind == yaml.MappingNode {
				for _, key := range []string{"images", "tags"} {
					if v := yamlValue(with, key); v != nil && v.Kind == yaml.ScalarNode {
						for _, ref := range strings.FieldsFunc(v.Value, func(r rune) bool { return r == '\n' || r == ',' }) {
							e.joinImage(graph.Produces, fileID(file), file, v.Line, ref)
						}
					}
				}
			}
		}
		if run := yamlValue(n, "run"); run != nil && run.Kind == yaml.ScalarNode {
			for _, ref := range dockerImages(run.Value) {
				e.joinImage(graph.Produces, fileID(file), file, run.Line, ref)
			}
		}
		for i := 1; i < len(n.Content); i += 2 {
			e.workflowNode(file, n.Content[i], depth+1)
		}
	}
}

// dockerImages returns the image names of the docker push and docker build -t commands in a shell script. A continued line
// is read as one line; a variable in a name is left for imageRepository to reject.
func dockerImages(script string) []string {
	script = strings.ReplaceAll(script, "\\\n", " ")
	var out []string
	for _, line := range strings.Split(script, "\n") {
		f := strings.Fields(line)
		if !containsToken(f, "docker") {
			continue
		}
		building := containsToken(f, "build")
		for i := range f {
			if i+1 >= len(f) {
				break
			}
			pushing := i > 0 && f[i-1] == "docker" && f[i] == "push"
			tagging := building && (f[i] == "-t" || f[i] == "--tag")
			if pushing || tagging {
				out = append(out, strings.Trim(f[i+1], `"'`))
			}
		}
	}
	return out
}

func containsToken(fields []string, want string) bool {
	for _, f := range fields {
		if f == want {
			return true
		}
	}
	return false
}

// codeReader says how one language reads an environment variable: the files to parse, the grammar, the call or access
// that reads a name, and the kinds of the nodes that declare a function.
type codeReader struct {
	lang    *ts.Language
	exts    []string
	needles []string
	name    func(*source, *ts.Node) string
	decl    map[string]bool
}

func codeReaders() []codeReader {
	return []codeReader{
		{
			lang:    phpLanguage(),
			exts:    []string{".php"},
			needles: []string{"env(", "getenv("},
			name:    phpEnvName,
			decl:    map[string]bool{"method_declaration": true, "function_definition": true},
		},
		{
			lang:    ts.NewLanguage(tsgo.Language()),
			exts:    []string{".go"},
			needles: []string{"Getenv(", "LookupEnv("},
			name:    goEnvName,
			decl:    map[string]bool{"function_declaration": true, "method_declaration": true},
		},
		{
			lang:    ts.NewLanguage(tstypescript.LanguageTypescript()),
			exts:    []string{".ts", ".mts"},
			needles: []string{"process.env.", "import.meta.env."},
			name:    tsEnvName,
			decl:    map[string]bool{"function_declaration": true, "method_definition": true, "arrow_function": true, "function_expression": true, "generator_function_declaration": true},
		},
		{
			lang:    ts.NewLanguage(tstypescript.LanguageTSX()),
			exts:    []string{".tsx", ".jsx", ".js", ".mjs"},
			needles: []string{"process.env.", "import.meta.env."},
			name:    tsEnvName,
			decl:    map[string]bool{"function_declaration": true, "method_definition": true, "arrow_function": true, "function_expression": true, "generator_function_declaration": true},
		},
		{
			lang:    ts.NewLanguage(tspython.Language()),
			exts:    []string{".py"},
			needles: []string{"environ", "getenv("},
			name:    pyEnvName,
			decl:    map[string]bool{"function_definition": true},
		},
	}
}

// code finds the reads of environment variables in the source files and links each to its enclosing function.
func (e *envScan) code(files []scan.File) {
	for _, r := range codeReaders() {
		var mine []scan.File
		for _, f := range files {
			if f.Kind != "doc" && containsExt(r.exts, f.Ext) && e.mentions(f.Path, r.needles) {
				mine = append(mine, f)
			}
		}
		for _, s := range parseFiles(mine, e.ctx.Root, r.lang) {
			walkNodes(s.root, func(n *ts.Node) {
				if name := r.name(s, n); joinableEnv(name) {
					e.link(graph.Consumes, e.enclosing(s, n, r.decl), "env:"+name, s.file.Path, line(n), "env "+name)
				}
			})
			s.close()
		}
	}
	for _, f := range files {
		if f.Ext == ".vue" && f.Kind != "doc" {
			e.vue(f.Path)
		}
	}
}

// vue reads the variables of a single-file component with a pattern: its script is not parsed as a tree, so the read is
// attributed to the file.
func (e *envScan) vue(file string) {
	b, ok := e.readFile(file)
	if !ok {
		return
	}
	for _, m := range vueEnv.FindAllSubmatchIndex(b, -1) {
		if name := string(b[m[2]:m[3]]); joinableEnv(name) {
			e.link(graph.Consumes, fileID(file), "env:"+name, file, 1+bytes.Count(b[:m[0]], []byte("\n")), "env "+name)
		}
	}
}

// mentions reports whether a file contains one of the needles, so only the files that may read a variable are parsed.
func (e *envScan) mentions(file string, needles []string) bool {
	b, ok := e.readFile(file)
	if !ok {
		return false
	}
	for _, n := range needles {
		if bytes.Contains(b, []byte(n)) {
			return true
		}
	}
	return false
}

func containsExt(exts []string, ext string) bool {
	for _, x := range exts {
		if x == ext {
			return true
		}
	}
	return false
}

// enclosing returns the function or method a node is in: the nearest declaration above it that the graph has a node for,
// else the file.
func (e *envScan) enclosing(s *source, n *ts.Node, decl map[string]bool) string {
	for p := n.Parent(); p != nil; p = p.Parent() {
		if decl[p.Kind()] {
			if id := e.fns[fnKey(s.file.Path, line(p))]; id != "" {
				return id
			}
		}
	}
	return fileID(s.file.Path)
}

// walkNodes calls visit on every named node below n, n included.
func walkNodes(n *ts.Node, visit func(*ts.Node)) {
	visit(n)
	for _, k := range kids(n) {
		walkNodes(k, visit)
	}
}

// phpEnvName is the name of `env('NAME', ...)` or `getenv('NAME')`.
func phpEnvName(s *source, n *ts.Node) string {
	if n.Kind() != "function_call_expression" {
		return ""
	}
	fn := field(n, "function")
	if fn == nil || (fn.Kind() != "name" && fn.Kind() != "qualified_name") {
		return ""
	}
	if name := strings.TrimPrefix(s.text(fn), "\\"); name != "env" && name != "getenv" {
		return ""
	}
	args := argsOf(field(n, "arguments"))
	if len(args) == 0 {
		return ""
	}
	return stringLiteral(s, args[0])
}

// goEnvName is the name of `os.Getenv("NAME")` or `os.LookupEnv("NAME")`.
func goEnvName(s *source, n *ts.Node) string {
	if n.Kind() != "call_expression" {
		return ""
	}
	fn := field(n, "function")
	if fn == nil || fn.Kind() != "selector_expression" {
		return ""
	}
	if t := s.text(fn); t != "os.Getenv" && t != "os.LookupEnv" {
		return ""
	}
	args := kids(field(n, "arguments"))
	if len(args) == 0 {
		return ""
	}
	return stringLiteral(s, args[0])
}

// tsEnvName is the name of `process.env.NAME` or `import.meta.env.NAME`.
func tsEnvName(s *source, n *ts.Node) string {
	if n.Kind() != "member_expression" {
		return ""
	}
	switch s.text(field(n, "object")) {
	case "process.env", "import.meta.env":
	default:
		return ""
	}
	if prop := field(n, "property"); prop != nil && prop.Kind() == "property_identifier" {
		return s.text(prop)
	}
	return ""
}

// pyEnvName is the name of `os.environ["NAME"]`, `os.environ.get("NAME")` or `os.getenv("NAME")`.
func pyEnvName(s *source, n *ts.Node) string {
	switch n.Kind() {
	case "subscript":
		if s.text(field(n, "value")) == "os.environ" {
			return pyString(s, field(n, "subscript"))
		}
	case "call":
		switch s.text(field(n, "function")) {
		case "os.environ.get", "os.getenv":
			if args := kids(field(n, "arguments")); len(args) > 0 {
				return pyString(s, args[0])
			}
		}
	}
	return ""
}

// pyString returns the value of a plain (not f- or raw) Python string literal, or "".
func pyString(s *source, n *ts.Node) string {
	if n == nil || n.Kind() != "string" {
		return ""
	}
	t := s.text(n)
	if len(t) < 2 || (t[0] != '"' && t[0] != '\'') || strings.HasPrefix(t, `"""`) || strings.HasPrefix(t, `'''`) {
		return ""
	}
	return t[1 : len(t)-1]
}
