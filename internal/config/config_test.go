package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGlobs(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"**/tests/**", "backend/tests/Unit/A.php", true},
		{"**/tests/**", "tests/A.php", true},
		{"**/*_test.go", "a/b/x_test.go", true},
		{"**/*_test.go", "a/b/x.go", false},
		{"backend/**", "backend/app/A.php", true},
		{"backend/*.php", "backend/app/A.php", false},
		{"**/*.md", "README.md", true},
	}
	for _, c := range cases {
		if got := Match([]string{c.pattern}, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestLoadDefaultsAndFile(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Excluded("vendor/x/y.php") || !c.IsTest("tests/a.php") || !c.IsDoc("docs/a.md") || c.Index.Db == "" {
		t.Errorf("defaults not applied: %+v", c.Index)
	}
	if err := os.WriteFile(filepath.Join(dir, "impact.toml"), []byte("[index]\nexclude = [\"gen/**\"]\n[[vocabulary]]\nname = \"ev\"\nvalues = [\"a\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.Excluded("gen/x.go") || len(c.Vocabulary) != 1 {
		t.Errorf("file not read: %+v", c)
	}
}

func TestGeneratedDefaultsAndUserGlobs(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"src/generated/api.ts", "web/__generated__/q.ts", "lib/user.generated.ts", "app/models_generated.go", "api/zz_generated.deepcopy.go", "proto/a.pb.go"} {
		if !c.IsGenerated(p) {
			t.Errorf("IsGenerated(%q) = false, want true by default", p)
		}
	}
	for _, p := range []string{"app/Services/Svc.php", "src/generators/gen.ts", "app/generated.go.md"} {
		if c.IsGenerated(p) {
			t.Errorf("IsGenerated(%q) = true, want false", p)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "impact.toml"), []byte("[index]\ngenerated = [\"**/schema/*.ts\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsGenerated("web/schema/types.ts") || !c.IsGenerated("src/generated/api.ts") {
		t.Errorf("user globs must add to the defaults, not replace them")
	}
}

func TestLoadRepoOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "impact.toml"), []byte("[index]\nrepo = \"convidera/shop\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Index.Repo != "convidera/shop" {
		t.Errorf("repo = %q, want convidera/shop", c.Index.Repo)
	}
}

func TestClusterOptions(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ClusterOptions(); got.Resolution != 1 || got.HubPercentile != 99 || got.MinSize != 5 || got.Seed != 1 {
		t.Errorf("absent [cluster] keeps the defaults, got %+v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "impact.toml"), []byte("[cluster]\nresolution = 2.5\nhub_percentile = 0\nmin_size = 4\nseed = 7\ndisabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ClusterOptions(); got.Resolution != 2.5 || got.HubPercentile != 0 || got.MinSize != 4 || got.Seed != 7 {
		t.Errorf("explicit values are merged, an explicit hub_percentile 0 disables the hub rule: got %+v", got)
	}
	if !c.Cluster.Disabled {
		t.Errorf("disabled = true is read")
	}
}
