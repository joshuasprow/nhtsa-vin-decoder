package bake

import (
	"context"
	"database/sql"
	"fmt"
	. "github.com/joshuasprow/nitrovin/prototypes/artifact-format/model"
	"io/fs"
	"os"
	"strings"

	_ "modernc.org/sqlite"
	"modernc.org/sqlite/vfs"
)

func sqlType(t ColType) string {
	switch t {
	case I32, I64, Bool:
		return "INTEGER"
	}
	return "TEXT"
}

// BuildSQLite writes every table as a clustered WITHOUT ROWID table keyed on
// its sort key (or a rowid table plus an index when the key is not unique),
// rows inserted in sort order, then VACUUM.
func BuildSQLite(d *Data, path string) error {
	os.Remove(path)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, p := range []string{"pragma journal_mode=off", "pragma synchronous=off", "pragma page_size=4096"} {
		if _, err := db.Exec(p); err != nil {
			return err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	for _, t := range d.Tables {
		def := t.Def
		var cols []string
		for _, c := range def.Cols {
			cols = append(cols, c.Name+" "+sqlType(c.Type)+" NOT NULL")
		}
		ddl := fmt.Sprintf("create table %s (%s", def.Name, strings.Join(cols, ", "))
		if def.Unique {
			ddl += fmt.Sprintf(", primary key (%s)) without rowid", strings.Join(def.Sort, ", "))
		} else {
			ddl += ")"
		}
		if _, err := tx.Exec(ddl); err != nil {
			return err
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", len(def.Cols)), ",")
		st, err := tx.Prepare(fmt.Sprintf("insert into %s values (%s)", def.Name, ph))
		if err != nil {
			return err
		}
		args := make([]any, len(def.Cols))
		for i := range t.N {
			for ci, c := range def.Cols {
				col := &t.Cols[ci]
				switch c.Type {
				case I32:
					args[ci] = col.I32[i]
				case I64:
					args[ci] = col.I64[i]
				case Bool:
					args[ci] = B2i(col.B[i])
				case Str:
					args[ci] = col.S[i]
				}
			}
			if _, err := st.Exec(args...); err != nil {
				return fmt.Errorf("%s row %d: %w", def.Name, i, err)
			}
		}
		st.Close()
		if !def.Unique {
			if _, err := tx.Exec(fmt.Sprintf("create index %s_key on %s (%s)", def.Name, def.Name, strings.Join(def.Sort, ", "))); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_, err = db.Exec("vacuum")
	return err
}

const candidatesSQL = `select ws.yearfrom, p.id, p.keys, p.elementid, p.attributeid, p.created
from wmi w
join wmi_vinschema ws on ws.wmiid = w.id
join vinschema s on s.id = ws.vinschemaid
join pattern p on p.vinschemaid = ws.vinschemaid
where w.wmi = ?1 and w.pad <= ?2 and (?3 = 0 or ?3 between ws.yearfrom and ws.yearto) and s.tobeqced = 0`

// SQLitePushdown moves the LIKE half of the key match and the element
// exclusion into SQL so fewer rows cross the driver (bracket keys still do).
var SQLitePushdown bool

const pushdownSQL = candidatesSQL + `
and p.elementid not in (26, 27, 29, 39)
and (instr(p.keys, '[') > 0 or ?4 like replace(p.keys, '*', '_') || '%')`

type SQLite struct {
	db         *sql.DB
	conn       *sql.Conn
	cand, look *sql.Stmt
	elements   []Element
	vfs        *vfs.FS
}

// OpenSQLiteFile opens a database file read-only and immutable (no locking,
// no journal probing).
func OpenSQLiteFile(path string) (*SQLite, error) {
	return openSQLite("file:" + path + "?mode=ro&immutable=1")
}

// OpenSQLiteFS serves the database from an fs.FS (embed.FS, or a MapFS over a
// decompressed buffer) through modernc's read-only VFS; nothing is copied.
func OpenSQLiteFS(fsys fs.FS, name string) (*SQLite, error) {
	vname, v, err := vfs.New(fsys)
	if err != nil {
		return nil, err
	}
	s, err := openSQLite("file:" + name + "?vfs=" + vname + "&mode=ro&immutable=1")
	if err != nil {
		return nil, err
	}
	s.vfs = v
	return s, nil
}

func openSQLite(dsn string) (*SQLite, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	s := &SQLite{db: db, conn: conn}
	if _, err := conn.ExecContext(ctx, "pragma cache_size=-65536"); err != nil {
		return nil, err
	}
	q := candidatesSQL
	if SQLitePushdown {
		q = pushdownSQL
	}
	if s.cand, err = conn.PrepareContext(ctx, q); err != nil {
		return nil, err
	}
	if s.look, err = conn.PrepareContext(ctx, "select name from lookup where tag = ?1 and id = ?2"); err != nil {
		return nil, err
	}
	rows, err := conn.QueryContext(ctx, "select id, lookuptable, isprivate, hasdecode from element order by id")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e Element
		if err := rows.Scan(&e.ID, &e.Lookup, &e.Private, &e.HasDecode); err != nil {
			return nil, err
		}
		e.Present = true
		for int(e.ID) >= len(s.elements) {
			s.elements = append(s.elements, Element{})
		}
		s.elements[e.ID] = e
	}
	return s, rows.Err()
}

func (s *SQLite) Elements() []Element { return s.elements }

func (s *SQLite) Candidates(wmi, vk string, year int32, now int64, fn func(*PatternRow)) {
	args := []any{wmi, now, year}
	if SQLitePushdown {
		args = append(args, vk)
	}
	rows, err := s.cand.Query(args...)
	if err != nil {
		panic(err)
	}
	defer rows.Close()
	var r PatternRow
	for rows.Next() {
		if err := rows.Scan(&r.Priority, &r.ID, &r.Keys, &r.ElementID, &r.Attr, &r.Created); err != nil {
			panic(err)
		}
		fn(&r)
	}
	if err := rows.Err(); err != nil {
		panic(err)
	}
}

func (s *SQLite) Lookup(tag string, id int32) (string, bool) {
	var name string
	if err := s.look.QueryRow(tag, id).Scan(&name); err != nil {
		return "", false
	}
	return name, true
}

func (s *SQLite) Close() error {
	s.cand.Close()
	s.look.Close()
	s.conn.Close()
	err := s.db.Close()
	if s.vfs != nil {
		s.vfs.Close()
	}
	return err
}

// LoadAll reads every table into columnar Go slices: the "SQLite as a
// container, decode from the heap" variant. Returns the row count.
func (s *SQLite) LoadAll() (int, error) {
	total := 0
	for _, def := range Defs {
		if def.Optional {
			continue
		}
		names := make([]string, len(def.Cols))
		for i, c := range def.Cols {
			names[i] = c.Name
		}
		rows, err := s.conn.QueryContext(context.Background(), fmt.Sprintf("select %s from %s", strings.Join(names, ", "), def.Name))
		if err != nil {
			return 0, err
		}
		t := &Table{Def: def, Cols: make([]Column, len(def.Cols))}
		dst := make([]any, len(def.Cols))
		var i32 []int32 = make([]int32, len(def.Cols))
		var i64 []int64 = make([]int64, len(def.Cols))
		var b []bool = make([]bool, len(def.Cols))
		var str []string = make([]string, len(def.Cols))
		for i, c := range def.Cols {
			switch c.Type {
			case I32:
				dst[i] = &i32[i]
			case I64:
				dst[i] = &i64[i]
			case Bool:
				dst[i] = &b[i]
			case Str:
				dst[i] = &str[i]
			}
		}
		for rows.Next() {
			if err := rows.Scan(dst...); err != nil {
				return 0, err
			}
			for i, c := range def.Cols {
				col := &t.Cols[i]
				switch c.Type {
				case I32:
					col.I32 = append(col.I32, i32[i])
				case I64:
					col.I64 = append(col.I64, i64[i])
				case Bool:
					col.B = append(col.B, b[i])
				case Str:
					col.S = append(col.S, str[i])
				}
			}
			t.N++
		}
		if err := rows.Err(); err != nil {
			return 0, err
		}
		total += t.N
	}
	return total, nil
}
