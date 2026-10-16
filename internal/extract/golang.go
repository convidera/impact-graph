package extract

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsgo "github.com/tree-sitter/tree-sitter-go/bindings/go"

	"github.com/convidera/impact/internal/graph"
)

// Types are keys "<import path>.<Name>", "[]<key>" for a slice, or "" when unknown or builtin.

type goFile struct {
	src     *source
	pkg     *goPkg
	imports map[string]string // local name -> import path
	fns     []*goFunc
}

type goPkg struct {
	Path   string
	Name   string
	Dir    string
	Types  map[string]*goType
	Funcs  map[string]*goFunc
	Consts map[string]string
}

type goType struct {
	Key, Name string
	Pkg       *goPkg
	Kind      string // struct, interface, other
	Fields    map[string]goField
	Embedded  []string
	Methods   map[string]*goFunc
	IfaceMeth []string
	File      *goFile
	Line      int
}

type goField struct {
	Type string
	ID   string
}

type goFunc struct {
	ID, Name string
	Recv     string
	Params   []string
	Results  []string
	Body     *ts.Node
	Node     *ts.Node
	File     *goFile
	Line     int
}

type golang struct {
	ctx     *Ctx
	pkgs    map[string]*goPkg
	types   map[string]*goType
	files   []*goFile
	mods    map[string][2]string // dir -> module path, module dir
	implMem map[string][]*goType
}

var goBuiltin = map[string]bool{
	"string": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true, "uint": true, "uint8": true, "uint16": true,
	"uint32": true, "uint64": true, "float32": true, "float64": true, "bool": true, "byte": true, "rune": true, "error": true,
	"any": true, "uintptr": true, "complex64": true, "complex128": true,
}

var goBuiltinFuncs = map[string]bool{
	"len": true, "cap": true, "make": true, "new": true, "append": true, "copy": true, "delete": true, "panic": true, "recover": true,
	"print": true, "println": true, "close": true, "min": true, "max": true, "clear": true,
}

var (
	goModule     = regexp.MustCompile(`(?m)^module\s+(\S+)`)
	majorVersion = regexp.MustCompile(`^v\d+$`)
)

func runGo(ctx *Ctx, files []scanFile) {
	if len(files) == 0 {
		return
	}
	g := &golang{ctx: ctx, pkgs: map[string]*goPkg{}, types: map[string]*goType{}, mods: map[string][2]string{}, implMem: map[string][]*goType{}}
	srcs := parseFiles(files, ctx.Root, ts.NewLanguage(tsgo.Language()))
	defer func() {
		for _, s := range srcs {
			s.close()
		}
	}()
	for _, s := range srcs {
		g.declareFile(s)
	}
	for _, f := range g.files {
		g.resolveImports(f)
	}
	for _, f := range g.files {
		g.analyzeFile(f)
	}
}

// modOf finds the Go module that owns a directory.
func (g *golang) modOf(dir string) (string, string) {
	for d := dir; ; d = path.Dir(d) {
		if m, ok := g.mods[d]; ok {
			return m[0], m[1]
		}
		if b, err := os.ReadFile(filepath.Join(g.ctx.Root, filepath.FromSlash(d), "go.mod")); err == nil {
			if m := goModule.FindSubmatch(b); m != nil {
				g.mods[d] = [2]string{string(m[1]), d}
				return string(m[1]), d
			}
		}
		if d == "." || d == "/" || d == "" {
			break
		}
	}
	g.mods[dir] = [2]string{"", ""}
	return "", ""
}

func (g *golang) importPath(dir string) string {
	mod, moddir := g.modOf(dir)
	if mod == "" {
		return dir
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(dir, moddir), "/")
	if rel == "" || rel == "." {
		return mod
	}
	return mod + "/" + rel
}

