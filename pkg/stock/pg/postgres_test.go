package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The tests named *Postgres run against the database at
// $NITROVIN_STOCK_PG_DSN, a URL (postgres://user:password@host:port/db) of
// a database they own: they drop and create the schemas parts, yard, stock
// and nvtest and the role nitrovin_stock in it. Without it they skip; CI's
// Stock connector job sets it, and CONTRIBUTING.md has a docker command.

// testDB is a connection to $NITROVIN_STOCK_PG_DSN as its own user, and
// the DSN; the test skips without one.
func testDB(t *testing.T) (*pgx.Conn, *url.URL) {
	t.Helper()
	dsn := os.Getenv("NITROVIN_STOCK_PG_DSN")
	if dsn == "" {
		t.Skip("NITROVIN_STOCK_PG_DSN is not set: no Postgres to test the connector against")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" {
		t.Fatalf("NITROVIN_STOCK_PG_DSN is not a postgres:// URL")
	}
	conn, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connecting to NITROVIN_STOCK_PG_DSN: %v", err)
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn, u
}

// exec runs sql, statements separated by semicolons, failing the test on
// an error.
func exec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// withUser is u as user and password.
func withUser(u *url.URL, user, password string) string {
	v := *u
	v.User = url.UserPassword(user, password)
	return v.String()
}

const resetViews = `DROP SCHEMA IF EXISTS stock, parts, yard CASCADE;
DO $$ BEGIN
  IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'nitrovin_stock') THEN
    EXECUTE 'DROP OWNED BY nitrovin_stock';
    DROP ROLE nitrovin_stock;
  END IF;
END $$`

