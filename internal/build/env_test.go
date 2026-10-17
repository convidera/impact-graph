package build_test

import (
	"sort"
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

// envFixture is one repository with a reader of an environment variable in every language and a definer in every format.
func envFixture() map[string]string {
	return map[string]string{
		".env.example":                "ADAPTER_CLONE_TIMEOUT=120\nAPP_NAME=shop\n# ADAPTER_COMMENTED=1\nexport ADAPTER_EXPORTED=yes\nDB_HOST=db\n",
		"config/adapter.php":          "<?php\nreturn [\n    'timeout' => env('ADAPTER_CLONE_TIMEOUT', 120),\n    'name' => env('APP_NAME'),\n];\n",
		"app/Adapter.php":             "<?php\nclass Adapter\n{\n    public function limit()\n    {\n        return getenv('ADAPTER_REPO_LIMIT');\n    }\n}\n",
		"cmd/main.go":                 "package main\n\nimport \"os\"\n\nfunc timeout() string {\n\treturn os.Getenv(\"ADAPTER_GO_TIMEOUT\")\n}\n\nfunc lookup() {\n\t_, _ = os.LookupEnv(\"ADAPTER_GO_LOOKUP\")\n}\n",
		"web/src/api.ts":              "export function base() {\n  return process.env.ADAPTER_PAGE_LIMIT\n}\n",
		"web/src/view.tsx":            "export const url = import.meta.env.ADAPTER_VIEW_URL\n",
		"worker/settings.py":          "import os\n\nTIMEOUT = os.environ[\"ADAPTER_WORKER_TIMEOUT\"]\nRETRIES = os.environ.get(\"ADAPTER_RETRY_COUNT\", \"3\")\nGROUP = os.getenv(\"ADAPTER_GROUP_NAME\")\n",
		"web/App.vue":                 "<script>\nconst url = import.meta.env.ADAPTER_VUE_URL\n</script>\n",
		"deploy/values.yaml":          "adapter:\n  image:\n    repository: registry.example.com/org/app-backend\n    tag: \"1.2\"\n  env:\n    - name: ADAPTER_HELM_TIMEOUT\n      value: \"120\"\n    - name: APP_DEBUG_X\n",
		"docker-compose.yml":          "services:\n  api:\n    image: postgres:16\n    environment:\n      ADAPTER_QUEUE_NAME: default\n  worker:\n    image: ghcr.io/org/app-worker:latest\n    environment:\n      - ADAPTER_LIST_FORM=1\n",
		".github/workflows/build.yml": "name: build\non: push\njobs:\n  image:\n    runs-on: runner\n    steps:\n      - uses: docker/build-push-action@v6\n        with:\n          images: ghcr.io/org/app-backend\n          tags: |\n            ghcr.io/org/app-backend:latest\n            ghcr.io/org/app-backend:${{ github.sha }}\n      - run: |\n          docker build -t ghcr.io/org/app-cli:dev .\n          docker push ghcr.io/org/app-cli:dev\n",
	}
}

// consumers lists the nodes that read a value, as ids.
func consumers(g *graph.Graph, value string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.Dst == value && e.Kind == graph.Consumes {
			out = append(out, e.Src)
		}
	}
	sort.Strings(out)
	return out
}

