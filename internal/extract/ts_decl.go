package extract

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	ts "github.com/tree-sitter/go-tree-sitter"
	tstypescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"

	"github.com/convidera/impact-graph/internal/graph"
)

// TypeScript, TSX, JavaScript and Vue single-file components. Symbols are functions at any nesting depth (a composable and
// the handlers inside it), classes, object-literal members, types and one symbol per module or component. Calls are
// resolved through imports (relative paths and the `@/` aliases of tsconfig), nested scopes, `new`, `this`, typed
// parameters and the members a function returns, so `const { start } = useStartSession(); start()` and
// `useSessionApi().sessionsIndex()` reach the function that is called.

type tsImport struct {
	Spec   string
	Target string // module path in the repository, "" for a package
	Name   string // "default", "*" or the exported name
}

type tsSym struct {
	ID, Name, Kind string // kinds: module, component, function, method, class, type, object, var, namespace, result
	Mod            *tsMod
	Node, Body     *ts.Node
	Params         *ts.Node
	Parent         *tsSym
	Local          map[string]*tsSym
	Members        map[string]*tsSym
	Aliases        map[string]string // object members that name another symbol (`{ start }`)
	FieldTypes     map[string]string // class field name -> type name
	Extends        string
	Init           *ts.Node // initialiser of a module level variable
	Key            string   // for `const { key } = init`: the member of the initialiser this variable is
	result         *tsSym
	resultDone     bool
	resultBusy     bool
}

type tsMod struct {
	Path     string
	Src      *source
	IsVue    bool
	Imports  map[string]tsImport
	Top      map[string]*tsSym
	Exports  map[string]*tsSym
	ReNamed  map[string]tsImport
	ReStar   []tsImport
	Default  *tsSym
	Module   *tsSym
	Template string
	TplOff   int
	ns       *tsSym
}

type tsAlias struct{ prefix, dir string }

type tsx struct {
	ctx     *Ctx
	mods    map[string]*tsMod
	order   []*tsMod
	routes  *routeIndex
	aliases map[string][]tsAlias
	paths   map[string]bool
}

var (
	tsconfigPaths = regexp.MustCompile(`"([^"*]+)/\*"\s*:\s*\[\s*"([^"*]*)/?\*"`)
	tsconfigBase  = regexp.MustCompile(`"baseUrl"\s*:\s*"([^"]*)"`)
	vueScript     = regexp.MustCompile(`(?ms)^<script([^>]*)>(.*?)^</script>`)
	vueTemplate   = regexp.MustCompile(`(?ms)^<template([^>]*)>(.*)^</template>`)
	tsExts        = []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".vue", "/index.ts", "/index.tsx", "/index.js", "/index.vue"}
)

func tsLanguage(ext string) *ts.Language {
	if ext == ".tsx" || ext == ".jsx" || ext == ".js" || ext == ".mjs" {
		return ts.NewLanguage(tstypescript.LanguageTSX())
	}
	return ts.NewLanguage(tstypescript.LanguageTypescript())
}

// loadTS reads and parses the files. A Vue file is parsed with its <script> blocks kept in place and everything else
// blanked out, so line numbers and byte offsets still match the file.
func (x *tsx) load(files []scanFile) {
	out := make([]*tsMod, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, f := range files {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, f scanFile) {
			defer wg.Done()
			defer func() { <-sem }()
			b, err := os.ReadFile(filepath.Join(x.ctx.Root, f.Path))
			if err != nil {
				return
			}
			m := &tsMod{Path: f.Path, IsVue: f.Ext == ".vue"}
			if m.IsVue {
				b, m.Template, m.TplOff = splitVue(b)
			}
			p := ts.NewParser()
			defer p.Close()
			if err := p.SetLanguage(tsLanguage(f.Ext)); err != nil {
				return
			}
			t := p.Parse(b, nil)
			m.Src = &source{file: f, src: b, tree: t, root: t.RootNode()}
			out[i] = m
		}(i, f)
	}
	wg.Wait()
	for _, m := range out {
		if m != nil {
			x.mods[m.Path] = m
			x.order = append(x.order, m)
			x.paths[m.Path] = true
		}
	}
}

