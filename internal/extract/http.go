package extract

import (
	"regexp"
	"strings"

	"github.com/convidera/impact/internal/graph"
)

// routeIndex joins the HTTP calls of a client (TypeScript, Python) to the routes of the backend: a call whose URL
// literal matches a route's path becomes a `requests` edge from the calling function to the route, and the route's
// handler carries the flow on. It is built after the backend extractors have created their route nodes.
type routeIndex struct {
	routes []routeEntry
}

type routeEntry struct {
	id   string
	verb string
	segs []string
}

var (
	paramSeg   = regexp.MustCompile(`^(\{[^}]*\}|:[A-Za-z_]\w*)$`)
	prefixSeg  = regexp.MustCompile(`^(api|v\d+|_)$`)
	originRe   = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^/]*`)
	httpVerbs  = map[string]string{"get": "GET", "post": "POST", "put": "PUT", "patch": "PATCH", "delete": "DELETE", "head": "HEAD", "options": "OPTIONS"}
	clientHint = regexp.MustCompile(`(?i)(axios|http|client|api|request|fetch|ky|\$)`)
)

func newRouteIndex(g *graph.Graph) *routeIndex {
	ri := &routeIndex{}
	for id, n := range g.Nodes {
		if n.Type != graph.TypeRoute {
			continue
		}
		verb, path, ok := strings.Cut(n.Label, " ")
		if !ok {
			continue
		}
		ri.routes = append(ri.routes, routeEntry{id: id, verb: verb, segs: splitPath(path)})
	}
	return ri
}

func splitPath(p string) []string {
	var out []string
	for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
		if s == "" {
			continue
		}
		if paramSeg.MatchString(s) {
			s = "{}"
		}
		out = append(out, s)
	}
	return out
}

// normalizeURL turns the static part of a URL expression into path segments. parts are the literal fragments with `\x00`
// standing for an interpolated value; a URL that starts with an interpolation followed by a slash is a base URL and is
// dropped (`${base}/sessions`).
func normalizeURL(raw string) (segs []string, rooted bool, ok bool) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "\x00/") {
		raw = raw[1:]
	}
	raw = originRe.ReplaceAllString(raw, "")
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" || strings.HasPrefix(raw, "\x00") {
		return nil, false, false
	}
	rooted = strings.HasPrefix(raw, "/")
	for _, s := range strings.Split(strings.Trim(raw, "/"), "/") {
		switch {
		case s == "":
			continue
		case strings.Contains(s, "\x00"):
			s = "{}"
		}
		segs = append(segs, s)
	}
	return segs, rooted, len(segs) > 0
}

func segMatch(a, b string) bool { return a == b || a == "{}" || b == "{}" }

// match returns the routes a URL and verb can mean, with a confidence. An empty verb matches every verb at lower
// confidence. A backend prefix that the client's base URL supplies (`api`, `v1`) may be missing from the call.
func (ri *routeIndex) match(verb, raw string) map[string]float64 {
	segs, rooted, ok := normalizeURL(raw)
	if !ok {
		return nil
	}
	out := map[string]float64{}
	for _, r := range ri.routes {
		if verb != "" && r.verb != verb && r.verb != "ANY" {
			continue
		}
		conf := 0.0
		switch {
		case len(r.segs) == len(segs) && allMatch(r.segs, segs):
			conf = 0.9
		case len(r.segs) > len(segs) && len(r.segs)-len(segs) <= 2 && allMatch(r.segs[len(r.segs)-len(segs):], segs) && allPrefix(r.segs[:len(r.segs)-len(segs)]):
			conf = 0.75
		}
		if conf == 0 {
			continue
		}
		if verb == "" {
			conf -= 0.15
		}
		if !rooted && len(segs) == 1 && !strings.ContainsAny(segs[0], "-_") {
			conf -= 0.15 // `cache.get('sessions')`: one plain word is more often a key than a URL
		}
		if conf > out[r.id] {
			out[r.id] = conf
		}
	}
	return out
}

func allMatch(a, b []string) bool {
	for i := range a {
		if !segMatch(a[i], b[i]) {
			return false
		}
	}
	return true
}

func allPrefix(segs []string) bool {
	for _, s := range segs {
		if !prefixSeg.MatchString(s) {
			return false
		}
	}
	return true
}

// request records the HTTP calls of one function.
func (ri *routeIndex) link(ctx *Ctx, from, verb, url, evidence string) {
	for id, conf := range ri.match(verb, url) {
		if conf >= 0.5 {
			ctx.G.AddEdge(from, id, graph.Requests, conf, evidence)
		}
	}
}