// seeded is a stock file's rows as the parts and yard systems of
// testdata/vocab/stock/live/views.sql hold them, and as a stock file:
// the connector's source and the same rows loaded as a file.
func seed(t *testing.T, conn *pgx.Conn, d *nitrovin.Decoder, f *vocab.File, shelf, dir string) {
	t.Helper()
	header := map[string]any{"format": 1, "kind": "stock", "name": f.Name, "version": f.Version, "source": f.Holds, "as_of": f.AsOf}
	if f.Holds == vocab.StockShelf {
		header["catalog"] = f.StockCatalog
		if f.Release != "" {
			header["release"] = f.Release
			exec(t, conn, `INSERT INTO parts.release (shelf, release) VALUES ($1, $2)`, shelf, f.Release)
		}
	}
	lines := []any{header}
	for _, r := range f.Stock {
		line := map[string]any{"id": r.ID, "location": r.Location, "fields": r.Fields}
		var make_, model *string
		if r.Vehicle != nil {
			make_, model = new(r.Vehicle.Entry["make"]), new(r.Vehicle.Entry["model"])
			line["vehicle"], line["year"] = r.Vehicle.Entry, *r.Year
		}
		if f.Holds == vocab.StockShelf {
			var typ, number, side, part *string
			switch {
			case r.Key != nil:
				typ, number = &r.Key.Type, &r.Key.Number
				line["type"], line["number"] = r.Key.Type, r.Key.Number
				if r.Key.Side != vocab.SideNone {
					side = new(r.Key.Side.String())
					line["side"] = *side
				}
			case len(r.PartTypes) == 1:
				part = &r.PartTypes[0]
				line["part"] = map[string]string{"spine": "part-types", "id": *part}
			default:
				t.Fatalf("%s: row %s reaches part types %v; a relation row has one", f.Name, r.ID, r.PartTypes)
			}
			exec(t, conn, `INSERT INTO parts.shelf (shelf, stock_id, bin, part_class, catalog_no, hand, spine_part, make, model, model_year, attrs)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, shelf, r.ID, r.Location, typ, number, side, part, make_, model, r.Year, string(r.Fields))
			lines = append(lines, line)
			continue
		}
		// The yard system decodes its VINs itself, by nitrovin's decode,
		// and keeps the decode beside the vehicle (ADR 0020).
		dec := r.Decoded
		if dec == nil && r.VIN != "" {
			got, err := d.Decode(contract.Request{VIN: r.VIN, Full: true, Now: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
			if err != nil {
				t.Fatal(err)
			}
			items := make([]vocab.Item, len(got.Items))
			for i, it := range got.Items {
				items[i] = vocab.Item{ElementID: it.ElementID, Value: it.Value.V, AttributeID: it.AttributeID.V}
			}
			if v := vocab.NewDecoded(d.Month(), items); v.Model != 0 && v.ModelYear != nil {
				dec = &vocab.StockDecoded{Make: v.Make, Model: v.Model, Year: *v.ModelYear}
			}
		}
		var dmake, dmodel, dyear *int32
		if dec != nil {
			dmake, dmodel, dyear = new(dec.Make), new(dec.Model), new(int32(dec.Year))
			line["decoded"] = map[string]any{"make": dec.Make, "model": dec.Model, "year": dec.Year}
		}
		exec(t, conn, `INSERT INTO yard.vehicles (stock_id, lot, vpic_make, vpic_model, vpic_year, make, model, model_year, attrs)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, r.ID, r.Location, dmake, dmodel, dyear, make_, model, r.Year, string(r.Fields))
		lines = append(lines, line)
	}
	var b []byte
	for _, l := range lines {
		j, err := json.Marshal(l)
		if err != nil {
			t.Fatal(err)
		}
		b = append(append(b, j...), '\n')
	}
	if err := os.WriteFile(filepath.Join(dir, f.Name+".jsonl"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// open loads paths beside d, with connectors reading their DSN from env.
func open(t *testing.T, d *nitrovin.Decoder, env map[string]string, paths ...string) *nitrovin.Translator {
	t.Helper()
	files, err := vocab.Open(paths...)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslatorWith(d, nitrovin.TranslatorConfig{Connect: Connect(func(k string) string { return env[k] })}, files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

var fitNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

// fitJSON is tr's answer to request as JSON.
func fitJSON(t *testing.T, tr *nitrovin.Translator, request string) []byte {
	t.Helper()
	req, err := contract.ParseFitRequest([]byte(request))
	if err != nil {
		t.Fatalf("%s: %v", request, err)
	}
	req.Now = fitNow
	f, err := tr.Fit(context.Background(), req)
	if err != nil {
		t.Fatalf("%s: %v", request, err)
	}
	b, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sourcesOf calls fn with every source block of an answer, decoded.
func sourcesOf(v any, fn func(block map[string]any)) {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			if k == "sources" {
				for _, b := range x.([]any) {
					fn(b.(map[string]any))
				}
				continue
			}
			sourcesOf(x, fn)
		}
	case []any:
		for _, x := range v {
			sourcesOf(x, fn)
		}
	}
}

// stockIDs are an answer's stock rows as "id/matched[/reached]", list by
// list, in answer order.
func stockIDs(t *testing.T, answer []byte) string {
	t.Helper()
	type list struct {
		Total int
		Rows  []struct{ ID, Matched, Reached string }
	}
	var f struct {
		Candidates []struct{ Shelf, Yard *list }
		Shelf      *list
		Yard       *list
		ByVehicle  *list `json:"shelf_by_vehicle"`
	}
	if err := json.Unmarshal(answer, &f); err != nil {
		t.Fatal(err)
	}
	var out []string
	add := func(name string, l *list) {
		if l == nil {
			return
		}
		s := fmt.Sprintf("%s %d:", name, l.Total)
		for _, r := range l.Rows {
			s += " " + r.ID + "/" + r.Matched
			if r.Reached == string(contract.ReachedRetired) {
				s += "/retired"
			}
		}
		out = append(out, s)
	}
	for i, c := range f.Candidates {
		add(fmt.Sprintf("candidate %d shelf", i), c.Shelf)
		add(fmt.Sprintf("candidate %d yard", i), c.Yard)
	}
	add("shelf", f.Shelf)
	add("yard", f.Yard)
	add("shelf_by_vehicle", f.ByVehicle)
	return strings.Join(out, "; ")
}

// TestParityPostgres: views over a synthetic parts system's and yard
// system's tables (testdata/vocab/stock/live/views.sql), holding the rows
// of testdata/vocab/stock's shelf and yard, answer every fit exactly as
// the same rows loaded as files do: the same candidates and rows, in the
// same order, with the same totals, pages, fields and release, each
// source block saying kind live, its connector and relation. The rows'
// ids and routes are the committed files' own.
func TestParityPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, resetViews)
	views, err := os.ReadFile("../../../testdata/vocab/stock/live/views.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, conn, string(views))
	t.Cleanup(func() { exec(t, conn, resetViews) })

	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	const fixtures = "../../../testdata/vocab"
	committed, err := vocab.Open(fixtures, fixtures+"/fitment", fixtures+"/stock")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, f := range committed {
		switch f.Name {
		case "shelf-stock":
			seed(t, conn, d, f, "main", dir)
		case "shelf-annex":
			seed(t, conn, d, f, "annex", dir)
		case "yard-stock":
			seed(t, conn, d, f, "", dir)
		}
	}
	reader := withUser(u, "nitrovin_stock", "stock-reader")
	live := open(t, d, map[string]string{"NITROVIN_STOCK_DSN": reader}, fixtures, fixtures+"/fitment", fixtures+"/stock/live")
	files := open(t, d, nil, fixtures, fixtures+"/fitment", dir)
	original := open(t, d, nil, fixtures, fixtures+"/fitment", fixtures+"/stock")

	for _, h := range live.Info().Vocabularies {
		if h.StockCounts != nil && h.Status != contract.StockUnchecked {
			t.Errorf("%s before any fit: status %q, want unchecked: nothing connects at load", h.Name, h.Status)
		}
	}

	const ram = `"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010`
	requests := []string{
		`{` + ram + `,"part":{"vocabulary":"part-codes","entry":{"code":"ROT"}},"stock":["shelf","yard"]}`,
		`{` + ram + `,"part":{"vocabulary":"part-codes","entry":{"code":"MIR"}},"stock":["shelf"]}`,
		`{` + ram + `,"part":{"vocabulary":"part-codes","entry":{"code":"MIR"}},"stock":["shelf"],"location":["north"]}`,
		`{"from":{"vocabulary":"catalog-models","entry":{"make":"Chevrolet","model":"Silverado 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"engine"},"stock":["shelf","yard"]}`,
		`{"from":"1D7RV1GT5AS123456","part":{"vocabulary":"part-codes","entry":{"code":"ROT"}},"stock":["shelf","yard"]}`,
		`{"from":"1D7RV1GT5AS123456","related":true,"stock":["yard"]}`,
		`{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"stock":["shelf","yard"]}`,
		`{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-2001"}},"stock":["shelf","yard"],"location":["north","east","south"],"limit":1}`,
		`{"from":{"fitment":{"type":"mirror","number":"MR-1001","side":"left"}},"stock":["shelf","yard"]}`,
	}
	// Every page of the paged fitment key's answer, by its cursor.
	for after := ""; ; {
		req := `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-2001"}},"stock":["shelf","yard"],"limit":2` + after + `}`
		requests = append(requests, req)
		var page struct{ Next string }
		if err := json.Unmarshal(fitJSON(t, files, req), &page); err != nil {
			t.Fatal(err)
		}
		if page.Next == "" {
			break
		}
		after = `,"after":"` + page.Next + `"`
	}

	var blocks int
	for _, req := range requests {
		gotLive, gotFile := fitJSON(t, live, req), fitJSON(t, files, req)
		if got, want := stockIDs(t, gotLive), stockIDs(t, fitJSON(t, original, req)); got != want {
			t.Errorf("%s\nlive rows  %s\nfixtures'  %s", req, got, want)
		}
		var a, b any
		if json.Unmarshal(gotLive, &a) != nil || json.Unmarshal(gotFile, &b) != nil {
			t.Fatal("an answer is not JSON")
		}
		sourcesOf(a, func(block map[string]any) {
			blocks++
			if block["kind"] != "live" || block["connector"] != "postgres" || block["relation"] == nil || block["status"] != "available" || block["as_of"] != nil {
				t.Errorf("%s: live block %v", req, block)
			}
			if block["name"] == "shelf-stock" && (block["release"] != "7.01" || block["catalog_mismatch"] != true) {
				t.Errorf("%s: shelf-stock's block %v; want release 7.01, read from its release relation, and catalog_mismatch", req, block)
			}
			for _, k := range []string{"kind", "connector", "relation"} {
				delete(block, k)
			}
		})
		sourcesOf(b, func(block map[string]any) {
			delete(block, "kind")
			delete(block, "as_of") // a file's header time; the views keep no as_of
		})
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s\nlive  %s\nfiles %s", req, gotLive, gotFile)
		}
	}
	if blocks == 0 {
		t.Fatal("no answer carried a stock source")
	}

	info, err := json.Marshal(live.Info())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range live.Info().Vocabularies {
		if h.StockCounts != nil && (h.Status != contract.StockAvailable || h.Connector != "postgres" || h.Relation == "" || h.Rows != nil || h.AsOf != "") {
			t.Errorf("%s in Info after fits: %+v", h.Name, *h.StockCounts)
		}
	}
	for _, secret := range []string{"stock-reader", "nitrovin_stock\"", u.Host, u.Hostname() + ":"} {
		if strings.Contains(string(info), secret) {
			t.Errorf("Info shows %q: %s", secret, info)
		}
	}
}

// nvtest is a schema of relations for the failure tests: nvtest.shelf has
// every shelf column and an as_of, and the views over it each break one
// rule.
const nvtest = `DROP SCHEMA IF EXISTS nvtest CASCADE;
CREATE SCHEMA nvtest;
CREATE TABLE nvtest.shelf (id text, location text, type text, number text, side text, part_type text,
  vehicle_make text, vehicle_model text, year smallint, fields jsonb, as_of timestamptz);
INSERT INTO nvtest.shelf VALUES
  ('S-1', 'north', 'mirror', 'MR-1001', 'left', NULL, NULL, NULL, NULL, '{"price": 129.5}', '2026-11-01 08:00:00+00'),
  ('S-2', 'south', 'mirror', 'MR-1001', 'left', NULL, NULL, NULL, NULL, NULL, '2026-10-30 23:30:00-05'),
  ('S-3', 'north', NULL, NULL, NULL, 'mirror', 'Dodge', 'Ram 1500', 2010, '[1, 2]', NULL),
  ('S-4', 'north', 'mirror', 'MR-1001', 'right', NULL, NULL, NULL, NULL, '{}', '2026-11-05 00:00:00+00'),
  (NULL, 'north', 'mirror', 'MR-1001', 'left', NULL, NULL, NULL, NULL, '{}', NULL);
CREATE TABLE nvtest.release (release text);
INSERT INTO nvtest.release VALUES ('7.02'), ('7.03');
CREATE VIEW nvtest.dated AS SELECT id, location, type, number, side, part_type, vehicle_make, vehicle_model, year, fields, as_of::date AS as_of FROM nvtest.shelf;
CREATE VIEW nvtest.no_fields AS SELECT id, location, type, number, side, part_type, vehicle_make, vehicle_model, year FROM nvtest.shelf;
CREATE VIEW nvtest.text_year AS SELECT id, location, type, number, side, part_type, vehicle_make, vehicle_model, year::text AS year, fields FROM nvtest.shelf;
CREATE VIEW nvtest.json_fields AS SELECT id, location, type, number, side, part_type, vehicle_make, vehicle_model, year, ('  ' || fields::text)::json AS fields FROM nvtest.shelf;
CREATE FUNCTION nvtest.sleepy() RETURNS boolean LANGUAGE sql VOLATILE AS $$ SELECT pg_sleep(5); SELECT true $$;
CREATE VIEW nvtest.slow AS SELECT * FROM nvtest.shelf WHERE nvtest.sleepy()`

// connector is a shelf connector on parts-catalog reading relation with
// dsn, its header's postgres members extra added, closed when the test
// ends.
func connector(t *testing.T, relation, dsn, extra string) *Source {
	t.Helper()
	s := unclosed(t, relation, dsn, extra)
	t.Cleanup(s.Close)
	return s
}

// unclosed is connector without the Close at the test's end, for a test
// of Close itself: a Close that hangs fails it, never its cleanup.
func unclosed(t *testing.T, relation, dsn, extra string) *Source {
	t.Helper()
	dir := t.TempDir()
	header := `{"format":1,"kind":"stock","name":"shelf-live","version":"1","source":"shelf","catalog":"parts-catalog","connector":{"postgres":{"relation":"` + relation + `","dsn_env":"DSN"` + extra + `}}}`
	if err := os.WriteFile(filepath.Join(dir, "shelf-live.jsonl"), []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open("../../../testdata/vocab/fitment", dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*vocab.File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	return New(stock.Declared{File: byName["shelf-live"], Catalog: byName["parts-catalog"], Vehicles: byName["catalog-models"]}, dsn)
}

var mirrorKey = [][]vocab.FitmentKey{{{Type: "mirror", Number: "MR-1001", Side: vocab.SideLeft}}}

// reason is the reason a lookup of mirrorKey gives, failing the test when
// it answers.
func reason(t *testing.T, ctx context.Context, s *Source) string {
	t.Helper()
	_, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 10})
	u, ok := errors.AsType[*stock.Unavailable](err)
	if !ok {
		t.Fatalf("lookup: %v, want *stock.Unavailable", err)
	}
	return u.Reason
}

// TestUnavailablePostgres: a relation that lacks a column, or has one of
// the wrong type, or is not there, makes the source unavailable naming it,
// in its lookups and its health; so does a release relation of two rows;
// and wrong credentials or database name a fixed reason with no secret.
func TestUnavailablePostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, nvtest)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	dsn := u.String()
	for _, tc := range []struct{ name, relation, dsn, extra, want string }{
		{"missing column", "nvtest.no_fields", dsn, "", "column fields is missing from nvtest.no_fields"},
		{"mistyped column", "nvtest.text_year", dsn, "", "column year of nvtest.text_year is text, not an integer (smallint, integer or bigint)"},
		{"no relation", "nvtest.nothing", dsn, "", "relation nvtest.nothing does not exist, or the connection's user cannot read it"},
		{"no release column", "nvtest.shelf", dsn, `,"release_relation":"nvtest.no_fields"`, "column release is missing from nvtest.no_fields"},
		{"two releases", "nvtest.shelf", dsn, `,"release_relation":"nvtest.release"`, "release relation nvtest.release holds more than one row"},
		{"wrong password", "nvtest.shelf", withUser(u, u.User.Username(), "not-the-password-hunter2"), "", reasonRefused},
		{"no database", "nvtest.shelf", strings.Replace(dsn, u.Path, "/stockdb_nowhere", 1), "", reasonNoDatabase},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := connector(t, tc.relation, tc.dsn, tc.extra)
			got := reason(t, context.Background(), s)
			if got != tc.want {
				t.Errorf("reason %q, want %q", got, tc.want)
			}
			if h := s.Health(); tc.name != "two releases" && (h.Down == nil || h.Down.Reason != tc.want) {
				t.Errorf("Health %+v", h)
			}
			for _, secret := range []string{"hunter2", u.Host, u.User.Username() + ":", "stockdb_nowhere"} {
				if strings.Contains(got, secret) {
					t.Errorf("the reason shows %q: %s", secret, got)
				}
			}
		})
	}
}

