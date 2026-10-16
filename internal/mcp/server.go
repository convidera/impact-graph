// Package mcp serves the graph queries as an MCP server (JSON-RPC 2.0 over newline-delimited stdio), so an agent can
// ask what a change influences while it plans, and what its diff left out before it finishes.
package mcp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/convidera/impact-graph/internal/graph"
	"github.com/convidera/impact-graph/internal/report"
	"github.com/convidera/impact-graph/internal/review"
)

// Source provides the graph and lets the server rebuild it.
type Source struct {
	Root    string
	Load    func() (*graph.Graph, error)
	Reindex func() (string, error)
	// ReadOnly serves a stored snapshot that has no working tree behind it (for example the merged snapshot of several
	// repositories): impact_reindex and impact_review are neither listed nor callable.
	ReadOnly bool
}

// Server answers MCP requests.
type Server struct {
	src Source
	mu  sync.Mutex
	g   *graph.Graph
}

// New returns a server over a source.
func New(src Source) *Server { return &Server{src: src} }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// Serve reads requests until the input ends.
func (s *Server) Serve(in io.Reader, out io.Writer) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	enc := json.NewEncoder(out)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req request
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			_ = enc.Encode(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
			continue
		}
		if req.ID == nil { // a notification
			continue
		}
		res, rerr := s.handle(req)
		resp := response{JSONRPC: "2.0", ID: req.ID, Result: res, Error: rerr}
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) handle(req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		instructions := "Impact graph of the repository. Before planning a change, call impact_impact on the symbols, columns, " +
			"routes or events it touches to see what else depends on them. Before you finish, call impact_review to see " +
			"which tests, documents, contracts and files the change is related to but does not contain."
		if s.src.ReadOnly {
			instructions = "Impact graph of the repositories of this session, indexed when they were checked out (a snapshot, not the " +
				"working tree). Node ids of a repository are prefixed with <owner>/<name>//; values shared across repositories, such as " +
				"event types, are one node. Before planning a change, call impact_impact on the symbols, columns, routes or events it " +
				"touches to see what else depends on them. Use impact_overview for the map of the code base."
		}
		return map[string]any{
			"protocolVersion": "2025-06-18",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "impact", "version": graph.EngineVersion},
			"instructions":    instructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": s.tools()}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		text, err := s.call(p.Name, p.Arguments)
		if err != nil {
			return map[string]any{"isError": true, "content": []map[string]any{{"type": "text", "text": err.Error()}}}, nil
		}
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}, nil
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}

