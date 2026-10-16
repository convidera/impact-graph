package graph

import (
	"container/heap"
	"math"
	"sort"
	"strings"
)

// Modes of a walk.
const (
	ModeImpact = "impact" // follow the direction a change spreads
	ModeFlow   = "flow"   // follow the direction a value travels
)

// Options of a walk.
type Options struct {
	Mode     string             // ModeImpact (default) or ModeFlow
	Dir      string             // "down" (default): what this reaches; "up": what reaches this
	MaxCost  float64            // budget; every edge costs its kind's Cost, structure edges are expensive
	MinConf  float64            // edges below this confidence are not walked
	HubLimit int                // a node with more walkable edges than this is reached but not expanded; 0 expands everything
	History  bool               // also walk co_changed edges
	Avoid    map[string]float64 // extra cost for entering a node (used to find alternative routes)
	Via      []string           // waypoints a walk must pass in order; only paths that did are reported
}

// DefaultOptions are what the CLI uses unless a flag says otherwise.
func DefaultOptions() Options {
	return Options{Mode: ModeImpact, Dir: "down", MaxCost: 7, MinConf: 0.7, HubLimit: 60}
}

// Hit is a node reached by a walk and how it was reached.
type Hit struct {
	ID       string  `json:"id"`
	Cost     float64 `json:"cost"`
	Depth    int     `json:"depth"`
	Parent   string  `json:"parent,omitempty"`
	Kind     string  `json:"kind,omitempty"`
	Evidence string  `json:"evidence,omitempty"`
	Conf     float64 `json:"conf"`           // weakest edge on the path
	EdgeConf float64 `json:"edge_conf"`      // confidence of the last edge
	Step     float64 `json:"step"`           // cost of the last edge alone
	Prov     string  `json:"prov,omitempty"` // provenance of the last edge
	Walked   string  `json:"walked"`         // "with" or "against" the stored direction of the last edge

	prev        *Hit
	via         string // the value node this function was entered from, when the last step was a consumes edge
	enteredCall bool   // the last step went down into a callee: a flow may not climb out of it into another caller
}

type item struct {
	id    string
	cost  float64
	depth int
	idx   int
}
type pq []*item

func (p pq) Len() int { return len(p) }
func (p pq) Less(i, j int) bool {
	if p[i].cost != p[j].cost {
		return p[i].cost < p[j].cost
	}
	return p[i].depth < p[j].depth
}
func (p pq) Swap(i, j int) { p[i], p[j] = p[j], p[i]; p[i].idx, p[j].idx = i, j }
func (p *pq) Push(x any)   { it := x.(*item); it.idx = len(*p); *p = append(*p, it) }
func (p *pq) Pop() any     { old := *p; n := len(old); it := old[n-1]; *p = old[:n-1]; return it }

type step struct {
	to      string
	e       *Edge
	against bool
	cost    float64
}

func (g *Graph) steps(cur string, o Options) []step {
	var out []step
	allow := func(orient Orient, forward bool) bool {
		if orient == Both {
			return true
		}
		if o.Dir == "up" {
			// Walking up inverts the influence direction.
			return (forward && orient == Rev) || (!forward && orient == Fwd)
		}
		return (forward && orient == Fwd) || (!forward && orient == Rev)
	}
	consider := func(i int, forward bool) {
		e := &g.Edges[i]
		if e.Conf < o.MinConf {
			return
		}
		info, ok := Kinds[e.Kind]
		if !ok {
			info = KindInfo{Data, Both, Both, 1, 1}
		}
		if info.Class == History && !o.History {
			return
		}
		orient := info.Impact
		if o.Mode == ModeFlow {
			orient = info.Flow
		}
		if orient == None || !allow(orient, forward) {
			return
		}
		cost := info.Cost
		to := e.Dst
		if !forward {
			to = e.Src
		}
		// A Both edge costs more when walked against the way the value travels (a return value, not an argument).
		if o.Mode == ModeFlow && orient == Both && (forward == (o.Dir == "up")) {
			cost = info.FlowRev
		}
		out = append(out, step{to, e, !forward, cost})
	}
	for _, i := range g.out[cur] {
		consider(i, true)
	}
	for _, i := range g.in[cur] {
		consider(i, false)
	}
	return out
}

