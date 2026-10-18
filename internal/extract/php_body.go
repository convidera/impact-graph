package extract

import (
	"regexp"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/convidera/impact-graph/internal/graph"
)

// Types are strings: a class FQN, "q:<FQN>" for a query builder of a model, "qt:<table>" for a query on a table
// (DB::table), or "" when unknown. An unknown type produces no edge: a wrong edge is worse than a missing one.

type scope struct {
	p      *php
	f      *phpFile
	cls    *phpClass
	from   string
	vars   map[string]string
	parent *scope
	skip   map[uintptr]bool // string nodes already consumed as column keys

	collect *[]string // while set, string values are collected instead of linked (the conditions of a match arm)
	armTag  string    // the vocabulary nodes the call of the current match arm is conditional on
	armCall uintptr   // the call node that carries armTag
}

func (s *scope) lookup(name string) string {
	for c := s; c != nil; c = c.parent {
		if t, ok := c.vars[name]; ok {
			return t
		}
	}
	return ""
}

func (s *scope) child() *scope {
	return &scope{p: s.p, f: s.f, cls: s.cls, from: s.from, vars: map[string]string{}, parent: s, skip: s.skip, armTag: s.armTag, armCall: s.armCall}
}

// analyze resolves every method body of the file into edges.
func (p *php) analyze(f *phpFile) {
	for _, c := range p.classes {
		if c.File != f {
			continue
		}
		for _, m := range c.Methods {
			if m.Body == nil {
				continue
			}
			p.analyzeMethod(f, c, m)
		}
		p.addUses(c)
	}
}

// analyzeMethod resolves one body. A construct the extractor does not expect must cost one function, not the index.
func (p *php) analyzeMethod(f *phpFile, c *phpClass, m *phpMethod) {
	defer func() {
		if r := recover(); r != nil {
			p.ctx.warn("%s: skipped %s: %v", f.src.file.Path, m.ID, r)
		}
	}()
	s := &scope{p: p, f: f, cls: c, from: m.ID, vars: map[string]string{}, skip: map[uintptr]bool{}}
	if !m.Static {
		s.vars["this"] = c.FQN
	}
	for _, pr := range m.Params {
		if pr.Type != "" {
			s.vars[pr.Name] = s.self(pr.Type)
		}
	}
	s.eval(m.Body)
}

// addUses records the structural edges of a class: what it extends, implements and mixes in.
func (p *php) addUses(c *phpClass) {
	ev := c.File.src.file.Path
	for _, parent := range append(append([]string{c.Extends}, c.Implements...), c.Traits...) {
		if pc := p.classes[parent]; pc != nil && parent != "" {
			p.ctx.G.AddEdge(c.ID, pc.ID, graph.Extends, 1, ev)
		}
	}
}

func (s *scope) self(t string) string {
	switch t {
	case "self", "static":
		return s.cls.FQN
	case "parent":
		return s.cls.Extends
	}
	return t
}

func (s *scope) ev(n *ts.Node, note string) string { return s.f.src.evidence(n, note) }

func (s *scope) edge(dst, kind string, conf float64, n *ts.Node, note string) {
	if dst == "" || !s.p.ctx.G.Has(dst) {
		return
	}
	tag := ""
	if kind == graph.Calls && s.armCall != 0 && n.Id() == s.armCall {
		tag = s.armTag
	}
	s.p.ctx.G.AddTagged(s.from, dst, kind, conf, s.ev(n, note), tag)
}

func argsOf(n *ts.Node) []*ts.Node {
	if n == nil {
		return nil
	}
	var out []*ts.Node
	for _, a := range kids(n) {
		if a.Kind() != "argument" {
			continue
		}
		k := kids(a)
		if len(k) > 0 {
			out = append(out, k[len(k)-1])
		}
	}
	return out
}

func (s *scope) evalAll(ns []*ts.Node) {
	for _, n := range ns {
		s.eval(n)
	}
}

