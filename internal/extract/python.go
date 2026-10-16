package extract

import (
	"path"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	tspython "github.com/tree-sitter/tree-sitter-python/bindings/go"

	"github.com/convidera/impact-graph/internal/graph"
)

// Python: modules, classes, functions and methods; imports (absolute inside the repository, relative with dots), calls to
// imported functions and module attributes, `self.method()`, `self.attr.method()` where the attribute was assigned a
// class in `__init__`, constructors, HTTP calls to backend routes and string literals of declared vocabularies.

type pyImport struct {
	Mod  string // module path in the repository, "" for a package that is not indexed
	Name string // "" for a module import, else the imported name
}

type pySym struct {
	ID, Name, Kind string // module, class, function, method
	Mod            *pyMod
	Node, Body     *ts.Node
	Parent         *pySym
	Members        map[string]*pySym
	Bases          []string
	AttrTypes      map[string]string
}

type pyMod struct {
	Path    string
	Src     *source
	Imports map[string]pyImport
	Top     map[string]*pySym
	Module  *pySym
}

type pyx struct {
	ctx    *Ctx
	mods   map[string]*pyMod
	order  []*pyMod
	routes *routeIndex
}

func runPython(ctx *Ctx, files []scanFile) {
	if len(files) == 0 {
		return
	}
	x := &pyx{ctx: ctx, mods: map[string]*pyMod{}, routes: newRouteIndex(ctx.G)}
	srcs := parseFiles(files, ctx.Root, ts.NewLanguage(tspython.Language()))
	defer func() {
		for _, s := range srcs {
			s.close()
		}
	}()
	for _, s := range srcs {
		m := &pyMod{Path: s.file.Path, Src: s, Imports: map[string]pyImport{}, Top: map[string]*pySym{}}
		x.mods[m.Path] = m
		x.order = append(x.order, m)
	}
	for _, m := range x.order {
		x.declare(m)
	}
	for _, m := range x.order {
		x.analyze(m)
	}
}

func (x *pyx) node(s *pySym, typ string, n *ts.Node) {
	line1, end := 0, 0
	if n != nil {
		line1, end = line(n), endLine(n)
	}
	label := s.Name
	if s.Parent != nil && s.Parent.Kind == "class" {
		label = s.Parent.Name + "." + s.Name
	}
	x.ctx.G.AddNode(graph.Node{ID: s.ID, Type: typ, Label: label, Path: s.Mod.Path, Line: line1,
		Props: map[string]string{"lang": "python", "kind": s.Kind, "file_kind": s.Mod.Src.file.Kind, "end": itoa(end)}})
	if s.Parent != nil && s.Parent.Kind == "class" {
		x.ctx.G.AddEdge(s.ID, s.Parent.ID, graph.MemberOf, 1, s.Mod.Path)
	}
}

func (x *pyx) declare(m *pyMod) {
	m.Module = &pySym{ID: "py:" + m.Path, Name: path.Base(m.Path), Kind: "module", Mod: m, Members: map[string]*pySym{}}
	x.ctx.G.AddNode(graph.Node{ID: m.Module.ID, Type: graph.TypeFunction, Label: m.Path, Path: m.Path, Line: 1,
		Props: map[string]string{"lang": "python", "kind": "module", "file_kind": m.Src.file.Kind, "end": itoa(endLine(m.Src.root))}})
	for _, st := range kids(m.Src.root) {
		n := st
		if n.Kind() == "decorated_definition" {
			if d := field(n, "definition"); d != nil {
				n = d
			}
		}
		switch n.Kind() {
		case "function_definition":
			if nm := field(n, "name"); nm != nil {
				name := m.Src.text(nm)
				s := &pySym{ID: "py:" + m.Path + "#" + name, Name: name, Kind: "function", Mod: m, Node: n, Body: field(n, "body"), Members: map[string]*pySym{}}
				m.Top[name] = s
				x.node(s, graph.TypeFunction, n)
			}
		case "class_definition":
			x.declareClass(m, n)
		}
	}
}

func (x *pyx) declareClass(m *pyMod, n *ts.Node) {
	nm := field(n, "name")
	if nm == nil {
		return
	}
	name := m.Src.text(nm)
	c := &pySym{ID: "py:" + m.Path + "#" + name, Name: name, Kind: "class", Mod: m, Node: n, Members: map[string]*pySym{}, AttrTypes: map[string]string{}}
	if sc := field(n, "superclasses"); sc != nil {
		for _, b := range kids(sc) {
			c.Bases = append(c.Bases, m.Src.text(b))
		}
	}
	m.Top[name] = c
	x.node(c, graph.TypeClass, n)
	body := field(n, "body")
	if body == nil {
		return
	}
	for _, st := range kids(body) {
		d := st
		if d.Kind() == "decorated_definition" {
			if dd := field(d, "definition"); dd != nil {
				d = dd
			}
		}
		if d.Kind() != "function_definition" {
			continue
		}
		if mn := field(d, "name"); mn != nil {
			mname := m.Src.text(mn)
			ms := &pySym{ID: c.ID + "." + mname, Name: mname, Kind: "method", Mod: m, Node: d, Body: field(d, "body"), Parent: c, Members: map[string]*pySym{}}
			c.Members[mname] = ms
			x.node(ms, graph.TypeMethod, d)
		}
	}
}