func (g *Graph) terminal(id string) bool {
	n := g.Nodes[id]
	if n == nil {
		return false
	}
	// tests, documents and the knowledge layer are where a walk ends: a requirement implemented by a hundred functions must
	// not connect all of them to each other
	return n.Type == TypeTest || n.Type == TypeDoc || n.Props["file_kind"] == "test" || n.Props["file_kind"] == "doc" || Layer(n.Type) == LayerKnowledge
}

// Walk explores from start with Dijkstra on edge cost. hubs maps each node that was reached but not expanded to its
// number of walkable edges. In flow mode a path that descends into a callee may not climb back out into a different
// caller (call and return must match), which keeps shared helpers from becoming bridges between unrelated flows.
func (g *Graph) Walk(start string, o Options) (map[string]*Hit, map[string]int) {
	if o.MaxCost == 0 {
		o.MaxCost = math.Inf(1)
	}
	type key struct {
		id    string
		down  bool
		stage int // waypoints of o.Via passed so far
	}
	best := map[key]*Hit{{start, false, 0}: {ID: start, Conf: 1, Walked: "with"}}
	hubs := map[string]int{}
	done := map[key]bool{}
	q := &pq{}
	keys := map[*item]key{}
	first := &item{id: start}
	keys[first] = key{start, false, 0}
	heap.Push(q, first)
	for q.Len() > 0 {
		it := heap.Pop(q).(*item)
		k := keys[it]
		delete(keys, it)
		if done[k] {
			continue
		}
		done[k] = true
		cur := best[k]
		if it.id != start && g.terminal(it.id) {
			continue
		}
		ss := g.steps(it.id, o)
		if it.id != start && o.HubLimit > 0 && len(ss) > o.HubLimit {
			hubs[it.id] = len(ss)
			continue
		}
		for _, s := range ss {
			if s.e.Tag != "" && cur.via != "" && !strings.Contains(","+s.e.Tag+",", ","+cur.via+",") {
				continue // a conditional call that belongs to another value
			}
			// entering a callee (down) or a caller (up) must be left through the same call, not through another one
			isCall := s.e.Kind == Calls || s.e.Kind == Requests
			entering := isCall && (s.against == (o.Dir == "up"))
			if o.Mode == ModeFlow && cur.enteredCall && isCall && !entering {
				continue
			}
			down := entering
			nc := cur.Cost + s.cost + o.Avoid[s.to]
			if nc > o.MaxCost {
				continue
			}
			stage := k.stage
			if stage < len(o.Via) && s.to == o.Via[stage] {
				stage++
			}
			nk := key{s.to, down && o.Mode == ModeFlow, stage}
			conf := math.Min(cur.Conf, s.e.Conf)
			if h, ok := best[nk]; ok && (h.Cost < nc || (h.Cost == nc && h.Conf >= conf)) {
				continue
			}
			w := "with"
			if s.against {
				w = "against"
			}
			h := &Hit{
				ID: s.to, Cost: nc, Depth: cur.Depth + 1, Parent: it.id, Kind: s.e.Kind, Evidence: s.e.Evidence,
				Conf: conf, EdgeConf: s.e.Conf, Prov: s.e.Prov, Step: s.cost, Walked: w, prev: cur, enteredCall: nk.down,
			}
			if s.e.Kind == Consumes && s.against {
				h.via = it.id
			}
			best[nk] = h
			ni := &item{id: s.to, cost: nc, depth: cur.Depth + 1}
			keys[ni] = nk
			heap.Push(q, ni)
		}
	}
	hits := map[string]*Hit{}
	for k, h := range best {
		if k.stage != len(o.Via) {
			continue
		}
		if old, ok := hits[k.id]; !ok || h.Cost < old.Cost || (h.Cost == old.Cost && h.Conf > old.Conf) {
			hits[k.id] = h
		}
	}
	for id := range hubs {
		if _, ok := hits[id]; !ok {
			delete(hubs, id)
		}
	}
	return hits, hubs
}

