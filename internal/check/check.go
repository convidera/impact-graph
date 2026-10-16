// Package check runs the drift checks of impact.toml: a list kept by hand in code or documentation (a PHP constant, a TS
// array, Go constants, a docs table) must equal, stay inside, or contain every value that a contract declares. It reads
// text and JSON, never the graph, so it has no language support to maintain and needs no cgo.
package check

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/convidera/impact/internal/config"
	"github.com/convidera/impact/internal/scan"
)

// Result is the outcome of one check.
type Result struct {
	Name     string   `json:"name"`
	OK       bool     `json:"ok"`
	Found    int      `json:"found"`
	Problems []string `json:"problems,omitempty"`
}

func readJSON(root, file string) (map[string]any, error) {
	b, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return doc, nil
}

func walkPath(doc any, path string) any {
	cur := doc
	for _, part := range strings.Split(path, ".") {
		if part == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[part]
	}
	return cur
}

func strs(v any) []string {
	var out []string
	arr, _ := v.([]any)
	for _, x := range arr {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func keys(v any) []string {
	var out []string
	m, _ := v.(map[string]any)
	for k := range m {
		out = append(out, k)
	}
	return out
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

// sourceValues reads the allowed values from a generated document or a registry.
func sourceValues(root, source string) ([]string, error) {
	kind, rest, _ := strings.Cut(source, ":")
	file, schema, _ := strings.Cut(rest, "#")
	if kind == "go-struct" || kind == "go-struct-required" {
		return goStruct(root, file, schema, kind == "go-struct-required")
	}
	doc, err := readJSON(root, file)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	var out []string
	switch kind {
	case "openapi-schema":
		s := asMap(asMap(asMap(doc["components"])["schemas"])[schema])
		if s == nil {
			return nil, fmt.Errorf("%s: no schema %s", source, schema)
		}
		out = keys(s["properties"])
	case "openapi-paths":
		for p := range asMap(doc["paths"]) {
			out = append(out, p)
			if i := strings.Index(p, "{"); i >= 0 { // a path stops at its first `{` too: '/v1/permissions/'.$id
				out = append(out, p[:i])
			}
		}
	case "openapi-operations":
		for _, item := range asMap(doc["paths"]) {
			for _, op := range asMap(item) {
				if id, ok := asMap(op)["operationId"].(string); ok {
					out = append(out, id)
				}
			}
		}
	case "openapi-event-types":
		arr, _ := doc["x-event-types"].([]any)
		for _, e := range arr {
			if t, ok := asMap(e)["type"].(string); ok {
				out = append(out, t)
			}
		}
	case "openapi-extension":
		out = strs(doc[schema])
	case "registry-payload-keys":
		arr, _ := doc["x-events"].([]any)
		for _, e := range arr {
			if asMap(e)["producer"] == schema {
				out = append(out, keys(asMap(asMap(e)["payload"])["properties"])...)
			}
		}
	case "registry-family-keys":
		arr, _ := doc["x-event-families"].([]any)
		for _, e := range arr {
			if asMap(e)["prefix"] == schema {
				out = append(out, keys(asMap(asMap(e)["payload"])["properties"])...)
			}
		}
	case "registry-list", "registry-keys":
		path, key, _ := strings.Cut(schema, "/")
		cur := walkPath(doc, path)
		if kind == "registry-list" {
			out = strs(cur)
			break
		}
		arr, _ := cur.([]any)
		for _, e := range arr {
			if s, ok := asMap(e)[key].(string); ok {
				out = append(out, s)
			}
		}
	default:
		return nil, fmt.Errorf("unknown check source %q", source)
	}
	return out, nil
}

var jsonTag = regexp.MustCompile("`[^`\\n]*json:\"([^\",]+)((?:,[a-z]+)*)\"")

// goStruct reads the json names of a Go struct: the CRD types are Go structs that controller-gen turns into the schema.
func goStruct(root, file, name string, requiredOnly bool) ([]string, error) {
	b, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return nil, err
	}
	re := regexp.MustCompile(`(?ms)^type\s+` + regexp.QuoteMeta(name) + `\s+struct\s*\{(.*?)^\}`)
	m := re.FindSubmatch(b)
	if m == nil {
		return nil, fmt.Errorf("no struct %s in %s", name, file)
	}
	var out []string
	for _, t := range jsonTag.FindAllSubmatch(m[1], -1) {
		if string(t[1]) != "-" && (!requiredOnly || !strings.Contains(string(t[2]), "omitempty")) {
			out = append(out, string(t[1]))
		}
	}
	return out, nil
}

// vocabValues reads the values of a vocabulary file, filtered by `where` (an attribute that is truthy, or `a=b`).
func vocabValues(root string, v config.Vocabulary, where string) ([]string, error) {
	out := append([]string{}, v.Values...)
	if v.File == "" {
		return out, nil
	}
	b, err := os.ReadFile(filepath.Join(root, v.File))
	if err != nil {
		return nil, err
	}
	var doc any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", v.File, err)
	}
	key := v.Key
	if key == "" {
		key = "name"
	}
	attr, want, eq := strings.Cut(where, "=")
	arr, _ := walkPath(doc, v.List).([]any)
	for _, e := range arr {
		switch t := e.(type) {
		case string:
			if where == "" {
				out = append(out, t)
			}
		case map[string]any:
			name, ok := t[key].(string)
			if !ok {
				continue
			}
			switch {
			case where == "":
			case eq:
				if fmt.Sprint(t[attr]) != want {
					continue
				}
			default:
				if !truthy(t[attr]) {
					continue
				}
			}
			out = append(out, name)
		}
	}
	return out, nil
}

func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case float64:
		return t != 0
	case []any:
		return len(t) > 0
	case map[string]any:
		return len(t) > 0
	}
	return true
}

