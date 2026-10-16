// Package config reads the optional impact.toml of a repository. The indexer works without it (zero config); the file
// adds what no extractor can infer: contract files, roles of the services, declared links and golden flows.
package config

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/convidera/impact/internal/graph"
)

// Names the indexer looks for, in order.
var Names = []string{"impact.toml", ".impact.toml"}

// Config is the parsed file; the zero value is a valid, empty configuration.
type Config struct {
	Index      Index        `toml:"index"`
	Vocabulary []Vocabulary `toml:"vocabulary"`
	Links      []LinkDecl   `toml:"link"`
	Golden     []Golden     `toml:"golden"`
	Checks     []Check      `toml:"check"`
	Cluster    Cluster      `toml:"cluster"`

	Root                                  string `toml:"-"`
	exclude, tests, docs, generated, only glob
}

// Index selects and classifies files.
type Index struct {
	Exclude   []string `toml:"exclude"`
	Include   []string `toml:"include"`
	Tests     []string `toml:"tests"`
	Docs      []string `toml:"docs"`
	Generated []string `toml:"generated"`
	Db        string   `toml:"db"`
	MaxBytes  int64    `toml:"max_file_bytes"`
	History   *History `toml:"history"`
	Repo      string   `toml:"repo"` // owner/name; overrides the name taken from the origin remote
}

// Cluster tunes the community detection that runs after the index is built. Absent keys keep the defaults of
// graph.DefaultClusterOptions; a zero Resolution, MinSize or Seed does too. HubPercentile is a pointer so that an explicit
// 0 disables the hub rule. Disabled skips the clustering altogether.
type Cluster struct {
	Resolution    float64  `toml:"resolution"`
	HubPercentile *float64 `toml:"hub_percentile"`
	MinSize       int      `toml:"min_size"`
	Seed          int64    `toml:"seed"`
	Disabled      bool     `toml:"disabled"`
}

// ClusterOptions returns the clustering options of the configuration, merged over the defaults.
func (c *Config) ClusterOptions() graph.ClusterOptions {
	o := graph.DefaultClusterOptions()
	if c == nil {
		return o
	}
	if c.Cluster.Resolution > 0 {
		o.Resolution = c.Cluster.Resolution
	}
	if c.Cluster.HubPercentile != nil {
		o.HubPercentile = *c.Cluster.HubPercentile
	}
	if c.Cluster.MinSize > 0 {
		o.MinSize = c.Cluster.MinSize
	}
	if c.Cluster.Seed != 0 {
		o.Seed = c.Cluster.Seed
	}
	return o
}

// History controls co-change mining from git.
type History struct {
	Disabled   bool `toml:"disabled"`
	Commits    int  `toml:"commits"`
	MaxFiles   int  `toml:"max_files_per_commit"`
	MinSupport int  `toml:"min_support"`
}

// Vocabulary declares a list of string-keyed values (event types, channels, error codes) that several services share.
// Every file or function that mentions one as a string literal, or through an enum case or constant with that value,
// is joined to the same node. Produce and Consume are globs that give the mention a direction.
type Vocabulary struct {
	Name    string   `toml:"name"`
	Type    string   `toml:"type"`
	File    string   `toml:"file"`
	List    string   `toml:"list"`
	Key     string   `toml:"key"`
	NameKey string   `toml:"name_key"` // same as Key
	Values  []string `toml:"values"`
	Produce []string `toml:"produce"`
	Consume []string `toml:"consume"`
}

// Check is a drift check: a list kept by hand in code or documentation must equal, stay inside, or contain every value a
// contract declares (a vocabulary or a generated document). It reads text, not the graph, so it works on any language.
//
//	[[check]]
//	name = "sse-named-events (backend)"
//	vocabulary = "session-event"            # or: source = "openapi-schema:integration/api.json#MessageRequest"
//	where = "named"                         # only entries whose attribute is truthy, or "producer=adapter"
//	files = ["backend/app/Events/Frame.php"]
//	region = ["NAMED_EVENTS = \\[", "\\];"]   # optional start and end pattern that narrow the text
//	extract = "'([a-z_]+)'"                 # group 1 is a value
//	mode = "equal"                          # equal | subset (default: found values must be declared) | superset
//	ignore = ["heartbeat"]                  # found values to skip
//	skip_tests = true                       # leave test files out of `files`
//	blocks = "appendEvent\\((.*?)\\);"        # optional: apply extract inside every match of this (group 1)
//	found_source = "openapi-event-types:integration/adapter-api.json"   # values from a generated document instead of code
//	add = ["message"]                       # allowed on top of the vocabulary
//
// A source is `openapi-schema:<file>#<Schema>` (property names), `openapi-paths:<file>`, `openapi-operations:<file>`,
// `openapi-event-types:<file>`, `openapi-extension:<file>#<x-key>`, `registry-list:<file>#a.b`,
// `registry-keys:<file>#a.b/key`, `registry-payload-keys:<file>#<producer>`, `registry-family-keys:<file>#<prefix>`,
// `go-struct:<file>#<Type>` (json names) or `go-struct-required:<file>#<Type>` (those without omitempty); a list unions them.
type Check struct {
	Name        string   `toml:"name"`
	Vocabulary  string   `toml:"vocabulary"`
	Source      any      `toml:"source"`
	FoundSource string   `toml:"found_source"`
	Where       string   `toml:"where"`
	Files       []string `toml:"files"`
	Region      []string `toml:"region"`
	Extract     string   `toml:"extract"`
	Mode        string   `toml:"mode"`
	Ignore      []string `toml:"ignore"`
	SkipTests   bool     `toml:"skip_tests"`
	Blocks      string   `toml:"blocks"`
	Add         []string `toml:"add"`
}

