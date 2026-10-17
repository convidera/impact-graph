package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

func testGraph() *graph.Graph {
	g := graph.New()
	g.AddNode(graph.Node{ID: "php:A::run", Type: graph.TypeMethod, Label: "A::run", Path: "a.php", Line: 3})
	g.AddNode(graph.Node{ID: "column:t.c", Type: graph.TypeColumn, Label: "t.c", Path: "m.php", Line: 1})
	g.AddNode(graph.Node{ID: "php:B::read", Type: graph.TypeMethod, Label: "B::read", Path: "b.php", Line: 5})
	g.AddEdge("php:A::run", "column:t.c", graph.Writes, 0.95, "a.php:4")
	g.AddEdge("php:B::read", "column:t.c", graph.Reads, 0.95, "b.php:6")
	g.Index()
	return g
}

func call(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var res []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("bad JSON line %q: %v", l, err)
		}
		res = append(res, m)
	}
	return res
}

func text(r map[string]any) string {
	c := r["result"].(map[string]any)["content"].([]any)
	return c[0].(map[string]any)["text"].(string)
}

func TestServeToolsAndTrace(t *testing.T) {
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return testGraph(), nil }, Reindex: func() (string, error) { return "ok", nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_trace","arguments":{"from":"A::run","to":"B::read"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_impact","arguments":{"node":"nothing-here"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
	)
	if len(res) != 5 {
		t.Fatalf("a notification gets no reply: got %d replies", len(res))
	}
	tools := res[1]["result"].(map[string]any)["tools"].([]any)
	names := map[string]bool{}
	for _, x := range tools {
		names[x.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"impact_find", "impact_impact", "impact_trace", "impact_review", "impact_reindex"} {
		if !names[n] {
			t.Errorf("tool %s is not listed", n)
		}
	}
	if got := text(res[2]); !strings.Contains(got, "column:t.c") || !strings.Contains(got, "php:B::read") {
		t.Errorf("trace through the column expected, got:\n%s", got)
	}
	if res[3]["result"].(map[string]any)["isError"] != true {
		t.Errorf("an unknown node is a tool error, not a protocol error: %v", res[3])
	}
	if res[4]["error"] == nil {
		t.Errorf("an unknown method is a protocol error")
	}
}

func TestServeCommunityTools(t *testing.T) {
	g := testGraph()
	// the test graph has three symbols, below the default MinSize of 5
	g.Cluster(graph.ClusterOptions{MinSize: 2})
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return g, nil }, Reindex: func() (string, error) { return "ok", nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"impact_overview","arguments":{"communities":2,"gods":3}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_community","arguments":{"id":0}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_community","arguments":{"id":99}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"impact_god_nodes","arguments":{"top":2,"type":"method"}}}`,
	)
	names := map[string]bool{}
	for _, x := range res[0]["result"].(map[string]any)["tools"].([]any) {
		names[x.(map[string]any)["name"].(string)] = true
	}
	for _, n := range []string{"impact_overview", "impact_community", "impact_god_nodes"} {
		if !names[n] {
			t.Errorf("tool %s is not listed", n)
		}
	}
	if got := text(res[1]); !strings.Contains(got, "communities (") || !strings.Contains(got, "god nodes:") {
		t.Errorf("overview:\n%s", got)
	}
	if got := text(res[2]); !strings.Contains(got, "members by type") {
		t.Errorf("community 0:\n%s", got)
	}
	if got := text(res[3]); got != "no community 99" {
		t.Errorf("unknown community = %q", got)
	}
	if got := text(res[4]); !strings.Contains(got, "degree") || strings.Contains(got, "table") {
		t.Errorf("god nodes of type method:\n%s", got)
	}
}

func TestReadOnlyServerHidesWhatNeedsAWorkingTree(t *testing.T) {
	s := New(Source{ReadOnly: true, Load: func() (*graph.Graph, error) { return testGraph(), nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_review","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_reindex","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"impact_find","arguments":{"text":"run"}}}`)
	if ins := res[0]["result"].(map[string]any)["instructions"].(string); strings.Contains(ins, "impact_review") || !strings.Contains(ins, "<owner>/<name>//") {
		t.Errorf("instructions must describe the snapshot and not promise review: %q", ins)
	}
	for _, tool := range res[1]["result"].(map[string]any)["tools"].([]any) {
		if n := tool.(map[string]any)["name"]; n == "impact_review" || n == "impact_reindex" {
			t.Errorf("%v must not be listed", n)
		}
	}
	for _, i := range []int{2, 3} {
		if r := res[i]["result"].(map[string]any); r["isError"] != true {
			t.Errorf("call %d must fail: %v", i, r)
		}
	}
	if !strings.Contains(text(res[4]), "php:A::run") {
		t.Errorf("queries must still work: %s", text(res[4]))
	}
}

