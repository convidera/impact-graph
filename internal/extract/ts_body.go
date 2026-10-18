package extract

import (
	"regexp"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/convidera/impact-graph/internal/graph"
)

type tsScope struct {
	x      *tsx
	m      *tsMod
	sym    *tsSym // the symbol edges are attributed to
	cls    *tsSym // `this`
	vars   map[string]*tsSym
	parent *tsScope
	quiet  bool // evaluate without recording edges (the initialiser of a module variable)
}

func (s *tsScope) child() *tsScope {
	return &tsScope{x: s.x, m: s.m, sym: s.sym, cls: s.cls, vars: map[string]*tsSym{}, parent: s, quiet: s.quiet}
}

func (s *tsScope) lookup(name string) *tsSym {
	for c := s; c != nil; c = c.parent {
		if v, ok := c.vars[name]; ok {
			return v
		}
	}
	sym := s.x.lookup(s.sym, name)
	if sym != nil && sym.Kind == "var" {
		return s.x.varBinding(sym)
	}
	return sym
}

// varBinding evaluates the initialiser of a module level variable once: `const api = useSessionApi()`, `const k = new K()`.
func (x *tsx) varBinding(v *tsSym) *tsSym {
	if v.resultDone || v.resultBusy {
		return v.result
	}
	v.resultBusy = true
	defer func() { v.resultBusy, v.resultDone = false, true }()
	s := &tsScope{x: x, m: v.Mod, sym: v.Mod.Module, vars: map[string]*tsSym{}, quiet: true}
	v.result = s.eval(v.Init)
	if v.Key != "" {
		if v.result != nil {
			v.result = x.member(v.result, v.Key)
		}
	}
	return v.result
}

func (s *tsScope) edge(dst *tsSym, kind string, conf float64, n *ts.Node, note string) {
	if s.quiet || dst == nil || dst.Kind == "namespace" || dst.Kind == "result" || dst.Kind == "var" || !s.x.ctx.G.Has(dst.ID) {
		return
	}
	s.x.ctx.G.AddEdge(s.sym.ID, dst.ID, kind, conf, s.m.Src.evidence(n, note))
}

func (x *tsx) analyze(m *tsMod) {
	var all []*tsSym
	var collect func(s *tsSym)
	seen := map[*tsSym]bool{}
	collect = func(s *tsSym) {
		if s == nil || seen[s] || s.Mod != m {
			return
		}
		seen[s] = true
		all = append(all, s)
		for _, c := range s.Local {
			collect(c)
		}
		for _, c := range s.Members {
			if c.Parent == s {
				collect(c)
			}
		}
	}
	for _, s := range m.Top {
		collect(s)
	}
	collect(m.Default)
	for _, s := range all {
		x.analyzeSym(m, s)
	}
	x.analyzeModule(m)
	if m.IsVue {
		x.analyzeTemplate(m)
	}
}

func (x *tsx) analyzeSym(m *tsMod, s *tsSym) {
	defer func() {
		if r := recover(); r != nil {
			x.ctx.warn("%s: skipped %s: %v", m.Path, s.ID, r)
		}
	}()
	switch s.Kind {
	case "function", "method":
		sc := &tsScope{x: x, m: m, sym: s, vars: map[string]*tsSym{}}
		if s.Parent != nil && s.Parent.Kind == "class" {
			sc.cls = s.Parent
		} else if s.Parent != nil {
			sc.cls = s.Parent // `this` inside a method of an object literal or a nested function keeps the outer receiver
		}
		sc.bindParams(s.Params)
		sc.eval(s.Params)
		if s.Node != nil {
			sc.eval(field(s.Node, "return_type"))
		}
		sc.eval(s.Body)
	case "class":
		if s.Extends != "" {
			if p := x.lookup(s, s.Extends); p != nil && x.ctx.G.Has(p.ID) {
				x.ctx.G.AddEdge(s.ID, p.ID, graph.Extends, 1, m.Path)
			}
		}
	}
}