// ---- imports ----

func (x *pyx) findModule(from string, dots int, dotted string) string {
	dir := path.Dir(from)
	var base string
	if dots > 0 {
		base = dir
		for i := 1; i < dots; i++ {
			base = path.Dir(base)
		}
		if dotted != "" {
			base = path.Join(base, strings.ReplaceAll(dotted, ".", "/"))
		}
		return x.fileFor(base)
	}
	rel := strings.ReplaceAll(dotted, ".", "/")
	// absolute: the module whose path ends with the dotted path, nearest to the importing file first
	var best string
	for p := range x.mods {
		for _, cand := range []string{rel + ".py", rel + "/__init__.py"} {
			if p == cand || strings.HasSuffix(p, "/"+cand) {
				if best == "" || commonPrefix(p, from) > commonPrefix(best, from) {
					best = p
				}
			}
		}
	}
	return best
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

func (x *pyx) fileFor(base string) string {
	for _, c := range []string{base + ".py", base + "/__init__.py"} {
		if _, ok := x.mods[c]; ok {
			return c
		}
	}
	return ""
}

func (x *pyx) analyze(m *pyMod) {
	// imports first, so calls in the bodies can use them
	for _, st := range kids(m.Src.root) {
		x.importStmt(m, st)
	}
	var syms []*pySym
	for _, s := range m.Top {
		syms = append(syms, s)
		for _, mm := range s.Members {
			syms = append(syms, mm)
		}
	}
	for _, s := range syms {
		if s.Kind == "class" {
			x.analyzeClass(m, s)
			continue
		}
		x.analyzeFunc(m, s)
	}
	// module level code
	sc := &pyScope{x: x, m: m, sym: m.Module, vars: map[string]*pySym{}}
	for _, st := range kids(m.Src.root) {
		switch st.Kind() {
		case "function_definition", "class_definition", "decorated_definition", "import_statement", "import_from_statement":
			continue
		}
		sc.eval(st)
	}
}

func (x *pyx) importStmt(m *pyMod, st *ts.Node) {
	switch st.Kind() {
	case "import_statement":
		for _, nm := range kids(st) {
			var dotted, alias string
			switch nm.Kind() {
			case "dotted_name":
				dotted = m.Src.text(nm)
			case "aliased_import":
				dotted = m.Src.text(field(nm, "name"))
				alias = m.Src.text(field(nm, "alias"))
			default:
				continue
			}
			local := alias
			if local == "" {
				local = strings.SplitN(dotted, ".", 2)[0]
			}
			target := x.findModule(m.Path, 0, dotted)
			if alias == "" && strings.Contains(dotted, ".") {
				target = x.findModule(m.Path, 0, strings.SplitN(dotted, ".", 2)[0])
			}
			m.Imports[local] = pyImport{Mod: target}
		}
	case "import_from_statement":
		modNode := field(st, "module_name")
		dots, dotted := 0, ""
		if modNode != nil {
			if modNode.Kind() == "relative_import" {
				if p := firstKid(modNode, "import_prefix"); p != nil {
					dots = strings.Count(m.Src.text(p), ".")
				}
				if d := firstKid(modNode, "dotted_name"); d != nil {
					dotted = m.Src.text(d)
				}
			} else {
				dotted = m.Src.text(modNode)
			}
		}
		target := x.findModule(m.Path, dots, dotted)
		for _, nm := range kids(st) {
			if nm.Id() == modNode.Id() {
				continue
			}
			var name, alias string
			switch nm.Kind() {
			case "dotted_name":
				name = m.Src.text(nm)
			case "aliased_import":
				name = m.Src.text(field(nm, "name"))
				alias = m.Src.text(field(nm, "alias"))
			default:
				continue
			}
			local := name
			if alias != "" {
				local = alias
			}
			// `from . import auth` imports a module; `from .api_client import call_tool` a name
			var sub string
			if dots > 0 {
				base := path.Dir(m.Path)
				for i := 1; i < dots; i++ {
					base = path.Dir(base)
				}
				if dotted != "" {
					base = path.Join(base, strings.ReplaceAll(dotted, ".", "/"))
				}
				sub = x.fileFor(path.Join(base, name))
			} else if dotted != "" {
				sub = x.findModule(m.Path, 0, dotted+"."+name)
			}
			if sub != "" && (target == "" || x.mods[target] == nil || x.mods[target].Top[name] == nil) {
				m.Imports[local] = pyImport{Mod: sub}
			} else {
				m.Imports[local] = pyImport{Mod: target, Name: name}
			}
		}
	}
}

// ---- bodies ----

type pyScope struct {
	x    *pyx
	m    *pyMod
	sym  *pySym
	cls  *pySym
	vars map[string]*pySym
}

func (x *pyx) analyzeClass(m *pyMod, c *pySym) {
	for _, b := range c.Bases {
		if p := x.resolveName(m, b); p != nil && p.Kind == "class" {
			x.ctx.G.AddEdge(c.ID, p.ID, graph.Extends, 1, m.Path)
		}
	}
	// attribute types from __init__: self.client = Client()
	if init := c.Members["__init__"]; init != nil && init.Body != nil {
		sc := &pyScope{x: x, m: m, sym: init, cls: c, vars: map[string]*pySym{}}
		sc.bindParams(init.Node)
		walk(init.Body, func(n *ts.Node) bool {
			if n.Kind() != "assignment" {
				return true
			}
			l, r := field(n, "left"), field(n, "right")
			if l == nil || r == nil || l.Kind() != "attribute" {
				return true
			}
			if o := field(l, "object"); o != nil && m.Src.text(o) == "self" {
				if t := sc.typeOf(r); t != nil {
					c.AttrTypes[m.Src.text(field(l, "attribute"))] = t.ID
				}
			}
			return true
		})
	}
	for _, mm := range c.Members {
		x.analyzeFunc(m, mm)
	}
}

func (x *pyx) analyzeFunc(m *pyMod, s *pySym) {
	if s.Kind == "class" || s.Body == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			x.ctx.warn("%s: skipped %s: %v", m.Path, s.ID, r)
		}
	}()
	sc := &pyScope{x: x, m: m, sym: s, vars: map[string]*pySym{}}
	if s.Parent != nil && s.Parent.Kind == "class" {
		sc.cls = s.Parent
	}
	sc.bindParams(s.Node)
	sc.eval(s.Body)
}

