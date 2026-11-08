package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// Each table is extracted with one COPY over a SELECT whose expressions follow
// artifact.Schema's columns. A column is read as itself unless overridden here.
// Rows come out in whatever order Postgres likes: artifact.Encode sorts them.
var (
	// micros turns a zoneless timestamp into microseconds since 1970-01-01
	// 00:00. extract(epoch) is numeric, so this is exact.
	micros = func(expr string) string {
		return fmt.Sprintf("(extract(epoch from %s) * 1000000)::bigint", expr)
	}
	// changedon is the only way spVinDecode reads either timestamp.
	changedon = micros("coalesce(updatedon, createdon)")

	extracts = map[string]extract{
		"wmi": {from: "vpic.wmi", exprs: map[string]string{
			"publicavailabilitydate": micros("publicavailabilitydate"),
			"changedon":              changedon,
		}},
		"pattern":            {from: "vpic.pattern", exprs: map[string]string{"changedon": changedon}},
		"enginemodelpattern": {from: "vpic.enginemodelpattern", exprs: map[string]string{"changedon": changedon}},
		"defaultvalue":       {from: "vpic.defaultvalue", exprs: map[string]string{"changedon": changedon}},
		"vehiclespecpattern": {from: "vpic.vehiclespecpattern", exprs: map[string]string{"changedon": changedon}},
		"lookup": {from: "pg_temp.nitrovin_lookups()", exprs: map[string]string{
			"table": "lookup_table", "id": "lookup_id", "name": "lookup_name",
		}},
	}
)

type extract struct {
	from  string
	exprs map[string]string
}

func (e extract) query(ts artifact.TableSchema) string {
	cols := make([]string, len(ts.Columns))
	for i, c := range ts.Columns {
		cols[i] = c.Name
		if x, ok := e.exprs[c.Name]; ok {
			cols[i] = x
		}
	}
	return fmt.Sprintf("SELECT %s FROM %s", strings.Join(cols, ", "), e.from)
}

func extractFor(ts artifact.TableSchema) extract {
	if e, ok := extracts[ts.Name]; ok {
		return e
	}
	return extract{from: "vpic." + ts.Name}
}

// lookupsFunc unions every lookup table some element names, as spVinDecode's
// fElementAttributeValue resolves them: by the relation Postgres finds for the
// unquoted name, which is the lowercase one. Names with no relation in the
// dump (ten in 2026_10) are skipped.
const lookupsFunc = `CREATE FUNCTION pg_temp.nitrovin_lookups()
  RETURNS TABLE (lookup_table text, lookup_id integer, lookup_name text)
  LANGUAGE plpgsql AS $$
DECLARE t text;
BEGIN
  FOR t IN SELECT DISTINCT c.relname::text
           FROM vpic.element e
           JOIN pg_class c ON c.relname = lower(e.lookuptable)
                          AND c.relnamespace = 'vpic'::regnamespace
  LOOP
    RETURN QUERY EXECUTE format('SELECT %L::text, id::integer, name::text FROM vpic.%I', t, t);
  END LOOP;
END $$;
`

// marker opens each table's rows in the stream, and endMarker closes the
// stream. COPY's text format escapes every backslash in data, so no data line
// can start with one followed by a dot.
const (
	marker    = `\.nitrovin `
	endMarker = marker + "end"
)

// writeSQL writes the psql script that extracts every table, in Schema order,
// as one stream: a marker line, then the table's COPY rows. It runs in one
// repeatable-read transaction, so every table comes from one snapshot, and
// rolls back, so it leaves nothing behind.
func writeSQL(w io.Writer) error {
	var b strings.Builder
	b.WriteString("\\set ON_ERROR_STOP on\n")
	b.WriteString("SET client_encoding = 'UTF8';\n")
	b.WriteString("BEGIN ISOLATION LEVEL REPEATABLE READ;\n")
	b.WriteString(lookupsFunc)
	for _, ts := range artifact.Schema() {
		fmt.Fprintf(&b, "\\echo '\\\\.nitrovin %s'\n", ts.Name)
		fmt.Fprintf(&b, "COPY (%s) TO STDOUT;\n", extractFor(ts).query(ts))
	}
	fmt.Fprintf(&b, "\\echo '\\\\.nitrovin end'\n")
	b.WriteString("ROLLBACK;\n")
	_, err := io.WriteString(w, b.String())
	return err
}
