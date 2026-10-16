package graph

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func edgeOf(t *testing.T, g *Graph, kind string) Edge {
	t.Helper()
	for _, e := range g.Edges {
		if e.Kind == kind {
			return e
		}
	}
	t.Fatalf("no %s edge in %+v", kind, g.Edges)
	return Edge{}
}

func TestStoreKeepsSchemaV2Fields(t *testing.T) {
	g := New()
	g.AddNode(Node{ID: "a", Type: TypeFunction, Label: "A", Path: "x.php", Line: 4, Repo: "o/r", Props: map[string]string{"community": "1", "k": "v"}})
	g.AddNode(Node{ID: "b", Type: TypeFunction, Label: "B"})
	g.AddTagged("a", "b", Calls, 0.9, "ev", "t1")
	g.AddEdgeProv("a", "b", Link, 0.8, "why", ProvHuman)
	g.Communities = []Community{{ID: 1, Label: "billing", Dir: "app/Billing", Size: 2, Cohesion: 0.75, Top: []string{"a", "b"}}}
	g.Meta["built_at"] = "now"
	path := filepath.Join(t.TempDir(), "sub", "g.db")
	if err := Save(g, path); err != nil {
		t.Fatal(err)
	}
	if g.Meta["schema_version"] != "" {
		t.Fatalf("Save must not mutate the caller's Meta: %v", g.Meta)
	}
	h, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	a := h.Nodes["a"]
	if a == nil || a.Repo != "o/r" || a.Path != "x.php" || a.Line != 4 || a.Props["k"] != "v" || a.Props["community"] != "1" {
		t.Fatalf("node fields lost: %+v", a)
	}
	if e := edgeOf(t, h, Calls); e.Tag != "t1" || e.Prov != ProvAST || e.Conf != 0.9 {
		t.Fatalf("call edge lost: %+v", e)
	}
	if e := edgeOf(t, h, Link); e.Prov != ProvHuman {
		t.Fatalf("explicit provenance lost: %+v", e)
	}
	if len(h.Communities) != 1 || h.Communities[0].Label != "billing" || h.Communities[0].Dir != "app/Billing" ||
		h.Communities[0].Size != 2 || h.Communities[0].Cohesion != 0.75 || len(h.Communities[0].Top) != 2 {
		t.Fatalf("communities lost: %+v", h.Communities)
	}
	if h.Meta["built_at"] != "now" || h.Meta["schema_version"] != "2" || h.Meta["engine_version"] != EngineVersion {
		t.Fatalf("meta lost: %v", h.Meta)
	}
	if hits, _ := h.Walk("a", Options{Mode: ModeImpact, Dir: "down", MaxCost: 5, MinConf: 0.5}); hits["b"] == nil {
		t.Fatal("loaded graph must be indexed for queries")
	}
}

// writeSnapshotFile creates a SQLite file with the given statements, the way an older or newer engine could have.
func writeSnapshotFile(t *testing.T, stmts ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

func TestLoadRefusesSchemaOne(t *testing.T) {
	path := writeSnapshotFile(t,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, type TEXT NOT NULL, label TEXT NOT NULL, path TEXT, line INTEGER, props TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE edges (src TEXT NOT NULL, dst TEXT NOT NULL, kind TEXT NOT NULL, conf REAL NOT NULL, evidence TEXT NOT NULL DEFAULT '', tag TEXT NOT NULL DEFAULT '', PRIMARY KEY (src, dst, kind))`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta (key,value) VALUES ('built_at','yesterday')`,
		`INSERT INTO nodes (id,type,label) VALUES ('a','function','A')`,
	)
	_, err := Load(path)
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("want *SchemaError, got %v", err)
	}
	if se.Found != 1 || se.Want != SchemaVersion || se.Path != path {
		t.Fatalf("wrong schema error: %+v", se)
	}
	want := "snapshot " + path + " has schema version 1, this engine reads 2: run `impact index` to rebuild it"
	if se.Error() != want {
		t.Fatalf("message\n got %q\nwant %q", se.Error(), want)
	}
}

func TestLoadRefusesNewerSchema(t *testing.T) {
	path := writeSnapshotFile(t,
		`CREATE TABLE nodes (id TEXT PRIMARY KEY, type TEXT NOT NULL, label TEXT NOT NULL, path TEXT, line INTEGER, props TEXT NOT NULL DEFAULT '{}', repo TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE edges (src TEXT NOT NULL, dst TEXT NOT NULL, kind TEXT NOT NULL, conf REAL NOT NULL, evidence TEXT NOT NULL DEFAULT '', tag TEXT NOT NULL DEFAULT '', prov TEXT NOT NULL DEFAULT '', PRIMARY KEY (src, dst, kind))`,
		`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)`,
		`INSERT INTO meta (key,value) VALUES ('schema_version','3')`,
	)
	_, err := Load(path)
	var se *SchemaError
	if !errors.As(err, &se) {
		t.Fatalf("want *SchemaError, got %v", err)
	}
	if se.Found != 3 {
		t.Fatalf("want Found 3, got %+v", se)
	}
	want := "snapshot " + path + " has schema version 3, this engine reads 2: it was written by a newer engine, update impact or re-run `impact index`"
	if se.Error() != want {
		t.Fatalf("message\n got %q\nwant %q", se.Error(), want)
	}
}

func TestLoadMissingFileDoesNotCreateIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "none.db")
	if _, err := Load(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("want ErrNotExist, got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Load must not create a missing snapshot")
	}
}