func TestEveryToolSchemaHasARequiredArrayNeverNull(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		s := New(Source{ReadOnly: readOnly, Load: func() (*graph.Graph, error) { return testGraph(), nil }})
		var out bytes.Buffer
		if err := s.Serve(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out); err != nil {
			t.Fatal(err)
		}
		var res struct {
			Result struct {
				Tools []struct {
					Name        string `json:"name"`
					InputSchema struct {
						Required *[]string `json:"required"`
					} `json:"inputSchema"`
				} `json:"tools"`
			} `json:"result"`
		}
		if err := json.Unmarshal(out.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
		if len(res.Result.Tools) == 0 {
			t.Fatal("no tools listed")
		}
		for _, tool := range res.Result.Tools {
			if tool.InputSchema.Required == nil {
				t.Errorf("readOnly=%v: %s has required: null (strict MCP clients reject the whole list)", readOnly, tool.Name)
			}
		}
	}
}

// MCP clients (OpenCode 2.0.25) validate the tool list and drop the whole server when an inputSchema.required is null.
func TestEveryToolSchemaHasARequiredArray(t *testing.T) {
	s := New(Source{Root: ".", Load: func() (*graph.Graph, error) { return testGraph(), nil }, Reindex: func() (string, error) { return "ok", nil }})
	res := call(t, s, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	tools := res[0]["result"].(map[string]any)["tools"].([]any)
	if len(tools) == 0 {
		t.Fatal("no tools")
	}
	for _, x := range tools {
		tool := x.(map[string]any)
		schema := tool["inputSchema"].(map[string]any)
		if _, ok := schema["required"].([]any); !ok {
			t.Errorf("%s: inputSchema.required must be an array, got %#v", tool["name"], schema["required"])
		}
	}
}

func TestReadOnlyServerReviewsACheckout(t *testing.T) {
	dir := t.TempDir()
	gitRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	gitRun("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.php"), []byte("<?php\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "impact.toml"), []byte("[[obligation]]\nname = \"regen\"\nwhen = [\"*.php\"]\nrequire = [\"api.json\"]\nfix = \"make generate\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun("add", "-A")
	gitRun("commit", "-q", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.php"), []byte("<?php\n// changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := New(Source{ReadOnly: true, Checkouts: map[string]string{"acme/shop": dir}, Load: func() (*graph.Graph, error) { return testGraph(), nil }})
	res := call(t, s,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"impact_review","arguments":{"base":"HEAD"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"impact_reindex","arguments":{}}}`)
	if ins := res[0]["result"].(map[string]any)["instructions"].(string); !strings.Contains(ins, "impact_review") {
		t.Errorf("instructions promise review when a working tree is known: %q", ins)
	}
	listed := map[string]bool{}
	for _, tool := range res[1]["result"].(map[string]any)["tools"].([]any) {
		listed[tool.(map[string]any)["name"].(string)] = true
	}
	if !listed["impact_review"] || listed["impact_reindex"] {
		t.Errorf("review is offered and reindex is not: %v", listed)
	}
	if got := text(res[2]); !strings.Contains(got, "obligations this change has not met") || !strings.Contains(got, "fix: make generate") {
		t.Errorf("the repository's own obligations are checked:\n%s", got)
	}
	if r := res[3]["result"].(map[string]any); r["isError"] != true {
		t.Errorf("reindex stays unavailable: %v", r)
	}
}
