package mcp

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

// mergedGraph is two repositories that share the event value event:order.
func mergedGraph() *graph.Graph {
	shop := graph.New()
	shop.Meta = map[string]string{"repo": "acme/shop", "commit": "1111111aaaa"}
	shop.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3})
	shop.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	shop.AddEdge("php:Cart", "event:order", graph.Produces, 0.9, "app/Cart.php:9")
	api := graph.New()
	api.Meta = map[string]string{"repo": "acme/api", "commit": "2222222bbbb"}
	api.AddNode(graph.Node{ID: "php:Order", Type: graph.TypeClass, Label: "Order", Path: "app/Order.php", Line: 3})
	api.AddNode(graph.Node{ID: "event:order", Type: graph.TypeValue, Label: "event:order"})
	api.AddEdge("php:Order", "event:order", graph.Consumes, 0.9, "app/Order.php:7")
	g := graph.Merge(map[string]*graph.Graph{"acme/shop": shop, "acme/api": api})
	g.Cluster(graph.ClusterOptions{MinSize: 1})
	return g
}

func isError(r map[string]any) bool {
	res, _ := r["result"].(map[string]any)
	flag, _ := res["isError"].(bool)
	return flag
}

func TestRepositoryArgumentRestrictsTheReadTools(t *testing.T) {
	g := mergedGraph()
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return g, nil }, Reindex: func() (string, error) { return "ok", nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"impact_find","arguments":{"text":"order","repository":"api"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_find","arguments":{"text":"order"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_find","arguments":{"text":"order","repository":"nope"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"impact_overview","arguments":{"repository":"acme/shop","communities":2,"gods":3}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"impact_god_nodes","arguments":{"top":5,"repository":"acme/api"}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"impact_community","arguments":{"id":0,"repository":"acme/shop"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"impact_god_nodes","arguments":{"top":5}}}`,
	)
	for _, tool := range res[0]["result"].(map[string]any)["tools"].([]any) {
		m := tool.(map[string]any)
		props := m["inputSchema"].(map[string]any)["properties"].(map[string]any)
		_, has := props["repository"]
		name := m["name"].(string)
		want := name == "impact_find" || name == "impact_overview" || name == "impact_community" || name == "impact_god_nodes" || name == "impact_review"
		if has != want {
			t.Errorf("%s: repository argument listed = %v, want %v", name, has, want)
		}
	}
	if got := text(res[1]); strings.Contains(got, "event:order") || !strings.Contains(got, "acme/api//php:Order") {
		t.Errorf("find restricted to acme/api:\n%s", got)
	}
	if got := text(res[2]); !strings.Contains(got, "event:order") || !strings.Contains(got, "acme/api//php:Order") {
		t.Errorf("find without a repository:\n%s", got)
	}
	if !isError(res[3]) || !strings.Contains(text(res[3]), "acme/api, acme/shop") {
		t.Errorf("unknown repository must be a tool error that lists the known ones: %v", res[3])
	}
	if got := text(res[4]); strings.Contains(got, "acme/api") || !strings.Contains(got, "acme/shop @ 1111111") {
		t.Errorf("overview of acme/shop:\n%s", got)
	}
	if got := text(res[5]); strings.Contains(got, "acme/shop//") {
		t.Errorf("god nodes of acme/api:\n%s", got)
	}
	if got := text(res[6]); got != "no community 0 in acme/shop" && (!strings.Contains(got, "members by type") || strings.Contains(got, "acme/api//")) {
		t.Errorf("community 0 restricted to acme/shop:\n%s", got)
	}
	if got := text(res[7]); !strings.Contains(got, "acme/shop//php:Cart") && !strings.Contains(got, "acme/api//php:Order") {
		t.Errorf("god nodes without a repository:\n%s", got)
	}
}

func TestRepositoryArgumentIsIgnoredOnASnapshot(t *testing.T) {
	g := graph.New()
	g.Meta = map[string]string{"repo": "acme/shop"}
	g.AddNode(graph.Node{ID: "php:Cart", Type: graph.TypeClass, Label: "Cart", Path: "app/Cart.php", Line: 3, Repo: "acme/shop"})
	g.Index()
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return g, nil }})
	res := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"impact_find","arguments":{"text":"Cart","repository":"anything"}}}`)
	if isError(res[0]) || !strings.Contains(text(res[0]), "php:Cart") {
		t.Errorf("a snapshot ignores the repository argument: %v", res[0])
	}
}

// joinedGraph is what the extractor makes of an app that reads a variable in PHP and a chart that sets it: one shared
// node, produced by the chart and consumed by the app, with the default confidence of a joined name.
func joinedGraph() *graph.Graph {
	app := graph.New()
	app.Meta = map[string]string{"repo": "acme/app", "commit": "1111111aaaa"}
	app.AddNode(graph.Node{ID: "php:Adapter::timeout", Type: graph.TypeMethod, Label: "Adapter::timeout", Path: "app/Adapter.php", Line: 3})
	app.AddNode(graph.Node{ID: "env:ADAPTER_CLONE_TIMEOUT", Type: graph.TypeValue, Label: "env:ADAPTER_CLONE_TIMEOUT"})
	app.AddEdgeProv("php:Adapter::timeout", "env:ADAPTER_CLONE_TIMEOUT", graph.Consumes, 0.7, "app/Adapter.php:5 env ADAPTER_CLONE_TIMEOUT", graph.ProvConvention)
	chart := graph.New()
	chart.Meta = map[string]string{"repo": "acme/chart", "commit": "2222222bbbb"}
	chart.AddNode(graph.Node{ID: "file:deploy/values.yaml", Type: graph.TypeFile, Label: "deploy/values.yaml", Path: "deploy/values.yaml"})
	chart.AddNode(graph.Node{ID: "env:ADAPTER_CLONE_TIMEOUT", Type: graph.TypeValue, Label: "env:ADAPTER_CLONE_TIMEOUT"})
	chart.AddEdgeProv("file:deploy/values.yaml", "env:ADAPTER_CLONE_TIMEOUT", graph.Produces, 0.7, "deploy/values.yaml:3 env ADAPTER_CLONE_TIMEOUT", graph.ProvConvention)
	g := graph.Merge(map[string]*graph.Graph{"acme/app": app, "acme/chart": chart})
	g.Cluster(graph.ClusterOptions{MinSize: 1})
	return g
}

func TestDefaultToolsFollowAJoinedName(t *testing.T) {
	g := joinedGraph()
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return g, nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"impact_impact","arguments":{"node":"acme/app//php:Adapter::timeout","direction":"up"}}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"impact_impact","arguments":{"node":"acme/chart//file:deploy/values.yaml"}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_trace","arguments":{"from":"acme/chart//file:deploy/values.yaml","to":"acme/app//php:Adapter::timeout"}}}`,
	)
	if got := text(res[0]); !strings.Contains(got, "acme/chart//file:deploy/values.yaml") {
		t.Errorf("impact_impact up from the PHP method must reach the chart:\n%s", got)
	}
	if got := text(res[1]); !strings.Contains(got, "acme/app//php:Adapter::timeout") {
		t.Errorf("impact_impact down from the chart must reach the PHP method:\n%s", got)
	}
	if isError(res[2]) || !strings.Contains(text(res[2]), "env:ADAPTER_CLONE_TIMEOUT") {
		t.Errorf("impact_trace from the chart to the PHP method must pass the shared name: %v", res[2])
	}
}