// TestRowsPostgres: rows carry their own as_of, a timestamp in UTC or a
// date, and each list's block in a fit says the oldest of that list's own
// rows, none for a list with none; a row with no id is never served;
// fields that are null or not an object are {}, and a json column's text
// is read as jsonb's; and every query runs in a READ ONLY transaction.
func TestRowsPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, nvtest)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	ctx := context.Background()

	s := connector(t, "nvtest.shelf", u.String(), "")
	rs, meta, err := s.ShelfByFitment(ctx, append(mirrorKey, nil), stock.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs[0].Rows {
		got = append(got, r.ID+" "+r.AsOf+" "+string(r.Fields))
	}
	want := []string{`S-1 2026-11-01T08:00:00Z {"price": 129.5}`, `S-2 2026-10-31T04:30:00Z {}`}
	if !slices.Equal(got, want) || rs[0].Total != 2 || rs[1].Total != 0 || meta.AsOf != "" {
		t.Errorf("rows %q, totals %d %d, meta as_of %q\nwant %q, totals 2 0, and the as_of the rows' own", got, rs[0].Total, rs[1].Total, meta.AsOf, want)
	}
	vehicle := []stock.ShelfVehicle{{PartTypes: []string{"mirror"}, Vehicles: []vocab.Vehicle{{Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}}, Year: new(2010)}}
	rs, _, err = s.ShelfByVehicle(ctx, vehicle, stock.Page{Limit: 10})
	if err != nil || len(rs[0].Rows) != 1 || string(rs[0].Rows[0].Fields) != "{}" || rs[0].Rows[0].Vehicle.Entry["model"] != "Ram 1500" || rs[0].Rows[0].AsOf != "" {
		t.Errorf("by vehicle: %+v, %v; want S-3 with fields {} (its fields are an array), and no as_of", rs, err)
	}

	s = connector(t, "nvtest.json_fields", u.String(), "")
	if rs, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 1}); err != nil || len(rs[0].Rows) != 1 || string(rs[0].Rows[0].Fields) != `{"price": 129.5}` {
		t.Errorf("a json column with leading spaces: %+v, %v; want S-1's object", rs, err)
	}

	s = connector(t, "nvtest.dated", u.String(), "")
	if rs, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 10}); err != nil || rs[0].Rows[1].AsOf != "2026-10-31" {
		t.Errorf("a date as_of: %+v, %v; want S-2's 2026-10-31", rs, err)
	}

	// In a fit, two candidates with rows of different times, and a list
	// whose rows keep none.
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	header := `{"format":1,"kind":"stock","name":"shelf-live","version":"1","source":"shelf","catalog":"parts-catalog","connector":{"postgres":{"relation":"nvtest.shelf","dsn_env":"DSN"}}}`
	if err := os.WriteFile(filepath.Join(dir, "shelf-live.jsonl"), []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tr := open(t, d, map[string]string{"DSN": u.String()}, "../../../testdata/vocab", "../../../testdata/vocab/fitment", dir)
	t.Cleanup(tr.Close)
	type list struct {
		Total   int
		Sources []contract.StockSource
	}
	var f struct {
		Candidates []struct {
			Fitment struct{ Side string }
			Shelf   list
		}
		ByVehicle list `json:"shelf_by_vehicle"`
	}
	if err := json.Unmarshal(fitJSON(t, tr, `{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"vocabulary":"part-codes","entry":{"code":"MIR"}},"stock":["shelf"]}`), &f); err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, c := range f.Candidates {
		got = append(got, fmt.Sprintf("%s %d %q", c.Fitment.Side, c.Shelf.Total, c.Shelf.Sources[0].AsOf))
	}
	got = append(got, fmt.Sprintf("by vehicle %d %q", f.ByVehicle.Total, f.ByVehicle.Sources[0].AsOf))
	if want := []string{`left 2 "2026-10-31T04:30:00Z"`, `right 1 "2026-11-05T00:00:00Z"`, `by vehicle 1 ""`}; !slices.Equal(got, want) {
		t.Errorf("as_of per list %q\nwant %q", got, want)
	}

	c, err := s.pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	_, err = c.Exec(ctx, `CREATE TABLE nvtest.written (id text)`)
	if pe, ok := errors.AsType[*pgconn.PgError](err); !ok || pe.Code != "25006" {
		t.Errorf("a write on the source's connection: %v; want read_only_sql_transaction", err)
	}
}