func (s *scope) eval(n *ts.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "variable_name":
		return s.lookup(strings.TrimPrefix(s.f.src.text(n), "$"))
	case "parenthesized_expression":
		if k := kids(n); len(k) > 0 {
			return s.eval(k[0])
		}
	case "object_creation_expression":
		return s.onNew(n)
	case "member_call_expression", "nullsafe_member_call_expression":
		return s.onMemberCall(n)
	case "scoped_call_expression":
		return s.onStaticCall(n)
	case "member_access_expression", "nullsafe_member_access_expression":
		return s.onProp(n, false)
	case "class_constant_access_expression":
		return s.onClassConst(n)
	case "assignment_expression":
		return s.onAssign(n)
	case "augmented_assignment_expression":
		if l := field(n, "left"); l != nil && (l.Kind() == "member_access_expression") {
			s.onProp(l, false)
			s.onProp(l, true)
		} else {
			s.eval(l)
		}
		s.eval(field(n, "right"))
		return ""
	case "anonymous_function", "arrow_function":
		s.onClosure(n)
		return ""
	case "string", "encapsed_string":
		s.onString(n)
		return ""
	case "binary_expression":
		l, r := s.eval(field(n, "left")), s.eval(field(n, "right"))
		if op := n.ChildByFieldName("operator"); op != nil && s.f.src.text(op) == "??" {
			if l != "" {
				return l
			}
			return r
		}
		return ""
	case "conditional_expression":
		var t string
		for _, k := range kids(n) {
			if v := s.eval(k); t == "" {
				t = v
			}
		}
		return t
	case "function_call_expression":
		return s.onFuncCall(n)
	case "expression_statement":
		s.docVars(n)
	case "catch_clause":
		if tn := firstKid(n, "type_list"); tn != nil {
			for _, k := range kids(tn) {
				if t := s.f.typeFromNode(k); t != "" {
					s.useClass(t, k)
				}
			}
		}
	case "match_conditional_expression":
		return s.onMatchArm(n)
	}
	for _, k := range kids(n) {
		s.eval(k)
	}
	return ""
}

// docVars applies `/** @var Type $name */` written above a statement.
func (s *scope) docVars(n *ts.Node) {
	if prev := n.PrevSibling(); prev != nil && prev.Kind() == "comment" {
		if m := docVar.FindStringSubmatch(s.f.src.text(prev)); m != nil && m[2] != "" {
			if t := s.f.docType(m[1]); t != "" {
				s.vars[m[2]] = t
			}
		}
	}
}

func (s *scope) onClosure(n *ts.Node) {
	c := s.child()
	for _, pr := range s.p.params(s.f, field(n, "parameters")) {
		if pr.Type != "" {
			c.vars[pr.Name] = c.self(pr.Type)
		} else {
			c.vars[pr.Name] = ""
		}
	}
	c.eval(field(n, "body"))
}

func (s *scope) onAssign(n *ts.Node) string {
	l, r := field(n, "left"), field(n, "right")
	rt := s.eval(r)
	if l == nil {
		return rt
	}
	switch l.Kind() {
	case "variable_name":
		name := strings.TrimPrefix(s.f.src.text(l), "$")
		if rt != "" {
			s.vars[name] = rt
		} else if _, ok := s.vars[name]; !ok {
			s.vars[name] = ""
		}
	case "member_access_expression", "nullsafe_member_access_expression":
		s.onProp(l, true)
	default:
		s.eval(l)
	}
	return rt
}

func (s *scope) onString(n *ts.Node) {
	if s.skip[n.Id()] {
		return
	}
	val := stringLiteral(s.f.src, n)
	if val == "" {
		return
	}
	s.mention(val, n, 0.9)
	if strings.Count(val, ".") == 1 && !strings.ContainsAny(val, " /") {
		if tbl, col, ok := strings.Cut(val, "."); ok {
			if id := s.p.columnID(tbl, col); id != "" {
				s.edge(id, graph.Reads, 0.8, n, "column named in a string")
			}
		}
	}
}

func (s *scope) mention(val string, n *ts.Node, conf float64) {
	if s.collect != nil {
		*s.collect = append(*s.collect, val)
		return
	}
	s.p.ctx.Vocab.mention(s.p.ctx, s.from, s.f.src.file.Path, val, s.ev(n, "literal "+val), conf, true)
}

