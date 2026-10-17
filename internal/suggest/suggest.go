// Package suggest mines git history for obligations: "when files under D change, a file C changes with them". The result is
// a list of candidate [[obligation]] tables for a person to confirm, not rules the engine applies by itself.
package suggest

import (
	"bufio"
	"bytes"
	"fmt"
	"os/exec"
	"path"
	"sort"
	"strings"

	"github.com/convidera/impact-graph/internal/config"
)

// Options bound the mining.
type Options struct {
	Commits    int     // how many commits to read
	MaxFiles   int     // commits touching more files than this are skipped (merges of everything, mass renames)
	MinSupport int     // commits in which trigger and companion changed together
	MinRatio   float64 // share of the commits touching the trigger that also touch the companion
	Top        int     // candidates to return
}

// DefaultOptions suit a repository with a few hundred commits.
func DefaultOptions() Options {
	return Options{Commits: 1500, MaxFiles: 40, MinSupport: 5, MinRatio: 0.8, Top: 12}
}

// Candidate is a proposed obligation with the evidence for it.
type Candidate struct {
	When    string  `json:"when"`    // glob of the trigger
	Require string  `json:"require"` // glob or path of the companion
	Support int     `json:"support"` // commits that touched both
	Of      int     `json:"of"`      // commits that touched the trigger
	Ratio   float64 `json:"ratio"`
}

// Mine reads the history of root and returns candidates, strongest first. Files that the configuration excludes, tests and
// generated files are not triggers: a test changing alongside code says nothing the graph does not already say, and the useful
// rule runs from the source to what is generated from it, not back.
func Mine(root string, cfg *config.Config, o Options) ([]Candidate, error) {
	out, err := exec.Command("git", "-C", root, "log", "--no-merges", "--name-only", "--pretty=format:%x00", "-n", fmt.Sprint(o.Commits)).Output()
	if err != nil {
		return nil, fmt.Errorf("git log: %w", err)
	}
	live := map[string]bool{}
	if ls, err := exec.Command("git", "-C", root, "ls-files").Output(); err == nil {
		for _, f := range strings.Split(strings.TrimSpace(string(ls)), "\n") {
			live[f] = true
		}
	}
	triggers := map[string]int{}
	companions := map[string]int{}
	pair := map[[2]string]int{}
	total := 0
	for _, c := range bytes.Split(out, []byte{0}) {
		var files []string
		sc := bufio.NewScanner(bytes.NewReader(c))
		for sc.Scan() {
			if p := strings.TrimSpace(sc.Text()); p != "" && live[p] && !cfg.Excluded(p) {
				files = append(files, p)
			}
		}
		if len(files) < 2 || len(files) > o.MaxFiles {
			continue
		}
		total++
		ts, cs := map[string]bool{}, map[string]bool{}
		for _, f := range files {
			cs[f] = true
			if d := path.Dir(f); d != "." {
				cs[d+"/**"] = true
			}
			if cfg.IsTest(f) || cfg.IsGenerated(f) {
				continue
			}
			for d := path.Dir(f); d != "." && d != "/"; d = path.Dir(d) {
				ts[d+"/**"] = true
			}
		}
		for t := range ts {
			triggers[t]++
			for k := range cs {
				if !covers(t, k) && !covers(k, t) {
					pair[[2]string{t, k}]++
				}
			}
		}
		for k := range cs {
			companions[k]++
		}
	}
	var all []Candidate
	for p, n := range pair {
		if n < o.MinSupport {
			continue
		}
		ratio := float64(n) / float64(triggers[p[0]])
		// a companion that changes in most commits (a changelog, a lock file) is not tied to the trigger: it has to change
		// with the trigger clearly more often than it changes at all
		if ratio < o.MinRatio || ratio-float64(companions[p[1]])/float64(total) < 0.25 {
			continue
		}
		all = append(all, Candidate{When: p[0], Require: p[1], Support: n, Of: triggers[p[0]], Ratio: ratio})
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		if a.Ratio != b.Ratio {
			return a.Ratio > b.Ratio
		}
		if a.Support != b.Support {
			return a.Support > b.Support
		}
		if a.When != b.When { // the broader trigger is the rule, the narrower ones are instances of it
			return len(a.When) < len(b.When)
		}
		if a.Require != b.Require { // the narrower companion is the more exact statement
			return len(a.Require) > len(b.Require)
		}
		return a.When+a.Require < b.When+b.Require
	})
	return prune(all, o.Top), nil
}

// prune drops a candidate when a stronger one already has the same companion and a broader (or equal) trigger, and drops
// companions that are only a narrower directory of an accepted one.
func prune(all []Candidate, top int) []Candidate {
	var out []Candidate
	for _, c := range all {
		dup := false
		for _, k := range out {
			if k.Require == c.Require && covers(k.When, c.When) {
				dup = true
				break
			}
			if k.When == c.When && (covers(k.Require, c.Require) || covers(c.Require, k.Require) && c.Support == k.Support) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, c)
		}
		if top > 0 && len(out) >= top {
			break
		}
	}
	return out
}

// covers reports whether the glob outer ("dir/**") contains the glob or path inner.
func covers(outer, inner string) bool {
	if !strings.HasSuffix(outer, "/**") {
		return outer == inner
	}
	return strings.HasPrefix(inner, strings.TrimSuffix(outer, "**"))
}

// TOML renders the candidates as [[obligation]] tables, the evidence in a comment, ready to paste after review.
func TOML(cs []Candidate) string {
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "# %d of %d commits that touched %s also touched %s (%.0f%%)\n", c.Support, c.Of, c.When, c.Require, c.Ratio*100)
		fmt.Fprintf(&b, "[[obligation]]\nname = %q\nwhen = [%q]\nrequire = [%q]\nwhy = \"history: they changed together in %d of %d commits\"\n\n",
			"a change under "+strings.TrimSuffix(c.When, "/**")+" comes with "+c.Require, c.When, c.Require, c.Support, c.Of)
	}
	return b.String()
}
