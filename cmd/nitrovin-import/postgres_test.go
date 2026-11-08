package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// TestPostgres builds the artifact from a restored dump and checks it against
// that database, with SQL independent of the extract's: row counts, NULL and
// empty-string counts per column, and sampled rows field for field, with
// timestamps rendered by Postgres. It also extracts and builds twice to check
// the bytes do not depend on the order Postgres returns rows in.
//
// NITROVIN_PSQL is a shell command running psql on the restored dump that
// reads a script on stdin, for example
//
//	NITROVIN_PSQL='docker exec -i nhtsa-oracle psql -X -q -U postgres -d vpic'
//
// NITROVIN_MONTH is the dump's data month (default 2026_10).
func TestPostgres(t *testing.T) {
	psqlCmd := os.Getenv("NITROVIN_PSQL")
	if psqlCmd == "" {
		t.Skip("NITROVIN_PSQL not set: no restored dump to check against")
	}
	month := os.Getenv("NITROVIN_MONTH")
	if month == "" {
		month = "2026_10"
	}
	psql := func(script string) []byte {
		t.Helper()
		cmd := exec.Command("sh", "-c", psqlCmd)
		cmd.Stdin = strings.NewReader("\\set ON_ERROR_STOP on\n" + script)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("psql: %v\n%s", err, stderr.String())
		}
		return out
	}
	// query runs one SELECT and returns its rows' fields as COPY text.
	query := func(sql string) [][]string {
		t.Helper()
		var rows [][]string
		for _, l := range strings.Split(strings.TrimSuffix(string(psql("COPY ("+sql+") TO STDOUT;\n")), "\n"), "\n") {
			if l != "" {
				rows = append(rows, strings.Split(l, "\t"))
			}
		}
		return rows
	}

	var sqlScript bytes.Buffer
	if err := writeSQL(&sqlScript); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var arts [2][]byte
	for i := range arts {
		in := filepath.Join(dir, fmt.Sprintf("extract%d.copy", i))
		if err := os.WriteFile(in, psql(sqlScript.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, fmt.Sprintf("a%d.artifact", i))
		if err := run([]string{"build", "-month", month, "-in", in, "-o", out}, nil, nil, &bytes.Buffer{}); err != nil {
			t.Fatal(err)
		}
		var err error
		if arts[i], err = os.ReadFile(out); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(arts[0], arts[1]) {
		t.Fatal("two extracts of one dump built different artifacts")
	}
	a, err := artifact.Open(arts[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(); err != nil { // hash and sort order
		t.Fatal(err)
	}
	t.Logf("%d bytes, %d strings, blake3 %s", a.Size(), a.Strings(), a.Manifest().Blake3)

	// Row counts against the dump. The lookup union's count is summed over
	// every relation an element's lookuptable names, by a different route than
	// the extract's.
	rows := a.Manifest().Rows
	var counts []string
	for _, ts := range artifact.Schema() {
		if ts.Name != "lookup" {
			counts = append(counts, fmt.Sprintf("SELECT '%s', count(*) FROM vpic.%s", ts.Name, ts.Name))
		}
	}
	counts = append(counts, `SELECT 'lookup', sum((xpath('/row/n/text()',
		query_to_xml(format('SELECT count(*) AS n FROM vpic.%I', t), false, true, '')))[1]::text::bigint)
		FROM (SELECT DISTINCT lower(lookuptable) t FROM vpic.element
		      WHERE to_regclass('vpic.' || quote_ident(lower(lookuptable))) IS NOT NULL) l`)
	for _, r := range query(strings.Join(counts, " UNION ALL ")) {
		if n, _ := strconv.Atoi(r[1]); rows[r[0]] != n {
			t.Errorf("%s: %d rows in the artifact, %d in the dump", r[0], rows[r[0]], n)
		}
	}

	// NULL and empty-string counts per column: NULL must stay NULL and ""
	// must stay "".
	for _, tb := range a.Tables() {
		if tb.Name() == "lookup" {
			continue // covered by the sampled rows below
		}
		var exprs []string
		for _, c := range tb.Columns() {
			x := rawColumn(c.Name)
			exprs = append(exprs, fmt.Sprintf("count(*) FILTER (WHERE %s IS NULL)", x))
			if c.Type == artifact.TypeString {
				exprs = append(exprs, fmt.Sprintf("count(*) FILTER (WHERE %s = '')", x))
			}
		}
		want := query(fmt.Sprintf("SELECT %s FROM vpic.%s", strings.Join(exprs, ", "), tb.Name()))[0]
		var got []string
		for _, c := range tb.Columns() {
			nulls, empties := nullsAndEmpties(tb, c)
			got = append(got, strconv.Itoa(nulls))
			if c.Type == artifact.TypeString {
				got = append(got, strconv.Itoa(empties))
			}
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s NULL/empty counts: artifact %v, dump %v", tb.Name(), got, want)
		}
	}

	// Sampled rows: the first, middle and last row of each table plus seven
	// at random, each compared with every dump row sharing its key.
	rng := rand.New(rand.NewPCG(29, 2026))
	checked := 0
	for _, tb := range a.Tables() {
		n := tb.Rows()
		if n == 0 {
			continue
		}
		f := newSpotFormatter(tb)
		picks := []int{0, n / 2, n - 1}
		for range 7 {
			picks = append(picks, rng.IntN(n))
		}
		cols := tb.Columns()
		for _, i := range picks {
			var got []string
			for j := range n {
				if f.sameKey(i, j) {
					got = append(got, f.row(j))
				}
			}
			var sql string
			if tb.Name() == "lookup" {
				table, _ := tb.Strings("table").At(i)
				sql = fmt.Sprintf("SELECT %s, id, name FROM vpic.%s WHERE id = %d",
					sqlString(table), table, tb.Int32s("id")[i])
			} else {
				var exprs, where []string
				for k, c := range cols {
					exprs = append(exprs, rawColumn(c.Name))
					if k < tb.Key() {
						where = append(where, fmt.Sprintf("%s = %s", rawColumn(c.Name), f.literal(k, i)))
					}
				}
				sql = fmt.Sprintf("SELECT %s FROM vpic.%s WHERE %s",
					strings.Join(exprs, ", "), tb.Name(), strings.Join(where, " AND "))
			}
			var want []string
			for _, r := range query(sql) {
				want = append(want, strings.Join(r, "\t"))
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("%s row %d:\nartifact %q\n    dump %q", tb.Name(), i, got, want)
			}
			checked += len(got)
		}
	}
	t.Logf("%d sampled rows compared with the dump", checked)
}

// rawColumn is how the dump itself holds an artifact column.
func rawColumn(name string) string {
	if name == "changedon" {
		return "coalesce(updatedon, createdon)"
	}
	return name
}

func nullsAndEmpties(tb *artifact.Table, c artifact.ColumnSchema) (nulls, empties int) {
	for i := range tb.Rows() {
		switch c.Type {
		case artifact.TypeString:
			s, ok := tb.Strings(c.Name).At(i)
			if !ok {
				nulls++
			} else if s == "" {
				empties++
			}
		case artifact.TypeBool:
			if _, ok := tb.Bools(c.Name).At(i); !ok {
				nulls++
			}
		case artifact.TypeInt32:
			if tb.Int32s(c.Name)[i] == artifact.NullInt32 {
				nulls++
			}
		case artifact.TypeInt64:
			if tb.Int64s(c.Name)[i] == artifact.NullInt64 {
				nulls++
			}
		}
	}
	return nulls, empties
}

// spotFormatter renders artifact rows as Postgres prints the dump's own
// columns: inspect's COPY text, except timestamps as Postgres text.
type spotFormatter struct {
	t    *artifact.Table
	rows rowFormatter
	cols []artifact.ColumnSchema
}

func newSpotFormatter(t *artifact.Table) spotFormatter {
	return spotFormatter{t: t, rows: newRowFormatter(t), cols: t.Columns()}
}

func (f spotFormatter) field(k, i int) string {
	if c := f.cols[k]; c.Type == artifact.TypeInt64 {
		v := f.t.Int64s(c.Name)[i]
		if v == artifact.NullInt64 {
			return `\N`
		}
		return time.UnixMicro(v).UTC().Format("2006-01-02 15:04:05.999999")
	}
	return f.rows.cols[k](i)
}

func (f spotFormatter) row(i int) string {
	fields := make([]string, len(f.cols))
	for k := range f.cols {
		fields[k] = f.field(k, i)
	}
	return strings.Join(fields, "\t")
}

func (f spotFormatter) sameKey(i, j int) bool {
	for k := range f.t.Key() {
		if f.rows.cols[k](i) != f.rows.cols[k](j) {
			return false
		}
	}
	return true
}

// literal is row i's key column k as an SQL literal.
func (f spotFormatter) literal(k, i int) string {
	if f.cols[k].Type == artifact.TypeString {
		s, _ := f.t.Strings(f.cols[k].Name).At(i)
		return sqlString(s)
	}
	return f.rows.cols[k](i)
}

func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