func str(name, desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// tools is the tool list of this server: the read-only variant leaves out what needs a working tree.
func (s *Server) tools() []map[string]any {
	all := allTools()
	if !s.src.ReadOnly {
		return all
	}
	kept := all[:0:0]
	for _, t := range all {
		if name := t["name"]; name != "impact_reindex" && name != "impact_review" {
			kept = append(kept, t)
		}
	}
	return kept
}

func allTools() []map[string]any {
	obj := func(props map[string]any, required ...string) map[string]any {
		// A tool without arguments has no required names: the list must still be an array, not null (the variadic is nil),
		// because MCP clients validate inputSchema.required and refuse the whole server on null.
		if required == nil {
			required = []string{}
		}
		return map[string]any{"type": "object", "properties": props, "required": required}
	}
	return []map[string]any{
		{
			"name":        "impact_find",
			"description": "Find graph nodes (classes, methods, columns, routes, MCP tools, events) whose id or name contains the text. Use it to get an exact node id for the other tools.",
			"inputSchema": obj(map[string]any{"text": str("text", "part of a name or id"), "limit": map[string]any{"type": "integer"}}, "text"),
		},
		{
			"name": "impact_impact",
			"description": "What a change to a node affects (direction down) or what can affect it (up), grouped into contracts, code, tests and docs. " +
				"Set flow to follow the data (arguments, return values, columns) instead of the change.",
			"inputSchema": obj(map[string]any{
				"node": str("node", "a node id, a repo path or a unique name"), "direction": map[string]any{"type": "string", "enum": []string{"down", "up"}},
				"flow": map[string]any{"type": "boolean"}, "max_cost": map[string]any{"type": "number"}, "only": str("only", "comma list of groups: contracts, code, tests, docs"),
				"limit": map[string]any{"type": "integer"},
			}, "node"),
		},
		{
			"name":        "impact_trace",
			"description": "The cheapest path a value takes from one node to another (for example from where a booking is created to the report that reads it). Use via to force a waypoint and paths to see alternative routes.",
			"inputSchema": obj(map[string]any{
				"from": str("from", "start node"), "to": str("to", "end node"), "via": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"paths": map[string]any{"type": "integer"},
			}, "from", "to"),
		},
		{
			"name": "impact_meet",
			"description": "How two things are connected: the database columns, struct fields and event types that data flowing out of the first and data flowing into the second both touch. " +
				"Use it to answer how an input (a tool, a route, a job) reaches an output (a report, a screen, a limit check) when they never call each other.",
			"inputSchema": obj(map[string]any{"from": str("from", "the producing side"), "to": str("to", "the consuming side"), "limit": map[string]any{"type": "integer"}}, "from", "to"),
		},
		{
			"name":        "impact_review",
			"description": "Review the working tree against a base commit: lists the tests, documents, contracts, callers and historically co-changed files that the change is related to but does not contain. Call it before finishing.",
			"inputSchema": obj(map[string]any{"base": str("base", "commit to compare with; default is the branch's upstream or merge base")}),
		},
		{
			"name": "impact_overview",
			"description": "The map of the repository: which snapshot this is, how many nodes and contracts it has, its largest communities (subsystems found by clustering) " +
				"with their most connected members, and its god nodes. Call it first when you start on a repository you do not know, before you choose the symbols to pass to impact_impact.",
			"inputSchema": obj(map[string]any{
				"communities": map[string]any{"type": "integer", "description": "how many communities to list, default 8"},
				"gods":        map[string]any{"type": "integer", "description": "how many god nodes to list, default 8"},
			}),
		},
		{
			"name": "impact_community",
			"description": "The members of one community: a subsystem that clustering found from the calls, reads and writes between symbols. Its label, directory, most connected members " +
				"and members by type. Use it to see what belongs to the subsystem a changed symbol is in, or to find the parts that a change there would touch. Take the id from impact_overview or impact_impact.",
			"inputSchema": obj(map[string]any{
				"id":    map[string]any{"type": "integer", "description": "community id, as in c<id>"},
				"limit": map[string]any{"type": "integer", "description": "ids to list per member type, default 20"},
			}, "id"),
		},
		{
			"name": "impact_god_nodes",
			"description": "The most connected symbols and contracts, with how many distinct neighbours they have and how many communities those span. Use it to see where a change is likely to flood " +
				"before you change a widely used symbol, a table or a route.",
			"inputSchema": obj(map[string]any{
				"top":  map[string]any{"type": "integer", "description": "how many nodes to list, default 15"},
				"type": str("type", "only nodes of this type, for example class, method, table, route"),
			}),
		},
		{
			"name":        "impact_reindex",
			"description": "Rebuild the graph from the working tree. Call it after large edits, before impact_review.",
			"inputSchema": obj(map[string]any{}),
		},
	}
}

func (s *Server) graph() (*graph.Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.g == nil {
		g, err := s.src.Load()
		if err != nil {
			return nil, err
		}
		s.g = g
	}
	return s.g, nil
}

func (s *Server) call(name string, raw json.RawMessage) (string, error) {
	if s.src.ReadOnly && (name == "impact_reindex" || name == "impact_review") {
		return "", fmt.Errorf("%s needs a working tree and is not available on this snapshot", name)
	}
	if name == "impact_reindex" {
		msg, err := s.src.Reindex()
		s.mu.Lock()
		s.g = nil
		s.mu.Unlock()
		return msg, err
	}
	g, err := s.graph()
	if err != nil {
		return "", err
	}
	var a struct {
		Text        string   `json:"text"`
		Limit       int      `json:"limit"`
		Node        string   `json:"node"`
		Direction   string   `json:"direction"`
		Flow        bool     `json:"flow"`
		MaxCost     float64  `json:"max_cost"`
		Only        string   `json:"only"`
		From        string   `json:"from"`
		To          string   `json:"to"`
		Via         []string `json:"via"`
		Paths       int      `json:"paths"`
		Base        string   `json:"base"`
		Communities int      `json:"communities"`
		Gods        int      `json:"gods"`
		ID          int      `json:"id"`
		Top         int      `json:"top"`
		Type        string   `json:"type"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %w", err)
		}
	}
	resolve := func(text string) (string, error) {
		id, cands := g.Resolve(text)
		if id != "" {
			return id, nil
		}
		if len(cands) == 0 {
			return "", fmt.Errorf("no node matches %q", text)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%q is ambiguous, pick one id:", text)
		for _, n := range cands {
			fmt.Fprintf(&b, "\n  %s", n.ID)
		}
		return "", fmt.Errorf("%s", b.String())
	}
	switch name {
	case "impact_find":
		if a.Limit == 0 {
			a.Limit = 25
		}
		out := report.Find(g, a.Text, a.Limit)
		if out == "" {
			return "no node matches " + a.Text, nil
		}
		return out, nil
	case "impact_impact":
		id, err := resolve(a.Node)
		if err != nil {
			return "", err
		}
		o := graph.DefaultOptions()
		if a.Direction == "up" {
			o.Dir = "up"
		}
		if a.Flow {
			o.Mode = graph.ModeFlow
		}
		if a.MaxCost > 0 {
			o.MaxCost = a.MaxCost
		}
		if a.Limit == 0 {
			a.Limit = 15
		}
		hits, hubs := g.Walk(id, o)
		return report.Impact(g, id, hits, hubs, o, a.Limit, false, false, a.Only), nil
	case "impact_trace":
		from, err := resolve(a.From)
		if err != nil {
			return "", err
		}
		to, err := resolve(a.To)
		if err != nil {
			return "", err
		}
		var via []string
		for _, v := range a.Via {
			id, err := resolve(v)
			if err != nil {
				return "", err
			}
			via = append(via, id)
		}
		o := graph.DefaultOptions()
		o.Mode, o.MaxCost = graph.ModeFlow, 30
		if a.Paths == 0 {
			a.Paths = 1
		}
		routes := g.TraceN(from, via, to, o, a.Paths)
		if len(routes) == 0 {
			return "", fmt.Errorf("no data path from %s to %s within the budget", from, to)
		}
		return report.Routes(g, o.Mode, routes, true), nil
	case "impact_meet":
		from, err := resolve(a.From)
		if err != nil {
			return "", err
		}
		to, err := resolve(a.To)
		if err != nil {
			return "", err
		}
		o := graph.DefaultOptions()
		o.MaxCost = 12
		if a.Limit == 0 {
			a.Limit = 15
		}
		return report.Meet(g, from, to, g.Meet(from, to, o), a.Limit), nil
	case "impact_overview":
		if a.Communities == 0 {
			a.Communities = 8
		}
		if a.Gods == 0 {
			a.Gods = 8
		}
		return report.Overview(g, a.Communities, a.Gods), nil
	case "impact_community":
		if a.Limit == 0 {
			a.Limit = 20
		}
		return report.Community(g, a.ID, a.Limit), nil
	case "impact_god_nodes":
		if a.Top == 0 {
			a.Top = 15
		}
		var types []string
		if a.Type != "" {
			types = []string{a.Type}
		}
		return report.GodNodes(g, g.GodNodes(a.Top, graph.GodOptions{Types: types})), nil
	case "impact_review":
		base := a.Base
		if base == "" {
			base = review.DefaultBase(s.src.Root)
		}
		changes, err := review.Changes(s.src.Root, base)
		if err != nil {
			return "", err
		}
		return review.Text(review.Run(g, base, changes, review.DefaultOptions()), 12), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}