func (g *golang) declareFile(s *source) {
	dir := path.Dir(s.file.Path)
	ip := g.importPath(dir)
	pkg := g.pkgs[ip]
	if pkg == nil {
		pkg = &goPkg{Path: ip, Dir: dir, Types: map[string]*goType{}, Funcs: map[string]*goFunc{}, Consts: map[string]string{}}
		g.pkgs[ip] = pkg
	}
	f := &goFile{src: s, pkg: pkg, imports: map[string]string{}}
	g.files = append(g.files, f)
	for _, n := range kids(s.root) {
		switch n.Kind() {
		case "package_clause":
			if pn := firstKid(n, "package_identifier"); pn != nil && pkg.Name == "" {
				pkg.Name = s.text(pn)
			}
		case "import_declaration":
			walk(n, func(x *ts.Node) bool {
				if x.Kind() == "import_spec" {
					p := stringLiteral(s, field(x, "path"))
					name := ""
					if nm := field(x, "name"); nm != nil {
						name = s.text(nm)
					}
					f.imports["\x00"+p] = name // resolved to a local name once every package is known
				}
				return true
			})
		case "type_declaration":
			for _, spec := range kids(n) {
				if spec.Kind() == "type_spec" {
					g.declareType(f, spec)
				}
			}
		case "const_declaration", "var_declaration":
			walk(n, func(x *ts.Node) bool {
				if x.Kind() == "const_spec" || x.Kind() == "var_spec" {
					if nm := field(x, "name"); nm != nil {
						if v := field(x, "value"); v != nil {
							if k := kids(v); len(k) > 0 {
								if val := stringLiteral(s, k[0]); val != "" {
									pkg.Consts[s.text(nm)] = val
								}
							}
						}
					}
				}
				return true
			})
		case "function_declaration":
			g.declareFunc(f, n, "")
		case "method_declaration":
			g.declareFunc(f, n, "m")
		}
	}
}

func (g *golang) resolveImports(f *goFile) {
	resolved := map[string]string{}
	for k, alias := range f.imports {
		if !strings.HasPrefix(k, "\x00") {
			continue
		}
		p := k[1:]
		name := alias
		if name == "" {
			if pk := g.pkgs[p]; pk != nil && pk.Name != "" {
				name = pk.Name
			} else {
				name = path.Base(p)
				if majorVersion.MatchString(name) {
					name = path.Base(path.Dir(p))
				}
			}
		}
		if name != "_" && name != "." {
			resolved[name] = p
		}
	}
	f.imports = resolved
}

// typeKey resolves a type expression of a file to a key.
func (g *golang) typeKey(f *goFile, n *ts.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "type_identifier":
		name := f.src.text(n)
		if goBuiltin[name] {
			return ""
		}
		return f.pkg.Path + "." + name
	case "qualified_type":
		pk := firstKid(n, "package_identifier")
		nm := firstKid(n, "type_identifier")
		if pk == nil || nm == nil {
			return ""
		}
		if ip, ok := f.imports[f.src.text(pk)]; ok {
			return ip + "." + f.src.text(nm)
		}
	case "pointer_type", "parenthesized_type":
		for _, k := range kids(n) {
			return g.typeKey(f, k)
		}
	case "generic_type":
		return g.typeKey(f, field(n, "type"))
	case "slice_type", "array_type":
		if e := g.typeKey(f, field(n, "element")); e != "" {
			return "[]" + e
		}
	}
	return ""
}

