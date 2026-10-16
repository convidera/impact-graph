package check

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/convidera/impact/internal/config"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		full := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func run(t *testing.T, files map[string]string, toml string) map[string]Result {
	t.Helper()
	root := t.TempDir()
	write(t, root, files)
	write(t, root, map[string]string{"impact.toml": toml})
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	rs, err := Run(root, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Result{}
	for _, r := range rs {
		out[r.Name] = r
	}
	return out
}

const registry = `{"x-events": [{"name": "a", "named": true}, {"name": "b"}, {"name": "c", "named": true, "producer": "adapter", "payload": {"properties": {"p1": {}, "p2": {}}}}],
 "x-list": {"k": ["x", "y"]}, "x-objs": [{"id": "o1"}, {"id": "o2"}]}`

func TestModesAndVocabulary(t *testing.T) {
	files := map[string]string{
		"contracts/r.json": registry,
		"src/named.php":    "<?php const NAMED = ['a', 'c'];\n// outside 'zz'\n",
		"src/all.php":      "case X = 'a'; case Y = 'b'; case Z = 'c';\n",
		"src/extra.php":    "case X = 'a'; case Y = 'nope';\n",
		"src/less.php":     "case X = 'a';\n",
	}
	rs := run(t, files, `
[[vocabulary]]
name = "ev"
file = "contracts/r.json"
list = "x-events"

[[check]]
name = "equal with where and region"
vocabulary = "ev"
where = "named"
files = ["src/named.php"]
region = ["NAMED = \\[", "\\];"]
extract = "'([a-z]+)'"
mode = "equal"

[[check]]
name = "subset passes"
vocabulary = "ev"
files = ["src/all.php"]
extract = "case \\w = '([a-z]+)'"

[[check]]
name = "subset fails on an undeclared value"
vocabulary = "ev"
files = ["src/extra.php"]
extract = "case \\w = '([a-z]+)'"

[[check]]
name = "superset fails when a declared value is missing"
vocabulary = "ev"
files = ["src/less.php"]
extract = "case \\w = '([a-z]+)'"
mode = "superset"

[[check]]
name = "equal fails when a declared value is missing"
vocabulary = "ev"
where = "named"
files = ["src/less.php"]
extract = "case \\w = '([a-z]+)'"
mode = "equal"

[[check]]
name = "ignore and add"
vocabulary = "ev"
files = ["src/extra.php"]
extract = "case \\w = '([a-z]+)'"
ignore = ["nope"]

[[check]]
name = "nothing extracted is a failure"
vocabulary = "ev"
files = ["src/all.php"]
extract = "NEVER ([a-z]+)"
`)
	want := map[string]bool{
		"equal with where and region":                     true,
		"subset passes":                                   true,
		"subset fails on an undeclared value":             false,
		"superset fails when a declared value is missing": false,
		"equal fails when a declared value is missing":    false,
		"ignore and add":                                  true,
		"nothing extracted is a failure":                  false,
	}
	for name, ok := range want {
		if rs[name].OK != ok {
			t.Errorf("%s: ok = %v, want %v (%v)", name, rs[name].OK, ok, rs[name].Problems)
		}
	}
	if p := rs["subset fails on an undeclared value"].Problems; len(p) != 1 || !strings.Contains(p[0], `"nope"`) {
		t.Errorf("the problem names the undeclared value and its file: %v", p)
	}
	if p := rs["equal fails when a declared value is missing"].Problems; len(p) != 1 || !strings.Contains(p[0], `declares "c"`) {
		t.Errorf("the problem names the missing value: %v", p)
	}
	if rs["equal with where and region"].Found != 2 {
		t.Errorf("a region keeps the match inside the constant: %+v", rs["equal with where and region"])
	}
}

func TestSources(t *testing.T) {
	files := map[string]string{
		"doc.json": `{"paths": {"/v1/a/{id}": {"get": {"operationId": "a.get"}}, "/v1/b": {"post": {"operationId": "b.post"}}},
 "components": {"schemas": {"Req": {"properties": {"p": {}, "q": {}}}}},
 "x-event-types": [{"type": "t1"}], "x-kinds": ["k1", "k2"]}`,
		"reg.json": registry,
		"types.go": "package x\n\ntype Spec struct {\n\tA string `json:\"a\"`\n\tB string `json:\"b,omitempty\"`\n\tC string `json:\"-\"`\n}\n",
		"use.php":  "$x = ['p' => 1, 'q' => 2]; call('/v1/b'); call('/v1/a/' . $id);\n",
		"keys.php": "keys: a b\n",
	}
	rs := run(t, files, `
[[check]]
name = "schema and paths in one check"
source = ["openapi-schema:doc.json#Req", "openapi-paths:doc.json"]
files = ["use.php"]
extract = "'([^']+)'"
mode = "subset"

[[check]]
name = "operations"
source = "openapi-operations:doc.json"
found_source = "openapi-operations:doc.json"
mode = "equal"

[[check]]
name = "event types and extension"
source = ["openapi-event-types:doc.json", "openapi-extension:doc.json#x-kinds"]
files = ["keys.php"]
extract = "(\\w+)"
mode = "subset"
add = ["keys", "a", "b"]

[[check]]
name = "registry keys and lists"
source = ["registry-list:reg.json#x-list.k", "registry-keys:reg.json#x-objs/id", "registry-payload-keys:reg.json#adapter"]
found_source = "registry-keys:reg.json#x-objs/id"
mode = "subset"

[[check]]
name = "go struct json names"
source = "go-struct:types.go#Spec"
found_source = "go-struct-required:types.go#Spec"
mode = "subset"

[[check]]
name = "go struct required are a subset of the names"
source = "go-struct-required:types.go#Spec"
found_source = "go-struct:types.go#Spec"
mode = "subset"

[[check]]
name = "unknown source"
source = "nope:doc.json#x"
files = ["use.php"]
extract = "x"
`)
	if !rs["schema and paths in one check"].OK {
		t.Errorf("schema property names and paths (also up to the first brace) are allowed: %v", rs["schema and paths in one check"].Problems)
	}
	for _, n := range []string{"operations", "event types and extension", "registry keys and lists", "go struct json names"} {
		if !rs[n].OK {
			t.Errorf("%s: %v", n, rs[n].Problems)
		}
	}
	if rs["go struct required are a subset of the names"].OK {
		t.Errorf("`b` is optional, so the names are not a subset of the required ones")
	}
	if rs["unknown source"].OK {
		t.Errorf("an unknown source kind fails")
	}
}
