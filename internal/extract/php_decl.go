package extract

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"
	tsphp "github.com/tree-sitter/tree-sitter-php/bindings/go"

	"github.com/convidera/impact/internal/graph"
	"github.com/convidera/impact/internal/scan"
)

type phpFile struct {
	src  *source
	ns   string
	uses map[string]string
}

type phpParam struct {
	Name string
	Type string
}

type phpProp struct {
	Name    string
	Type    string
	Line    int
	Classes []string // X::class entries of an array default (a server's tools)
}

type phpMethod struct {
	Name   string
	Class  *phpClass
	Static bool
	Params []phpParam
	Ret    string // resolved class FQN, "q:<FQN>" for a query builder of a model, or ""
	Node   *ts.Node
	Body   *ts.Node
	ID     string
	Line   int
}

type phpClass struct {
	FQN, Short, Kind string
	File             *phpFile
	Line, End        int
	Node             *ts.Node
	Extends          string
	Implements       []string
	Traits           []string
	Props            map[string]*phpProp
	Methods          map[string]*phpMethod
	Consts           map[string]string // constant or enum case -> string value
	DocProps         map[string]string // @property types
	Table            string
	ResourceModel    string // the model a JsonResource presents, by naming convention
	ID               string
}

type tableInfo struct {
	Name string
	Cols map[string]int // column -> line of its last declaration
	File map[string]string
}

type php struct {
	ctx       *Ctx
	files     []*phpFile
	classes   map[string]*phpClass
	byShort   map[string][]*phpClass
	tables    map[string]*tableInfo
	colOwners map[string][]string // column name -> tables that have it
	factories map[string]string   // static factory method id -> method it calls on the instance (Feature::with -> handle)
	impls     map[string][]string // interface or parent FQN -> implementing classes
	modelMemo map[string]bool
}

var phpPrimitives = map[string]bool{
	"int": true, "float": true, "string": true, "bool": true, "array": true, "mixed": true, "void": true, "null": true,
	"callable": true, "iterable": true, "object": true, "never": true, "false": true, "true": true, "self": true,
	"static": true, "parent": true,
}

func phpLanguage() *ts.Language { return ts.NewLanguage(tsphp.LanguagePHP()) }

func newPHP(ctx *Ctx) *php {
	return &php{
		ctx: ctx, classes: map[string]*phpClass{}, byShort: map[string][]*phpClass{}, tables: map[string]*tableInfo{},
		colOwners: map[string][]string{}, factories: map[string]string{}, impls: map[string][]string{}, modelMemo: map[string]bool{},
	}
}

// resolveName expands a class name as written in a file to its fully qualified name.
func (f *phpFile) resolveName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if strings.HasPrefix(name, "\\") {
		return name[1:]
	}
	first, rest, hasRest := strings.Cut(name, "\\")
	if full, ok := f.uses[first]; ok {
		if hasRest {
			return full + "\\" + rest
		}
		return full
	}
	if f.ns != "" {
		return f.ns + "\\" + name
	}
	return name
}

// typeFromNode reads a declared type and returns the first class-like name in it.
func (f *phpFile) typeFromNode(n *ts.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "named_type", "optional_type", "union_type", "intersection_type", "type_list", "disjunctive_normal_form_type":
		for _, k := range kids(n) {
			if t := f.typeFromNode(k); t != "" {
				return t
			}
		}
	case "name", "qualified_name":
		t := f.src.text(n)
		if phpPrimitives[strings.ToLower(t)] {
			if strings.EqualFold(t, "self") || strings.EqualFold(t, "static") {
				return strings.ToLower(t)
			}
			return ""
		}
		return f.resolveName(t)
	}
	return ""
}

var (
	docProp   = regexp.MustCompile(`@property(?:-read|-write)?\s+([^\s$]+)\s+\$(\w+)`)
	docReturn = regexp.MustCompile(`@return\s+([^\s]+)`)
	docVar    = regexp.MustCompile(`@var\s+([^\s$]+)(?:\s+\$(\w+))?`)
)

