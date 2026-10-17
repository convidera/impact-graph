// Package graph is the in-memory model of an impact graph: typed nodes, edges with a kind, a confidence and the evidence
// that produced them. It has no knowledge of any language or framework and no cgo dependency, so a query service can
// link it without the tree-sitter grammars.
package graph

import (
	"sort"
	"strings"
)

// SchemaVersion is the version of the snapshot format. It goes up when a snapshot written by an older engine can no longer be
// read correctly; Load refuses any other version and asks for a re-index.
const SchemaVersion = 2

// EngineVersion identifies the engine that wrote a snapshot. Release builds set it with
// -ldflags "-X github.com/convidera/impact-graph/internal/graph.EngineVersion=v1.2.3".
var EngineVersion = "dev"

// Community is a group of closely connected symbols and contracts (a subsystem), found by Leiden clustering.
type Community struct {
	ID       int      `json:"id"`
	Label    string   `json:"label"`
	Dir      string   `json:"dir,omitempty"` // the directory most of its members live in
	Size     int      `json:"size"`
	Cohesion float64  `json:"cohesion"`      // share of the members' edge weight that stays inside the community
	Top      []string `json:"top,omitempty"` // ids of the most connected members
}

// Node is a symbol (class, function, field), a file, or a contract (table, column, route, event, tool).
type Node struct {
	ID    string            `json:"id"`
	Type  string            `json:"type"`
	Label string            `json:"label"`
	Path  string            `json:"path,omitempty"`
	Line  int               `json:"line,omitempty"`
	Repo  string            `json:"repo,omitempty"` // owner/name of the repository the node comes from; empty for values shared across repositories
	Props map[string]string `json:"props,omitempty"`
}

// Edge connects two nodes. The stored direction is the natural one for the kind (a caller calls a callee, a function
// reads a column); Kinds says how a query walks it.
type Edge struct {
	Src      string  `json:"src"`
	Dst      string  `json:"dst"`
	Kind     string  `json:"kind"`
	Conf     float64 `json:"conf"`
	Evidence string  `json:"evidence,omitempty"`
	// Prov says what kind of knowledge produced the edge (see the Prov constants).
	Prov string `json:"prov,omitempty"`
	// Tag restricts a call to the values it is conditional on (the arm of a `match` on an event type): a walk that
	// entered the calling function through one value may only leave through calls tagged with that value or untagged.
	Tag string `json:"tag,omitempty"`
}

// Node types.
const (
	TypeFile     = "file"
	TypeTest     = "test"
	TypeDoc      = "doc"
	TypeClass    = "class"
	TypeFunction = "function"
	TypeMethod   = "method"
	TypeField    = "field"
	TypeTable    = "table"
	TypeColumn   = "column"
	TypeRoute    = "route"
	TypeTool     = "mcp-tool"
	TypeSchema   = "schema"    // a named schema of an OpenAPI document
	TypeOp       = "operation" // an operation (verb and path) of an OpenAPI document
	TypeValue    = "value"
	TypeConfig   = "config" // a key path in a Helm values file (kind "key"), or a path pattern a declared mount derives from a Go struct (kind "pattern")

	// Knowledge layer: what the code is for. Nothing extracts these from code; they come from tickets, requirement documents,
	// decisions and the links between them (a session started from a ticket, a pull request that names one).
	TypeRequirement = "requirement"
	TypeTicket      = "ticket"
	TypeDecision    = "decision"
	TypeSection     = "doc-section"
	TypeFeature     = "feature"
)

// Layers of the graph.
const (
	LayerFile      = "file"
	LayerCode      = "code"
	LayerContract  = "contract"
	LayerKnowledge = "knowledge"
)

