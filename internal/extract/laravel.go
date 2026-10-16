package extract

import (
	"sort"
	"strings"

	ts "github.com/tree-sitter/go-tree-sitter"

	"github.com/convidera/impact-graph/internal/graph"
)

// The Laravel pack: what the framework does that no parser can see. The database schema comes from the migrations, a
// model's table from its name, a route's handler from the routes files, an MCP tool's name from its class.

var schemaIgnored = map[string]bool{
	"index": true, "unique": true, "primary": true, "foreign": true, "fullText": true, "spatialIndex": true, "rawIndex": true,
	"dropIndex": true, "dropUnique": true, "dropPrimary": true, "dropForeign": true, "dropFullText": true, "dropSpatialIndex": true,
	"comment": true, "engine": true, "charset": true, "collation": true, "temporary": true, "rename": true, "foreignIdFor": true,
	"dropMorphs": true, "dropConstrainedForeignIdFor": true, "dropForeignIdFor": true, "constrained": true, "after": true,
}

type chainCall struct {
	name string
	args []*ts.Node
	node *ts.Node
}

// unroll flattens `$a->b(1)->c(2)` into the calls from the root outwards and the root node.
func unroll(n *ts.Node, src *source) ([]chainCall, *ts.Node) {
	var calls []chainCall
	cur := n
	for cur != nil && (cur.Kind() == "member_call_expression" || cur.Kind() == "nullsafe_member_call_expression") {
		nm := field(cur, "name")
		if nm != nil {
			calls = append(calls, chainCall{src.text(nm), argsOf(field(cur, "arguments")), cur})
		}
		cur = field(cur, "object")
	}
	for i, j := 0, len(calls)-1; i < j; i, j = i+1, j-1 {
		calls[i], calls[j] = calls[j], calls[i]
	}
	return calls, cur
}

func (p *php) columnID(table, col string) string {
	if t := p.tables[table]; t != nil {
		if _, ok := t.Cols[col]; ok {
			return "column:" + table + "." + col
		}
	}
	return ""
}

func (p *php) tableInfo(name string) *tableInfo {
	t := p.tables[name]
	if t == nil {
		t = &tableInfo{Name: name, Cols: map[string]int{}, File: map[string]string{}}
		p.tables[name] = t
	}
	return t
}