// docType resolves a docblock type such as `\App\Foo|null`, `Foo[]` or `Builder<Foo>` to a class name or a query type.
func (f *phpFile) docType(t string) string {
	t = strings.TrimSpace(t)
	if i := strings.Index(t, "<"); i > 0 {
		base := t[:i]
		inner := strings.TrimSuffix(t[i+1:], ">")
		inner = strings.SplitN(inner, ",", 2)[0]
		if strings.HasSuffix(base, "Builder") || strings.HasSuffix(base, "Relation") {
			if m := f.docType(inner); m != "" && !strings.HasPrefix(m, "q:") {
				return "q:" + m
			}
		}
		return ""
	}
	for _, part := range strings.Split(t, "|") {
		part = strings.TrimSuffix(strings.TrimSpace(part), "[]")
		if part == "" || phpPrimitives[strings.ToLower(part)] || strings.ContainsAny(part, "{}()'\" ") {
			continue
		}
		return f.resolveName(part)
	}
	return ""
}

func (s *source) docBefore(n *ts.Node) string {
	if p := n.PrevSibling(); p != nil && p.Kind() == "comment" {
		if t := s.text(p); strings.HasPrefix(t, "/**") {
			return t
		}
	}
	return ""
}

// declare parses every PHP file and records its classes, so bodies can be resolved against all of them.
func (p *php) declare(files []scan.File) []*source {
	srcs := parseFiles(files, p.ctx.Root, phpLanguage())
	for _, s := range srcs {
		f := &phpFile{src: s, uses: map[string]string{}}
		p.files = append(p.files, f)
		p.declareFile(f, s.root)
	}
	for _, c := range p.classes {
		p.byShort[c.Short] = append(p.byShort[c.Short], c)
	}
	// who implements or extends whom
	for _, c := range p.classes {
		for _, parent := range append([]string{c.Extends}, c.Implements...) {
			if parent != "" {
				p.impls[parent] = append(p.impls[parent], c.FQN)
			}
		}
	}
	p.findFactories()
	return srcs
}

func (p *php) declareFile(f *phpFile, n *ts.Node) {
	for _, c := range kids(n) {
		switch c.Kind() {
		case "namespace_definition":
			if nm := field(c, "name"); nm != nil {
				f.ns = f.src.text(nm)
			}
			if body := field(c, "body"); body != nil {
				p.declareFile(f, body)
			}
		case "namespace_use_declaration":
			p.declareUse(f, c)
		case "class_declaration", "interface_declaration", "trait_declaration", "enum_declaration":
			p.declareClass(f, c)
		case "function_definition":
			p.declareFunction(f, c)
		}
	}
}

func (p *php) declareUse(f *phpFile, n *ts.Node) {
	text := f.src.text(n)
	if strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(text, "use")), "function") || strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(text, "use")), "const") {
		return
	}
	prefix := ""
	if g := firstKid(n, "namespace_name"); g != nil {
		prefix = f.src.text(g) + "\\"
	}
	var clauses []*ts.Node
	for _, k := range kids(n) {
		switch k.Kind() {
		case "namespace_use_clause":
			clauses = append(clauses, k)
		case "namespace_use_group":
			clauses = append(clauses, kids(k)...)
		}
	}
	for _, cl := range clauses {
		nm := firstKid(cl, "qualified_name", "name")
		if nm == nil {
			continue
		}
		full := strings.TrimPrefix(prefix+strings.TrimPrefix(f.src.text(nm), "\\"), "\\")
		alias := lastSegment(full, "\\")
		if a := field(cl, "alias"); a != nil {
			alias = f.src.text(a)
		}
		f.uses[alias] = full
	}
}

func (p *php) declareFunction(f *phpFile, n *ts.Node) {
	name := f.src.text(field(n, "name"))
	fqn := name
	if f.ns != "" {
		fqn = f.ns + "\\" + name
	}
	c := &phpClass{
		FQN: fqn, Short: name, Kind: "function", File: f, Line: line(n), End: endLine(n), Node: n, ID: "php:" + fqn,
		Props: map[string]*phpProp{}, Methods: map[string]*phpMethod{}, Consts: map[string]string{}, DocProps: map[string]string{},
	}
	m := &phpMethod{Name: name, Class: c, Node: n, Body: field(n, "body"), ID: c.ID, Line: line(n), Static: true}
	m.Params = p.params(f, field(n, "parameters"))
	m.Ret = p.retType(f, n, c)
	c.Methods[name] = m
	p.classes[fqn] = c
	p.addSymbol(c, graph.TypeFunction)
}