// Run executes every check of the configuration against the files of the repository.
func Run(root string, cfg *config.Config) ([]Result, error) {
	files, err := scan.List(root, cfg)
	if err != nil {
		return nil, err
	}
	vocabs := map[string]config.Vocabulary{}
	for _, v := range cfg.Vocabulary {
		vocabs[v.Name] = v
	}
	var out []Result
	for _, c := range cfg.Checks {
		out = append(out, runOne(root, cfg, files, vocabs, c))
	}
	return out, nil
}

func runOne(root string, cfg *config.Config, files []scan.File, vocabs map[string]config.Vocabulary, c config.Check) Result {
	var problems []string
	fail := func(f string, a ...any) { problems = append(problems, fmt.Sprintf(f, a...)) }
	mode := c.Mode
	if mode == "" {
		mode = "subset"
	}
	allowed := map[string]bool{}
	for _, a := range c.Add {
		allowed[a] = true
	}
	var scope string
	if srcs := c.Sources(); len(srcs) > 0 {
		for _, s := range srcs {
			vals, err := sourceValues(root, s)
			if err != nil {
				return Result{Name: c.Name, Problems: []string{err.Error()}}
			}
			for _, v := range vals {
				allowed[v] = true
			}
		}
		scope = strings.Join(srcs, ", ")
	} else {
		v, ok := vocabs[c.Vocabulary]
		if !ok {
			return Result{Name: c.Name, Problems: []string{fmt.Sprintf("unknown vocabulary %q", c.Vocabulary)}}
		}
		vals, err := vocabValues(root, v, c.Where)
		if err != nil {
			return Result{Name: c.Name, Problems: []string{err.Error()}}
		}
		for _, x := range vals {
			allowed[x] = true
		}
		scope = c.Vocabulary
		if c.Where != "" {
			scope += " (" + c.Where + ")"
		}
	}

	found := map[string]string{}
	if c.FoundSource != "" {
		vals, err := sourceValues(root, c.FoundSource)
		if err != nil {
			return Result{Name: c.Name, Problems: []string{err.Error()}}
		}
		for _, v := range vals {
			found[v] = c.FoundSource
		}
	}
	extractRe := "$^"
	if c.Extract != "" {
		extractRe = c.Extract
	}
	rx, err := regexp.Compile("(?m)" + extractRe)
	if err != nil {
		return Result{Name: c.Name, Problems: []string{"extract: " + err.Error()}}
	}
	var blocks *regexp.Regexp
	if c.Blocks != "" {
		if blocks, err = regexp.Compile("(?s)" + c.Blocks); err != nil {
			return Result{Name: c.Name, Problems: []string{"blocks: " + err.Error()}}
		}
	}
	var regionStart, regionEnd *regexp.Regexp
	if len(c.Region) == 2 {
		if regionStart, err = regexp.Compile(c.Region[0]); err != nil {
			return Result{Name: c.Name, Problems: []string{"region: " + err.Error()}}
		}
		if regionEnd, err = regexp.Compile(c.Region[1]); err != nil {
			return Result{Name: c.Name, Problems: []string{"region: " + err.Error()}}
		}
	}
	for _, f := range files {
		if !config.Match(c.Files, f.Path) || (c.SkipTests && cfg.IsTest(f.Path)) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, f.Path))
		if err != nil {
			continue
		}
		text := string(b)
		if regionStart != nil {
			s := regionStart.FindStringIndex(text)
			if s == nil {
				fail("%s: region start %q not found", f.Path, c.Region[0])
				continue
			}
			rest := text[s[1]:]
			if e := regionEnd.FindStringIndex(rest); e != nil {
				text = rest[:e[0]]
			} else {
				text = rest
			}
		}
		parts := []string{text}
		if blocks != nil {
			parts = nil
			for _, m := range blocks.FindAllStringSubmatch(text, -1) {
				if len(m) > 1 {
					parts = append(parts, m[1])
				}
			}
		}
		for _, part := range parts {
			for _, m := range rx.FindAllStringSubmatch(part, -1) {
				for _, g := range m[1:] { // the first group that took part (alternations)
					if g != "" {
						if _, seen := found[g]; !seen {
							found[g] = f.Path
						}
						break
					}
				}
			}
		}
	}
	for _, v := range c.Ignore {
		delete(found, v)
	}
	names := make([]string, 0, len(found))
	for v := range found {
		names = append(names, v)
	}
	sort.Strings(names)
	for _, v := range names {
		if !allowed[v] && mode != "superset" {
			fail("%s: %q is not declared in %s", found[v], v, scope)
		}
	}
	if mode == "equal" || mode == "superset" {
		where := strings.Join(c.Files, ", ")
		if where == "" {
			where = c.FoundSource
		}
		var missing []string
		for v := range allowed {
			if _, ok := found[v]; !ok {
				missing = append(missing, v)
			}
		}
		sort.Strings(missing)
		for _, v := range missing {
			fail("%s declares %q but %s does not list it", scope, v, where)
		}
	}
	if len(found) == 0 && len(problems) == 0 {
		where := strings.Join(c.Files, ", ")
		if where == "" {
			where = c.FoundSource
		}
		fail("nothing extracted from %s (check files/extract)", where)
	}
	return Result{Name: c.Name, OK: len(problems) == 0, Found: len(found), Problems: problems}
}