func (p *php) buildSchema() {
	var migs []*phpFile
	for _, f := range p.files {
		if strings.Contains(f.src.file.Path, "database/migrations/") {
			migs = append(migs, f)
		}
	}
	sort.Slice(migs, func(i, j int) bool { return migs[i].src.file.Path < migs[j].src.file.Path })
	created := map[string][2]string{}
	for _, f := range migs {
		walk(f.src.root, func(n *ts.Node) bool {
			if n.Kind() == "method_declaration" {
				if nm := field(n, "name"); nm != nil && f.src.text(nm) == "down" {
					return false // rollback code describes the previous schema, not the current one
				}
			}
			if n.Kind() != "scoped_call_expression" {
				return true
			}
			sc := field(n, "scope")
			if sc == nil || lastSegment(f.src.text(sc), "\\") != "Schema" {
				return true
			}
			p.schemaCall(f, n, created)
			return true
		})
	}
	g := p.ctx.G
	names := make([]string, 0, len(p.tables))
	for name := range p.tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		t := p.tables[name]
		c := created[name]
		g.AddNode(graph.Node{ID: "table:" + name, Type: graph.TypeTable, Label: name, Path: c[0], Line: atoi(c[1])})
		for col, ln := range t.Cols {
			id := "column:" + name + "." + col
			g.AddNode(graph.Node{ID: id, Type: graph.TypeColumn, Label: name + "." + col, Path: t.File[col], Line: ln})
			g.AddEdge(id, "table:"+name, graph.MemberOf, 1, t.File[col])
			p.colOwners[col] = append(p.colOwners[col], name)
		}
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func (p *php) schemaCall(f *phpFile, n *ts.Node, created map[string][2]string) {
	nm := field(n, "name")
	if nm == nil {
		return
	}
	args := argsOf(field(n, "arguments"))
	name := f.src.text(nm)
	str := func(i int) string {
		if i < len(args) {
			return stringLiteral(f.src, args[i])
		}
		return ""
	}
	switch name {
	case "create", "table":
		tbl := str(0)
		if tbl == "" || len(args) < 2 {
			return
		}
		info := p.tableInfo(tbl)
		if name == "create" {
			created[tbl] = [2]string{f.src.file.Path, itoa(line(n))}
		}
		closure := args[1]
		varName := "table"
		if ps := field(closure, "parameters"); ps != nil {
			if pr := p.params(f, ps); len(pr) > 0 {
				varName = pr[0].Name
			}
		}
		walk(field(closure, "body"), func(x *ts.Node) bool {
			if x.Kind() != "expression_statement" {
				return true
			}
			expr := kids(x)
			if len(expr) == 0 {
				return false
			}
			calls, root := unroll(expr[0], f.src)
			if root == nil || root.Kind() != "variable_name" || strings.TrimPrefix(f.src.text(root), "$") != varName || len(calls) == 0 {
				return true
			}
			p.columnOp(f, info, calls[0], x)
			return false
		})
	case "rename":
		if from, to := str(0), str(1); from != "" && to != "" && p.tables[from] != nil {
			p.tables[to] = p.tables[from]
			p.tables[to].Name = to
			delete(p.tables, from)
			created[to] = created[from]
		}
	case "drop", "dropIfExists":
		delete(p.tables, str(0))
	}
}

func (p *php) columnOp(f *phpFile, t *tableInfo, c chainCall, at *ts.Node) {
	first := func() string {
		if len(c.args) > 0 {
			return stringLiteral(f.src, c.args[0])
		}
		return ""
	}
	add := func(col string) {
		if col != "" {
			t.Cols[col] = line(at)
			t.File[col] = f.src.file.Path
		}
	}
	del := func(cols ...string) {
		for _, col := range cols {
			delete(t.Cols, col)
			delete(t.File, col)
		}
	}
	strs := func() []string {
		var out []string
		for _, a := range c.args {
			if v := stringLiteral(f.src, a); v != "" {
				out = append(out, v)
			}
			if a.Kind() == "array_creation_expression" {
				for _, el := range kids(a) {
					for _, x := range kids(el) {
						if v := stringLiteral(f.src, x); v != "" {
							out = append(out, v)
						}
					}
				}
			}
		}
		return out
	}
	switch c.name {
	case "id":
		if v := first(); v != "" {
			add(v)
		} else {
			add("id")
		}
	case "timestamps", "timestampsTz", "nullableTimestamps":
		add("created_at")
		add("updated_at")
	case "softDeletes", "softDeletesTz":
		if v := first(); v != "" {
			add(v)
		} else {
			add("deleted_at")
		}
	case "rememberToken":
		add("remember_token")
	case "morphs", "nullableMorphs", "uuidMorphs", "nullableUuidMorphs", "ulidMorphs", "nullableUlidMorphs":
		add(first() + "_type")
		add(first() + "_id")
	case "dropColumn", "dropConstrainedForeignId", "dropForeignId":
		del(strs()...)
	case "dropTimestamps", "dropTimestampsTz":
		del("created_at", "updated_at")
	case "dropSoftDeletes", "dropSoftDeletesTz":
		del("deleted_at")
	case "dropRememberToken":
		del("remember_token")
	case "renameColumn":
		if len(c.args) >= 2 {
			from, to := stringLiteral(f.src, c.args[0]), stringLiteral(f.src, c.args[1])
			if _, ok := t.Cols[from]; ok && to != "" {
				del(from)
				add(to)
			}
		}
	default:
		if !schemaIgnored[c.name] && !strings.HasPrefix(c.name, "drop") {
			add(first())
		}
	}
}

// ---- models ----

func (p *php) isModel(fqn string) bool {
	if v, ok := p.modelMemo[fqn]; ok {
		return v
	}
	p.modelMemo[fqn] = false
	for _, a := range p.ancestors(fqn) {
		if (strings.HasPrefix(a, "Illuminate\\Database\\Eloquent\\") && (strings.HasSuffix(a, "\\Model") || strings.HasSuffix(a, "\\Pivot"))) ||
			a == "Illuminate\\Foundation\\Auth\\User" {
			p.modelMemo[fqn] = true
			return true
		}
	}
	return false
}

func (p *php) tableOfModel(fqn string) string {
	if fqn == "" {
		return ""
	}
	if c := p.classes[fqn]; c != nil {
		return c.Table
	}
	return ""
}

// assignModels gives every model its table (`$table` or the plural snake-case class name) when the schema has it.
func (p *php) assignModels() {
	for _, c := range p.classes {
		if c.Kind != "class" || !p.isModel(c.FQN) {
			continue
		}
		var cands []string
		for _, a := range p.ancestors(c.FQN) {
			if pc := p.classes[a]; pc != nil {
				if t := pc.Consts["$table"]; t != "" {
					cands = append(cands, t)
				}
			}
		}
		cands = append(cands, plural(snake(c.Short)))
		for _, t := range cands {
			if p.tables[t] != nil {
				c.Table = t
				p.ctx.G.AddEdge(c.ID, "table:"+t, graph.MapsTo, 0.95, c.File.src.file.Path)
				break
			}
		}
	}
}

// assignResources ties a JsonResource to the model it presents: `TimesheetEntryDisplayResource` shows `TimesheetEntry`.
func (p *php) assignResources() {
	for _, c := range p.classes {
		if c.Kind != "class" || !p.hasAncestorSuffix(c.FQN, "JsonResource") {
			continue
		}
		name := strings.TrimSuffix(c.Short, "Resource")
		words := splitCamel(name)
		for n := len(words); n > 0; n-- {
			cand := strings.Join(words[:n], "")
			found := ""
			for _, m := range p.byShort[cand] {
				if m.Kind == "class" && p.isModel(m.FQN) && m.Table != "" {
					if found == "" || m.FQN == c.File.uses[cand] {
						found = m.FQN
					}
				}
			}
			if found != "" {
				c.ResourceModel = found
				break
			}
		}
	}
}

func splitCamel(s string) []string {
	var out []string
	start := 0
	for i := 1; i < len(s); i++ {
		if s[i] >= 'A' && s[i] <= 'Z' && (s[i-1] < 'A' || s[i-1] > 'Z') {
			out = append(out, s[start:i])
			start = i
		}
	}
	return append(out, s[start:])
}

// ---- MCP tools ----

// registerTools names every Laravel MCP tool and registers the name as a vocabulary value, so clients that mention the
// name as a string are joined to the tool.
func (p *php) registerTools() {
	g := p.ctx.G
	for _, c := range p.classes {
		if c.Kind != "class" || !p.hasAncestorSuffix(c.FQN, "Mcp\\Server\\Tool") || p.classes[c.FQN] == nil {
			continue
		}
		name := c.Consts["$name"]
		if name == "" {
			name = kebab(c.Short)
		}
		id := "mcp:" + name
		props := map[string]string{"class": c.FQN}
		if d := p.attributeString(c, "Description"); d != "" {
			props["description"] = d
		}
		g.AddNode(graph.Node{ID: id, Type: graph.TypeTool, Label: name, Path: c.File.src.file.Path, Line: c.Line, Props: props})
		if h := c.Methods["handle"]; h != nil {
			g.AddEdge(h.ID, id, graph.Exposes, 1, c.File.src.file.Path+":"+itoa(h.Line)+" MCP tool "+name)
		} else {
			g.AddEdge(c.ID, id, graph.Exposes, 0.9, c.File.src.file.Path)
		}
		p.ctx.Vocab.add(name, id, nil, nil)
	}
}

func (p *php) attributeString(c *phpClass, attr string) string {
	attrs := field(c.Node, "attributes")
	if attrs == nil {
		return ""
	}
	out := ""
	walk(attrs, func(n *ts.Node) bool {
		if n.Kind() == "attribute" && out == "" {
			if nm := firstKid(n, "name", "qualified_name"); nm != nil && lastSegment(c.File.src.text(nm), "\\") == attr {
				walk(n, func(x *ts.Node) bool {
					if v := stringLiteral(c.File.src, x); v != "" && out == "" {
						out = v
					}
					return true
				})
			}
		}
		return true
	})
	return out
}

// registers links a class to the classes listed in one of its properties (a server's `$tools`).
func (p *php) registers() {
	for _, c := range p.classes {
		for _, pr := range c.Props {
			if len(pr.Classes) < 2 {
				continue
			}
			for _, t := range pr.Classes {
				if tc := p.classes[t]; tc != nil {
					p.ctx.G.AddEdge(c.ID, tc.ID, graph.Registers, 0.9, c.File.src.file.Path+":"+itoa(pr.Line)+" $"+pr.Name)
				}
			}
		}
	}
}

// ---- routes ----

var routeVerbs = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE", "options": "OPTIONS", "any": "ANY"}

func (p *php) routes() {
	for _, f := range p.files {
		path := f.src.file.Path
		if !strings.HasPrefix(path, "routes/") && !strings.Contains(path, "/routes/") {
			continue
		}
		prefix := ""
		if strings.HasSuffix(path, "/api.php") || path == "routes/api.php" || strings.HasPrefix(path, "routes/api/") || strings.Contains(path, "/routes/api/") {
			prefix = "api" // routes/api.php, and the files it requires, are served under /api
		}
		p.routeWalk(f, f.src.root, prefix)
	}
}

func (p *php) routeWalk(f *phpFile, n *ts.Node, prefix string) {
	walk(n, func(x *ts.Node) bool {
		switch x.Kind() {
		case "member_call_expression":
			nm := field(x, "name")
			if nm == nil || f.src.text(nm) != "group" {
				return true
			}
			calls, root := unroll(x, f.src)
			if root == nil || root.Kind() != "name" || lastSegment(f.src.text(root), "\\") != "Route" {
				// Route::prefix('x')->group(...) has a scoped call as its root
				if root == nil || root.Kind() != "scoped_call_expression" {
					return true
				}
			}
			np := prefix
			if root != nil && root.Kind() == "scoped_call_expression" {
				if sn := field(root, "name"); sn != nil && f.src.text(sn) == "prefix" {
					if a := argsOf(field(root, "arguments")); len(a) > 0 {
						np = joinURI(np, stringLiteral(f.src, a[0]))
					}
				}
			}
			for _, c := range calls {
				if c.name == "prefix" && len(c.args) > 0 {
					np = joinURI(np, stringLiteral(f.src, c.args[0]))
				}
			}
			if last := calls[len(calls)-1]; len(last.args) > 0 {
				p.routeWalk(f, last.args[len(last.args)-1], np)
			}
			return false
		case "scoped_call_expression":
			sc, nm := field(x, "scope"), field(x, "name")
			if sc == nil || nm == nil || lastSegment(f.src.text(sc), "\\") != "Route" {
				return true
			}
			verb := f.src.text(nm)
			args := argsOf(field(x, "arguments"))
			switch {
			case routeVerbs[verb] != "" && len(args) >= 2:
				p.route(f, x, routeVerbs[verb], joinURI(prefix, stringLiteral(f.src, args[0])), args[1])
				return false
			case verb == "group" && len(args) >= 2:
				np := prefix
				if args[0].Kind() == "array_creation_expression" {
					for _, el := range kids(args[0]) {
						if k := kids(el); len(k) == 2 && stringLiteral(f.src, k[0]) == "prefix" {
							np = joinURI(np, stringLiteral(f.src, k[1]))
						}
					}
				}
				p.routeWalk(f, args[len(args)-1], np)
				return false
			case (verb == "apiResource" || verb == "resource") && len(args) >= 2:
				base := joinURI(prefix, stringLiteral(f.src, args[0]))
				for _, r := range [][3]string{{"GET", "", "index"}, {"POST", "", "store"}, {"GET", "/{id}", "show"}, {"PUT", "/{id}", "update"}, {"DELETE", "/{id}", "destroy"}} {
					p.route(f, x, r[0], base+r[1], args[1], r[2])
				}
				return false
			}
		}
		return true
	})
}

func joinURI(a, b string) string {
	a, b = strings.Trim(a, "/"), strings.Trim(b, "/")
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "/" + b
}

// route records one route and links its handler to it.
func (p *php) route(f *phpFile, call *ts.Node, verb, uri string, handler *ts.Node, forceMethod ...string) {
	id := "route:" + verb + " /" + strings.TrimPrefix(uri, "/")
	g := p.ctx.G
	props := map[string]string{}
	// ->name('x') chained after the call
	for cur := call; cur.Parent() != nil && cur.Parent().Kind() == "member_call_expression"; cur = cur.Parent() {
		par := cur.Parent()
		if o := field(par, "object"); o == nil || o.Id() != cur.Id() {
			break
		}
		if nm := field(par, "name"); nm != nil && f.src.text(nm) == "name" {
			if a := argsOf(field(par, "arguments")); len(a) > 0 {
				props["name"] = stringLiteral(f.src, a[0])
			}
		}
	}
	g.AddNode(graph.Node{ID: id, Type: graph.TypeRoute, Label: verb + " /" + strings.TrimPrefix(uri, "/"), Path: f.src.file.Path, Line: line(call), Props: props})
	var cls, method string
	switch handler.Kind() {
	case "array_creation_expression":
		var parts []*ts.Node
		for _, el := range kids(handler) {
			if k := kids(el); len(k) > 0 {
				parts = append(parts, k[0])
			}
		}
		if len(parts) == 2 && parts[0].Kind() == "class_constant_access_expression" {
			if k := kids(parts[0]); len(k) == 2 {
				cls = f.resolveName(f.src.text(k[0]))
			}
			method = stringLiteral(f.src, parts[1])
		}
	case "class_constant_access_expression":
		if k := kids(handler); len(k) == 2 {
			cls, method = f.resolveName(f.src.text(k[0])), "__invoke"
		}
	}
	if len(forceMethod) > 0 {
		method = forceMethod[0]
		if handler.Kind() == "class_constant_access_expression" {
			if k := kids(handler); len(k) == 2 {
				cls = f.resolveName(f.src.text(k[0]))
			}
		}
	}
	if cls == "" || method == "" {
		return
	}
	if m := p.findMethod(cls, method); m != nil {
		g.AddEdge(m.ID, id, graph.Exposes, 0.95, f.src.file.Path+":"+itoa(line(call))+" route "+verb+" /"+uri)
	}
}

// runPHP runs the PHP pipeline in dependency order.
func runPHP(ctx *Ctx, files []scanFile) {
	if len(files) == 0 {
		return
	}
	p := newPHP(ctx)
	srcs := p.declare(files)
	defer func() {
		for _, s := range srcs {
			s.close()
		}
	}()
	p.buildSchema()
	p.assignModels()
	p.assignResources()
	p.registerTools()
	for _, f := range p.files {
		p.analyze(f)
	}
	p.routes()
	p.registers()
}