// analyzeModule evaluates what runs when a module loads: router tables, store registration, top-level calls.
func (x *tsx) analyzeModule(m *tsMod) {
	defer func() {
		if r := recover(); r != nil {
			x.ctx.warn("%s: skipped module level code: %v", m.Path, r)
		}
	}()
	sc := &tsScope{x: x, m: m, sym: m.Module, vars: map[string]*tsSym{}}
	for _, st := range kids(m.Src.root) {
		x.moduleStmt(sc, st)
	}
}

func (x *tsx) moduleStmt(sc *tsScope, st *ts.Node) {
	switch st.Kind() {
	case "import_statement", "function_declaration", "generator_function_declaration", "class_declaration", "abstract_class_declaration",
		"interface_declaration", "type_alias_declaration", "enum_declaration":
		return
	case "export_statement":
		if d := field(st, "declaration"); d != nil {
			x.moduleStmt(sc, d)
			return
		}
		if v := field(st, "value"); v != nil && v.Kind() != "object" && !isFunctionNode(v) {
			sc.eval(v)
		}
		return
	case "lexical_declaration", "variable_declaration":
		for _, d := range kids(st) {
			if d.Kind() != "variable_declarator" {
				continue
			}
			v := field(d, "value")
			if v == nil || isFunctionNode(v) || v.Kind() == "object" || (v.Kind() == "call_expression" && lastFunctionArg(v) != nil) {
				continue
			}
			sc.eval(v)
		}
		return
	}
	sc.eval(st)
}

func (s *tsScope) bindParams(params *ts.Node) {
	if params == nil {
		return
	}
	for _, p := range kids(params) {
		pn := field(p, "pattern")
		if pn == nil || pn.Kind() != "identifier" {
			continue
		}
		if tn := typeNameOf(s.m.Src, field(p, "type")); tn != "" {
			if t := s.x.lookup(s.sym, tn); t != nil && t.Kind == "class" {
				s.vars[s.m.Src.text(pn)] = t
				continue
			}
		}
		s.vars[s.m.Src.text(pn)] = nil
	}
}

func unwrapTS(n *ts.Node) *ts.Node {
	for n != nil {
		switch n.Kind() {
		case "parenthesized_expression", "await_expression", "as_expression", "non_null_expression", "satisfies_expression", "type_assertion":
			k := kids(n)
			if len(k) == 0 {
				return n
			}
			n = k[0]
		default:
			return n
		}
	}
	return n
}

func (s *tsScope) eval(n *ts.Node) *tsSym {
	if n == nil {
		return nil
	}
	switch n.Kind() {
	case "identifier":
		name := s.m.Src.text(n)
		t := s.lookup(name)
		if t != nil && (t.Kind == "function") && s.x.mods[t.Mod.Path] != nil && !s.isLocalVar(name) {
			s.edge(t, graph.Calls, 0.75, n, "referenced as a value")
		}
		return t
	case "this":
		return s.cls
	case "parenthesized_expression", "await_expression", "as_expression", "non_null_expression", "satisfies_expression", "type_assertion":
		if k := kids(n); len(k) > 0 {
			r := s.eval(k[0])
			for _, c := range k[1:] {
				s.eval(c)
			}
			return r
		}
	case "call_expression":
		return s.onCall(n)
	case "new_expression":
		return s.onNew(n)
	case "member_expression":
		return s.onMember(n)
	case "arrow_function", "function_expression", "function":
		c := s.child()
		c.bindParams(field(n, "parameters"))
		c.eval(field(n, "parameters"))
		c.eval(field(n, "body"))
		return nil
	case "lexical_declaration", "variable_declaration":
		s.onDeclaration(n)
		return nil
	case "string":
		if v := stringLiteralTS(s.m.Src, n); v != "" {
			s.mention(v, n)
		}
		return nil
	case "type_identifier":
		if t := s.x.lookup(s.sym, s.m.Src.text(n)); t != nil && (t.Kind == "type" || t.Kind == "class") {
			s.edge(t, graph.Uses, 1, n, "")
		}
		return nil
	case "function_declaration", "generator_function_declaration", "class_declaration", "abstract_class_declaration":
		return nil // own symbols, analysed on their own
	case "assignment_expression":
		r := s.eval(field(n, "right"))
		if l := field(n, "left"); l != nil && l.Kind() == "identifier" {
			s.vars[s.m.Src.text(l)] = r
		} else {
			s.eval(l)
		}
		return r
	}
	for _, k := range kids(n) {
		s.eval(k)
	}
	return nil
}