// splitVue keeps the <script> blocks of a single-file component, blanks the rest and returns the template.
func splitVue(b []byte) (script []byte, template string, off int) {
	script = make([]byte, len(b))
	for i, c := range b {
		if c == '\n' {
			script[i] = '\n'
		} else {
			script[i] = ' '
		}
	}
	for _, m := range vueScript.FindAllSubmatchIndex(b, -1) {
		copy(script[m[4]:m[5]], b[m[4]:m[5]])
	}
	if m := vueTemplate.FindSubmatchIndex(b); m != nil {
		return script, string(b[m[4]:m[5]]), m[4]
	}
	return script, "", 0
}

func (x *tsx) newSym(m *tsMod, parent *tsSym, name, kind string, node, body *ts.Node) *tsSym {
	id := "ts:" + m.Path + "#" + name
	if parent != nil && parent.Kind != "module" && parent.Kind != "component" {
		id = parent.ID + "." + name
	}
	s := &tsSym{ID: id, Name: name, Kind: kind, Mod: m, Node: node, Body: body, Parent: parent, Local: map[string]*tsSym{}, Members: map[string]*tsSym{}}
	if node != nil {
		s.Params = field(node, "parameters")
	}
	typ := graph.TypeFunction
	label := name
	switch kind {
	case "class", "type", "object", "component":
		typ = graph.TypeClass
	case "method":
		typ = graph.TypeMethod
	}
	if parent != nil {
		label = parent.Name + "." + name
		if parent.Kind == "module" || parent.Kind == "component" {
			label = name
		}
	}
	if kind == "method" && parent != nil {
		label = parent.Name + "." + name
	}
	line1, end := 0, 0
	if node != nil {
		line1, end = line(node), endLine(node)
	}
	x.ctx.G.AddNode(graph.Node{
		ID: id, Type: typ, Label: label, Path: m.Path, Line: line1,
		Props: map[string]string{"lang": "ts", "kind": kind, "file_kind": m.Src.file.Kind, "end": itoa(end)},
	})
	if parent != nil && (parent.Kind == "class" || parent.Kind == "object") {
		x.ctx.G.AddEdge(id, parent.ID, graph.MemberOf, 1, m.Path)
	}
	return s
}

// ---- declarations ----

func (x *tsx) declare(m *tsMod) {
	m.Imports, m.Top, m.Exports, m.ReNamed = map[string]tsImport{}, map[string]*tsSym{}, map[string]*tsSym{}, map[string]tsImport{}
	stem := strings.TrimSuffix(path.Base(m.Path), path.Ext(m.Path))
	if m.IsVue {
		m.Module = x.newSym(m, nil, stem, "component", nil, nil)
		m.Module.ID = "ts:" + m.Path
		m.Default = m.Module
		x.ctx.G.AddNode(graph.Node{ID: m.Module.ID, Type: graph.TypeClass, Label: stem, Path: m.Path, Line: 1,
			Props: map[string]string{"lang": "ts", "kind": "component", "file_kind": m.Src.file.Kind, "end": itoa(endLine(m.Src.root))}})
	} else {
		m.Module = &tsSym{ID: "ts:" + m.Path, Name: path.Base(m.Path), Kind: "module", Mod: m, Local: map[string]*tsSym{}, Members: map[string]*tsSym{}}
		x.ctx.G.AddNode(graph.Node{ID: m.Module.ID, Type: graph.TypeFunction, Label: m.Path, Path: m.Path, Line: 1,
			Props: map[string]string{"lang": "ts", "kind": "module", "file_kind": m.Src.file.Kind, "end": itoa(endLine(m.Src.root))}})
	}
	m.Module.Body = m.Src.root
	for _, st := range kids(m.Src.root) {
		x.declareStmt(m, st, false)
	}
}

