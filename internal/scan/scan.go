// Package scan lists the files of a repository and classifies them.
package scan

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/convidera/impact-graph/internal/config"
)

// File is one indexed file.
type File struct {
	Path string // slash-separated, relative to the root
	Kind string // code, test, doc or generated
	Ext  string
}

var indexed = map[string]bool{
	".php": true, ".go": true, ".md": true, ".mdx": true, ".rst": true, ".ts": true, ".tsx": true, ".js": true,
	".mjs": true, ".jsx": true, ".mts": true, ".vue": true, ".py": true, ".json": true, ".yaml": true, ".yml": true, ".toml": true, ".sql": true,
}

// List returns the files to index: tracked and untracked-but-not-ignored files when the root is a git checkout,
// otherwise a directory walk.
func List(root string, cfg *config.Config) ([]File, error) {
	var rels []string
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		out, err := exec.Command("git", "-C", root, "ls-files", "-co", "--exclude-standard", "-z").Output()
		if err == nil {
			for _, p := range bytes.Split(out, []byte{0}) {
				if len(p) > 0 {
					rels = append(rels, string(p))
				}
			}
		}
	}
	if len(rels) == 0 {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor") {
				return filepath.SkipDir
			}
			if !d.IsDir() {
				rel, _ := filepath.Rel(root, p)
				rels = append(rels, filepath.ToSlash(rel))
			}
			return nil
		})
	}
	var out []File
	for _, rel := range rels {
		ext := strings.ToLower(filepath.Ext(rel))
		if !indexed[ext] || cfg.Excluded(rel) {
			continue
		}
		st, err := os.Stat(filepath.Join(root, rel))
		if err != nil || st.IsDir() || st.Size() > cfg.Index.MaxBytes {
			continue
		}
		kind := "code"
		switch {
		case cfg.IsDoc(rel):
			kind = "doc"
		case cfg.IsTest(rel):
			kind = "test"
		case cfg.IsGenerated(rel):
			kind = "generated"
		}
		out = append(out, File{Path: rel, Kind: kind, Ext: ext})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