// mentionConst is mention for a value that comes in through a class constant or an enum case.
func (s *scope) mentionConst(val string, n *ts.Node, conf float64) {
	if s.collect != nil {
		*s.collect = append(*s.collect, val)
		return
	}
	s.p.ctx.Vocab.mention(s.p.ctx, s.from, s.f.src.file.Path, val, s.ev(n, "constant "+val), conf, false)
}

func (s *scope) useClass(fqn string, n *ts.Node) {
	if c := s.p.classes[fqn]; c != nil {
		s.edge(c.ID, graph.Uses, 1, n, "")
	}
}

// createsClass is useClass for a `new X(...)` instantiation: the edge is `creates`, not `uses`.
func (s *scope) createsClass(fqn string, n *ts.Node) {
	if c := s.p.classes[fqn]; c != nil {
		s.edge(c.ID, graph.Creates, 1, n, "")
	}
}

func (s *scope) resolveScope(n *ts.Node) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "relative_scope":
		return s.self(s.f.src.text(n))
	case "name", "qualified_name":
		t := s.f.src.text(n)
		if t == "self" || t == "static" || t == "parent" {
			return s.self(t)
		}
		return s.f.resolveName(t)
	case "variable_name":
		return s.lookup(strings.TrimPrefix(s.f.src.text(n), "$"))
	}
	return ""
}

func (s *scope) onNew(n *ts.Node) string {
	if firstKid(n, "declaration_list") != nil {
		return ""
	}
	var cn *ts.Node
	for _, k := range kids(n) {
		if k.Kind() == "name" || k.Kind() == "qualified_name" || k.Kind() == "relative_scope" {
			cn = k
			break
		}
	}
	args := argsOf(firstKid(n, "arguments"))
	fqn := s.resolveScope(cn)
	c := s.p.classes[fqn]
	if c == nil {
		s.evalAll(args)
		return fqn
	}
	s.createsClass(fqn, n)
	if ctor := s.p.findMethod(fqn, "__construct"); ctor != nil {
		s.edge(ctor.ID, graph.Calls, 1, n, "new "+c.Short)
	}
	if s.p.isModel(fqn) {
		s.markKeys(args)
		s.modelWrite(fqn, "make", args, n)
	}
	if s.p.hasAncestorSuffix(fqn, "JsonResource") {
		if m := s.p.findMethod(fqn, "toArray"); m != nil {
			s.edge(m.ID, graph.Calls, 0.8, n, "framework: JsonResource::toArray")
		}
	}
	s.evalAll(args)
	return fqn
}

func (s *scope) onClassConst(n *ts.Node) string {
	k := kids(n)
	if len(k) != 2 {
		return ""
	}
	name := s.f.src.text(k[1])
	fqn := s.resolveScope(k[0])
	if fqn == "" {
		return ""
	}
	s.useClass(fqn, n)
	if name == "class" {
		return ""
	}
	if v, ok := s.p.constValue(fqn, name); ok {
		s.mentionConst(v, n, 0.85)
	}
	return ""
}

func (s *scope) onFuncCall(n *ts.Node) string {
	fn := field(n, "function")
	args := argsOf(field(n, "arguments"))
	types := make([]string, len(args))
	for i, a := range args {
		types[i] = s.eval(a)
	}
	if fn == nil || (fn.Kind() != "name" && fn.Kind() != "qualified_name") {
		return ""
	}
	name := s.f.src.text(fn)
	switch name {
	case "app", "resolve":
		if len(args) == 1 && args[0].Kind() == "class_constant_access_expression" {
			if k := kids(args[0]); len(k) == 2 {
				return s.resolveScope(k[0])
			}
		}
	case "dispatch", "dispatch_sync":
		if len(types) > 0 && types[0] != "" {
			if m := s.p.findMethod(types[0], "handle"); m != nil {
				s.edge(m.ID, graph.Calls, 0.85, n, "framework: dispatched job")
			}
		}
	}
	for _, cand := range []string{s.f.ns + "\\" + name, name} {
		if c := s.p.classes[strings.TrimPrefix(cand, "\\")]; c != nil && c.Kind == "function" {
			s.edge(c.ID, graph.Calls, 1, n, "")
			if m := c.Methods[c.Short]; m != nil {
				return m.Ret
			}
		}
	}
	return ""
}

// ---- calls ----