func (x *tsx) declareStmt(m *tsMod, st *ts.Node, exported bool) {
	switch st.Kind() {
	case "import_statement":
		x.declareImport(m, st)
	case "export_statement":
		x.declareExport(m, st)
	default:
		for _, s := range x.declareDecl(m, nil, st) {
			m.Top[s.Name] = s
			if exported {
				m.Exports[s.Name] = s
			}
		}
	}
}

func (x *tsx) declareImport(m *tsMod, st *ts.Node) {
	src := stringLiteralTS(m.Src, field(st, "source"))
	target := x.resolveSpec(m.Path, src)
	clause := firstKid(st, "import_clause")
	if clause == nil {
		return
	}
	for _, k := range kids(clause) {
		switch k.Kind() {
		case "identifier":
			m.Imports[m.Src.text(k)] = tsImport{src, target, "default"}
		case "namespace_import":
			if id := firstKid(k, "identifier"); id != nil {
				m.Imports[m.Src.text(id)] = tsImport{src, target, "*"}
			}
		case "named_imports":
			for _, sp := range kids(k) {
				if sp.Kind() != "import_specifier" {
					continue
				}
				name := m.Src.text(field(sp, "name"))
				local := name
				if a := field(sp, "alias"); a != nil {
					local = m.Src.text(a)
				}
				m.Imports[local] = tsImport{src, target, name}
			}
		}
	}
}

func (x *tsx) declareExport(m *tsMod, st *ts.Node) {
	isDefault := false
	for _, c := range allChildren(st) {
		if c.Kind() == "default" {
			isDefault = true
		}
	}
	if src := field(st, "source"); src != nil { // re-exports
		spec := stringLiteralTS(m.Src, src)
		target := x.resolveSpec(m.Path, spec)
		clause := firstKid(st, "export_clause")
		if clause == nil {
			m.ReStar = append(m.ReStar, tsImport{spec, target, "*"})
			return
		}
		for _, sp := range kids(clause) {
			name := m.Src.text(field(sp, "name"))
			out := name
			if a := field(sp, "alias"); a != nil {
				out = m.Src.text(a)
			}
			m.ReNamed[out] = tsImport{spec, target, name}
		}
		return
	}
	if clause := firstKid(st, "export_clause"); clause != nil { // export { a, b as c }
		for _, sp := range kids(clause) {
			name := m.Src.text(field(sp, "name"))
			out := name
			if a := field(sp, "alias"); a != nil {
				out = m.Src.text(a)
			}
			m.ReNamed["\x00local:"+out] = tsImport{"", "", name}
		}
		return
	}
	if d := field(st, "declaration"); d != nil {
		for _, s := range x.declareDecl(m, nil, d) {
			m.Top[s.Name] = s
			m.Exports[s.Name] = s
			if isDefault {
				m.Default = s
			}
		}
		return
	}
	if v := field(st, "value"); v != nil && isDefault {
		switch v.Kind() {
		case "object":
			s := x.newSym(m, nil, "default", "object", v, nil)
			x.declareObject(m, s, v)
			m.Default = s
			m.Exports["default"] = s
		case "arrow_function", "function_expression", "function":
			s := x.newSym(m, nil, "default", "function", v, field(v, "body"))
			x.declareNested(m, s)
			m.Default = s
			m.Exports["default"] = s
		case "identifier":
			m.ReNamed["\x00default"] = tsImport{"", "", m.Src.text(v)}
		case "call_expression":
			s := x.newSym(m, nil, "default", "function", v, lastFunctionArg(v))
			x.declareNested(m, s)
			m.Default = s
			m.Exports["default"] = s
		}
	}
}