// Sources returns the allowed-value sources of a check as a list.
func (c Check) Sources() []string {
	switch v := c.Source.(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// LinkDecl is a declared edge with a reason, for what no extractor can see (a call through reflection or a queue).
type LinkDecl struct {
	From string `toml:"from"`
	To   string `toml:"to"`
	Kind string `toml:"kind"`
	Why  string `toml:"why"`
}

// Golden pins a flow that must exist; `impact golden` fails when an extractor regresses or a refactor hides it.
type Golden struct {
	Name    string   `toml:"name"`
	From    string   `toml:"from"`
	To      string   `toml:"to"`
	Via     []string `toml:"via"`
	Mode    string   `toml:"mode"`
	MaxCost float64  `toml:"max_cost"`
	Absent  bool     `toml:"absent"`
}

type glob struct{ res []*regexp.Regexp }

// Load reads <root>/impact.toml when present.
func Load(root string, explicit string) (*Config, error) {
	c := &Config{Root: root}
	path := explicit
	if path == "" {
		for _, n := range Names {
			if _, err := os.Stat(filepath.Join(root, n)); err == nil {
				path = filepath.Join(root, n)
				break
			}
		}
	}
	if path != "" {
		if _, err := toml.DecodeFile(path, c); err != nil {
			return nil, err
		}
	}
	c.exclude = compile(append(DefaultExclude(), c.Index.Exclude...))
	c.tests = compile(append(DefaultTests(), c.Index.Tests...))
	c.docs = compile(append([]string{"**/*.md", "**/*.mdx", "**/*.rst"}, c.Index.Docs...))
	c.generated = compile(append(DefaultGenerated(), c.Index.Generated...))
	c.only = compile(c.Index.Include)
	if c.Index.MaxBytes == 0 {
		c.Index.MaxBytes = 1 << 20
	}
	if c.Index.Db == "" {
		c.Index.Db = ".impact/graph.db"
	}
	return c, nil
}

// DefaultExclude are directories and files nobody wants indexed.
func DefaultExclude() []string {
	return []string{
		"**/node_modules/**", "**/vendor/**", "**/.git/**", "**/dist/**", "**/build/**", "**/storage/**", "**/.impact/**",
		"**/__pycache__/**", "**/*.min.js", "**/*.lock", "**/.venv/**", "**/bootstrap/cache/**", "**/public/build/**",
	}
}

// DefaultTests classifies test files by convention across the stacks the indexer knows.
func DefaultTests() []string {
	return []string{
		"**/tests/**", "**/test/**", "**/__tests__/**", "**/*_test.go", "**/*.spec.ts", "**/*.spec.js", "**/*.test.ts",
		"**/*.test.js", "**/*.test.mjs", "**/*Test.php", "**/e2e/**", "**/test_*.py", "**/*_test.py",
	}
}

// DefaultGenerated classifies generated code by convention: a file in a generated directory, a generated name, or the
// output of a code generator for Go or protocol buffers.
func DefaultGenerated() []string {
	return []string{
		"**/generated/**", "**/__generated__/**", "**/*.generated.*", "**/*_generated.go", "**/zz_generated*.go", "**/*.pb.go",
	}
}

// Excluded reports whether a path is left out of the index.
func (c *Config) Excluded(p string) bool {
	if c.exclude.match(p) {
		return true
	}
	return len(c.only.res) > 0 && !c.only.match(p)
}

// IsTest reports whether a path is a test file.
func (c *Config) IsTest(p string) bool { return c.tests.match(p) }

// IsDoc reports whether a path is documentation.
func (c *Config) IsDoc(p string) bool { return c.docs.match(p) }

// IsGenerated reports whether a path is generated code (kept in the graph, but not reported as a result).
func (c *Config) IsGenerated(p string) bool { return c.generated.match(p) }

// Match reports whether a path matches any of the glob patterns (`**` crosses directories).
func Match(patterns []string, p string) bool { return compile(patterns).match(p) }

func compile(patterns []string) glob {
	var g glob
	for _, p := range patterns {
		g.res = append(g.res, globRegex(p))
	}
	return g
}

func (g glob) match(p string) bool {
	for _, r := range g.res {
		if r.MatchString(p) {
			return true
		}
	}
	return false
}

func globRegex(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 3
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case p[i] == '*':
			b.WriteString("[^/]*")
			i++
		case p[i] == '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
			i++
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}