func (s *tsScope) isLocalVar(name string) bool {
	for c := s; c != nil; c = c.parent {
		if _, ok := c.vars[name]; ok {
			return true
		}
	}
	return false
}

func (s *tsScope) mention(v string, n *ts.Node) {
	if s.quiet {
		return
	}
	s.x.ctx.Vocab.mentionAs(s.x.ctx, s.sym.ID, s.m.Path, v, s.m.Src.evidence(n, "literal "+v), 0.9, graph.Consumes, true)
}

func (s *tsScope) onDeclaration(n *ts.Node) {
	for _, d := range kids(n) {
		if d.Kind() != "variable_declarator" {
			continue
		}
		nm, v := field(d, "name"), field(d, "value")
		if nm == nil {
			continue
		}
		if nm.Kind() == "identifier" {
			name := s.m.Src.text(nm)
			if loc := s.sym.Local[name]; loc != nil && v != nil && (isFunctionNode(v) || (v.Kind() == "call_expression" && lastFunctionArg(v) != nil)) {
				s.vars[name] = loc // its body is analysed as its own symbol
				if v.Kind() == "call_expression" {
					s.evalCallNoBody(v)
				}
				continue
			}
			if loc := s.sym.Local[name]; loc != nil && v != nil && v.Kind() == "object" {
				s.vars[name] = loc
				continue
			}
			b := s.eval(v)
			if b == nil {
				if tn := typeNameOf(s.m.Src, field(d, "type")); tn != "" {
					if t := s.x.lookup(s.sym, tn); t != nil && t.Kind == "class" {
						b = t
					}
				}
			}
			s.vars[name] = b
			continue
		}
		b := s.eval(v)
		if nm.Kind() == "object_pattern" {
			for _, p := range kids(nm) {
				var key, local string
				switch p.Kind() {
				case "shorthand_property_identifier_pattern":
					key = s.m.Src.text(p)
					local = key
				case "pair_pattern":
					key = s.m.Src.text(field(p, "key"))
					if val := field(p, "value"); val != nil && val.Kind() == "identifier" {
						local = s.m.Src.text(val)
					}
				}
				if local != "" {
					var t *tsSym
					if b != nil {
						t = s.x.member(b, key)
					}
					s.vars[local] = t
				}
			}
		}
	}
}

// evalCallNoBody evaluates the arguments of `defineStore('id', () => ...)` other than the callback that is its own symbol.
func (s *tsScope) evalCallNoBody(call *ts.Node) {
	cb := lastFunctionArg(call)
	for _, a := range kids(field(call, "arguments")) {
		if cb != nil && a.Id() == cb.Id() {
			continue
		}
		s.eval(a)
	}
}

func (s *tsScope) onNew(n *ts.Node) *tsSym {
	c := field(n, "constructor")
	args := kids(field(n, "arguments"))
	var cls *tsSym
	if c != nil && c.Kind() == "identifier" {
		cls = s.lookup(s.m.Src.text(c))
	}
	if cls != nil && cls.Kind == "class" {
		s.edge(cls, graph.Uses, 1, n, "")
		if ctor := s.x.member(cls, "constructor"); ctor != nil {
			s.edge(ctor, graph.Calls, 1, n, "new "+cls.Name)
		}
	} else {
		cls = nil
	}
	for _, a := range args {
		s.eval(a)
	}
	return cls
}

func (s *tsScope) onMember(n *ts.Node) *tsSym {
	obj, prop := field(n, "object"), field(n, "property")
	o := s.eval(obj)
	if prop == nil || o == nil {
		return nil
	}
	name := s.m.Src.text(prop)
	if m := s.x.member(o, name); m != nil {
		return m
	}
	if o.FieldTypes != nil {
		if tn := o.FieldTypes[name]; tn != "" {
			if t := s.x.lookup(o, tn); t != nil && t.Kind == "class" {
				return t
			}
		}
	}
	return nil
}

