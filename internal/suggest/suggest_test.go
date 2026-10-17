package suggest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/config"
)

func commit(t *testing.T, root string, n int, files ...string) {
	t.Helper()
	for _, f := range files {
		full := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(strconv.Itoa(n)+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "c" + strconv.Itoa(n)}} {
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.email=t@t", "-c", "user.name=t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func repo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return root
}

func TestMinesASourceToGeneratedFileRule(t *testing.T) {
	root := repo(t)
	for i := 0; i < 6; i++ {
		commit(t, root, i, "app/data/d"+strconv.Itoa(i%3)+".php", "api/openapi.json", "web/generated/client.ts")
	}
	for i := 0; i < 12; i++ { // unrelated work, so the generated file is not simply busy
		commit(t, root, 100+i, "docs/d"+strconv.Itoa(i)+".md", "app/other/x"+strconv.Itoa(i)+".php")
	}
	cfg, _ := config.Load(root, "")
	cs, err := Mine(root, cfg, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range cs {
		if c.When == "app/data/**" && c.Require == "api/openapi.json" {
			found = true
			if c.Support != 6 || c.Of != 6 || c.Ratio != 1 {
				t.Errorf("evidence wrong: %+v", c)
			}
		}
		if strings.HasPrefix(c.When, "web/generated") {
			t.Errorf("a generated file must not be a trigger: %+v", c)
		}
	}
	if !found {
		t.Fatalf("source -> generated rule not found in %+v", cs)
	}
	if out := TOML(cs); !strings.Contains(out, "[[obligation]]") || !strings.Contains(out, `when = ["app/data/**"]`) {
		t.Errorf("TOML: %s", out)
	}
}

func TestIgnoresWeakAndBusyCompanions(t *testing.T) {
	root := repo(t)
	// busy.txt changes in every commit; a/** changes with it only because it is busy
	for i := 0; i < 10; i++ {
		files := []string{"busy.txt", "b/x" + strconv.Itoa(i) + ".go"}
		if i%2 == 0 {
			files = append(files, "a/y"+strconv.Itoa(i)+".go")
		}
		commit(t, root, i, files...)
	}
	cfg, _ := config.Load(root, "")
	cs, _ := Mine(root, cfg, DefaultOptions())
	for _, c := range cs {
		if c.When == "a/**" && c.Require == "busy.txt" {
			t.Errorf("a companion that changes in every commit says nothing: %+v", c)
		}
	}
}
