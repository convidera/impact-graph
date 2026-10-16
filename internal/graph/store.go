package graph

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	_ "modernc.org/sqlite" // pure Go driver: querying needs no cgo
)

const schema = `
CREATE TABLE IF NOT EXISTS nodes (id TEXT PRIMARY KEY, type TEXT NOT NULL, label TEXT NOT NULL, path TEXT, line INTEGER, props TEXT NOT NULL DEFAULT '{}', repo TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS edges (src TEXT NOT NULL, dst TEXT NOT NULL, kind TEXT NOT NULL, conf REAL NOT NULL, evidence TEXT NOT NULL DEFAULT '', tag TEXT NOT NULL DEFAULT '', prov TEXT NOT NULL DEFAULT '', PRIMARY KEY (src, dst, kind));
CREATE INDEX IF NOT EXISTS edges_dst ON edges (dst);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
CREATE TABLE IF NOT EXISTS communities (id INTEGER PRIMARY KEY, label TEXT NOT NULL, dir TEXT NOT NULL DEFAULT '', size INTEGER NOT NULL, cohesion REAL NOT NULL, top TEXT NOT NULL DEFAULT '');
`

// SchemaError is returned by Load for a snapshot whose schema version is not the one this engine reads.
type SchemaError struct {
	Path  string
	Found int
	Want  int
}

// Error says which version the snapshot has and what to do about it.
func (e *SchemaError) Error() string {
	if e.Found > e.Want {
		return fmt.Sprintf("snapshot %s has schema version %d, this engine reads %d: it was written by a newer engine, update impact or re-run `impact index`", e.Path, e.Found, e.Want)
	}
	return fmt.Sprintf("snapshot %s has schema version %d, this engine reads %d: run `impact index` to rebuild it", e.Path, e.Found, e.Want)
}

// Save writes the graph as a SQLite snapshot, replacing the file. A snapshot is immutable and self-contained: it can be
// stored as an artifact next to a commit and loaded by any process that can read SQLite.
func Save(g *Graph, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := writeSnapshot(g, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func writeSnapshot(g *Graph, file string) error {
	db, err := sql.Open("sqlite", file)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertAll(tx, g); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return db.Close()
}

func insertAll(tx *sql.Tx, g *Graph) error {
	ns, err := tx.Prepare("INSERT INTO nodes (id,type,label,path,line,props,repo) VALUES (?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer ns.Close()
	for _, n := range g.Nodes {
		props, _ := json.Marshal(n.Props)
		if _, err := ns.Exec(n.ID, n.Type, n.Label, n.Path, n.Line, string(props), n.Repo); err != nil {
			return err
		}
	}
	es, err := tx.Prepare("INSERT INTO edges (src,dst,kind,conf,evidence,tag,prov) VALUES (?,?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer es.Close()
	for _, e := range g.Edges {
		if _, err := es.Exec(e.Src, e.Dst, e.Kind, e.Conf, e.Evidence, e.Tag, e.Prov); err != nil {
			return err
		}
	}
	cs, err := tx.Prepare("INSERT INTO communities (id,label,dir,size,cohesion,top) VALUES (?,?,?,?,?,?)")
	if err != nil {
		return err
	}
	defer cs.Close()
	for _, c := range g.Communities {
		top, _ := json.Marshal(c.Top)
		if _, err := cs.Exec(c.ID, c.Label, c.Dir, c.Size, c.Cohesion, string(top)); err != nil {
			return err
		}
	}
	meta := map[string]string{}
	for k, v := range g.Meta {
		meta[k] = v
	}
	meta["schema_version"] = strconv.Itoa(SchemaVersion)
	meta["engine_version"] = EngineVersion
	ms, err := tx.Prepare("INSERT INTO meta (key,value) VALUES (?,?)")
	if err != nil {
		return err
	}
	defer ms.Close()
	for k, v := range meta {
		if _, err := ms.Exec(k, v); err != nil {
			return err
		}
	}
	return nil
}

// Load reads a snapshot and indexes it for queries. A snapshot of another schema version is refused with a *SchemaError.
func Load(path string) (*Graph, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	meta, err := readMeta(db)
	if err != nil {
		return nil, fmt.Errorf("read snapshot %s: %w", path, err)
	}
	found := 1 // snapshots written before the version was stored are schema 1
	if v, ok := meta["schema_version"]; ok {
		if found, err = strconv.Atoi(v); err != nil {
			return nil, fmt.Errorf("snapshot %s has an invalid schema_version %q", path, v)
		}
	}
	if found != SchemaVersion {
		return nil, &SchemaError{Path: path, Found: found, Want: SchemaVersion}
	}
	g := New()
	if err := loadNodes(db, g); err != nil {
		return nil, err
	}
	if err := loadEdges(db, g); err != nil {
		return nil, err
	}
	if err := loadCommunities(db, g); err != nil {
		return nil, err
	}
	g.Meta = meta
	g.Index()
	return g, nil
}

func readMeta(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query("SELECT key,value FROM meta")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	meta := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		meta[k] = v
	}
	return meta, rows.Err()
}

func loadNodes(db *sql.DB, g *Graph) error {
	rows, err := db.Query("SELECT id,type,label,COALESCE(path,''),COALESCE(line,0),COALESCE(props,'{}'),COALESCE(repo,'') FROM nodes")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n Node
		var props string
		if err := rows.Scan(&n.ID, &n.Type, &n.Label, &n.Path, &n.Line, &props, &n.Repo); err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(props), &n.Props)
		g.AddNode(n)
	}
	return rows.Err()
}

func loadEdges(db *sql.DB, g *Graph) error {
	rows, err := db.Query("SELECT src,dst,kind,conf,COALESCE(evidence,''),COALESCE(tag,''),COALESCE(prov,'') FROM edges")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var e Edge
		if err := rows.Scan(&e.Src, &e.Dst, &e.Kind, &e.Conf, &e.Evidence, &e.Tag, &e.Prov); err != nil {
			return err
		}
		g.add(e.Src, e.Dst, e.Kind, e.Conf, e.Evidence, e.Tag, e.Prov)
	}
	return rows.Err()
}

func loadCommunities(db *sql.DB, g *Graph) error {
	rows, err := db.Query("SELECT id,label,COALESCE(dir,''),size,cohesion,COALESCE(top,'') FROM communities ORDER BY id")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c Community
		var top string
		if err := rows.Scan(&c.ID, &c.Label, &c.Dir, &c.Size, &c.Cohesion, &top); err != nil {
			return err
		}
		if top != "" {
			_ = json.Unmarshal([]byte(top), &c.Top)
		}
		g.Communities = append(g.Communities, c)
	}
	return rows.Err()
}