func (s *scope) onMemberCall(n *ts.Node) string {
	obj, nm := field(n, "object"), field(n, "name")
	args := argsOf(field(n, "arguments"))
	ot := s.eval(obj)
	if nm == nil || nm.Kind() != "name" {
		s.evalAll(args)
		return ""
	}
	name := s.f.src.text(nm)
	switch {
	case strings.HasPrefix(ot, "q:") || strings.HasPrefix(ot, "qt:"):
		return s.builderCall(ot, name, args, n)
	case ot == "":
		s.evalAll(args)
		s.guessColumns(name, args, n)
		return s.guessMethod(name, n)
	}
	return s.callMethod(ot, name, args, n, false)
}

func (s *scope) onStaticCall(n *ts.Node) string {
	sc, nm := field(n, "scope"), field(n, "name")
	args := argsOf(field(n, "arguments"))
	fqn := s.resolveScope(sc)
	if nm == nil || nm.Kind() != "name" {
		s.evalAll(args)
		return ""
	}
	name := s.f.src.text(nm)
	if fqn == "" {
		s.evalAll(args)
		return ""
	}
	if fqn == "Illuminate\\Support\\Facades\\DB" || fqn == "DB" {
		if name == "table" && len(args) > 0 {
			if t := stringLiteral(s.f.src, args[0]); t != "" {
				s.skip[args[0].Id()] = true
				s.evalAll(args)
				return "qt:" + t
			}
		}
		s.evalAll(args)
		return ""
	}
	return s.callMethod(fqn, name, args, n, true)
}

// callMethod resolves a call on a class (instance or static) and returns the type of the result.
func (s *scope) callMethod(fqn, name string, args []*ts.Node, n *ts.Node, static bool) string {
	c := s.p.classes[fqn]
	if c == nil {
		s.evalAll(args)
		return ""
	}
	s.useClass(fqn, n)
	if m := s.p.findMethod(fqn, name); m != nil {
		s.evalAll(args)
		if target, ok := s.p.factories[m.ID]; ok && static {
			if t := s.p.findMethod(fqn, target); t != nil {
				s.edge(t.ID, graph.Calls, 0.9, n, "factory "+m.Class.Short+"::"+m.Name)
				return s.self(t.Ret)
			}
		}
		s.edge(m.ID, graph.Calls, 0.95, n, "")
		// a call through an interface or abstract parent may run any implementation
		if mc := m.Class; mc.Kind == "interface" || m.Body == nil {
			for _, impl := range s.p.implementers(mc.FQN) {
				if im := s.p.classes[impl].Methods[name]; im != nil && im.Body != nil {
					s.edge(im.ID, graph.Calls, 0.85, n, "implementation of "+mc.Short)
				}
			}
		}
		if m.Ret == "" {
			return ""
		}
		if strings.HasPrefix(m.Ret, "q:") {
			return m.Ret
		}
		return s.p.selfOf(m.Ret, fqn)
	}
	if s.p.isModel(fqn) {
		if static {
			if sm := s.p.findMethod(fqn, "scope"+strings.ToUpper(name[:1])+name[1:]); sm != nil {
				s.edge(sm.ID, graph.Calls, 0.9, n, "local scope")
			}
			return s.builderCall("q:"+fqn, name, args, n)
		}
		return s.modelCall(fqn, name, args, n)
	}
	// framework hooks: a resource or job is run by the framework, not by a call
	switch {
	case s.p.hasAncestorSuffix(fqn, "JsonResource") && (name == "make" || name == "collection" || name == "resolve"):
		if m := s.p.findMethod(fqn, "toArray"); m != nil {
			s.edge(m.ID, graph.Calls, 0.8, n, "framework: JsonResource::toArray")
		}
		s.evalAll(args)
		return fqn
	case s.p.hasAncestorSuffix(fqn, "Dispatchable") && strings.HasPrefix(name, "dispatch"):
		if m := s.p.findMethod(fqn, "handle"); m != nil {
			s.edge(m.ID, graph.Calls, 0.9, n, "framework: dispatched job")
		}
	}
	s.evalAll(args)
	return ""
}

func (p *php) selfOf(t, fqn string) string {
	if t == "self" || t == "static" {
		return fqn
	}
	return t
}