func (g *golang) declareType(f *goFile, spec *ts.Node) {
	nm := field(spec, "name")
	if nm == nil {
		return
	}
	name := f.src.text(nm)
	key := f.pkg.Path + "." + name
	t := &goType{Key: key, Name: name, Pkg: f.pkg, Kind: "other", Fields: map[string]goField{}, Methods: map[string]*goFunc{}, File: f, Line: line(spec)}
	f.pkg.Types[name] = t
	g.types[key] = t
	gr := g.ctx.G
	id := "go:" + key
	gr.AddNode(graph.Node{ID: id, Type: graph.TypeClass, Label: name, Path: f.src.file.Path, Line: line(spec),
		Props: map[string]string{"lang": "go", "file_kind": f.src.file.Kind, "end": itoa(endLine(spec)), "pkg": f.pkg.Path}})
	switch ty := field(spec, "type"); {
	case ty != nil && ty.Kind() == "struct_type":
		t.Kind = "struct"
		if fl := firstKid(ty, "field_declaration_list"); fl != nil {
			for _, fd := range kids(fl) {
				if fd.Kind() != "field_declaration" {
					continue
				}
				ftype := g.typeKey(f, field(fd, "type"))
				var names []*ts.Node
				for _, k := range kids(fd) {
					if k.Kind() == "field_identifier" {
						names = append(names, k)
					}
				}
				if len(names) == 0 { // embedded
					if ftype != "" {
						t.Embedded = append(t.Embedded, ftype)
					}
					continue
				}
				for _, nn := range names {
					fname := f.src.text(nn)
					fid := id + "." + fname
					t.Fields[fname] = goField{ftype, fid}
					gr.AddNode(graph.Node{ID: fid, Type: graph.TypeField, Label: name + "." + fname, Path: f.src.file.Path, Line: line(nn),
						Props: map[string]string{"lang": "go", "file_kind": f.src.file.Kind, "type": ftype}})
					gr.AddEdge(fid, id, graph.MemberOf, 1, f.src.file.Path)
				}
			}
		}
	case ty != nil && ty.Kind() == "interface_type":
		t.Kind = "interface"
		for _, k := range kids(ty) {
			switch k.Kind() {
			case "method_elem":
				if mn := field(k, "name"); mn != nil {
					t.IfaceMeth = append(t.IfaceMeth, f.src.text(mn))
					g.declareIfaceMethod(f, t, k, f.src.text(mn))
				}
			case "type_elem", "type_identifier", "qualified_type":
				walk(k, func(x *ts.Node) bool {
					if x.Kind() == "type_identifier" || x.Kind() == "qualified_type" {
						if ek := g.typeKey(f, x); ek != "" {
							t.Embedded = append(t.Embedded, ek)
						}
						return false
					}
					return true
				})
			}
		}
	}
}

func (g *golang) declareIfaceMethod(f *goFile, t *goType, n *ts.Node, name string) {
	fn := &goFunc{ID: "go:" + t.Key + "." + name, Name: name, Recv: t.Key, File: f, Node: n, Line: line(n)}
	fn.Results = g.results(f, field(n, "result"))
	t.Methods[name] = fn
	g.ctx.G.AddNode(graph.Node{ID: fn.ID, Type: graph.TypeMethod, Label: t.Name + "." + name, Path: f.src.file.Path, Line: line(n),
		Props: map[string]string{"lang": "go", "file_kind": f.src.file.Kind, "end": itoa(endLine(n)), "interface": "true"}})
	g.ctx.G.AddEdge(fn.ID, "go:"+t.Key, graph.MemberOf, 1, f.src.file.Path)
}

func (g *golang) results(f *goFile, r *ts.Node) []string {
	if r == nil {
		return nil
	}
	if r.Kind() != "parameter_list" {
		return []string{g.typeKey(f, r)}
	}
	var out []string
	for _, p := range kids(r) {
		if p.Kind() != "parameter_declaration" {
			continue
		}
		n := 0
		for _, k := range kids(p) {
			if k.Kind() == "identifier" {
				n++
			}
		}
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, g.typeKey(f, field(p, "type")))
		}
	}
	return out
}

type goParam struct{ Name, Type string }

func (g *golang) params(f *goFile, pl *ts.Node) []goParam {
	var out []goParam
	if pl == nil {
		return out
	}
	for _, p := range kids(pl) {
		if p.Kind() != "parameter_declaration" && p.Kind() != "variadic_parameter_declaration" {
			continue
		}
		t := g.typeKey(f, field(p, "type"))
		for _, k := range kids(p) {
			if k.Kind() == "identifier" {
				out = append(out, goParam{f.src.text(k), t})
			}
		}
	}
	return out
}