func allChildren(n *ts.Node) []*ts.Node {
	out := make([]*ts.Node, 0, n.ChildCount())
	for i := uint(0); i < n.ChildCount(); i++ {
		out = append(out, n.Child(i))
	}
	return out
}

func lastFunctionArg(call *ts.Node) *ts.Node {
	args := field(call, "arguments")
	if args == nil {
		return nil
	}
	k := kids(args)
	for i := len(k) - 1; i >= 0; i-- {
		if k[i].Kind() == "arrow_function" || k[i].Kind() == "function_expression" || k[i].Kind() == "function" {
			return k[i]
		}
	}
	return nil
}

func isFunctionNode(n *ts.Node) bool {
	switch n.Kind() {
	case "arrow_function", "function_expression", "function", "generator_function":
		return true
	}
	return false
}

// declareDecl creates the symbols of one declaration; parent is nil at module level.
func (x *tsx) declareDecl(m *tsMod, parent *tsSym, n *ts.Node) []*tsSym {
	var out []*tsSym
	switch n.Kind() {
	case "function_declaration", "generator_function_declaration":
		if nm := field(n, "name"); nm != nil {
			s := x.newSym(m, parent, m.Src.text(nm), "function", n, field(n, "body"))
			x.declareNested(m, s)
			out = append(out, s)
		}
	case "class_declaration", "abstract_class_declaration":
		if nm := field(n, "name"); nm != nil {
			s := x.newSym(m, parent, m.Src.text(nm), "class", n, nil)
			x.declareClass(m, s, n)
			out = append(out, s)
		}
	case "interface_declaration", "type_alias_declaration", "enum_declaration":
		if nm := field(n, "name"); nm != nil {
			s := x.newSym(m, parent, m.Src.text(nm), "type", n, nil)
			out = append(out, s)
		}
	case "lexical_declaration", "variable_declaration":
		for _, d := range kids(n) {
			if d.Kind() != "variable_declarator" {
				continue
			}
			nm, v := field(d, "name"), field(d, "value")
			if nm != nil && nm.Kind() == "object_pattern" && v != nil && parent == nil {
				for _, p := range kids(nm) {
					var key, local string
					switch p.Kind() {
					case "shorthand_property_identifier_pattern":
						key = m.Src.text(p)
						local = key
					case "pair_pattern":
						key = m.Src.text(field(p, "key"))
						if val := field(p, "value"); val != nil && val.Kind() == "identifier" {
							local = m.Src.text(val)
						}
					}
					if local != "" {
						out = append(out, &tsSym{ID: "ts:" + m.Path + "#" + local, Name: local, Kind: "var", Mod: m, Init: v, Key: key, Local: map[string]*tsSym{}, Members: map[string]*tsSym{}})
					}
				}
				continue
			}
			if nm == nil || nm.Kind() != "identifier" || v == nil {
				continue
			}
			name := m.Src.text(nm)
			switch {
			case isFunctionNode(v):
				s := x.newSym(m, parent, name, "function", v, field(v, "body"))
				x.declareNested(m, s)
				out = append(out, s)
			case v.Kind() == "call_expression" && lastFunctionArg(v) != nil:
				cb := lastFunctionArg(v)
				s := x.newSym(m, parent, name, "function", v, field(cb, "body"))
				s.Params = field(cb, "parameters")
				x.declareNested(m, s)
				out = append(out, s)
			case v.Kind() == "object":
				s := x.newSym(m, parent, name, "object", v, nil)
				x.declareObject(m, s, v)
				out = append(out, s)
			default:
				if parent == nil {
					s := &tsSym{ID: "ts:" + m.Path + "#" + name, Name: name, Kind: "var", Mod: m, Init: v, Local: map[string]*tsSym{}, Members: map[string]*tsSym{}}
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// declareNested creates the symbols of the functions declared directly in a function body.
func (x *tsx) declareNested(m *tsMod, s *tsSym) {
	if s.Body == nil || s.Body.Kind() != "statement_block" {
		return
	}
	for _, st := range kids(s.Body) {
		for _, c := range x.declareDecl(m, s, st) {
			if c.Kind != "var" {
				s.Local[c.Name] = c
			}
		}
	}
}

func (x *tsx) declareClass(m *tsMod, s *tsSym, n *ts.Node) {
	s.FieldTypes = map[string]string{}
	if h := firstKid(n, "class_heritage"); h != nil {
		if e := firstKid(h, "extends_clause"); e != nil {
			if v := field(e, "value"); v != nil {
				s.Extends = m.Src.text(v)
			}
		}
	}
	body := field(n, "body")
	if body == nil {
		return
	}
	for _, c := range kids(body) {
		switch c.Kind() {
		case "method_definition":
			nm := field(c, "name")
			if nm == nil {
				continue
			}
			name := m.Src.text(nm)
			ms := x.newSym(m, s, name, "method", c, field(c, "body"))
			x.declareNested(m, ms)
			s.Members[name] = ms
			if name == "constructor" {
				for _, p := range kids(field(c, "parameters")) {
					if firstKid(p, "accessibility_modifier") != nil {
						if pn := field(p, "pattern"); pn != nil {
							s.FieldTypes[m.Src.text(pn)] = typeNameOf(m.Src, field(p, "type"))
						}
					}
				}
			}
		case "public_field_definition":
			nm := field(c, "name")
			if nm == nil {
				continue
			}
			name := m.Src.text(nm)
			if v := field(c, "value"); v != nil && isFunctionNode(v) {
				ms := x.newSym(m, s, name, "method", v, field(v, "body"))
				x.declareNested(m, ms)
				s.Members[name] = ms
			} else if t := field(c, "type"); t != nil {
				s.FieldTypes[name] = typeNameOf(m.Src, t)
			}
		}
	}
}

func (x *tsx) declareObject(m *tsMod, s *tsSym, obj *ts.Node) {
	s.Aliases = map[string]string{}
	for _, c := range kids(obj) {
		switch c.Kind() {
		case "method_definition":
			if nm := field(c, "name"); nm != nil {
				name := m.Src.text(nm)
				ms := x.newSym(m, s, name, "method", c, field(c, "body"))
				x.declareNested(m, ms)
				s.Members[name] = ms
			}
		case "pair":
			k, v := field(c, "key"), field(c, "value")
			if k == nil || v == nil {
				continue
			}
			name := strings.Trim(m.Src.text(k), "'\"`")
			switch {
			case isFunctionNode(v):
				ms := x.newSym(m, s, name, "method", v, field(v, "body"))
				x.declareNested(m, ms)
				s.Members[name] = ms
			case v.Kind() == "identifier":
				s.Aliases[name] = m.Src.text(v)
			}
		case "shorthand_property_identifier":
			n := m.Src.text(c)
			s.Aliases[n] = n
		}
	}
}

// typeNameOf reads the name of the first type in a type annotation.
func typeNameOf(src *source, n *ts.Node) string {
	if n == nil {
		return ""
	}
	var name string
	walk(n, func(c *ts.Node) bool {
		if name != "" {
			return false
		}
		if c.Kind() == "type_identifier" || (c.Kind() == "identifier" && c.Parent() != nil && c.Parent().Kind() == "nested_type_identifier") {
			name = src.text(c)
			return false
		}
		return true
	})
	return name
}

// stringLiteralTS returns the value of a plain string literal, or "".
func stringLiteralTS(src *source, n *ts.Node) string {
	if n == nil || n.Kind() != "string" {
		return ""
	}
	t := src.text(n)
	if len(t) < 2 {
		return ""
	}
	return t[1 : len(t)-1]
}

// ---- module resolution ----

func (x *tsx) aliasesFor(dir string) []tsAlias {
	if a, ok := x.aliases[dir]; ok {
		return a
	}
	var out []tsAlias
	for d := dir; ; d = path.Dir(d) {
		matches, _ := filepath.Glob(filepath.Join(x.ctx.Root, filepath.FromSlash(d), "tsconfig*.json"))
		sort.Strings(matches)
		for _, f := range matches {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			base := d
			if bm := tsconfigBase.FindSubmatch(b); bm != nil {
				base = path.Join(d, string(bm[1]))
			}
			for _, m := range tsconfigPaths.FindAllSubmatch(b, -1) {
				out = append(out, tsAlias{prefix: string(m[1]) + "/", dir: path.Join(base, strings.TrimPrefix(string(m[2]), "./"))})
			}
		}
		if len(out) > 0 || d == "." || d == "/" || d == "" {
			break
		}
	}
	x.aliases[dir] = out
	return out
}

// resolveSpec finds the module a specifier points to: a relative path or an alias; packages resolve to "".
func (x *tsx) resolveSpec(from, spec string) string {
	var base string
	switch {
	case strings.HasPrefix(spec, "."):
		base = path.Join(path.Dir(from), spec)
	default:
		for _, a := range x.aliasesFor(path.Dir(from)) {
			if strings.HasPrefix(spec, a.prefix) {
				base = path.Join(a.dir, strings.TrimPrefix(spec, a.prefix))
				break
			}
		}
	}
	if base == "" {
		return ""
	}
	for _, e := range tsExts {
		if x.paths[base+e] {
			return base + e
		}
	}
	return ""
}

// ---- lookup ----

// lookup finds the symbol a name means inside sym: nested declarations outwards, module level, then imports.
func (x *tsx) lookup(sym *tsSym, name string) *tsSym {
	for s := sym; s != nil; s = s.Parent {
		if c := s.Local[name]; c != nil {
			return c
		}
	}
	if sym == nil {
		return nil
	}
	m := sym.Mod
	if s := m.Top[name]; s != nil {
		return s
	}
	return x.importTarget(m, name)
}

func (x *tsx) importTarget(m *tsMod, local string) *tsSym {
	imp, ok := m.Imports[local]
	if !ok || imp.Target == "" {
		return nil
	}
	tm := x.mods[imp.Target]
	if tm == nil {
		return nil
	}
	switch imp.Name {
	case "default":
		return x.exportOf(tm, "default", 0)
	case "*":
		return x.namespace(tm)
	}
	return x.exportOf(tm, imp.Name, 0)
}

func (x *tsx) namespace(m *tsMod) *tsSym {
	if m.ns != nil {
		return m.ns
	}
	m.ns = &tsSym{ID: m.Module.ID + "#*", Name: "*", Kind: "namespace", Mod: m, Local: map[string]*tsSym{}, Members: map[string]*tsSym{}}
	for name := range m.Exports {
		if !strings.HasPrefix(name, "\x00") {
			m.ns.Members[name] = m.Exports[name]
		}
	}
	for name := range m.ReNamed {
		real := strings.TrimPrefix(name, "\x00local:")
		if strings.HasPrefix(real, "\x00") {
			continue
		}
		if s := x.exportOf(m, real, 0); s != nil {
			m.ns.Members[real] = s
		}
	}
	return m.ns
}

func (x *tsx) exportOf(m *tsMod, name string, depth int) *tsSym {
	if depth > 4 {
		return nil
	}
	if name == "default" {
		if m.Default != nil {
			return m.Default
		}
		if r, ok := m.ReNamed["\x00default"]; ok {
			return x.lookup(m.Module, r.Name)
		}
	}
	if s := m.Exports[name]; s != nil {
		return s
	}
	if r, ok := m.ReNamed["\x00local:"+name]; ok {
		return x.lookup(m.Module, r.Name)
	}
	if r, ok := m.ReNamed[name]; ok && r.Target != "" {
		if tm := x.mods[r.Target]; tm != nil {
			return x.exportOf(tm, r.Name, depth+1)
		}
	}
	for _, r := range m.ReStar {
		if tm := x.mods[r.Target]; tm != nil {
			if s := x.exportOf(tm, name, depth+1); s != nil {
				return s
			}
		}
	}
	return nil
}

// fixAliases resolves `{ start }` members of objects once every module is declared.
func (x *tsx) fixAliases(m *tsMod) {
	var visit func(s *tsSym)
	seen := map[*tsSym]bool{}
	visit = func(s *tsSym) {
		if s == nil || seen[s] {
			return
		}
		seen[s] = true
		for name, target := range s.Aliases {
			if t := x.lookup(s, target); t != nil {
				if _, ok := s.Members[name]; !ok {
					s.Members[name] = t
				}
			}
		}
		for _, c := range s.Local {
			visit(c)
		}
		for _, c := range s.Members {
			if c.Parent == s {
				visit(c)
			}
		}
	}
	for _, s := range m.Top {
		visit(s)
	}
	visit(m.Default)
}

// member finds a member of an object, a class (through its parents) or a module namespace.
func (x *tsx) member(o *tsSym, name string) *tsSym {
	for depth := 0; o != nil && depth < 6; depth++ {
		if s := o.Members[name]; s != nil {
			return s
		}
		if o.Kind != "class" || o.Extends == "" {
			return nil
		}
		o = x.lookup(o, o.Extends)
	}
	return nil
}

// resultOf is the object a function returns: the members of `return { a, b }`, or what a function it returns gives.
func (x *tsx) resultOf(s *tsSym) *tsSym {
	if s == nil || s.Kind == "class" || s.Kind == "type" {
		return nil
	}
	if s.resultDone || s.resultBusy {
		return s.result
	}
	s.resultBusy = true
	defer func() { s.resultBusy, s.resultDone = false, true }()
	if s.Body == nil {
		return nil
	}
	var value *ts.Node
	if s.Body.Kind() != "statement_block" {
		value = s.Body
	} else {
		for _, st := range kids(s.Body) {
			if st.Kind() == "return_statement" {
				if k := kids(st); len(k) > 0 {
					value = k[0]
				}
			}
		}
	}
	s.result = x.structResult(s, value)
	return s.result
}

func (x *tsx) structResult(s *tsSym, e *ts.Node) *tsSym {
	for e != nil {
		switch e.Kind() {
		case "parenthesized_expression", "await_expression", "as_expression", "non_null_expression", "satisfies_expression":
			k := kids(e)
			if len(k) == 0 {
				return nil
			}
			e = k[0]
			continue
		case "object":
			r := &tsSym{ID: s.ID + "()", Name: s.Name + "()", Kind: "result", Mod: s.Mod, Local: map[string]*tsSym{}, Members: map[string]*tsSym{}, Parent: s, Aliases: map[string]string{}}
			for _, c := range kids(e) {
				switch c.Kind() {
				case "shorthand_property_identifier":
					r.Aliases[s.Mod.Src.text(c)] = s.Mod.Src.text(c)
				case "pair":
					k, v := field(c, "key"), field(c, "value")
					if k != nil && v != nil && v.Kind() == "identifier" {
						r.Aliases[strings.Trim(s.Mod.Src.text(k), "'\"`")] = s.Mod.Src.text(v)
					}
				}
			}
			for name, target := range r.Aliases {
				if t := x.lookup(s, target); t != nil {
					r.Members[name] = t
				}
			}
			return r
		case "call_expression":
			fn := field(e, "function")
			if fn != nil && fn.Kind() == "identifier" {
				if g := x.lookup(s, s.Mod.Src.text(fn)); g != nil {
					return x.resultOf(g)
				}
			}
			return nil
		case "new_expression":
			if c := field(e, "constructor"); c != nil && c.Kind() == "identifier" {
				return x.lookup(s, s.Mod.Src.text(c))
			}
			return nil
		}
		return nil
	}
	return nil
}
