package build_test

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/convidera/impact/internal/build"
	"github.com/convidera/impact/internal/config"
	"github.com/convidera/impact/internal/graph"
)

const metaGo = `package shop

const Turned = "turn_finished"

func Close() string { return Turned }
`

// metaRepo writes a small Go repository under <tmp>/<dir> and commits it; origin "" leaves out the remote.
func metaRepo(t *testing.T, dir, origin string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), dir)
	write(t, root, map[string]string{"shop/shop.go": metaGo})
	gitIn(t, root, "init", "-q")
	if origin != "" {
		gitIn(t, root, "remote", "add", "origin", origin)
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "init")
	return root
}

func head(t *testing.T, root string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func TestRepoNameOrigins(t *testing.T) {
	cases := []struct {
		name, origin, want string
	}{
		{"https origin", "https://github.com/convidera/shop.git", "convidera/shop"},
		{"https origin without .git", "https://github.com/convidera/shop", "convidera/shop"},
		{"ssh scp-like origin", "git@github.com:convidera/shop.git", "convidera/shop"},
		{"ssh url origin", "ssh://git@github.com/convidera/shop.git", "convidera/shop"},
		{"proxy origin", "http://127.0.0.1:8099/github.com/convidera/shop.git", "convidera/shop"},
		{"trailing slash", "https://github.com/convidera/shop.git/", "convidera/shop"},
		{"one segment falls back to the directory", "https://example.com/shop", "shop-api"},
		{"no origin falls back to the directory", "", "shop-api"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := metaRepo(t, "shop-api", c.origin)
			if got := build.RepoName(root, &config.Config{}); got != c.want {
				t.Errorf("RepoName = %q, want %q", got, c.want)
			}
		})
	}
}

func TestBuildRecordsRepositoryIdentity(t *testing.T) {
	root := metaRepo(t, "shop-src", "https://github.com/convidera/shop.git")
	write(t, root, map[string]string{"impact.toml": "[[vocabulary]]\nname = \"ev\"\ntype = \"event\"\nvalues = [\"turn_finished\"]\n"})
	g := index(t, root)
	if g.Meta["repo"] != "convidera/shop" {
		t.Errorf("meta repo = %q", g.Meta["repo"])
	}
	if g.Meta["commit"] != head(t, root) {
		t.Errorf("meta commit = %q, want HEAD %q", g.Meta["commit"], head(t, root))
	}
	if g.Meta["root"] != "shop-src" || g.Meta["built_at"] == "" {
		t.Errorf("meta root/built_at = %q/%q", g.Meta["root"], g.Meta["built_at"])
	}
	values, others := 0, 0
	for _, n := range g.Nodes {
		if n.Type == graph.TypeValue {
			values++
			if n.Repo != "" {
				t.Errorf("value node %s has repo %q, want none", n.ID, n.Repo)
			}
			continue
		}
		others++
		if n.Repo != "convidera/shop" {
			t.Errorf("node %s (%s) has repo %q", n.ID, n.Type, n.Repo)
		}
	}
	if values == 0 || others == 0 {
		t.Fatalf("fixture produced %d value and %d other nodes", values, others)
	}
}

func TestBuildConfigRepoOverridesRemote(t *testing.T) {
	root := metaRepo(t, "shop-src", "https://github.com/convidera/shop.git")
	write(t, root, map[string]string{"impact.toml": "[index]\nrepo = \"convidera/shop-renamed\"\n"})
	g := index(t, root)
	if g.Meta["repo"] != "convidera/shop-renamed" {
		t.Errorf("meta repo = %q, want the impact.toml override", g.Meta["repo"])
	}
}

func TestBuildOutsideGitHasEmptyCommit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plain")
	write(t, root, map[string]string{"shop/shop.go": metaGo})
	g := index(t, root)
	if g.Meta["commit"] != "" {
		t.Errorf("meta commit = %q, want empty outside git", g.Meta["commit"])
	}
	if g.Meta["repo"] != "plain" {
		t.Errorf("meta repo = %q, want the directory name", g.Meta["repo"])
	}
}