func (s *tsScope) onCall(n *ts.Node) *tsSym {
	fn, argsNode := field(n, "function"), field(n, "arguments")
	var args []*ts.Node
	if argsNode != nil {
		args = kids(argsNode)
	}
	if fn == nil {
		return nil
	}
	s.httpCall(n, fn, args)
	var target *tsSym
	switch fn.Kind() {
	case "identifier":
		target = s.lookup(s.m.Src.text(fn))
	case "member_expression":
		target = s.onMember(fn)
	case "import":
		if len(args) > 0 {
			if spec := stringLiteralTS(s.m.Src, args[0]); spec != "" {
				if tm := s.x.mods[s.x.resolveSpec(s.m.Path, spec)]; tm != nil {
					s.edge(tm.Default, graph.Uses, 0.9, n, "dynamic import")
				}
			}
		}
	default:
		s.eval(fn)
	}
	for _, a := range args {
		s.eval(a)
	}
	if target == nil {
		return nil
	}
	if target.Kind == "function" || target.Kind == "method" {
		s.edge(target, graph.Calls, 0.95, n, "")
		return s.x.resultOf(target)
	}
	if target.Kind == "class" {
		s.edge(target, graph.Uses, 1, n, "")
	}
	return nil
}

// ---- HTTP ----

// httpCall links `axios.get('/x')`, `fetch('/x', {method: 'POST'})` and similar calls to the backend route they reach.
func (s *tsScope) httpCall(call, fn *ts.Node, args []*ts.Node) {
	if s.quiet || s.x.routes == nil || len(args) == 0 {
		return
	}
	verb := ""
	switch fn.Kind() {
	case "identifier":
		switch s.m.Src.text(fn) {
		case "fetch", "$fetch", "ofetch":
			verb = "GET"
			if len(args) > 1 {
				verb = methodOption(s.m.Src, args[1], verb)
			}
		case "axios", "ky":
			verb = ""
		default:
			return
		}
	case "member_expression":
		prop, obj := field(fn, "property"), field(fn, "object")
		if prop == nil || obj == nil {
			return
		}
		v, ok := httpVerbs[s.m.Src.text(prop)]
		if !ok {
			return
		}
		recv := s.m.Src.text(obj)
		if !clientHint.MatchString(recv) && !s.importsHTTPClient() {
			return
		}
		verb = v
	default:
		return
	}
	url := s.urlOf(args[0])
	if url == "" {
		return
	}
	s.x.routes.link(s.x.ctx, s.sym.ID, verb, url, s.m.Src.evidence(call, "HTTP "+verb+" "+strings.ReplaceAll(url, "\x00", "${}")))
}

func (s *tsScope) importsHTTPClient() bool {
	for _, imp := range s.m.Imports {
		if imp.Target == "" && (imp.Spec == "axios" || imp.Spec == "ky" || imp.Spec == "ofetch") {
			return true
		}
	}
	return false
}

func methodOption(src *source, opts *ts.Node, def string) string {
	if opts == nil || opts.Kind() != "object" {
		return def
	}
	for _, p := range kids(opts) {
		if p.Kind() == "pair" && strings.Trim(src.text(field(p, "key")), "'\"") == "method" {
			if v := stringLiteralTS(src, field(p, "value")); v != "" {
				return strings.ToUpper(v)
			}
		}
	}
	return def
}

// urlOf reads the static shape of a URL expression; an interpolation becomes \x00.
func (s *tsScope) urlOf(n *ts.Node) string {
	n = unwrapTS(n)
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "string":
		return stringLiteralTS(s.m.Src, n)
	case "template_string":
		var b strings.Builder
		for _, c := range allChildren(n) {
			switch c.Kind() {
			case "string_fragment":
				b.WriteString(s.m.Src.text(c))
			case "template_substitution":
				b.WriteString("\x00")
			}
		}
		return b.String()
	case "binary_expression":
		if op := field(n, "operator"); op != nil && s.m.Src.text(op) == "+" {
			l := s.urlOf(field(n, "left"))
			if l == "" {
				return ""
			}
			if r := s.urlOf(field(n, "right")); r != "" && !strings.HasPrefix(l, "\x00") {
				return l + r
			}
			return l + "\x00"
		}
	}
	return ""
}