func (x *pyx) resolveName(m *pyMod, name string) *pySym {
	if s := m.Top[name]; s != nil {
		return s
	}
	imp, ok := m.Imports[name]
	if !ok || imp.Mod == "" {
		return nil
	}
	tm := x.mods[imp.Mod]
	if tm == nil {
		return nil
	}
	if imp.Name == "" {
		return tm.Module
	}
	return tm.Top[imp.Name]
}

func (s *pyScope) bindParams(fn *ts.Node) {
	if fn == nil {
		return
	}
	ps := field(fn, "parameters")
	if ps == nil {
		return
	}
	for _, p := range kids(ps) {
		switch p.Kind() {
		case "typed_parameter", "typed_default_parameter":
			var id *ts.Node
			if p.Kind() == "typed_parameter" {
				id = firstKid(p, "identifier")
			} else {
				id = field(p, "name")
			}
			if id == nil {
				continue
			}
			if t := field(p, "type"); t != nil {
				if cls := s.x.resolveName(s.m, s.m.Src.text(t)); cls != nil && cls.Kind == "class" {
					s.vars[s.m.Src.text(id)] = cls
				}
			}
		}
	}
}

func (s *pyScope) edge(dst *pySym, kind string, conf float64, n *ts.Node, note string) {
	if dst == nil || !s.x.ctx.G.Has(dst.ID) {
		return
	}
	s.x.ctx.G.AddEdge(s.sym.ID, dst.ID, kind, conf, s.m.Src.evidence(n, note))
}

// typeOf is the class an expression constructs or a variable holds.
func (s *pyScope) typeOf(n *ts.Node) *pySym {
	switch n.Kind() {
	case "call":
		if fn := field(n, "function"); fn != nil {
			if t := s.resolve(fn); t != nil && t.Kind == "class" {
				return t
			}
		}
	case "identifier":
		return s.vars[s.m.Src.text(n)]
	}
	return nil
}

// resolve finds the symbol a callee expression names.
func (s *pyScope) resolve(fn *ts.Node) *pySym {
	switch fn.Kind() {
	case "identifier":
		name := s.m.Src.text(fn)
		if v, ok := s.vars[name]; ok {
			return v
		}
		return s.x.resolveName(s.m, name)
	case "attribute":
		obj, attr := field(fn, "object"), field(fn, "attribute")
		if obj == nil || attr == nil {
			return nil
		}
		name := s.m.Src.text(attr)
		var o *pySym
		if obj.Kind() == "identifier" && s.m.Src.text(obj) == "self" {
			o = s.cls
		} else {
			o = s.resolveObject(obj)
		}
		if o == nil {
			return nil
		}
		return s.x.memberOf(o, name)
	}
	return nil
}