// Layer says which layer a node type belongs to.
func Layer(nodeType string) string {
	switch nodeType {
	case TypeFile, TypeTest, TypeDoc:
		return LayerFile
	case TypeTable, TypeColumn, TypeRoute, TypeTool, TypeSchema, TypeOp, TypeValue, TypeConfig:
		return LayerContract
	case TypeRequirement, TypeTicket, TypeDecision, TypeSection, TypeFeature:
		return LayerKnowledge
	}
	return LayerCode
}

// Provenance values: how an edge came to be. A query can trust a syntax edge more than a convention, and a convention more
// than a guess; the later values are reserved for the knowledge layer.
const (
	ProvAST        = "ast"        // the syntax and declared types of the code say so: a call, a field read, an import
	ProvConvention = "convention" // a framework or naming convention says so: an Eloquent column, a route, a tool name, a URL
	ProvContract   = "contract"   // a declared contract says so: a vocabulary value mentioned in code
	ProvDeclared   = "declared"   // a link written in impact.toml, with its reason
	ProvDocs       = "docs"       // a document names the thing
	ProvHistory    = "history"    // the files changed together in git history
	ProvGuess      = "guess"      // a heuristic with low confidence: an unknown receiver, the only method with that name
	ProvTicket     = "ticket"     // a ticket, a session started from it or a pull request that names it
	ProvEmbedding  = "embedding"  // semantic similarity
	ProvLLM        = "llm"        // proposed by a model
	ProvHuman      = "human"      // confirmed or written by a person
)

// DefaultProv derives the provenance of an edge from its kind, confidence and evidence when the producer does not say.
func DefaultProv(kind, dst string, conf float64, evidence string) string {
	switch kind {
	case DocumentedIn:
		return ProvDocs
	case CoChanged:
		return ProvHistory
	case Link, Configures:
		return ProvDeclared
	case Produces, Consumes, Mentions:
		return ProvContract
	case Requests, Exposes, MapsTo, Registers:
		return ProvConvention
	case Implements, Refines, DerivedFrom:
		return ProvHuman
	}
	if (kind == Reads || kind == Writes) && strings.HasPrefix(dst, "column:") {
		return ProvConvention
	}
	for _, marker := range []string{"framework:", "factory ", "local scope"} {
		if strings.Contains(evidence, marker) {
			return ProvConvention
		}
	}
	if conf < 0.7 {
		return ProvGuess
	}
	return ProvAST
}

// Edge kinds.
const (
	Calls        = "calls"         // caller -> callee
	Requests     = "requests"      // client function -> backend route it calls over HTTP
	Reads        = "reads"         // function -> column or field it reads
	Writes       = "writes"        // function -> column or field it writes
	Produces     = "produces"      // function -> value (event type, key) it emits
	Consumes     = "consumes"      // function -> value it handles
	Mentions     = "mentions"      // function or file -> value, role unknown
	Exposes      = "exposes"       // function -> route or tool it serves
	Uses         = "uses"          // function -> class it constructs, calls statically or type-hints
	Extends      = "extends"       // child -> parent class, interface or trait
	MemberOf     = "member_of"     // method or field -> its class
	Registers    = "registers"     // class -> class it lists (a server's tools, a provider's bindings)
	MapsTo       = "maps_to"       // model class <-> table
	DocumentedIn = "documented_in" // node -> document that names it
	CoChanged    = "co_changed"    // file <-> file that change together in git history
	Link         = "link"          // declared in the configuration, with a written reason
	Configures   = "configures"    // values key <-> the pattern of a declared mount it matches <-> the Go field the pattern comes from

	// Knowledge layer.
	Implements  = "implements"   // code or contract -> requirement or feature it realises
	Refines     = "refines"      // requirement -> broader requirement or feature
	DerivedFrom = "derived_from" // requirement or ticket -> what it came from (a ticket, a document, a decision)
)

// Orient says in which direction a query walks an edge of some kind.
type Orient int

const (
	None Orient = iota
	Fwd         // src -> dst
	Rev         // dst -> src
	Both
)

// Class groups kinds for ranking: data edges carry a value, structure edges are fan-out ("this file imports that one"),
// attach edges hang tests and docs on a node, history edges are statistical.
type Class int

