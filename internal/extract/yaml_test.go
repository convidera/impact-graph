package extract

import "testing"

func TestValuesFileNames(t *testing.T) {
	for name, want := range map[string]bool{
		"values.yaml": true, "values.yml": true, "values-stage.yaml": true, "values.stage.yaml": true, "stage.values.yaml": true,
		"agent-environments.values.yaml": true, "my-values.yml": true, "Values.YAML": true,
		"docker-compose.yaml": false, "valuesx.yaml": false, "revalues.yaml": false, "values.json": false, "config.yaml": false,
	} {
		if got := valuesFile.MatchString(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}

func TestJSONTag(t *testing.T) {
	for lit, want := range map[string][2]any{
		"`json:\"adapter,omitempty\"`":         {"adapter", false},
		"`json:\",inline\"`":                   {"", true},
		"`json:\"-\"`":                         {"-", false},
		"`yaml:\"x\"`":                         {"", false},
		"\"json:\\\"name\\\" yaml:\\\"y\\\"\"": {"name", false},
		"":                                     {"", false},
	} {
		name, inline := jsonTag(lit)
		if name != want[0] || inline != want[1] {
			t.Errorf("%s: %q %v, want %v", lit, name, inline, want)
		}
	}
}