// TestSlowPostgres: a query that outlives the caller's deadline stops
// there, unavailable, and the database stops running it; one that outlives
// the statement timeout says so; Close with a slow query under way waits
// for it without holding up the lookup or Health; a fit's list of a slow
// source answers unavailable while the rest of the answer stands; and when
// every connection is busy, a lookup waits only its own bound.
func TestSlowPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, nvtest)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	ctx := context.Background()
	// running is how many queries on nvtest.slow the database runs, as
	// the connector writes the relation, quoted; it waits up to 2 s for
	// want.
	running := func(want int) int {
		t.Helper()
		var n int
		for range 40 {
			if err := conn.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity WHERE state = 'active' AND query LIKE '%"nvtest"."slow"%' AND pid <> pg_backend_pid()`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n == want {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		return n
	}
	// slowLookup starts a lookup of s under ctx, its reason on the
	// channel, once the database runs its query.
	slowLookup := func(ctx context.Context, s *Source) chan string {
		t.Helper()
		out := make(chan string, 1)
		go func() {
			_, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 10})
			u, _ := errors.AsType[*stock.Unavailable](err)
			if u == nil {
				out <- fmt.Sprint("not unavailable: ", err)
				return
			}
			out <- u.Reason
		}()
		if n := running(1); n != 1 {
			t.Fatalf("%d queries on nvtest.slow run; want the lookup's", n)
		}
		return out
	}

	t.Run("deadline", func(t *testing.T) {
		s := connector(t, "nvtest.slow", u.String(), `,"statement_timeout_ms":10000`)
		if _, _, err := s.ready(ctx); err != nil {
			t.Fatal(err)
		}
		dctx, cancel := context.WithCancel(ctx)
		defer cancel()
		got := slowLookup(dctx, s)
		start := time.Now()
		cancel()
		select {
		case r := <-got:
			if r != reasonCancelled {
				t.Errorf("reason %q, want %q", r, reasonCancelled)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("the lookup ran on 2 s after its context ended")
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("the lookup took %v past its context", took)
		}
		if n := running(0); n != 0 {
			t.Errorf("%d queries on nvtest.slow still run after the context ended", n)
		}
	})

	t.Run("close", func(t *testing.T) {
		s := unclosed(t, "nvtest.slow", u.String(), `,"statement_timeout_ms":10000`)
		if _, _, err := s.ready(ctx); err != nil {
			t.Fatal(err)
		}
		got := slowLookup(ctx, s)
		closed := make(chan struct{})
		go func() {
			s.Close() // waits for the lookup's connection
			close(closed)
		}()
		// Health answers while Close waits, and soon says closed.
		healthy := make(chan stock.Health, 1)
		go func() {
			for {
				h := s.Health()
				if h.Down != nil && h.Down.Reason == reasonClosed {
					healthy <- h
					return
				}
				time.Sleep(10 * time.Millisecond)
			}
		}()
		select {
		case <-healthy:
		case <-time.After(2 * time.Second):
			t.Fatal("Health did not say closed within 2 s of Close: it waits on Close, or Close never marked it")
		}
		// The database ends the backend: the lookup fails on a dead
		// connection, which it gives back for Close to finish.
		exec(t, conn, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE '%"nvtest"."slow"%' AND pid <> pg_backend_pid()`)
		select {
		case <-closed:
		case <-time.After(3 * time.Second):
			t.Fatal("Close did not return 3 s after the lookup's backend ended")
		}
		select {
		case r := <-got:
			if r == "" || strings.HasPrefix(r, "not unavailable") {
				t.Errorf("the lookup: %q", r)
			}
		case <-time.After(time.Second):
			t.Error("the lookup did not return")
		}
	})

	t.Run("statement timeout", func(t *testing.T) {
		s := connector(t, "nvtest.slow", u.String(), `,"statement_timeout_ms":300`)
		start := time.Now()
		if got, want := reason(t, ctx, s), "the query ran past its statement timeout, 300 ms"; got != want {
			t.Errorf("reason %q, want %q", got, want)
		}
		if took := time.Since(start); took > 3*time.Second {
			t.Errorf("the lookup took %v with a 300 ms statement timeout", took)
		}
	})

	t.Run("fit", func(t *testing.T) {
		d, err := nitrovin.Embedded()
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		header := `{"format":1,"kind":"stock","name":"shelf-slow","version":"1","source":"shelf","catalog":"parts-catalog","connector":{"postgres":{"relation":"nvtest.slow","dsn_env":"DSN","statement_timeout_ms":10000}}}`
		if err := os.WriteFile(filepath.Join(dir, "shelf-slow.jsonl"), []byte(header+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tr := open(t, d, map[string]string{"DSN": u.String()}, "../../../testdata/vocab", "../../../testdata/vocab/fitment", "../../../testdata/vocab/stock", dir)
		req, err := contract.ParseFitRequest([]byte(`{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-2001"}},"stock":["shelf","yard"]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Now = fitNow
		fctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
		defer cancel()
		f, err := tr.Fit(fctx, req)
		if err != nil {
			t.Fatal(err)
		}
		last := f.Shelf.Sources[len(f.Shelf.Sources)-1]
		if last.Name != "shelf-slow" || last.Status != contract.StockUnavailable || last.Reason != contract.StockCancelled || f.Shelf.Total == 0 || f.Yard.Total == 0 {
			t.Errorf("the slow source's block %+v, shelf %d, yard %d; want it unavailable, the files' rows standing", last, f.Shelf.Total, f.Yard.Total)
		}
	})

	t.Run("pool exhausted", func(t *testing.T) {
		s := connector(t, "nvtest.shelf", u.String(), `,"max_connections":1,"connect_timeout_ms":200,"statement_timeout_ms":300`)
		if _, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 1}); err != nil {
			t.Fatal(err)
		}
		held, err := s.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if got, want := reason(t, ctx, s), "all 1 of its connections were busy until the lookup's deadline"; got != want {
			t.Errorf("reason %q, want %q", got, want)
		}
		if took := time.Since(start); took < 400*time.Millisecond || took > 2*time.Second {
			t.Errorf("waited %v for a connection; want its bound, 500 ms", took)
		}
		held.Release()
		if _, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 1}); err != nil {
			t.Errorf("after the connection is free: %v", err)
		}
	})
}

// TestChangedPostgres: a relation that loses a column after its check
// fails the next lookup and resets the source, which checks again after
// Retry and names the column.
func TestChangedPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, nvtest)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	exec(t, conn, `CREATE VIEW nvtest.changing AS SELECT * FROM nvtest.shelf`)
	ctx := context.Background()
	s := connector(t, "nvtest.changing", u.String(), "")
	clock := time.Now()
	s.now = func() time.Time { return clock }
	if _, _, err := s.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	exec(t, conn, `DROP VIEW nvtest.changing; CREATE VIEW nvtest.changing AS SELECT id, location, type, number, side, part_type, vehicle_make, vehicle_model, year FROM nvtest.shelf`)
	if got := reason(t, ctx, s); got != reasonChanged {
		t.Errorf("after the change: %q, want %q", got, reasonChanged)
	}
	if h := s.Health(); h.Down == nil || h.Down.Reason != reasonChanged {
		t.Errorf("Health %+v", h)
	}
	clock = clock.Add(Retry)
	if got, want := reason(t, ctx, s), "column fields is missing from nvtest.changing"; got != want {
		t.Errorf("after Retry: %q, want %q", got, want)
	}
}

// TestVPICPostgres: under a catalog whose vehicles are vpic, a relation's
// vehicle columns are vehicle_make, vehicle_model and vehicle_ext, one of
// them set; rows match a vehicle as a whole, as a file's do, so a row
// setting two of them matches neither, and none is served twice.
func TestVPICPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE; CREATE SCHEMA nvtest;
CREATE TABLE nvtest.shelf (id text, location text, type text, number text, side text, part_type text,
  vehicle_make integer, vehicle_model integer, vehicle_ext text, year integer, fields jsonb);
INSERT INTO nvtest.shelf VALUES
  ('V-1', 'north', NULL, NULL, NULL, 'mirror', NULL, 1938, NULL, 2010, '{}'),
  ('V-2', 'north', NULL, NULL, NULL, 'mirror', 476, NULL, NULL, 2010, '{}'),
  ('V-3', 'north', NULL, NULL, NULL, 'mirror', NULL, NULL, 'ext:classics/corvair', 2010, '{}'),
  ('V-4', 'north', NULL, NULL, NULL, 'mirror', 476, 1938, NULL, 2010, '{}');
CREATE INDEX shelf_by_vehicle ON nvtest.shelf (part_type, vehicle_make, vehicle_model, vehicle_ext, year)`)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	dir := t.TempDir()
	catalog := `{"format":1,"kind":"catalog","name":"vpic-catalog","version":"1","spine":"part-types","vehicles":"vpic"}
{"fitment":{"type":"mirror","number":"MR-9","side":"left"},"applications":[{"vehicle":{"model":1938},"first":2009,"last":2012,"options":""}]}
`
	header := `{"format":1,"kind":"stock","name":"shelf-vpic","version":"1","source":"shelf","catalog":"vpic-catalog","connector":{"postgres":{"relation":"nvtest.shelf","dsn_env":"DSN"}}}` + "\n"
	for name, data := range map[string]string{"vpic-catalog.jsonl": catalog, "shelf-vpic.jsonl": header} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := vocab.Open("../../../testdata/vocab/fitment/part-types.jsonl", dir)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*vocab.File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	s := New(stock.Declared{File: byName["shelf-vpic"], Catalog: byName["vpic-catalog"]}, u.String())
	t.Cleanup(s.Close)
	lists := []stock.ShelfVehicle{
		{PartTypes: []string{"mirror"}, Vehicles: []vocab.Vehicle{{Target: vocab.Target{Model: 1938}}}, Year: new(2010)},
		{PartTypes: []string{"mirror"}, Vehicles: []vocab.Vehicle{{Target: vocab.Target{Make: 476}}, {Target: vocab.Target{Ext: "ext:classics/corvair"}}}},
		{PartTypes: []string{"mirror"}, Vehicles: []vocab.Vehicle{{Target: vocab.Target{Make: 476}}, {Target: vocab.Target{Model: 1938}}}},
	}
	rs, _, err := s.ShelfByVehicle(context.Background(), lists, stock.Page{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rs {
		var ids []string
		for _, row := range r.Rows {
			ids = append(ids, fmt.Sprintf("%s:%d/%d/%s", row.ID, row.Vehicle.Make, row.Vehicle.Model, row.Vehicle.Ext))
		}
		got = append(got, strings.Join(ids, " "))
	}
	if want := []string{"V-1:0/1938/", "V-2:476/0/ V-3:0/0/ext:classics/corvair", "V-1:0/1938/ V-2:476/0/"}; !slices.Equal(got, want) || rs[2].Total != 2 {
		t.Errorf("rows %q, want %q", got, want)
	}

	// The documented index serves every vehicle column of the match: the
	// plan, with sequential scans priced out, reads it, never the table.
	ctx := context.Background()
	exec(t, conn, `SET enable_seqscan = off`)
	t.Cleanup(func() { exec(t, conn, `RESET enable_seqscan`) })
	rows, err := conn.Query(ctx, "EXPLAIN "+s.q.byVehicle, int32(1), []string{}, int64(0), int64(10),
		[]int32{0}, []string{"mirror"}, []*int32{new(int32(2010))}, []*int32{nil}, []*int32{new(int32(1938))}, []*string{nil})
	if err != nil {
		t.Fatal(err)
	}
	var plan []string
	var line string
	if _, err := pgx.ForEachRow(rows, []any{&line}, func() error { plan = append(plan, line); return nil }); err != nil {
		t.Fatal(err)
	}
	text := strings.Join(plan, "\n")
	if !strings.Contains(text, "shelf_by_vehicle") || strings.Contains(text, "Seq Scan on shelf") {
		t.Errorf("the vpic match does not read shelf_by_vehicle:\n%s", text)
	}
	if !strings.Contains(text, "vehicle_model = ") || !strings.Contains(text, "vehicle_make IS NULL") {
		t.Errorf("the index conditions are not the key's column and IS NULLs:\n%s", text)
	}
}

// TestLostPostgres: a database lost after its check passed (connections
// gone, new ones hanging past the connect timeout) makes the source
// unavailable and resets it: the next lookups answer at once, dialling
// nothing, for Retry, and Info says unavailable. Close then releases the
// pool: a lookup after it is unavailable, and the pool lends nothing.
func TestLostPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, nvtest)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	ctx := context.Background()
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	header := `{"format":1,"kind":"stock","name":"shelf-live","version":"1","source":"shelf","catalog":"parts-catalog","connector":{"postgres":{"relation":"nvtest.shelf","dsn_env":"DSN","connect_timeout_ms":300}}}`
	if err := os.WriteFile(filepath.Join(dir, "shelf-live.jsonl"), []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open("../../../testdata/vocab/fitment", dir)
	if err != nil {
		t.Fatal(err)
	}
	var src *Source
	var hang atomic.Bool
	var dials atomic.Int32
	connect := func(decl stock.Declared) stock.Live {
		src = New(decl, u.String())
		dial := src.cfg.ConnConfig.DialFunc
		src.cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			if hang.Load() {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			return dial(ctx, network, addr)
		}
		return src
	}
	tr, err := nitrovin.NewTranslatorWith(d, nitrovin.TranslatorConfig{Connect: connect}, files...)
	if err != nil {
		t.Fatal(err)
	}
	status := func() string {
		for _, v := range tr.Info().Vocabularies {
			if v.Name == "shelf-live" {
				return v.Status + " " + v.Reason
			}
		}
		return "absent"
	}
	if _, _, err := src.ShelfByFitment(ctx, mirrorKey, stock.Page{Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if got := status(); got != "available " {
		t.Fatalf("after a lookup: %q", got)
	}

	hang.Store(true)
	src.pool.Reset() // the database's connections are gone
	start := time.Now()
	if got := reason(t, ctx, src); got != reasonUnreachable {
		t.Errorf("the first lookup after: %q, want %q", got, reasonUnreachable)
	}
	if took := time.Since(start); took < 250*time.Millisecond || took > 2*time.Second {
		t.Errorf("it took %v; want about the 300 ms connect timeout", took)
	}
	if got, want := status(), "unavailable "+reasonUnreachable; got != want {
		t.Errorf("Info after it: %q, want %q", got, want)
	}
	n := dials.Load()
	start = time.Now()
	if got := reason(t, ctx, src); got != reasonUnreachable || dials.Load() != n || time.Since(start) > 100*time.Millisecond {
		t.Errorf("the next lookup: %q after %v and %d more dials; want at once, none", got, time.Since(start), dials.Load()-n)
	}

	hang.Store(false)
	tr.Close()
	tr.Close()
	if got := reason(t, ctx, src); got != reasonClosed {
		t.Errorf("a lookup after Close: %q, want %q", got, reasonClosed)
	}
	if c, err := src.pool.Acquire(ctx); err == nil {
		c.Release()
		t.Error("the pool lends a connection after Close")
	}
}

// TestNoCatalogPostgres: a yard read with no catalog loaded has no
// vehicle columns, and still answers by its decodes.
func TestNoCatalogPostgres(t *testing.T) {
	conn, u := testDB(t)
	exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE; CREATE SCHEMA nvtest;
CREATE TABLE nvtest.yard (id text, location text, decoded_make integer, decoded_model integer, decoded_year integer, year integer, fields jsonb);
INSERT INTO nvtest.yard VALUES ('Y-1', 'north', 476, 1938, 2010, NULL, '{}'), ('Y-2', 'north', 476, 1938, 2016, NULL, '{}')`)
	t.Cleanup(func() { exec(t, conn, `DROP SCHEMA IF EXISTS nvtest CASCADE`) })
	dir := t.TempDir()
	header := `{"format":1,"kind":"stock","name":"yard-live","version":"1","source":"yard","connector":{"postgres":{"relation":"nvtest.yard","dsn_env":"DSN"}}}`
	if err := os.WriteFile(filepath.Join(dir, "yard-live.jsonl"), []byte(header+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := New(stock.Declared{File: files[0]}, u.String())
	t.Cleanup(s.Close)
	q := stock.YardQuery{Models: []stock.ModelYears{{Model: 1938, First: new(2009), Last: new(2012)}}}
	rs, _, err := s.Yard(context.Background(), []stock.YardQuery{q}, stock.Page{Limit: 10})
	if err != nil || len(rs[0].Rows) != 1 || rs[0].Rows[0].ID != "Y-1" || rs[0].Rows[0].Matched != stock.MatchedVIN {
		t.Errorf("Yard with no catalog: %+v, %v; want Y-1 by its decode", rs, err)
	}
}