func (p *php) addSymbol(c *phpClass, typ string) {
	p.ctx.G.AddNode(graph.Node{
		ID: c.ID, Type: typ, Label: c.Short, Path: c.File.src.file.Path, Line: c.Line,
		Props: map[string]string{"lang": "php", "kind": c.Kind, "file_kind": c.File.src.file.Kind, "end": itoa(c.End), "fqn": c.FQN},
	})
}

func (p *php) declareClass(f *phpFile, n *ts.Node) {
	nameNode := field(n, "name")
	if nameNode == nil {
		return
	}
	short := f.src.text(nameNode)
	fqn := short
	if f.ns != "" {
		fqn = f.ns + "\\" + short
	}
	kind := strings.TrimSuffix(n.Kind(), "_declaration")
	c := &phpClass{
		FQN: fqn, Short: short, Kind: kind, File: f, Line: line(n), End: endLine(n), Node: n, ID: "php:" + fqn,
		Props: map[string]*phpProp{}, Methods: map[string]*phpMethod{}, Consts: map[string]string{}, DocProps: map[string]string{},
	}
	for _, k := range kids(n) {
		switch k.Kind() {
		case "base_clause":
			for _, nm := range kids(k) {
				if t := f.typeFromNode(nm); t != "" {
					if kind == "interface" {
						c.Implements = append(c.Implements, t)
					} else {
						c.Extends = t
					}
				}
			}
		case "class_interface_clause":
			for _, nm := range kids(k) {
				if t := f.typeFromNode(nm); t != "" {
					c.Implements = append(c.Implements, t)
				}
			}
		}
	}
	doc := f.src.docBefore(n)
	for _, m := range docProp.FindAllStringSubmatch(doc, -1) {
		if t := f.docType(m[1]); t != "" {
			c.DocProps[m[2]] = t
		}
	}
	p.classes[fqn] = c
	p.addSymbol(c, graph.TypeClass)
	body := field(n, "body")
	if body == nil {
		return
	}
	for _, m := range kids(body) {
		switch m.Kind() {
		case "use_declaration":
			for _, nm := range kids(m) {
				if t := f.typeFromNode(nm); t != "" {
					c.Traits = append(c.Traits, t)
				}
			}
		case "const_declaration":
			for _, el := range kids(m) {
				if el.Kind() != "const_element" {
					continue
				}
				nm := firstKid(el, "name")
				if nm == nil {
					continue
				}
				if k := kids(el); len(k) > 0 {
					if v := stringLiteral(f.src, k[len(k)-1]); v != "" {
						c.Consts[f.src.text(nm)] = v
					}
				}
			}
		case "enum_case":
			if nm := field(m, "name"); nm != nil {
				if v := stringLiteral(f.src, field(m, "value")); v != "" {
					c.Consts[f.src.text(nm)] = v
				}
			}
		case "property_declaration":
			p.declareProps(f, c, m)
		case "method_declaration":
			p.declareMethod(f, c, m)
		}
	}
	p.inferPropTypes(f, c)
}

// inferPropTypes types an untyped property from the constructor: `$this->entry = $entry;` where `$entry` is a typed
// parameter. Feature and action classes keep their inputs this way, and without it nothing they do to them is visible.
func (p *php) inferPropTypes(f *phpFile, c *phpClass) {
	ctor := c.Methods["__construct"]
	if ctor == nil || ctor.Body == nil {
		return
	}
	types := map[string]string{}
	for _, pr := range ctor.Params {
		if pr.Type != "" {
			types[pr.Name] = pr.Type
		}
	}
	walk(ctor.Body, func(n *ts.Node) bool {
		if n.Kind() != "assignment_expression" {
			return true
		}
		l, r := field(n, "left"), field(n, "right")
		if l == nil || r == nil || l.Kind() != "member_access_expression" || r.Kind() != "variable_name" {
			return true
		}
		obj, nm := field(l, "object"), field(l, "name")
		if obj == nil || nm == nil || f.src.text(obj) != "$this" {
			return true
		}
		t := types[strings.TrimPrefix(f.src.text(r), "$")]
		name := f.src.text(nm)
		if t == "" {
			return true
		}
		if pr := c.Props[name]; pr != nil && pr.Type == "" {
			pr.Type = t
		} else if pr == nil {
			c.Props[name] = &phpProp{Name: name, Type: t, Line: line(n)}
		}
		return true
	})
}