const (
	Data Class = iota
	Struct
	Attach
	History
)

// KindInfo describes how a kind is walked. Impact asks "what does a change here affect"; Flow asks "where does the
// value go". FlowRev is the cost of walking a Both edge against its stored direction (a return value instead of an
// argument).
type KindInfo struct {
	Class   Class
	Impact  Orient
	Flow    Orient
	Cost    float64
	FlowRev float64
}

// Kinds is the semantic table of every edge kind.
var Kinds = map[string]KindInfo{
	Calls:        {Data, Rev, Both, 1, 1.3},
	Requests:     {Data, Rev, Both, 1, 1.3},
	Reads:        {Data, Rev, Rev, 1, 1},
	Writes:       {Data, Fwd, Fwd, 1, 1},
	Produces:     {Data, Fwd, Fwd, 1, 1},
	Consumes:     {Data, Rev, Rev, 1, 1},
	Mentions:     {Data, Both, Both, 1.5, 1.5},
	Exposes:      {Data, Fwd, Both, 1, 1},
	Uses:         {Struct, Rev, None, 3, 3},
	Extends:      {Struct, Rev, None, 3, 3},
	MemberOf:     {Struct, Fwd, None, 3, 3},
	Registers:    {Struct, Rev, None, 2, 2},
	MapsTo:       {Struct, Both, None, 2, 2},
	DocumentedIn: {Attach, Fwd, None, 0.5, 0.5},
	CoChanged:    {History, Both, None, 2, 2},
	Link:         {Data, Fwd, Both, 1, 1},
	Configures:   {Data, Both, Both, 1, 1},
	Implements:   {Attach, Both, None, 1, 1},
	Refines:      {Attach, Both, None, 1, 1},
	DerivedFrom:  {Attach, Both, None, 1, 1},
}

// Graph is an indexed set of nodes and edges.
type Graph struct {
	Nodes map[string]*Node
	Edges []Edge
	seen  map[string]int
	out   map[string][]int
	in    map[string][]int
	Meta  map[string]string

	Communities []Community
}

// New returns an empty graph.
func New() *Graph {
	return &Graph{Nodes: map[string]*Node{}, seen: map[string]int{}, Meta: map[string]string{}}
}

// AddNode adds a node; an existing node keeps its first path and line and gains new props.
func (g *Graph) AddNode(n Node) *Node {
	if old, ok := g.Nodes[n.ID]; ok {
		if old.Path == "" {
			old.Path, old.Line = n.Path, n.Line
		}
		for k, v := range n.Props {
			if old.Props == nil {
				old.Props = map[string]string{}
			}
			old.Props[k] = v
		}
		return old
	}
	if n.Label == "" {
		n.Label = n.ID
	}
	cp := n
	g.Nodes[n.ID] = &cp
	return &cp
}

// AddEdge adds an edge; the same (src, dst, kind) found twice keeps the stronger evidence. The provenance is derived from
// the kind, confidence and evidence (DefaultProv).
func (g *Graph) AddEdge(src, dst, kind string, conf float64, evidence string) {
	g.add(src, dst, kind, conf, evidence, "", "")
}

// AddTagged is AddEdge with a Tag.
func (g *Graph) AddTagged(src, dst, kind string, conf float64, evidence, tag string) {
	g.add(src, dst, kind, conf, evidence, tag, "")
}

// AddEdgeProv is AddEdge with an explicit provenance.
func (g *Graph) AddEdgeProv(src, dst, kind string, conf float64, evidence, prov string) {
	g.add(src, dst, kind, conf, evidence, "", prov)
}

