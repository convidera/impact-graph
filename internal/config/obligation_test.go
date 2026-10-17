package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, toml string) error {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "impact.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root, "")
	return err
}

func TestObligationNeedsNameWhenAndRequire(t *testing.T) {
	if err := load(t, "[[obligation]]\nname = \"x\"\nwhen = [\"a/**\"]\nrequire = [\"b\"]\n"); err != nil {
		t.Errorf("a complete obligation loads: %v", err)
	}
	for _, c := range []struct{ toml, want string }{
		{"[[obligation]]\nwhen = [\"a\"]\nrequire = [\"b\"]\n", "no name"},
		{"[[obligation]]\nname = \"x\"\nrequire = [\"b\"]\n", "no `when`"},
		{"[[obligation]]\nname = \"x\"\nwhen = [\"a\"]\n", "no `require`"},
	} {
		if err := load(t, c.toml); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: got %v, want %q", c.toml, err, c.want)
		}
	}
}