func (g *golang) declareFunc(f *goFile, n *ts.Node, kind string) {
	nm := field(n, "name")
	if nm == nil {
		return
	}
	name := f.src.text(nm)
	fn := &goFunc{Name: name, Node: n, Body: field(n, "body"), File: f, Line: line(n)}
	fn.Results = g.results(f, field(n, "result"))
	for _, p := range g.params(f, field(n, "parameters")) {
		fn.Params = append(fn.Params, p.Type)
	}
	gr := g.ctx.G
	props := map[string]string{"lang": "go", "file_kind": f.src.file.Kind, "end": itoa(endLine(n))}
	if kind == "m" {
		recv := g.params(f, field(n, "receiver"))
		if len(recv) == 0 {
			if rl := field(n, "receiver"); rl != nil {
				for _, p := range kids(rl) {
					recv = append(recv, goParam{"", g.typeKey(f, field(p, "type"))})
				}
			}
		}
		if len(recv) == 0 || recv[0].Type == "" {
			return
		}
		fn.Recv = recv[0].Type
		fn.ID = "go:" + fn.Recv + "." + name
		t := g.types[fn.Recv]
		if t == nil {
			t = &goType{Key: fn.Recv, Name: lastSegment(fn.Recv, "."), Pkg: f.pkg, Kind: "other", Fields: map[string]goField{}, Methods: map[string]*goFunc{}, File: f}
			g.types[fn.Recv] = t
		}
		t.Methods[name] = fn
		f.fns = append(f.fns, fn)
		gr.AddNode(graph.Node{ID: fn.ID, Type: graph.TypeMethod, Label: t.Name + "." + name, Path: f.src.file.Path, Line: fn.Line, Props: props})
		gr.AddEdge(fn.ID, "go:"+fn.Recv, graph.MemberOf, 1, f.src.file.Path)
		return
	}
	fn.ID = "go:" + f.pkg.Path + "." + name
	f.pkg.Funcs[name] = fn
	f.fns = append(f.fns, fn)
	gr.AddNode(graph.Node{ID: fn.ID, Type: graph.TypeFunction, Label: name, Path: f.src.file.Path, Line: fn.Line, Props: props})
}

// ---- hierarchy ----

// methodOf finds a method on a type, searching embedded types.
func (g *golang) methodOf(key, name string) *goFunc {
	seen := map[string]bool{}
	var visit func(string) *goFunc
	visit = func(k string) *goFunc {
		if seen[k] {
			return nil
		}
		seen[k] = true
		t := g.types[k]
		if t == nil {
			return nil
		}
		if m := t.Methods[name]; m != nil {
			return m
		}
		for _, e := range t.Embedded {
			if m := visit(e); m != nil {
				return m
			}
		}
		return nil
	}
	return visit(key)
}

func (g *golang) fieldOf(key, name string) (goField, bool) {
	seen := map[string]bool{}
	var visit func(string) (goField, bool)
	visit = func(k string) (goField, bool) {
		if seen[k] {
			return goField{}, false
		}
		seen[k] = true
		t := g.types[k]
		if t == nil {
			return goField{}, false
		}
		if f, ok := t.Fields[name]; ok {
			return f, true
		}
		for _, e := range t.Embedded {
			if f, ok := visit(e); ok {
				return f, true
			}
		}
		return goField{}, false
	}
	return visit(key)
}

// ifaceMethods is the method name set of an interface including embedded interfaces.
func (g *golang) ifaceMethods(key string) []string {
	seen := map[string]bool{}
	var out []string
	var visit func(string)
	visit = func(k string) {
		if seen[k] {
			return
		}
		seen[k] = true
		t := g.types[k]
		if t == nil {
			return
		}
		out = append(out, t.IfaceMeth...)
		for _, e := range t.Embedded {
			visit(e)
		}
	}
	visit(key)
	return out
}

