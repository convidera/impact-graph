package build_test

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/graph"
)

// indexed indexes each fixture repository and merges them under its owner/name.
func indexed(t *testing.T, repos map[string]map[string]string) *graph.Graph {
	t.Helper()
	parts := map[string]*graph.Graph{}
	for repo, files := range repos {
		root := t.TempDir()
		write(t, root, files)
		parts[repo] = index(t, root)
	}
	return graph.Merge(parts)
}

// noLinks says that a merged graph has no shared value and no connection between its repositories.
func noLinks(t *testing.T, g *graph.Graph) {
	t.Helper()
	if l := g.Links(); l.Shared != 0 || l.Through != 0 || l.Cross != 0 || len(l.Pairs) != 0 {
		t.Fatalf("repositories must not be connected: %+v", l)
	}
}

// reachedRepos lists the repositories of the nodes a walk reaches, other than the start's.
func reachedRepos(hits map[string]*graph.Hit) map[string]bool {
	out := map[string]bool{}
	for id := range hits {
		if i := strings.Index(id, "//"); i >= 0 {
			out[id[:i]] = true
		}
	}
	return out
}

func laravelShop() map[string]string {
	return map[string]string{
		".env.example":     "MEMCACHED_HOST=127.0.0.1\n",
		"config/cache.php": "<?php\nreturn ['memcached' => ['host' => env('MEMCACHED_HOST', '127.0.0.1')]];\n",
		"app/Cache.php":    "<?php\nclass Cache\n{\n    public function host()\n    {\n        return getenv('MEMCACHED_HOST');\n    }\n}\n",
	}
}

func TestBothReadingAnUnsetNameInEnvExampleAreNotConnected(t *testing.T) {
	g := indexed(t, map[string]map[string]string{"acme/shop": laravelShop(), "acme/api": laravelShop()})
	noLinks(t, g)
	if g.Has("env:MEMCACHED_HOST") {
		t.Fatal("the name is read by two repositories and set by neither: it must not be one shared node")
	}
	o := graph.DefaultOptions()
	o.Dir, o.MinConf = "up", 0.5 // edges are 0.6 until the next commit raises them to the default threshold
	hits, _ := g.Walk("acme/shop//php:Cache::host", o)
	if hits["acme/shop//env:MEMCACHED_HOST"] == nil {
		t.Fatal("the read must still lead to the name of its own repository")
	}
	if repos := reachedRepos(hits); repos["acme/api"] {
		t.Fatalf("the walk of acme/shop reached acme/api: %v", repos)
	}
}

func TestReadInCodeAndSetInHelmIsOneConnection(t *testing.T) {
	app := map[string]string{
		"app/Adapter.php": "<?php\nclass Adapter\n{\n    public function timeout()\n    {\n        return env('ADAPTER_CLONE_TIMEOUT', 120);\n    }\n}\n",
	}
	chart := map[string]string{
		"deploy/values.yaml": "adapter:\n  env:\n    - name: ADAPTER_CLONE_TIMEOUT\n      value: \"120\"\n",
	}
	g := indexed(t, map[string]map[string]string{"acme/app": app, "acme/chart": chart})
	l := g.Links()
	if l.Shared != 1 || l.Through != 1 || l.Cross != 0 || len(l.Pairs) != 1 || l.Pairs[0].Values != 1 {
		t.Fatalf("exactly one connection through one shared node: %+v", l)
	}
	from := nodeOf(t, g, "acme/app", "app/Adapter.php", graph.TypeMethod)
	o := graph.DefaultOptions()
	o.Dir, o.MinConf = "up", 0.5 // edges are 0.6 until the next commit raises them to the default threshold
	hits, _ := g.Walk(from, o)
	if hits["acme/chart//file:deploy/values.yaml"] == nil {
		t.Fatal("a walk from the PHP method, with the default options, must reach the values file of acme/chart")
	}
	down := graph.DefaultOptions()
	down.MinConf = 0.5
	hits, _ = g.Walk("acme/chart//file:deploy/values.yaml", down)
	if hits[from] == nil {
		t.Fatal("a change to the values file, walked down with the default options, must reach the PHP method")
	}
}

func TestBothSettingANameInComposeAreNotConnected(t *testing.T) {
	compose := func() map[string]string {
		return map[string]string{"docker-compose.yml": "services:\n  api:\n    environment:\n      ADAPTER_CLONE_TIMEOUT: \"120\"\n"}
	}
	g := indexed(t, map[string]map[string]string{"acme/app": compose(), "acme/other": compose()})
	noLinks(t, g)
	if g.Has("env:ADAPTER_CLONE_TIMEOUT") {
		t.Fatal("two compose files that set the same name are two services, not one")
	}
}

func TestProducerAndConsumerInOneRepositoryStayConnectedAfterAMerge(t *testing.T) {
	app := map[string]string{
		"docker-compose.yml": "services:\n  api:\n    environment:\n      ADAPTER_CLONE_TIMEOUT: \"120\"\n",
		"app/Adapter.php":    "<?php\nclass Adapter\n{\n    public function timeout()\n    {\n        return env('ADAPTER_CLONE_TIMEOUT', 120);\n    }\n}\n",
	}
	unrelated := map[string]string{"cmd/main.go": "package main\n\nfunc main() {}\n"}
	g := indexed(t, map[string]map[string]string{"acme/app": app, "acme/unrelated": unrelated})
	from := nodeOf(t, g, "acme/app", "app/Adapter.php", graph.TypeMethod)
	o := graph.DefaultOptions()
	o.Dir, o.MinConf = "up", 0.5 // edges are 0.6 until the next commit raises them to the default threshold
	hits, _ := g.Walk(from, o)
	if hits["acme/app//file:docker-compose.yml"] == nil {
		t.Fatal("the read must still reach the compose file of its own repository after the merge")
	}
	if repos := reachedRepos(hits); repos["acme/unrelated"] {
		t.Fatal("an unrelated repository must not be reached")
	}
}

func TestImagePushedAndDeployedIsOneConnection(t *testing.T) {
	workflow := map[string]string{
		".github/workflows/build.yml": "name: build\non: push\njobs:\n  image:\n    runs-on: runner\n    steps:\n      - uses: docker/build-push-action@v6\n        with:\n          tags: ghcr.io/org/app-backend:latest\n",
	}
	chart := map[string]string{"deploy/values.yaml": "image:\n  repository: org/app-backend\n  tag: \"1.2\"\n"}
	g := indexed(t, map[string]map[string]string{"acme/app": workflow, "acme/chart": chart})
	if l := g.Links(); l.Shared != 1 || l.Through != 1 || len(l.Pairs) != 1 {
		t.Fatalf("a pushed image that another repository deploys is one connection: %+v", l)
	}
}

func TestImageOnlyDeployedInBothIsNotConnected(t *testing.T) {
	deploy := func(image string) map[string]string {
		return map[string]string{"docker-compose.yml": "services:\n  api:\n    image: " + image + "\n"}
	}
	g := indexed(t, map[string]map[string]string{
		"acme/app":   deploy("ghcr.io/org/app-backend:latest"),
		"acme/chart": {"deploy/values.yaml": "image: org/app-backend:1.2\n"},
	})
	noLinks(t, g)
}