// PathTo lists the node ids from the start of a walk to id.
func PathTo(hits map[string]*Hit, id string) []string {
	var out []string
	for h := hits[id]; h != nil; h = h.prev {
		out = append(out, h.ID)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// pathHits lists the hits along the path to id.
func pathHits(hits map[string]*Hit, id string) []*Hit {
	var out []*Hit
	for h := hits[id]; h != nil; h = h.prev {
		out = append(out, h)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Trace finds the cheapest path from a to b under the options (hubs are crossed, because a path that ends at a hub is
// still a path); nil when there is none within the budget.
func (g *Graph) Trace(a, b string, o Options) []*Hit {
	o.HubLimit = 0
	hits, _ := g.Walk(a, o)
	if _, ok := hits[b]; !ok {
		return nil
	}
	return pathHits(hits, b)
}

// TraceN returns up to n distinct routes from a to b: after each route its intermediate nodes become expensive, so the
// next search is pushed onto another way through the code.
func (g *Graph) TraceN(a string, via []string, b string, o Options, n int) [][]*Hit {
	var out [][]*Hit
	o.Avoid = map[string]float64{}
	o.Via = via
	for len(out) < n {
		p := g.Trace(a, b, o)
		if p == nil {
			break
		}
		dup := false
		for _, q := range out {
			if samePath(p, q) {
				dup = true
			}
		}
		if len(out) > 0 && trueCost(p) > 2*trueCost(out[0])+2 {
			break // too far from the best route to be a useful alternative
		}
		if !dup {
			out = append(out, p)
		}
		fresh := false
		for _, h := range p[1 : len(p)-1] {
			if o.Avoid[h.ID] == 0 {
				fresh = true
			}
			o.Avoid[h.ID] += 4
		}
		if !fresh && dup {
			break
		}
		if len(p) <= 2 {
			break
		}
	}
	return out
}

func samePath(a, b []*Hit) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			return false
		}
	}
	return true
}

// Meeting is a contract that both sides of a Meet touch.
type Meeting struct {
	ID       string   `json:"id"`
	FromCost float64  `json:"from_cost"`
	ToCost   float64  `json:"to_cost"`
	Down     []string `json:"down"` // path from a to the contract
	Up       []string `json:"up"`   // path from the contract to b
}

// Meet answers "how are these two things connected": it walks the flow down from a and up from b and returns the
// contracts (columns, fields, event types) both reach, cheapest first. Two features that never call each other usually
// meet in the database column one writes and the other reads.
func (g *Graph) Meet(a, b string, o Options) []Meeting {
	o.Mode = ModeFlow
	if o.HubLimit == 0 || o.HubLimit > 40 {
		o.HubLimit = 40 // a function that touches dozens of contracts connects everything to everything
	}
	down := o
	down.Dir = "down"
	up := o
	up.Dir = "up"
	ha, _ := g.Walk(a, down)
	hb, _ := g.Walk(b, up)
	var out []Meeting
	for id, x := range ha {
		y, ok := hb[id]
		n := g.Nodes[id]
		if !ok || n == nil || id == a || id == b {
			continue
		}
		switch n.Type {
		case TypeColumn, TypeField, TypeValue:
		default:
			continue
		}
		m := Meeting{ID: id, FromCost: x.Cost, ToCost: y.Cost, Down: PathTo(ha, id)}
		path := PathTo(hb, id) // from b up to the contract; reverse it
		for i := len(path) - 1; i >= 0; i-- {
			m.Up = append(m.Up, path[i])
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		ci, cj := out[i].FromCost+out[i].ToCost, out[j].FromCost+out[j].ToCost
		if ci != cj {
			return ci < cj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Hub is a node that concentrates many edges: a place where impact queries flood.
type Hub struct {
	ID     string `json:"id"`
	In     int    `json:"in"`
	Out    int    `json:"out"`
	Struct int    `json:"struct"`
}

// Hubs ranks nodes by degree.
func (g *Graph) Hubs(top int) []Hub {
	m := map[string]*Hub{}
	get := func(id string) *Hub {
		h := m[id]
		if h == nil {
			h = &Hub{ID: id}
			m[id] = h
		}
		return h
	}
	for _, e := range g.Edges {
		get(e.Src).Out++
		get(e.Dst).In++
		if Kinds[e.Kind].Class == Struct {
			get(e.Src).Struct++
			get(e.Dst).Struct++
		}
	}
	out := make([]Hub, 0, len(m))
	for _, h := range m {
		out = append(out, *h)
	}
	sort.Slice(out, func(i, j int) bool {
		if di, dj := out[i].In+out[i].Out, out[j].In+out[j].Out; di != dj {
			return di > dj
		}
		return out[i].ID < out[j].ID
	})
	if top > 0 && len(out) > top {
		out = out[:top]
	}
	return out
}

func trueCost(p []*Hit) float64 {
	c := 0.0
	for _, h := range p {
		c += h.Step
	}
	return c
}