var commonNames = map[string]bool{
	"get": true, "set": true, "all": true, "add": true, "has": true, "make": true, "find": true, "first": true, "load": true,
	"save": true, "map": true, "each": true, "push": true, "pull": true, "put": true, "run": true, "handle": true, "toArray": true,
	"count": true, "delete": true, "update": true, "create": true, "render": true, "format": true, "value": true, "values": true,
	"keys": true, "filter": true, "merge": true, "toJson": true, "json": true, "input": true, "query": true, "where": true,
	"validate": true, "authorize": true, "user": true, "id": true, "name": true, "__construct": true, "boot": true, "register": true,
}

// guessMethod links a call on an untyped receiver to the one project method that has that name. It is a guess, so the
// edge has low confidence and common names are never guessed.
func (s *scope) guessMethod(name string, n *ts.Node) string {
	if len(name) < 6 || commonNames[name] {
		return ""
	}
	var found *phpMethod
	for _, c := range s.p.classes {
		if m := c.Methods[name]; m != nil && c.Kind != "function" {
			if found != nil {
				return ""
			}
			found = m
		}
	}
	if found != nil {
		s.edge(found.ID, graph.Calls, 0.55, n, "receiver type unknown, only method with this name")
		return found.Ret
	}
	return ""
}

// ---- Eloquent ----