func (p *php) declareProps(f *phpFile, c *phpClass, n *ts.Node) {
	typ := f.typeFromNode(field(n, "type"))
	if typ == "" {
		if m := docVar.FindStringSubmatch(f.src.docBefore(n)); m != nil {
			typ = f.docType(m[1])
		}
	}
	for _, el := range kids(n) {
		if el.Kind() != "property_element" {
			continue
		}
		nm := field(el, "name")
		if nm == nil {
			continue
		}
		name := strings.TrimPrefix(f.src.text(nm), "$")
		pr := &phpProp{Name: name, Type: typ, Line: line(el)}
		if def := field(el, "default_value"); def != nil {
			walk(def, func(x *ts.Node) bool {
				if x.Kind() == "class_constant_access_expression" {
					if cn := kids(x); len(cn) == 2 && f.src.text(cn[1]) == "class" {
						pr.Classes = append(pr.Classes, f.resolveName(f.src.text(cn[0])))
					}
				}
				return true
			})
			if v := stringLiteral(f.src, def); v != "" {
				c.Consts["$"+name] = v
			}
		}
		c.Props[name] = pr
	}
}

func (p *php) params(f *phpFile, n *ts.Node) []phpParam {
	var out []phpParam
	if n == nil {
		return out
	}
	for _, k := range kids(n) {
		switch k.Kind() {
		case "simple_parameter", "property_promotion_parameter", "variadic_parameter":
			nm := field(k, "name")
			if nm == nil {
				continue
			}
			out = append(out, phpParam{Name: strings.TrimPrefix(f.src.text(nm), "$"), Type: f.typeFromNode(field(k, "type"))})
		}
	}
	return out
}

func (p *php) retType(f *phpFile, n *ts.Node, c *phpClass) string {
	t := f.typeFromNode(field(n, "return_type"))
	if doc := f.src.docBefore(n); doc != "" {
		if m := docReturn.FindStringSubmatch(doc); m != nil {
			if d := f.docType(m[1]); strings.HasPrefix(d, "q:") || (t == "" && d != "") {
				t = d
			}
		}
	}
	switch t {
	case "self", "static":
		return c.FQN
	}
	return t
}

func (p *php) declareMethod(f *phpFile, c *phpClass, n *ts.Node) {
	nm := field(n, "name")
	if nm == nil {
		return
	}
	name := f.src.text(nm)
	m := &phpMethod{Name: name, Class: c, Node: n, Body: field(n, "body"), Line: line(n), ID: c.ID + "::" + name}
	for _, k := range kids(n) {
		if k.Kind() == "static_modifier" {
			m.Static = true
		}
	}
	m.Params = p.params(f, field(n, "parameters"))
	m.Ret = p.retType(f, n, c)
	c.Methods[name] = m
	// promoted constructor parameters are typed properties
	if name == "__construct" {
		if params := field(n, "parameters"); params != nil {
			for _, k := range kids(params) {
				if k.Kind() == "property_promotion_parameter" {
					if pn := field(k, "name"); pn != nil {
						pname := strings.TrimPrefix(f.src.text(pn), "$")
						c.Props[pname] = &phpProp{Name: pname, Type: f.typeFromNode(field(k, "type")), Line: line(k)}
					}
				}
			}
		}
	}
	g := p.ctx.G
	g.AddNode(graph.Node{
		ID: m.ID, Type: graph.TypeMethod, Label: c.Short + "::" + name, Path: f.src.file.Path, Line: m.Line,
		Props: map[string]string{"lang": "php", "file_kind": f.src.file.Kind, "end": itoa(endLine(n)), "class": c.FQN},
	})
	g.AddEdge(m.ID, c.ID, graph.MemberOf, 1, f.src.evidence(n, ""))
}

// ---- hierarchy ----