// implementers lists the project types whose method set covers an interface, a name-based approximation of Go's
// structural typing that is good enough to follow a call through an interface.
func (g *golang) implementers(key string) []*goType {
	if r, ok := g.implMem[key]; ok {
		return r
	}
	meths := g.ifaceMethods(key)
	var out []*goType
	if len(meths) > 0 {
		for _, t := range g.types {
			if t.Kind == "interface" || t.Key == key {
				continue
			}
			ok := true
			for _, m := range meths {
				if g.methodOf(t.Key, m) == nil {
					ok = false
					break
				}
			}
			if ok {
				out = append(out, t)
			}
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	}
	g.implMem[key] = out
	return out
}

// ---- bodies ----

type goScope struct {
	g      *golang
	f      *goFile
	from   string
	vars   map[string]string
	parent *goScope

	lastResults []string
}

func (s *goScope) lookup(n string) (string, bool) {
	for c := s; c != nil; c = c.parent {
		if t, ok := c.vars[n]; ok {
			return t, true
		}
	}
	return "", false
}

func (s *goScope) child() *goScope {
	return &goScope{g: s.g, f: s.f, from: s.from, vars: map[string]string{}, parent: s}
}

func (s *goScope) edge(dst, kind string, conf float64, n *ts.Node, note string) {
	if dst == "" || !s.g.ctx.G.Has(dst) {
		return
	}
	s.g.ctx.G.AddEdge(s.from, dst, kind, conf, s.f.src.evidence(n, note))
}

func (g *golang) analyzeFile(f *goFile) {
	for _, pkgFn := range f.fns {
		if pkgFn.Body == nil {
			continue
		}
		g.analyzeFunc(f, pkgFn)
	}
	// package-level var initialisers and consts are not attributed to a function
}

// analyzeFunc resolves one body; a construct the extractor does not expect must cost one function, not the index.
func (g *golang) analyzeFunc(f *goFile, fn *goFunc) {
	defer func() {
		if r := recover(); r != nil {
			g.ctx.warn("%s: skipped %s: %v", f.src.file.Path, fn.ID, r)
		}
	}()
	s := &goScope{g: g, f: f, from: fn.ID, vars: map[string]string{}}
	n := fn.Node
	if rl := field(n, "receiver"); rl != nil {
		for _, p := range g.params(f, rl) {
			s.vars[p.Name] = p.Type
		}
	}
	for _, p := range g.params(f, field(n, "parameters")) {
		s.vars[p.Name] = p.Type
	}
	s.eval(fn.Body)
}

func (s *goScope) eval(n *ts.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "identifier":
		if t, ok := s.lookup(s.f.src.text(n)); ok {
			return t
		}
		if v, ok := s.f.pkg.Consts[s.f.src.text(n)]; ok {
			s.mention(v, n, 0.85)
		}
		return ""
	case "parenthesized_expression":
		if k := kids(n); len(k) > 0 {
			return s.eval(k[0])
		}
	case "unary_expression":
		return s.eval(field(n, "operand"))
	case "call_expression":
		return s.onCall(n)
	case "selector_expression":
		return s.onSelector(n, false)
	case "composite_literal":
		return s.onComposite(n)
	case "short_var_declaration":
		s.onDeclare(field(n, "left"), field(n, "right"), nil)
		return ""
	case "var_declaration":
		for _, spec := range kids(n) {
			if spec.Kind() != "var_spec" {
				continue
			}
			var names []*ts.Node
			for _, k := range kids(spec) {
				if k.Kind() == "identifier" {
					names = append(names, k)
				}
			}
			ty := s.g.typeKey(s.f, field(spec, "type"))
			if v := field(spec, "value"); v != nil {
				s.onDeclareNames(names, v, ty)
			} else {
				for _, nn := range names {
					s.vars[s.f.src.text(nn)] = ty
				}
			}
		}
		return ""
	case "assignment_statement":
		l, r := field(n, "left"), field(n, "right")
		s.eval(r)
		if l != nil {
			for _, x := range kids(l) {
				if x.Kind() == "selector_expression" {
					s.onSelector(x, true)
				} else {
					s.eval(x)
				}
			}
		}
		return ""
	case "type_assertion_expression":
		s.eval(field(n, "operand"))
		return s.g.typeKey(s.f, field(n, "type"))
	case "index_expression":
		t := s.eval(field(n, "operand"))
		s.eval(field(n, "index"))
		if strings.HasPrefix(t, "[]") {
			return t[2:]
		}
		return ""
	case "func_literal":
		c := s.child()
		for _, p := range s.g.params(s.f, field(n, "parameters")) {
			c.vars[p.Name] = p.Type
		}
		c.eval(field(n, "body"))
		return ""
	case "range_clause", "for_clause":
		return s.onRange(n)
	case "interpreted_string_literal", "raw_string_literal":
		if v := stringLiteral(s.f.src, n); v != "" {
			s.mention(v, n, 0.9)
		}
		return ""
	case "for_statement":
		if rc := firstKid(n, "range_clause"); rc != nil {
			c := s.child()
			c.onRange(rc)
			for _, k := range kids(n) {
				if k.Kind() != "range_clause" {
					c.eval(k)
				}
			}
			return ""
		}
	}
	for _, k := range kids(n) {
		s.eval(k)
	}
	return ""
}