func (g *Graph) add(src, dst, kind string, conf float64, evidence, tag, prov string) {
	if src == dst || src == "" || dst == "" {
		return
	}
	if prov == "" {
		prov = DefaultProv(kind, dst, conf, evidence)
	}
	key := src + "\x00" + dst + "\x00" + kind
	if i, ok := g.seen[key]; ok {
		if conf > g.Edges[i].Conf {
			g.Edges[i].Conf, g.Edges[i].Evidence, g.Edges[i].Prov = conf, evidence, prov
		}
		if tag == "" {
			g.Edges[i].Tag = "" // one unconditional call makes the edge unconditional
		}
		return
	}
	g.seen[key] = len(g.Edges)
	g.Edges = append(g.Edges, Edge{Src: src, Dst: dst, Kind: kind, Conf: conf, Evidence: evidence, Prov: prov, Tag: tag})
}

// Index builds the adjacency lists; call it after the last AddEdge and before any query.
func (g *Graph) Index() {
	g.out = make(map[string][]int, len(g.Nodes))
	g.in = make(map[string][]int, len(g.Nodes))
	for i, e := range g.Edges {
		g.out[e.Src] = append(g.out[e.Src], i)
		g.in[e.Dst] = append(g.in[e.Dst], i)
	}
}

// Has reports whether a node exists.
func (g *Graph) Has(id string) bool { _, ok := g.Nodes[id]; return ok }

// Find returns nodes whose id or label contains text (case-insensitive), shortest id first.
func (g *Graph) Find(text string, limit int) []*Node { return g.FindIn(text, limit, "") }

// FindIn is Find restricted to a repository (see InRepo).
func (g *Graph) FindIn(text string, limit int, repo string) []*Node {
	needle := strings.ToLower(text)
	var out []*Node
	for _, n := range g.Nodes {
		if !g.InRepo(n, repo) {
			continue
		}
		if strings.Contains(strings.ToLower(n.ID), needle) || strings.Contains(strings.ToLower(n.Label), needle) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i].ID) != len(out[j].ID) {
			return len(out[i].ID) < len(out[j].ID)
		}
		return out[i].ID < out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Resolve turns user input into one node id: an exact id, a repo path, or the single node that matches. Several
// matches are an error that lists them, so a caller never silently picks the wrong one.
func (g *Graph) Resolve(text string) (string, []*Node) {
	for _, c := range []string{text, "file:" + text} {
		if g.Has(c) {
			return c, nil
		}
	}
	found := g.Find(text, 25)
	if len(found) == 1 {
		return found[0].ID, nil
	}
	// an id that ends with the text at a name boundary beats ids that merely contain it (`view.vue#save` over `view.vue#saveRow`)
	var exact []*Node
	for _, n := range found {
		if n.Label == text || endsAtBoundary(n.ID, text) {
			exact = append(exact, n)
		}
	}
	if len(exact) == 1 {
		return exact[0].ID, nil
	}
	return "", found
}

// Group is the section of a result: contracts (tables, columns, routes, events, tools), code, tests or docs.
func (g *Graph) Group(id string) string {
	n := g.Nodes[id]
	if n == nil {
		return "code"
	}
	switch n.Type {
	case TypeTable, TypeColumn, TypeRoute, TypeTool, TypeSchema, TypeOp, TypeValue, TypeField:
		return "contracts"
	case TypeTest:
		return "tests"
	case TypeDoc:
		return "docs"
	case TypeRequirement, TypeTicket, TypeDecision, TypeSection, TypeFeature:
		return "knowledge"
	}
	if k := n.Props["file_kind"]; k == "test" {
		return "tests"
	} else if k == "doc" {
		return "docs"
	}
	return "code"
}

// Out and In list the indexes of the edges that leave and enter a node.
func (g *Graph) Out(id string) []int { return g.out[id] }

// In lists the edges that enter a node.
func (g *Graph) In(id string) []int { return g.in[id] }

func endsAtBoundary(id, text string) bool {
	if !strings.HasSuffix(id, text) {
		return false
	}
	i := len(id) - len(text)
	return i == 0 || strings.ContainsRune(":/#.\\", rune(id[i-1]))
}
