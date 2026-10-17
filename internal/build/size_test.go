package build_test

import (
	"strings"
	"testing"
)

// bigPadding is about 600 KiB of comment lines: above the 512 KiB YAML limit, below the scanner's 1 MiB limit.
var bigPadding = strings.Repeat("# padding line that makes the file large enough\n", 13500)

func TestSourceFilesAboveTheYAMLLimitStillReadTheirVariables(t *testing.T) {
	for _, tc := range []struct {
		name, path, src string
	}{
		{"go", "cmd/big.go", "package main\n\nimport \"os\"\n\n// " + strings.ReplaceAll(bigPadding, "# ", "// ") + "\nfunc read() string {\n\treturn os.Getenv(\"SOME_UNIQUE_VAR\")\n}\n"},
		{"php", "app/Big.php", "<?php\n/*\n" + bigPadding + "*/\nclass Big\n{\n    public function read()\n    {\n        return getenv('SOME_UNIQUE_VAR');\n    }\n}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{tc.path: tc.src})
			g := index(t, root)
			var readers []string
			for _, id := range consumers(g, "env:SOME_UNIQUE_VAR") {
				if n := g.Nodes[id]; n != nil && n.Path == tc.path {
					readers = append(readers, id)
				}
			}
			if len(readers) != 1 {
				t.Fatalf("the read at the end of a %d KiB file must produce the edge, readers %v", len(tc.src)/1024, readers)
			}
		})
	}
}

func TestManifestsAboveTheYAMLLimitAreSkipped(t *testing.T) {
	compose := func(padding string) string {
		return padding + "services:\n  api:\n    environment:\n      SOME_UNIQUE_VAR: \"1\"\n"
	}
	root := t.TempDir()
	write(t, root, map[string]string{"small/docker-compose.yml": compose("")})
	if g := index(t, root); !g.Has("env:SOME_UNIQUE_VAR") {
		t.Fatal("control: a small compose file sets the variable")
	}
	root = t.TempDir()
	write(t, root, map[string]string{"big/docker-compose.yml": compose(bigPadding)})
	if g := index(t, root); g.Has("env:SOME_UNIQUE_VAR") {
		t.Fatal("a manifest above the YAML limit is not parsed and sets nothing")
	}
}