func (s *goScope) onRange(n *ts.Node) string {
	if n.Kind() != "range_clause" {
		for _, k := range kids(n) {
			s.eval(k)
		}
		return ""
	}
	t := s.eval(field(n, "right"))
	if l := field(n, "left"); l != nil {
		names := kids(l)
		if len(names) == 2 && strings.HasPrefix(t, "[]") {
			s.vars[s.f.src.text(names[1])] = t[2:]
		}
		for i, nn := range names {
			if nn.Kind() == "identifier" && (i != 1 || !strings.HasPrefix(t, "[]")) {
				s.vars[s.f.src.text(nn)] = ""
			}
		}
	}
	return ""
}

func (s *goScope) mention(v string, n *ts.Node, conf float64) {
	s.g.ctx.Vocab.mention(s.g.ctx, s.from, s.f.src.file.Path, v, s.f.src.evidence(n, "literal "+v), conf)
}

func (s *goScope) onDeclare(left, right *ts.Node, _ []string) {
	if left == nil || right == nil {
		return
	}
	s.onDeclareNames(kids(left), right, "")
}

// onDeclareNames types the variables of `a, b := f()` or `var a T = x`.
func (s *goScope) onDeclareNames(names []*ts.Node, right *ts.Node, declared string) {
	rs := kids(right)
	if right.Kind() != "expression_list" {
		rs = []*ts.Node{right}
	}
	if len(rs) == 1 && len(names) > 1 {
		res := s.evalMulti(rs[0])
		for i, nn := range names {
			t := ""
			if i < len(res) {
				t = res[i]
			}
			s.set(nn, t)
		}
		return
	}
	for i, r := range rs {
		t := s.eval(r)
		if declared != "" {
			t = declared
		}
		if i < len(names) {
			s.set(names[i], t)
		}
	}
}

func (s *goScope) set(n *ts.Node, t string) {
	if n.Kind() == "identifier" {
		s.vars[s.f.src.text(n)] = t
	}
}

// evalMulti evaluates a call and returns the types of all its results.
func (s *goScope) evalMulti(n *ts.Node) []string {
	if n.Kind() == "call_expression" {
		s.lastResults = nil
		t := s.onCall(n)
		if s.lastResults != nil {
			return s.lastResults
		}
		return []string{t}
	}
	return []string{s.eval(n)}
}

func (s *goScope) onComposite(n *ts.Node) string {
	ty := s.g.typeKey(s.f, field(n, "type"))
	if t := s.g.types[strings.TrimPrefix(ty, "[]")]; t != nil {
		s.edge("go:"+t.Key, graph.Uses, 1, n, "")
	}
	s.literalBody(ty, field(n, "body"))
	return ty
}

// literalBody walks the body of a composite literal of type ty. In a slice literal an element may omit its type
// (`[]Child{{Cost: 1}}`), which is then the element type of the slice.
func (s *goScope) literalBody(ty string, body *ts.Node) {
	if body == nil {
		return
	}
	for _, el := range kids(body) {
		switch el.Kind() {
		case "keyed_element":
			k := kids(el)
			if len(k) != 2 {
				continue
			}
			if ty != "" && !strings.HasPrefix(ty, "[]") {
				if f, ok := s.g.fieldOf(ty, s.f.src.text(k[0])); ok {
					s.edge(f.ID, graph.Writes, 0.95, el, "field of "+lastSegment(ty, ".")+" set in a literal")
				}
			}
			s.evalElement(k[0], "") // a string key of a map literal is a value like any other (`"prompt": spec.Prompt`)
			s.evalElement(k[1], fieldType(s.g, ty, s.f.src.text(k[0])))
		case "literal_element":
			s.evalElement(el, strings.TrimPrefix(ty, "[]"))
		default:
			s.eval(el)
		}
	}
}