var (
	identRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(\.[A-Za-z_][A-Za-z0-9_]*)?$`)

	// builder methods whose first string argument names a column
	colMethods = map[string]bool{
		"orderBy": true, "orderByDesc": true, "latest": true, "oldest": true, "groupBy": true, "pluck": true, "value": true,
		"sum": true, "avg": true, "min": true, "max": true, "count": true, "having": true, "orHaving": true, "distinct": true,
		"increment": true, "decrement": true, "whereKey": false, "select": true, "addSelect": true,
	}
	terminalModel = map[string]bool{
		"first": true, "firstOrFail": true, "find": true, "findOrFail": true, "sole": true, "firstOrCreate": true,
		"updateOrCreate": true, "create": true, "forceCreate": true, "firstWhere": true, "findOrNew": true, "firstOrNew": true, "make": true,
	}
	terminalOther = map[string]bool{
		"get": true, "all": true, "paginate": true, "simplePaginate": true, "cursor": true, "pluck": true, "value": true,
		"sum": true, "avg": true, "min": true, "max": true, "count": true, "exists": true, "doesntExist": true, "delete": true,
		"update": true, "insert": true, "insertOrIgnore": true, "upsert": true, "toArray": true, "toSql": true, "chunk": true,
		"each": true, "destroy": true, "forceDelete": true, "truncate": true, "findMany": true, "increment": true, "decrement": true,
	}
	writeFirst  = map[string]bool{"create": true, "forceCreate": true, "make": true, "fill": true, "forceFill": true, "update": true, "insert": true, "insertOrIgnore": true, "insertGetId": true, "upsert": true}
	matchValues = map[string]bool{"updateOrCreate": true, "firstOrCreate": true, "firstOrNew": true, "updateOrInsert": true}
	modelFluent = map[string]bool{"update": true, "fill": true, "forceFill": true, "save": true, "load": true, "loadMissing": true, "fresh": true, "refresh": true, "setAttribute": true, "withoutRelations": true, "replicate": true, "increment": true, "decrement": true, "touch": true}
)

func isWhere(name string) bool {
	return strings.HasPrefix(name, "where") || strings.HasPrefix(name, "orWhere") || name == "has" || name == "whereHas"
}

func (s *scope) tableOf(t string) (table, model string) {
	switch {
	case strings.HasPrefix(t, "q:"):
		model = t[2:]
		return s.p.tableOfModel(model), model
	case strings.HasPrefix(t, "qt:"):
		return t[3:], ""
	}
	return s.p.tableOfModel(t), t
}

// colRef reads a column name argument: `col`, `table.col` or `col as alias`.
func (s *scope) colRef(table string, a *ts.Node) (tbl, col string) {
	v := stringLiteral(s.f.src, a)
	if i := strings.Index(strings.ToLower(v), " as "); i > 0 {
		v = v[:i]
	}
	v = strings.TrimSpace(v)
	if !identRe.MatchString(v) {
		return "", ""
	}
	if t, c, ok := strings.Cut(v, "."); ok {
		return t, c
	}
	return table, v
}

func (s *scope) column(table string, a *ts.Node, kind string, conf float64, n *ts.Node, note string) {
	t, c := s.colRef(table, a)
	if id := s.p.columnID(t, c); id != "" {
		s.skip[a.Id()] = true
		s.edge(id, kind, conf, n, note)
	}
}

// keys returns the string keys of an array literal.
func (s *scope) keys(a *ts.Node) []*ts.Node {
	var out []*ts.Node
	if a == nil || a.Kind() != "array_creation_expression" {
		return nil
	}
	for _, el := range kids(a) {
		if el.Kind() != "array_element_initializer" {
			continue
		}
		k := kids(el)
		if len(k) == 2 && stringLiteral(s.f.src, k[0]) != "" {
			out = append(out, k[0])
		} else if len(k) == 1 && k[0].Kind() == "array_creation_expression" {
			out = append(out, s.keys(k[0])...) // insert([[...], [...]])
		}
	}
	return out
}

func (s *scope) markKeys(args []*ts.Node) {
	for _, a := range args {
		for _, k := range s.keys(a) {
			s.skip[k.Id()] = true
		}
	}
}

func (s *scope) writeKeys(table string, a *ts.Node, kind string, n *ts.Node, note string) {
	for _, k := range s.keys(a) {
		s.column(table, k, kind, 0.95, n, note)
	}
}

// builderCall handles a call on a query builder: columns in arguments are reads, array keys of writes are writes.
func (s *scope) builderCall(t, name string, args []*ts.Node, n *ts.Node) string {
	table, model := s.tableOf(t)
	if table != "" {
		note := "query on " + table + ": " + name
		switch {
		case isWhere(name) && len(args) > 0 && name != "whereHas" && name != "has":
			s.column(table, args[0], graph.Reads, 0.95, n, note)
			if name == "whereColumn" && len(args) > 2 {
				s.column(table, args[2], graph.Reads, 0.95, n, note)
			}
		case colMethods[name]:
			for i, a := range args {
				if i == 0 || name == "select" || name == "addSelect" || name == "groupBy" || (name == "pluck" && i == 1) {
					s.column(table, a, graph.Reads, 0.95, n, note)
				}
			}
			if name == "increment" || name == "decrement" {
				s.column(table, args[0], graph.Writes, 0.95, n, note)
			}
		case strings.HasSuffix(name, "oin") && len(args) >= 3: // join, leftJoin, rightJoin, crossJoin
			if tn := stringLiteral(s.f.src, args[0]); tn != "" {
				s.skip[args[0].Id()] = true
			}
			s.column(table, args[1], graph.Reads, 0.95, n, note)
			if len(args) > 3 {
				s.column(table, args[3], graph.Reads, 0.95, n, note)
			}
		}
		switch {
		case writeFirst[name] && len(args) > 0:
			s.writeKeys(table, args[0], graph.Writes, n, note)
			if name == "upsert" && len(args) > 2 {
				for _, e := range kids(args[2]) {
					for _, x := range kids(e) {
						s.column(table, x, graph.Writes, 0.9, n, note)
					}
				}
			}
		case matchValues[name] && len(args) > 0:
			s.writeKeys(table, args[0], graph.Reads, n, note)
			if len(args) > 1 {
				s.writeKeys(table, args[1], graph.Writes, n, note)
			}
			if name == "updateOrCreate" || name == "firstOrCreate" || name == "firstOrNew" {
				s.writeKeys(table, args[0], graph.Writes, n, note)
			}
		case name == "setAttribute" && len(args) > 0:
			s.column(table, args[0], graph.Writes, 0.95, n, note)
		}
	}
	s.evalAll(args)
	if model != "" {
		if sm := s.p.findMethod(model, "scope"+strings.ToUpper(name[:1])+name[1:]); sm != nil {
			s.edge(sm.ID, graph.Calls, 0.9, n, "local scope")
		}
		if terminalModel[name] {
			return model
		}
	}
	if terminalOther[name] || terminalModel[name] {
		return ""
	}
	return t
}

// modelCall handles an instance method of a model (fill, update, ...).
func (s *scope) modelCall(model, name string, args []*ts.Node, n *ts.Node) string {
	table := s.p.tableOfModel(model)
	if table != "" {
		note := "model " + s.p.classes[model].Short + ": " + name
		switch {
		case (writeFirst[name]) && len(args) > 0:
			s.writeKeys(table, args[0], graph.Writes, n, note)
		case name == "setAttribute" && len(args) > 0:
			s.column(table, args[0], graph.Writes, 0.95, n, note)
		case name == "increment" || name == "decrement":
			if len(args) > 0 {
				s.column(table, args[0], graph.Writes, 0.95, n, note)
			}
		}
	}
	s.evalAll(args)
	if modelFluent[name] {
		return model
	}
	return ""
}

func (s *scope) modelWrite(model, name string, args []*ts.Node, n *ts.Node) {
	table := s.p.tableOfModel(model)
	if table == "" || len(args) == 0 {
		return
	}
	s.writeKeys(table, args[0], graph.Writes, n, "new "+s.p.classes[model].Short)
}

// guessColumns links a column argument of a builder call on an untyped receiver to the one table that has such a column.
func (s *scope) guessColumns(name string, args []*ts.Node, n *ts.Node) {
	if len(args) == 0 || (!isWhere(name) && !colMethods[name]) {
		return
	}
	v := stringLiteral(s.f.src, args[0])
	if !strings.Contains(v, "_") || !identRe.MatchString(v) || strings.Contains(v, ".") {
		return
	}
	if owners := s.p.colOwners[v]; len(owners) == 1 {
		if id := s.p.columnID(owners[0], v); id != "" {
			s.edge(id, graph.Reads, 0.6, n, "receiver type unknown, only table with column "+v)
		}
	}
}

// onProp handles `$obj->name`: a column of a model when `$obj` is one, otherwise a typed property.
func (s *scope) onProp(n *ts.Node, write bool) string {
	obj, nm := field(n, "object"), field(n, "name")
	ot := s.eval(obj)
	if nm == nil || nm.Kind() != "name" || ot == "" {
		return ""
	}
	name := s.f.src.text(nm)
	c := s.p.classes[ot]
	if c == nil {
		return ""
	}
	if pr := s.p.findProp(ot, name); pr != nil {
		if pr.Type != "" {
			return s.self(pr.Type)
		}
		return ""
	}
	if s.p.isModel(ot) {
		if table := s.p.tableOfModel(ot); table != "" {
			kind := graph.Reads
			if write {
				kind = graph.Writes
			}
			if id := s.p.columnID(table, name); id != "" {
				s.edge(id, kind, 0.85, n, "attribute of "+c.Short)
			}
		}
		return s.p.docProp(ot, name)
	}
	if c.ResourceModel != "" {
		if name == "resource" {
			return c.ResourceModel
		}
		if table := s.p.tableOfModel(c.ResourceModel); table != "" && !write {
			if id := s.p.columnID(table, name); id != "" {
				s.edge(id, graph.Reads, 0.75, n, "resource attribute of "+s.p.classes[c.ResourceModel].Short)
			}
		}
	}
	return s.p.docProp(ot, name)
}

// onMatchArm links a `match` arm keyed by vocabulary values: the dispatcher still consumes the values, and the call in
// the arm is tagged with them, so a flow that came in through one event does not leave through the arms of the others.
func (s *scope) onMatchArm(n *ts.Node) string {
	conds, ret := field(n, "conditional_expressions"), field(n, "return_expression")
	var vals []string
	s.collect = &vals
	s.eval(conds)
	s.collect = nil
	var tags []string
	for _, v := range vals {
		s.mention(v, conds, 0.9)
		if s.p.ctx.Vocab != nil {
			for _, ref := range s.p.ctx.Vocab.byValue[v] {
				tags = append(tags, ref.node)
			}
		}
	}
	prevTag, prevCall := s.armTag, s.armCall
	if len(tags) > 0 && ret != nil {
		switch ret.Kind() {
		case "member_call_expression", "nullsafe_member_call_expression", "scoped_call_expression":
			s.armTag, s.armCall = strings.Join(tags, ","), ret.Id()
		}
	}
	t := s.eval(ret)
	s.armTag, s.armCall = prevTag, prevCall
	return t
}
