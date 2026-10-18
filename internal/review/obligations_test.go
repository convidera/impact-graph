package review

import (
	"strings"
	"testing"

	"github.com/convidera/impact-graph/internal/config"
)

var rules = []config.Obligation{
	{Name: "regenerate", When: []string{"app/Data/**"}, Unless: []string{"**/*Test.php"}, Require: []string{"api/openapi.json", "api/internal.json"}, Why: "derived", Fix: "make generate"},
	{Name: "test plan", When: []string{"web/**/*.vue"}, Require: []string{"plans/**"}},
}

func TestObligationFiresWhenTriggeredAndUnmet(t *testing.T) {
	got := Obligations(rules, []string{"app/Data/A.php", "app/Data/B.php", "README.md"})
	if len(got) != 1 || got[0].Name != "regenerate" {
		t.Fatalf("got %+v", got)
	}
	if strings.Join(got[0].TriggeredBy, ",") != "app/Data/A.php,app/Data/B.php" || got[0].Fix != "make generate" {
		t.Errorf("details: %+v", got[0])
	}
}

func TestObligationIsMetByAnyRequiredFile(t *testing.T) {
	if got := Obligations(rules, []string{"app/Data/A.php", "api/internal.json"}); len(got) != 0 {
		t.Errorf("any one required file satisfies it: %+v", got)
	}
	if got := Obligations(rules, []string{"web/pages/a.vue", "plans/a.md"}); len(got) != 0 {
		t.Errorf("a glob in require: %+v", got)
	}
}

func TestObligationIgnoresUnlessAndUntriggered(t *testing.T) {
	if got := Obligations(rules, []string{"app/Data/ATest.php"}); len(got) != 0 {
		t.Errorf("unless: %+v", got)
	}
	if got := Obligations(rules, []string{"docs/a.md"}); len(got) != 0 {
		t.Errorf("not triggered: %+v", got)
	}
}

func TestObligationListsAtMostFiveTriggers(t *testing.T) {
	var files []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g"} {
		files = append(files, "app/Data/"+n+".php")
	}
	if got := Obligations(rules, files); len(got) != 1 || len(got[0].TriggeredBy) != 5 {
		t.Errorf("got %+v", got)
	}
}

func TestTextShowsObligationsFirst(t *testing.T) {
	r := Report{Base: "main", Changed: []string{"app/Data/A.php"}, Unmet: Obligations(rules, []string{"app/Data/A.php"})}
	out := Text(r, 5)
	i, j := strings.Index(out, "obligations this change has not met"), strings.Index(out, "nothing related")
	if i < 0 || j >= 0 {
		t.Fatalf("an unmet obligation is reported and 'nothing missing' is not claimed:\n%s", out)
	}
	for _, want := range []string{"regenerate", "triggered by app/Data/A.php", "api/openapi.json or api/internal.json", "why: derived", "fix: make generate"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}