func fieldType(g *golang, ty, name string) string {
	if ty == "" || strings.HasPrefix(ty, "[]") {
		return ""
	}
	if f, ok := g.fieldOf(ty, name); ok {
		return f.Type
	}
	return ""
}

// evalElement evaluates a literal element; a bare `{...}` takes the expected type.
func (s *goScope) evalElement(n *ts.Node, expected string) {
	if n.Kind() == "literal_element" {
		if k := kids(n); len(k) == 1 {
			n = k[0]
		}
	}
	if n.Kind() == "literal_value" {
		if t := s.g.types[strings.TrimPrefix(expected, "[]")]; t != nil {
			s.edge("go:"+t.Key, graph.Uses, 1, n, "")
		}
		s.literalBody(expected, n)
		return
	}
	s.eval(n)
}

func (s *goScope) onSelector(n *ts.Node, write bool) string {
	op, fld := field(n, "operand"), field(n, "field")
	if op == nil || fld == nil {
		return ""
	}
	name := s.f.src.text(fld)
	// pkg.Const
	if op.Kind() == "identifier" {
		if _, local := s.lookup(s.f.src.text(op)); !local {
			if ip, ok := s.f.imports[s.f.src.text(op)]; ok {
				if pk := s.g.pkgs[ip]; pk != nil {
					if v, ok := pk.Consts[name]; ok {
						s.mention(v, n, 0.85)
					}
				}
				return ""
			}
		}
	}
	ot := s.eval(op)
	if ot == "" || strings.HasPrefix(ot, "[]") {
		return ""
	}
	if f, ok := s.g.fieldOf(ot, name); ok {
		kind := graph.Reads
		if write {
			kind = graph.Writes
		}
		s.edge(f.ID, kind, 0.9, n, "field of "+lastSegment(ot, "."))
		return f.Type
	}
	return ""
}

func (s *goScope) onCall(n *ts.Node) string {
	fn, args := field(n, "function"), field(n, "arguments")
	if args != nil {
		defer func() {
			for _, a := range kids(args) {
				s.eval(a)
			}
		}()
	}
	if fn == nil {
		return ""
	}
	switch fn.Kind() {
	case "identifier":
		name := s.f.src.text(fn)
		if _, local := s.lookup(name); local || goBuiltinFuncs[name] {
			return ""
		}
		if f := s.f.pkg.Funcs[name]; f != nil {
			s.edge(f.ID, graph.Calls, 1, n, "")
			return s.results(f)
		}
		if t := s.f.pkg.Types[name]; t != nil { // conversion T(x)
			return t.Key
		}
	case "selector_expression":
		op, fld := field(fn, "operand"), field(fn, "field")
		if op == nil || fld == nil {
			return ""
		}
		name := s.f.src.text(fld)
		if op.Kind() == "identifier" {
			if _, local := s.lookup(s.f.src.text(op)); !local {
				if ip, ok := s.f.imports[s.f.src.text(op)]; ok {
					if pk := s.g.pkgs[ip]; pk != nil {
						if f := pk.Funcs[name]; f != nil {
							s.edge(f.ID, graph.Calls, 1, n, "")
							return s.results(f)
						}
						if t := pk.Types[name]; t != nil {
							return t.Key
						}
					}
					return ""
				}
			}
		}
		ot := s.eval(op)
		if ot == "" || strings.HasPrefix(ot, "[]") {
			return ""
		}
		t := s.g.types[ot]
		if t == nil {
			return ""
		}
		m := s.g.methodOf(ot, name)
		if m == nil {
			return ""
		}
		s.edge(m.ID, graph.Calls, 0.95, n, "")
		if owner := s.g.types[m.Recv]; owner != nil && owner.Kind == "interface" {
			for _, impl := range s.g.implementers(owner.Key) {
				if im := s.g.methodOf(impl.Key, name); im != nil && im.Body != nil {
					s.edge(im.ID, graph.Calls, 0.8, n, "implementation of "+owner.Name)
				}
			}
		}
		return s.results(m)
	case "func_literal":
		s.eval(fn)
	}
	return ""
}

func (s *goScope) results(f *goFunc) string {
	s.lastResults = f.Results
	if len(f.Results) > 0 {
		return f.Results[0]
	}
	return ""
}
