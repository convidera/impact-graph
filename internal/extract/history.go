package extract

import (
	"bufio"
	"bytes"
	"os/exec"
	"sort"
	"strings"

	"github.com/convidera/impact-graph/internal/graph"
)

// runHistory mines git for files that change together. It needs no language support, so it works on any repository and
// finds exactly the things a checklist asks for and an agent forgets: the changelog entry, the generated client, the
// test plan next to the screen. Two files become a `co_changed` edge when, in a commit that touched A, B was touched
// too often enough (support and ratio); the edge confidence is that ratio.
func runHistory(ctx *Ctx) {
	h := ctx.Cfg.Index.History
	commits, maxFiles, minSupport := 1500, 40, 3
	if h != nil {
		if h.Disabled {
			return
		}
		if h.Commits > 0 {
			commits = h.Commits
		}
		if h.MaxFiles > 0 {
			maxFiles = h.MaxFiles
		}
		if h.MinSupport > 0 {
			minSupport = h.MinSupport
		}
	}
	out, err := exec.Command("git", "-C", ctx.Root, "log", "--no-merges", "--name-only", "--pretty=format:%x00", "-n", itoa(commits)).Output()
	if err != nil {
		return
	}
	indexed := map[string]bool{}
	for _, f := range ctx.Files {
		indexed[f.Path] = true
	}
	touch := map[string]int{}
	pair := map[[2]string]int{}
	for _, c := range bytes.Split(out, []byte{0}) {
		var files []string
		sc := bufio.NewScanner(bytes.NewReader(c))
		for sc.Scan() {
			if p := strings.TrimSpace(sc.Text()); p != "" && indexed[p] {
				files = append(files, p)
			}
		}
		if len(files) < 2 || len(files) > maxFiles {
			continue
		}
		sort.Strings(files)
		for _, f := range files {
			touch[f]++
		}
		for i := range files {
			for j := i + 1; j < len(files); j++ {
				pair[[2]string{files[i], files[j]}]++
			}
		}
	}
	for p, n := range pair {
		if n < minSupport {
			continue
		}
		// confidence: how often the less frequently changed file comes with the other
		denom := touch[p[0]]
		if touch[p[1]] < denom {
			denom = touch[p[1]]
		}
		conf := float64(n) / float64(denom)
		if conf < 0.5 {
			continue
		}
		ev := "changed together in " + itoa(n) + " commits"
		ctx.G.AddEdge(fileID(p[0]), fileID(p[1]), graph.CoChanged, conf, ev)
	}
}
