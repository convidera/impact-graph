// Package build runs the whole indexing pipeline: scan, extract, store.
package build

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/convidera/impact/internal/config"
	"github.com/convidera/impact/internal/extract"
	"github.com/convidera/impact/internal/graph"
	"github.com/convidera/impact/internal/scan"
)

// Stats summarises one run.
type Stats struct {
	Files       int
	Nodes       int
	Edges       int
	Communities int
	Seconds     float64
	Warnings    []string
}

// Build indexes the repository at root.
func Build(root string, cfg *config.Config) (*graph.Graph, Stats, error) {
	start := time.Now()
	files, err := scan.List(root, cfg)
	if err != nil {
		return nil, Stats{}, err
	}
	g := graph.New()
	ctx := &extract.Ctx{Root: root, Cfg: cfg, G: g, Files: files}
	extract.Run(ctx)
	repo := RepoName(root, cfg)
	for _, n := range g.Nodes {
		if n.Type != graph.TypeValue {
			n.Repo = repo
		}
	}
	g.Meta["built_at"] = time.Now().UTC().Format(time.RFC3339)
	g.Meta["root"] = filepath.Base(root)
	g.Meta["repo"] = repo
	g.Meta["commit"] = commit(root)
	if cfg == nil || !cfg.Cluster.Disabled {
		g.Cluster(cfg.ClusterOptions())
	}
	g.Index()
	return g, Stats{Files: len(files), Nodes: len(g.Nodes), Edges: len(g.Edges), Communities: len(g.Communities), Seconds: time.Since(start).Seconds(), Warnings: ctx.Warn}, nil
}

// RepoName returns the owner/name of the repository at root: the [index] repo of impact.toml when set, else the last two
// path segments of the origin remote, else the name of the directory.
func RepoName(root string, cfg *config.Config) string {
	if cfg != nil && cfg.Index.Repo != "" {
		return cfg.Index.Repo
	}
	if out, err := exec.Command("git", "-C", root, "remote", "get-url", "origin").Output(); err == nil {
		if name := parseRemote(string(out)); name != "" {
			return name
		}
	}
	return filepath.Base(root)
}

// parseRemote reads owner/name from a git remote URL: https, ssh, scp-like (git@host:owner/name) or a proxy path such as
// http://host/github.com/owner/name.git. It returns "" when the URL has fewer than two path segments.
func parseRemote(remote string) string {
	s := strings.TrimRight(strings.TrimSpace(remote), "/")
	var path string
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		j := strings.Index(rest, "/")
		if j < 0 {
			return ""
		}
		path = rest[j:]
	} else if i := strings.Index(s, ":"); i >= 0 {
		path = s[i+1:]
	} else {
		return ""
	}
	path = strings.TrimSuffix(strings.TrimRight(path, "/"), ".git")
	var parts []string
	for _, p := range strings.Split(path, "/") {
		if p != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) < 2 {
		return ""
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1]
}

// commit returns the HEAD commit of the repository at root, or "" when root is not a git repository.
func commit(root string) string {
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func (s Stats) String() string {
	return fmt.Sprintf("indexed %d files -> %d nodes, %d edges, %d communities in %.1fs", s.Files, s.Nodes, s.Edges, s.Communities, s.Seconds)
}