// ---- Vue templates ----

var (
	tplTag   = regexp.MustCompile(`<([A-Za-z][\w.-]*)`)
	tplAttr  = regexp.MustCompile(`(?s)(?:\s|^)(?:@|:|v-)[\w:.\[\]-]*\s*=\s*"([^"]*)"`)
	tplMust  = regexp.MustCompile(`(?s)\{\{(.*?)\}\}`)
	tplIdent = regexp.MustCompile(`(^|[^\w.$])([A-Za-z_$][\w$]*)`)
)

var htmlTags = map[string]bool{}

func init() {
	for _, t := range strings.Fields("a abbr b body br button canvas code div em footer form h1 h2 h3 h4 h5 h6 head header hr i iframe img input label li link main nav ol option p pre section select small span strong style sub summary sup svg table tbody td template textarea tfoot th thead title tr u ul video slot component transition keep-alive teleport path circle rect g line polygon") {
		htmlTags[t] = true
	}
}

func lineAt(text string, off int) int { return strings.Count(text[:off], "\n") }

// analyzeTemplate links a component to the child components it renders and to the script functions and imports its
// template calls (`@click="save"`, `{{ format(x) }}`).
func (x *tsx) analyzeTemplate(m *tsMod) {
	if m.Template == "" {
		return
	}
	g := x.ctx.G
	startLine := strings.Count(string(m.Src.src[:m.TplOff]), "\n") + 1
	ev := func(off int, note string) string {
		return m.Path + ":" + itoa(startLine+lineAt(m.Template, off)) + " " + note
	}
	pascal := func(tag string) string {
		parts := strings.FieldsFunc(tag, func(r rune) bool { return r == '-' })
		var b strings.Builder
		for _, p := range parts {
			if p != "" {
				b.WriteString(strings.ToUpper(p[:1]) + p[1:])
			}
		}
		return b.String()
	}
	for _, loc := range tplTag.FindAllStringSubmatchIndex(m.Template, -1) {
		tag := m.Template[loc[2]:loc[3]]
		if htmlTags[strings.ToLower(tag)] && strings.ToLower(tag) == tag {
			continue
		}
		var target *tsSym
		for _, cand := range []string{tag, pascal(tag)} {
			if t := x.lookup(m.Module, cand); t != nil {
				target = t
				break
			}
		}
		if target != nil && target.Kind == "component" || (target != nil && target.Mod != m && target.Kind != "var" && x.ctx.G.Has(target.ID)) {
			g.AddEdge(m.Module.ID, target.ID, graph.Uses, 0.95, ev(loc[0], "renders <"+tag+">"))
		}
	}
	scan := func(expr string, base int) {
		for _, loc := range tplIdent.FindAllStringSubmatchIndex(expr, -1) {
			name := expr[loc[4]:loc[5]]
			t := x.lookup(m.Module, name)
			if t == nil || t == m.Module || (t.Kind != "function" && t.Kind != "object") || !g.Has(t.ID) {
				continue
			}
			g.AddEdge(m.Module.ID, t.ID, graph.Calls, 0.85, ev(base+loc[4], "used in the template"))
		}
	}
	for _, loc := range tplAttr.FindAllStringSubmatchIndex(m.Template, -1) {
		scan(m.Template[loc[2]:loc[3]], loc[2])
	}
	for _, loc := range tplMust.FindAllStringSubmatchIndex(m.Template, -1) {
		scan(m.Template[loc[2]:loc[3]], loc[2])
	}
}

// ---- entry point ----

func runTS(ctx *Ctx, files []scanFile) {
	if len(files) == 0 {
		return
	}
	x := &tsx{ctx: ctx, mods: map[string]*tsMod{}, aliases: map[string][]tsAlias{}, paths: map[string]bool{}, routes: newRouteIndex(ctx.G)}
	x.load(files)
	defer func() {
		for _, m := range x.order {
			m.Src.close()
		}
	}()
	for _, m := range x.order {
		x.declare(m)
	}
	for _, m := range x.order {
		x.fixAliases(m)
	}
	for _, m := range x.order {
		x.analyze(m)
	}
}