// resolveObject resolves the receiver of a method call: a module, a class, a typed variable or `self.attr`.
func (s *pyScope) resolveObject(obj *ts.Node) *pySym {
	switch obj.Kind() {
	case "identifier":
		name := s.m.Src.text(obj)
		if v, ok := s.vars[name]; ok {
			return v
		}
		return s.x.resolveName(s.m, name)
	case "attribute":
		o, a := field(obj, "object"), field(obj, "attribute")
		if o != nil && a != nil && s.m.Src.text(o) == "self" && s.cls != nil {
			if id := s.cls.AttrTypes[s.m.Src.text(a)]; id != "" {
				return s.x.symByID(id)
			}
		}
	}
	return nil
}

func (x *pyx) symByID(id string) *pySym {
	for _, m := range x.order {
		for _, s := range m.Top {
			if s.ID == id {
				return s
			}
		}
	}
	return nil
}

func (x *pyx) memberOf(o *pySym, name string) *pySym {
	switch o.Kind {
	case "module":
		return o.Mod.Top[name]
	case "class":
		for depth := 0; o != nil && depth < 6; depth++ {
			if m := o.Members[name]; m != nil {
				return m
			}
			var next *pySym
			for _, b := range o.Bases {
				if p := x.resolveName(o.Mod, b); p != nil && p.Kind == "class" {
					next = p
					break
				}
			}
			o = next
		}
	}
	return nil
}

func (s *pyScope) eval(n *ts.Node) {
	if n == nil {
		return
	}
	switch n.Kind() {
	case "call":
		s.onCall(n)
		return
	case "assignment":
		l, r := field(n, "left"), field(n, "right")
		s.eval(r)
		if l != nil && l.Kind() == "identifier" && r != nil {
			s.vars[s.m.Src.text(l)] = s.typeOf(r)
		}
		return
	case "string":
		if v := s.stringValue(n); v != "" && !strings.Contains(v, "\x00") {
			s.x.ctx.Vocab.mentionAs(s.x.ctx, s.sym.ID, s.m.Path, v, s.m.Src.evidence(n, "literal "+v), 0.9, graph.Consumes)
		}
		return
	case "function_definition", "class_definition":
		if n.Id() != s.sym.nodeID() {
			return // a nested definition is analysed with its parent's scope below
		}
	}
	for _, k := range kids(n) {
		s.eval(k)
	}
}

func (p *pySym) nodeID() uintptr {
	if p.Node == nil {
		return 0
	}
	return p.Node.Id()
}

func (s *pyScope) onCall(n *ts.Node) {
	fn, args := field(n, "function"), field(n, "arguments")
	if fn == nil {
		return
	}
	s.httpCall(n, fn, args)
	if t := s.resolve(fn); t != nil {
		switch t.Kind {
		case "function", "method":
			s.edge(t, graph.Calls, 0.95, n, "")
		case "class":
			s.edge(t, graph.Uses, 1, n, "")
			if ctor := s.x.memberOf(t, "__init__"); ctor != nil {
				s.edge(ctor, graph.Calls, 1, n, "constructs "+t.Name)
			}
		}
	}
	s.eval(fn)
	if args != nil {
		for _, a := range kids(args) {
			s.eval(a)
		}
	}
}

// stringValue is the static text of a string; an interpolation becomes \x00.
func (s *pyScope) stringValue(n *ts.Node) string {
	var b strings.Builder
	for _, c := range kids(n) {
		switch c.Kind() {
		case "string_content":
			b.WriteString(s.m.Src.text(c))
		case "interpolation":
			b.WriteString("\x00")
		}
	}
	return b.String()
}

func (s *pyScope) httpCall(call, fn, args *ts.Node) {
	if s.x.routes == nil || args == nil || fn.Kind() != "attribute" {
		return
	}
	attr, obj := field(fn, "attribute"), field(fn, "object")
	if attr == nil || obj == nil {
		return
	}
	verb, ok := httpVerbs[s.m.Src.text(attr)]
	if !ok {
		return
	}
	if !clientHint.MatchString(s.m.Src.text(obj)) {
		return
	}
	a := kids(args)
	if len(a) == 0 {
		return
	}
	var url string
	switch a[0].Kind() {
	case "string":
		url = s.stringValue(a[0])
	case "binary_operator":
		if l := field(a[0], "left"); l != nil && l.Kind() == "string" {
			url = s.stringValue(l) + "\x00"
		}
	}
	if url == "" {
		return
	}
	s.x.routes.link(s.x.ctx, s.sym.ID, verb, url, s.m.Src.evidence(call, "HTTP "+verb+" "+strings.ReplaceAll(url, "\x00", "{}")))
}