// definers lists the nodes that define or produce a value.
func definers(g *graph.Graph, value string) []string {
	var out []string
	for _, e := range g.Edges {
		if e.Dst == value && e.Kind == graph.Produces {
			out = append(out, e.Src)
		}
	}
	sort.Strings(out)
	return out
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func TestEnvironmentNamesConnectTheReadersToTheirDefinitions(t *testing.T) {
	root := t.TempDir()
	write(t, root, envFixture())
	g := index(t, root)

	cases := []struct {
		value, path string
		fn          bool // the read is in a function or method of the file, not at file level
	}{
		{"env:ADAPTER_CLONE_TIMEOUT", "config/adapter.php", false},
		{"env:ADAPTER_REPO_LIMIT", "app/Adapter.php", true},
		{"env:ADAPTER_GO_TIMEOUT", "cmd/main.go", true},
		{"env:ADAPTER_GO_LOOKUP", "cmd/main.go", true},
		{"env:ADAPTER_PAGE_LIMIT", "web/src/api.ts", true},
		{"env:ADAPTER_VIEW_URL", "web/src/view.tsx", false},
		{"env:ADAPTER_VUE_URL", "web/App.vue", false},
		{"env:ADAPTER_WORKER_TIMEOUT", "worker/settings.py", false},
		{"env:ADAPTER_RETRY_COUNT", "worker/settings.py", false},
		{"env:ADAPTER_GROUP_NAME", "worker/settings.py", false},
	}
	for _, c := range cases {
		var found bool
		for _, id := range consumers(g, c.value) {
			n := g.Nodes[id]
			if n == nil || n.Path != c.path {
				continue
			}
			isFunc := (n.Type == graph.TypeFunction || n.Type == graph.TypeMethod) && n.Props["kind"] != "module"
			if c.fn == isFunc {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: readers %v, want one in %s (function: %v)", c.value, consumers(g, c.value), c.path, c.fn)
		}
	}

	for _, value := range []string{"env:ADAPTER_CLONE_TIMEOUT", "env:ADAPTER_HELM_TIMEOUT", "env:ADAPTER_QUEUE_NAME", "env:ADAPTER_LIST_FORM"} {
		if !g.Has(value) {
			t.Errorf("%s is not a value node", value)
		}
	}
	if c := consumers(g, "env:ADAPTER_CLONE_TIMEOUT"); !contains(c, "file:.env.example") {
		t.Errorf("a dotenv key is expected by the application, consumers %v", c)
	}
	if c := consumers(g, "env:ADAPTER_EXPORTED"); !contains(c, "file:.env.example") {
		t.Errorf("an export line is expected by the application, consumers %v", c)
	}
	if d := definers(g, "env:ADAPTER_CLONE_TIMEOUT"); contains(d, "file:.env.example") {
		t.Errorf("a dotenv file must not be a producer, definers %v", d)
	}
	if d := definers(g, "env:ADAPTER_HELM_TIMEOUT"); !contains(d, "file:deploy/values.yaml") {
		t.Errorf("helm env list defined by %v", d)
	}
	if d := definers(g, "env:ADAPTER_QUEUE_NAME"); !contains(d, "file:docker-compose.yml") {
		t.Errorf("compose environment map defined by %v", d)
	}
	if d := definers(g, "env:ADAPTER_LIST_FORM"); !contains(d, "file:docker-compose.yml") {
		t.Errorf("compose environment list defined by %v", d)
	}
	for _, value := range []string{"env:ADAPTER_COMMENTED", "env:APP_NAME", "env:DB_HOST", "env:APP_DEBUG_X"} {
		if g.Has(value) {
			t.Errorf("%s is a generic or commented name and must not join", value)
		}
	}
}

func TestImagesConnectTheWorkflowsToTheManifests(t *testing.T) {
	root := t.TempDir()
	write(t, root, envFixture())
	g := index(t, root)

	if d := definers(g, "image:org/app-backend"); !contains(d, "file:.github/workflows/build.yml") {
		t.Errorf("the workflow pushes org/app-backend, definers %v", d)
	}
	if d := definers(g, "image:org/app-cli"); !contains(d, "file:.github/workflows/build.yml") {
		t.Errorf("docker build -t and docker push are both read, definers %v", d)
	}
	if c := consumers(g, "image:org/app-backend"); !contains(c, "file:deploy/values.yaml") {
		t.Errorf("the Helm repository is a consumer, consumers %v", c)
	}
	if c := consumers(g, "image:org/app-worker"); !contains(c, "file:docker-compose.yml") {
		t.Errorf("the compose image is a consumer, consumers %v", c)
	}
	if g.Has("image:postgres") || g.Has("image:postgres:16") {
		t.Error("an image without an owner must not join")
	}
	if g.Has("image:org/app-backend:latest") {
		t.Error("the tag must be dropped from the image name")
	}
}

func TestEnvironmentJoinsCarryTheConventionProvenance(t *testing.T) {
	root := t.TempDir()
	write(t, root, envFixture())
	g := index(t, root)
	joined := 0
	for _, e := range g.Edges {
		if strings.HasPrefix(e.Dst, "env:") || strings.HasPrefix(e.Dst, "image:") {
			joined++
			if e.Prov != graph.ProvConvention || e.Conf != 0.6 {
				t.Errorf("%s -> %s: prov %q conf %v, want convention 0.6", e.Src, e.Dst, e.Prov, e.Conf)
			}
		}
	}
	if joined == 0 {
		t.Fatal("the fixture produced no joins")
	}
}

// nodeOf returns the id of the first node of a type in a file of a merged repository.
func nodeOf(t *testing.T, g *graph.Graph, repo, path, typ string) string {
	t.Helper()
	for _, n := range g.Nodes {
		if n.Repo == repo && n.Path == path && n.Type == typ {
			return n.ID
		}
	}
	t.Fatalf("no %s in %s of %s", typ, path, repo)
	return ""
}

func TestMergedRepositoriesJoinThroughEnvAndImageNames(t *testing.T) {
	appRoot, chartRoot := t.TempDir(), t.TempDir()
	write(t, appRoot, map[string]string{
		"config/adapter.php":          "<?php\nreturn [\n    'timeout' => env('ADAPTER_CLONE_TIMEOUT', 120),\n];\n",
		"app/Adapter.php":             "<?php\nclass Adapter\n{\n    public function timeout()\n    {\n        return env('ADAPTER_CLONE_TIMEOUT', 120);\n    }\n}\n",
		".github/workflows/build.yml": "name: build\non: push\njobs:\n  image:\n    runs-on: runner\n    steps:\n      - uses: docker/build-push-action@v6\n        with:\n          tags: ghcr.io/org/app-backend:latest\n",
	})
	write(t, chartRoot, map[string]string{
		"deploy/values.yaml": "adapter:\n  image: registry.example.com/org/app-backend:1.2\n  env:\n    - name: ADAPTER_CLONE_TIMEOUT\n      value: \"120\"\n",
	})
	a, b := index(t, appRoot), index(t, chartRoot)
	g := graph.Merge(map[string]*graph.Graph{"acme/app": a, "acme/chart": b})

	l := g.Links()
	if l.Shared != 2 || l.Through != 2 || l.Cross != 0 {
		t.Fatalf("env and image name are the two shared values, reached from both repositories: %+v", l)
	}
	if want := (graph.RepoPair{A: "acme/app", B: "acme/chart", Values: 2}); len(l.Pairs) != 1 || l.Pairs[0] != want {
		t.Fatalf("pairs = %+v", l.Pairs)
	}

	from := nodeOf(t, g, "acme/app", "app/Adapter.php", graph.TypeMethod)
	o := graph.DefaultOptions()
	o.Dir, o.MinConf = "up", 0.5
	hits, _ := g.Walk(from, o)
	key := "acme/chart//file:deploy/values.yaml"
	if hits[key] == nil {
		t.Fatalf("the PHP method %s does not reach the values file of acme/chart; reached %d nodes", from, len(hits))
	}
	if path := strings.Join(graph.PathTo(hits, key), " -> "); !strings.Contains(path, "env:ADAPTER_CLONE_TIMEOUT") {
		t.Fatalf("the route to the values file must pass the shared variable: %s", path)
	}
}
