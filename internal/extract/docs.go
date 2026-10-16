package extract

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/convidera/impact-graph/internal/graph"
)

var (
	backtick = regexp.MustCompile("`([^`\\n]{3,160})`")
	mdLink   = regexp.MustCompile(`\]\(([^)#\s]+)`)
)

// runDocs links code to the documents that name it: a backticked path, class, `Class::method`, table, `table.column`,
// route, tool or vocabulary value, or a relative link to a file. A document is an attach point: a change to the thing
// it names is a reason to read the document.
func runDocs(ctx *Ctx) {
	g := ctx.G
	idx := map[string][]string{} // name as written in docs -> node ids
	add := func(name, id string) {
		if len(name) >= 4 {
			idx[name] = append(idx[name], id)
		}
	}
	for id, n := range g.Nodes {
		switch n.Type {
		case graph.TypeFile, graph.TypeTest:
			add(n.Path, id)
		case graph.TypeClass:
			if n.Props["lang"] == "php" {
				add(n.Props["fqn"], id)
				add(n.Label, id)
			} else {
				add(n.Label, id)
			}
		case graph.TypeMethod:
			if n.Props["lang"] == "php" {
				add(n.Label, id) // Class::method
			}
		case graph.TypeTable:
			add(n.Label, id)
		case graph.TypeColumn, graph.TypeTool:
			add(n.Label, id)
		case graph.TypeValue:
			add(strings.SplitN(id, ":", 2)[1], id)
		case graph.TypeRoute:
			add(strings.SplitN(n.Label, " ", 2)[1], id)
		}
	}
	for _, f := range ctx.Files {
		if f.Kind != "doc" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(ctx.Root, f.Path))
		if err != nil {
			continue
		}
		text := string(b)
		doc := fileID(f.Path)
		link := func(ids []string, conf float64) {
			if len(ids) == 0 || len(ids) > 3 { // an ambiguous name documents nothing in particular
				return
			}
			for _, id := range ids {
				if id != doc {
					g.AddEdge(id, doc, graph.DocumentedIn, conf, f.Path)
				}
			}
		}
		for _, m := range backtick.FindAllStringSubmatch(text, -1) {
			tok := strings.TrimSpace(m[1])
			tok = strings.TrimPrefix(tok, "\\")
			link(idx[tok], 0.85)
			if i := strings.Index(tok, "("); i > 0 {
				link(idx[tok[:i]], 0.8)
			}
		}
		for _, m := range mdLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") {
				continue
			}
			p := path.Clean(path.Join(path.Dir(f.Path), target))
			if g.Has("file:" + p) {
				g.AddEdge("file:"+p, doc, graph.DocumentedIn, 0.9, f.Path)
			}
		}
	}
}