// ancestors lists a class, its parents, traits and interfaces (also external ones, by name), nearest first.
func (p *php) ancestors(fqn string) []string {
	var out []string
	seen := map[string]bool{}
	var visit func(string)
	visit = func(x string) {
		if x == "" || seen[x] {
			return
		}
		seen[x] = true
		out = append(out, x)
		c := p.classes[x]
		if c == nil {
			return
		}
		for _, t := range c.Traits {
			visit(t)
		}
		visit(c.Extends)
		for _, i := range c.Implements {
			visit(i)
		}
	}
	visit(fqn)
	return out
}

func (p *php) hasAncestorSuffix(fqn, suffix string) bool {
	for _, a := range p.ancestors(fqn) {
		if a == suffix || strings.HasSuffix(a, "\\"+suffix) {
			return true
		}
	}
	return false
}

// findMethod looks a method up in a class, its traits, parents and interfaces.
func (p *php) findMethod(fqn, name string) *phpMethod {
	for _, a := range p.ancestors(fqn) {
		if c := p.classes[a]; c != nil {
			if m := c.Methods[name]; m != nil {
				return m
			}
		}
	}
	return nil
}

func (p *php) findProp(fqn, name string) *phpProp {
	for _, a := range p.ancestors(fqn) {
		if c := p.classes[a]; c != nil {
			if pr := c.Props[name]; pr != nil {
				return pr
			}
		}
	}
	return nil
}

func (p *php) docProp(fqn, name string) string {
	for _, a := range p.ancestors(fqn) {
		if c := p.classes[a]; c != nil {
			if t := c.DocProps[name]; t != "" {
				return t
			}
		}
	}
	return ""
}

func (p *php) constValue(fqn, name string) (string, bool) {
	for _, a := range p.ancestors(fqn) {
		if c := p.classes[a]; c != nil {
			if v, ok := c.Consts[name]; ok {
				return v, true
			}
		}
	}
	return "", false
}

// implementers lists every project class below a parent or interface, transitively.
func (p *php) implementers(fqn string) []string {
	var out []string
	seen := map[string]bool{fqn: true}
	var visit func(string)
	visit = func(x string) {
		for _, c := range p.impls[x] {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
				visit(c)
			}
		}
	}
	visit(fqn)
	sort.Strings(out)
	return out
}

// findFactories detects static methods that build an instance of the called class and run one of its methods:
// `Feature::with(...)` reflects the called class and runs `handle()`, so a call `CreateX::with()` reaches `CreateX::handle`.
func (p *php) findFactories() {
	for _, c := range p.classes {
		for _, m := range c.Methods {
			if !m.Static || m.Body == nil {
				continue
			}
			src := c.File.src
			body := src.text(m.Body)
			if !strings.Contains(body, "get_called_class") && !strings.Contains(body, "static::class") && !strings.Contains(body, "new static") {
				continue
			}
			target := ""
			walk(m.Body, func(x *ts.Node) bool {
				if x.Kind() == "member_call_expression" {
					if nm := field(x, "name"); nm != nil {
						name := src.text(nm)
						if name != "newInstanceArgs" && name != "newInstance" && name != "getName" {
							target = name
						}
					}
				}
				return true
			})
			if target != "" {
				p.factories[m.ID] = target
			}
		}
	}
}

// walk visits n and its descendants depth first; returning false skips the children.
func walk(n *ts.Node, fn func(*ts.Node) bool) {
	if n == nil {
		return
	}
	if !fn(n) {
		return
	}
	for i := uint(0); i < n.ChildCount(); i++ {
		walk(n.Child(i), fn)
	}
}

// stringLiteral returns the value of a plain string node, or "".
func stringLiteral(s *source, n *ts.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "string", "encapsed_string", "string_value", "interpreted_string_literal", "raw_string_literal":
		for _, k := range kids(n) {
			if k.Kind() != "string_content" && k.Kind() != "string_value" && k.Kind() != "interpreted_string_literal_content" && k.Kind() != "raw_string_literal_content" {
				return "" // interpolation
			}
		}
		t := s.text(n)
		if len(t) >= 2 {
			return t[1 : len(t)-1]
		}
	}
	return ""
}

func itoa(i int) string { return strconv.Itoa(i) }
