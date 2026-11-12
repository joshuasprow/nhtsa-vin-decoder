package vocab

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The files a catalog's cases load beside it.
const (
	catalogHeader = `{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts","vehicles":"yard"}`
	vpicCatalog   = `{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts","vehicles":"vpic"}`
	ram           = `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938}]}`
	padsFitment   = `{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram"},"options":""}]}`
)

var (
	partSpine = file{"a.jsonl", lines(spineHeader, `{"id":"pads"}`, `{"id":"rotor"}`)}
	yardFile  = file{"b.jsonl", lines(vehicleHeader, ram)}
)

// catalogFile is the catalog of a case, c.jsonl: catalogHeader, then l.
func catalogFile(l ...string) file { return file{"c.jsonl", append([]string{catalogHeader}, l...)} }

// openFiles writes files to a temporary directory and opens them in order.
func openFiles(t *testing.T, files []file) (string, []*File, error) {
	t.Helper()
	dir := t.TempDir()
	var paths []string
	for _, f := range files {
		p := filepath.Join(dir, f.name)
		if err := os.WriteFile(p, []byte(strings.Join(f.lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	got, err := Open(paths...)
	return dir, got, err
}

// TestCatalogFaults refuses one catalog per fault class, each at the line
// it is on: every fault ADR 0017 lists, and the schema's rules a catalog
// line can break.
func TestCatalogFaults(t *testing.T) {
	app := func(vehicle, rest string) string {
		return `{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":` + vehicle + `,"options":""` + rest + `}]}`
	}
	redirect := func(from, to string) string {
		return `{"redirect":{"vehicle":{"make":"Dodge","model":"Ram"},"from":"` + from + `","to":"` + to + `"}}`
	}
	retired := func(key, to string) string {
		return `{"retired":{"type":"pads","number":"` + key + `"},"to":{"type":"pads","number":"` + to + `"}}`
	}
	for _, tc := range []struct {
		name   string
		files  []file
		file   string
		line   int
		detail string
	}{
		// The header.
		{"missing spine", []file{partSpine, yardFile, {"c.jsonl", lines(strings.Replace(catalogHeader, `,"spine":"parts"`, ``, 1))}}, "c.jsonl", 1, `missing field "spine"`},
		{"missing vehicles", []file{partSpine, yardFile, {"c.jsonl", lines(strings.Replace(catalogHeader, `,"vehicles":"yard"`, ``, 1))}}, "c.jsonl", 1, `missing field "vehicles"`},
		{"bad vehicles name", []file{partSpine, yardFile, {"c.jsonl", lines(strings.Replace(catalogHeader, `"yard"`, `"Yard"`, 1))}}, "c.jsonl", 1, `/vehicles: "Yard" is not a name`},
		{"empty release", []file{partSpine, yardFile, {"c.jsonl", lines(strings.Replace(catalogHeader, `}`, `,"release":""}`, 1))}}, "c.jsonl", 1, "/release: must not be empty"},
		{"vocabulary field in a catalog header", []file{partSpine, yardFile, {"c.jsonl", lines(strings.Replace(catalogHeader, `}`, `,"subject":"part"}`, 1))}}, "c.jsonl", 1, `unknown field "subject"`},

		// Between files (Link).
		{"second catalog", []file{partSpine, yardFile, catalogFile(padsFitment), {"d.jsonl", lines(strings.Replace(catalogHeader, `"cat"`, `"cat2"`, 1))}}, "d.jsonl", 1, "is a catalog too: a deployment loads one catalog"},
		{"spine not loaded", []file{yardFile, catalogFile(padsFitment)}, "c.jsonl", 1, `/spine: spine "parts" is not loaded`},
		{"spine of another kind", []file{{"a.jsonl", lines(strings.Replace(setHeader, `"classics"`, `"parts"`, 1))}, yardFile, catalogFile()}, "c.jsonl", 1, `/spine: "parts" is an extension set, not a spine`},
		{"vehicle vocabulary not loaded", []file{partSpine, catalogFile(padsFitment)}, "c.jsonl", 1, `/vehicles: vocabulary "yard" is not loaded`},
		{"vehicles a part vocabulary", []file{partSpine, {"b.jsonl", lines(strings.Replace(partHeader, `"listing"`, `"yard"`, 1))}, catalogFile()}, "c.jsonl", 1, `/vehicles: "yard" is a part vocabulary, not a vehicle vocabulary`},
		{"vehicles a spine", []file{partSpine, yardFile, {"c.jsonl", lines(strings.Replace(catalogHeader, `"vehicles":"yard"`, `"vehicles":"parts"`, 1))}}, "c.jsonl", 1, `/vehicles: "parts" is a spine, not a vehicle vocabulary`},
		{"part type the spine lacks", []file{partSpine, yardFile, catalogFile(strings.Replace(padsFitment, `"pads"`, `"wipers"`, 1))}, "c.jsonl", 2, `/fitment/type: spine "parts" has no id "wipers"`},
		{"redirect from a type the spine lacks", []file{partSpine, yardFile, catalogFile(padsFitment, redirect("wipers", "pads"))}, "c.jsonl", 3, `/redirect/from: spine "parts" has no id "wipers"`},
		{"redirect to a type the spine lacks", []file{partSpine, yardFile, catalogFile(padsFitment, redirect("rotor", "wipers"))}, "c.jsonl", 3, `/redirect/to: spine "parts" has no id "wipers"`},
		{"vehicle missing a field", []file{partSpine, yardFile, catalogFile(app(`{"make":"Dodge"}`, ``))}, "c.jsonl", 2, `/applications/0/vehicle: fields "make" are not vocabulary "yard"'s: "make", "model"`},
		{"vehicle with another field", []file{partSpine, yardFile, catalogFile(app(`{"make":"Dodge","model":"Ram","trim":"SLT"}`, ``))}, "c.jsonl", 2, `/applications/0/vehicle: fields "make", "model", "trim" are not`},
		{"redirect vehicle's fields", []file{partSpine, yardFile, catalogFile(padsFitment, `{"redirect":{"vehicle":{"model":"Ram"},"from":"rotor","to":"pads"}}`)}, "c.jsonl", 3, `/redirect/vehicle: fields "model" are not`},
		{"vpic extension not loaded", []file{partSpine, {"c.jsonl", lines(vpicCatalog, app(`{"ext":"ext:classics/corvair"}`, ``))}}, "c.jsonl", 2, `/applications/0/vehicle/ext: extension set "classics" is not loaded`},

		// Lines.
		{"line of no kind", []file{partSpine, yardFile, catalogFile(`{"note":"x"}`)}, "c.jsonl", 2, "holds none of fitment, redirect or retired"},
		{"line of an unknown field", []file{partSpine, yardFile, catalogFile(`{"part":{"type":"pads"}}`)}, "c.jsonl", 2, `unknown field "part"`},
		{"line of two kinds", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1"},"applications":[],"redirect":{}}`)}, "c.jsonl", 2, "holds more than one of fitment, redirect or retired: fitment, redirect"},
		{"another kind's field", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1"},"applications":[],"to":{"type":"pads","number":"P2"}}`)}, "c.jsonl", 2, `unknown field "to"`},
		{"missing applications", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1"}}`)}, "c.jsonl", 2, `missing field "applications"`},
		{"key without number", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads"},"applications":[]}`)}, "c.jsonl", 2, `/fitment: missing field "number"`},
		{"unknown key field", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1","position":"front"},"applications":[]}`)}, "c.jsonl", 2, `/fitment: unknown field "position"`},
		{"bad side", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1","side":"front"},"applications":[]}`)}, "c.jsonl", 2, "/fitment/side: must be left, right or both"},
		{"empty side", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1","side":""},"applications":[]}`)}, "c.jsonl", 2, "/fitment/side: must be left, right or both"},
		{"missing options", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram"}}]}`)}, "c.jsonl", 2, `/applications/0: missing field "options"`},
		{"missing vehicle", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1"},"applications":[{"options":""}]}`)}, "c.jsonl", 2, `/applications/0: missing field "vehicle"`},
		{"vehicle field not a string", []file{partSpine, yardFile, catalogFile(app(`{"make":"Dodge","model":1500}`, ``))}, "c.jsonl", 2, "/applications/0/vehicle/model: must be a string"},
		{"application first after last", []file{partSpine, yardFile, catalogFile(app(`{"make":"Dodge","model":"Ram"}`, `,"first":2014,"last":2012`))}, "c.jsonl", 2, "/applications/0: first year 2014 is after last year 2012"},
		{"empty qualifiers", []file{partSpine, yardFile, catalogFile(app(`{"make":"Dodge","model":"Ram"}`, `,"qualifiers":{}`))}, "c.jsonl", 2, "/applications/0/qualifiers: must name at least one element"},
		{"unknown application field", []file{partSpine, yardFile, catalogFile(app(`{"make":"Dodge","model":"Ram"}`, `,"note":"x"`))}, "c.jsonl", 2, `/applications/0: unknown field "note"`},
		{"vpic vehicle of two targets", []file{partSpine, {"c.jsonl", lines(vpicCatalog, app(`{"make":476,"model":1938}`, ``))}}, "c.jsonl", 2, "/applications/0/vehicle: names more than one vehicle: make, model"},
		{"vpic vehicle's other field", []file{partSpine, {"c.jsonl", lines(vpicCatalog, app(`{"model":1938,"trim":"SLT"}`, ``))}}, "c.jsonl", 2, `/applications/0/vehicle: unknown field "trim"`},
		{"redirect missing to", []file{partSpine, yardFile, catalogFile(`{"redirect":{"vehicle":{"make":"Dodge","model":"Ram"},"from":"rotor"}}`)}, "c.jsonl", 2, `/redirect: missing field "to"`},
		{"retired missing to", []file{partSpine, yardFile, catalogFile(padsFitment, `{"retired":{"type":"pads","number":"P0"}}`)}, "c.jsonl", 3, `missing field "to"`},

		// Within the file.
		{"duplicate fitment key", []file{partSpine, yardFile, catalogFile(padsFitment, strings.Replace(padsFitment, `"options":""`, `"options":"other"`, 1))}, "c.jsonl", 3, "/fitment: duplicate fitment key: line 2 has it too"},
		{"duplicate application", []file{partSpine, yardFile, catalogFile(`{"fitment":{"type":"pads","number":"P1"},"applications":[` +
			`{"vehicle":{"make":"Dodge","model":"Ram"},"first":2009,"options":"4WD","qualifiers":{"15":[2,3]}},` +
			`{"vehicle":{"make":"Dodge","model":"Ram"},"first":2010,"options":"4WD","qualifiers":{"15":[2,3]}},` +
			`{"vehicle":{"model":"ram","make":" DODGE"},"first":2009,"options":"4WD","qualifiers":{"15":[3,2]}}]}`)}, "c.jsonl", 2,
			"/applications/2: duplicate application: /applications/0 has the same vehicle, years, options and qualifiers"},
		{"retired number that is live", []file{partSpine, yardFile, catalogFile(padsFitment, retired("P1", "P1"))}, "c.jsonl", 3, `/retired: {"type":"pads","number":"P1"} is a live fitment, on line 2`},
		{"retired number pointing nowhere", []file{partSpine, yardFile, catalogFile(padsFitment, retired("P0", "P9"))}, "c.jsonl", 3, `/to: no fitment {"type":"pads","number":"P9"} in the file`},
		{"retired number twice", []file{partSpine, yardFile, catalogFile(padsFitment, retired("P0", "P1"), retired("P0", "P1"))}, "c.jsonl", 4, "/retired: duplicate retired number: line 3 retires it too"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, files, err := openFiles(t, tc.files)
			faults, ok := errors.AsType[Faults](err)
			if !ok {
				t.Fatalf("Open: %d files, error %v; want Faults", len(files), err)
			}
			if len(faults) != 1 {
				t.Fatalf("faults %v; want one", faults)
			}
			f := faults[0]
			if f.File != filepath.Join(dir, tc.file) || f.Line != tc.line || f.Severity != SeverityFault || !strings.Contains(f.Detail, tc.detail) {
				t.Errorf("fault %+v; want %s:%d, a fault naming %q", f, tc.file, tc.line, tc.detail)
			}
		})
	}
}

// TestCatalogNotFaults: what looks like a fault and is not. One number on
// two sides is two fitments; one vehicle in two year ranges, or with other
// option text, is two applications; a vehicle's fields match after
// Normalize.
func TestCatalogNotFaults(t *testing.T) {
	_, files, err := openFiles(t, []file{partSpine, yardFile, catalogFile(
		`{"fitment":{"type":"pads","number":"P1","side":"left"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram"},"last":2009,"options":""},{"vehicle":{"make":"Dodge","model":"Ram"},"first":2010,"options":""},{"vehicle":{"make":"Dodge","model":"Ram"},"first":2010,"options":"4WD"}]}`,
		`{"fitment":{"type":"pads","number":"P1","side":"right"},"applications":[{"vehicle":{"make":" dodge ","model":"RAM"},"options":""}]}`,
		`{"fitment":{"type":"pads","number":"P1"},"applications":[]}`,
	)})
	if err != nil {
		t.Fatal(err)
	}
	c := files[2].Catalog()
	if c == nil || len(c.Vehicles()) != 1 {
		t.Fatalf("catalog %v: want one vehicle across the spellings", c)
	}
	if got := c.Info(); got.Fitments != 3 || got.Applications != 4 {
		t.Errorf("Info %+v; want 3 fitments, 4 applications", got)
	}
}

// fixtureCatalog is testdata/vocab/fitment's catalog.
func fixtureCatalog(t *testing.T) *Catalog {
	t.Helper()
	files, err := Open("../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewSet(fixtureMonth{}, files)
	if err != nil {
		t.Fatal(err)
	}
	c := set.Catalog()
	if c == nil {
		t.Fatal("no catalog in testdata/vocab/fitment")
	}
	return c
}

// numbers is each candidate's number and side, and how many of its
// applications matched.
func numbers(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		s := c.Fitment.Key.Number
		if c.Fitment.Key.Side != SideNone {
			s += "/" + c.Fitment.Key.Side.String()
		}
		out = append(out, s+":"+strings.Repeat("a", len(c.Applications)))
	}
	return out
}

// TestCatalogReader reads the fixture catalog as fitment.Reader does.
func TestCatalogReader(t *testing.T) {
	c := fixtureCatalog(t)
	ram := Vehicle{Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}
	silverado := Vehicle{Entry: map[string]string{"make": "chevrolet", "model": "SILVERADO  1500"}} // matched after Normalize
	left, both := SideLeft, SideBoth
	for _, tc := range []struct {
		name     string
		partType string
		vehicles []Vehicle
		year     *int
		side     *Side
		want     []string
	}{
		{"both sides, one number", "mirror", []Vehicle{ram}, new(2010), nil, []string{"MR-1001/left:aa", "MR-1001/right:a"}},
		{"a side", "mirror", []Vehicle{ram}, new(2010), &left, []string{"MR-1001/left:aa"}},
		{"a side none has", "mirror", []Vehicle{ram}, new(2010), &both, nil},
		{"a year outside every application", "mirror", []Vehicle{ram}, new(2014), nil, nil},
		{"any year", "brake-rotor-front", []Vehicle{ram}, nil, nil, []string{"BR-2001:a", "BR-2002:a"}},
		{"an open range", "brake-rotor-front", []Vehicle{ram}, new(2030), nil, []string{"BR-2002:a"}},
		{"several vehicles", "brake-rotor-front", []Vehicle{silverado, ram, ram}, new(2010), nil, []string{"BR-2001:aa"}},
		{"a vehicle the catalog lacks", "mirror", []Vehicle{{Entry: map[string]string{"make": "Dodge", "model": "Dakota"}}}, nil, nil, nil},
		{"a vpic vehicle in a vocabulary's catalog", "mirror", []Vehicle{{Target: Target{Model: 1938}}}, nil, nil, nil},
		{"a part type with no fitment", "brake-rotor-rear", []Vehicle{silverado}, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := numbers(c.ByVehicle(tc.partType, tc.vehicles, tc.year, tc.side)); !slices.Equal(got, tc.want) {
				t.Errorf("ByVehicle %v; want %v", got, tc.want)
			}
		})
	}
	cs := c.ByVehicle("mirror", []Vehicle{ram}, new(2010), &left)
	if a := cs[0].Applications[1]; a.Options != "4WD, w/ towing package" || len(a.Qualifiers) != 1 || a.Qualifiers[0].Element != 15 || a.Vehicle.Entry["model"] != "Ram 1500" {
		t.Errorf("MR-1001's second application: %+v", a)
	}

	if fit, rt := c.ByNumber("mirror", "MR-1001", SideRight); fit == nil || rt != nil || fit.Line != 3 {
		t.Errorf("ByNumber MR-1001 right: %+v, %+v; want line 3, directly", fit, rt)
	}
	if fit, _ := c.ByNumber("mirror", "MR-1001", SideNone); fit != nil {
		t.Errorf("ByNumber MR-1001 with no side: %+v; want none, side being part of the key", fit)
	}
	if fit, rt := c.ByNumber("brake-rotor-front", "BR-1999", SideNone); fit == nil || fit.Key.Number != "BR-2001" || rt == nil || rt.Note != "7.01 → 7.02" {
		t.Errorf("ByNumber BR-1999: %+v, %+v; want BR-2001 through the retired number", fit, rt)
	}
	if fit, rt := c.ByNumber("engine", "BR-1999", SideNone); fit != nil || rt != nil {
		t.Errorf("ByNumber of another part type: %+v, %+v", fit, rt)
	}

	if rds := c.Redirect("brake-rotor-rear", silverado, new(2008)); len(rds) != 1 || rds[0].To != "brake-rotor-front" || rds[0].Options == "" {
		t.Errorf("Redirect rear rotor, Silverado 2008: %+v", rds)
	}
	for _, y := range []*int{new(2014), nil} {
		if rds := c.Redirect("brake-rotor-rear", silverado, y); (y == nil) != (len(rds) == 1) {
			t.Errorf("Redirect rear rotor, Silverado %v: %+v", y, rds)
		}
	}
	if rds := c.Redirect("brake-rotor-rear", ram, nil); rds != nil {
		t.Errorf("Redirect for a vehicle with none: %+v", rds)
	}

	want := CatalogInfo{Name: "parts-catalog", Version: "2026.11", Release: "7.02", DataMonth: "2026_10", Spine: "part-types", Vehicles: "catalog-models",
		Hash: c.File().Hash, Fitments: 5, Applications: 7, Redirects: 1, Retired: 1}
	if got := c.Info(); got != want {
		t.Errorf("Info %+v; want %+v", got, want)
	}
}

// TestCatalogInSet: a Set keeps the catalog beside its files, and neither
// translates nor lists it.
func TestCatalogInSet(t *testing.T) {
	files, err := Open("../../testdata/vocab", "../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	set, err := NewSet(fixtureMonth{}, files)
	if err != nil {
		t.Fatal(err)
	}
	if set.Catalog() == nil || slices.ContainsFunc(set.Files(), func(f *File) bool { return f.Kind == KindCatalog }) {
		t.Errorf("Catalog %v; Files hold a catalog", set.Catalog())
	}
	l, err := set.List(ListQuery{})
	if err != nil || slices.ContainsFunc(l.Catalog, func(c CatalogEntry) bool { return c.Kind == KindCatalog }) {
		t.Errorf("the list catalog: %v, %+v", err, l)
	}
	for _, tc := range []struct {
		do     func() error
		detail string
	}{
		{func() error { _, err := set.List(ListQuery{Of: "parts-catalog"}); return err }, `/of: "parts-catalog" is a catalog`},
		{func() error {
			_, err := set.Translate(Query{From: From{Ref: Ref{Target: Target{Model: 1938}}}, To: []string{"parts-catalog"}})
			return err
		}, `"parts-catalog" is a catalog, not a vocabulary`},
		{func() error {
			_, err := set.Translate(Query{From: From{Ref: Ref{Spine: "parts-catalog", Target: Target{ID: "mirror"}}}})
			return err
		}, `"parts-catalog" is a catalog, not a spine`},
	} {
		if err := tc.do(); err == nil || !strings.Contains(err.Error(), tc.detail) {
			t.Errorf("error %v; want %q", err, tc.detail)
		}
	}
	if err := WriteIndex(&strings.Builder{}, set.Catalog().File()); err == nil {
		t.Error("WriteIndex wrote a catalog's index")
	}
}

// TestCatalogDrift: one finding per vehicle the vocabulary lacks or leaves
// unmapped, per vPIC id the month lacks and per qualifier value it never
// decodes, at the first line naming each; and one per redirect no
// application answers. Only the month's own drift says what month the
// file was built against.
func TestCatalogDrift(t *testing.T) {
	yard := file{"b.jsonl", lines(vehicleHeader, ram, `{"entry":{"make":"Dodge","model":"Lark"},"mappings":[]}`)}
	cat := file{"c.jsonl", lines(
		strings.Replace(catalogHeader, `}`, `,"data_month":"2026_09"}`, 1),
		`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"make":"Dodge","model":"Ram"},"first":2009,"options":"","qualifiers":{"15":[2,99]}},{"vehicle":{"make":"Dodge","model":"Nova"},"options":"a"},{"vehicle":{"make":"Dodge","model":"Lark"},"options":"b"}]}`,
		`{"fitment":{"type":"pads","number":"P2"},"applications":[{"vehicle":{"make":"dodge","model":"nova"},"options":""},{"vehicle":{"make":"Dodge","model":"Ram"},"options":"","qualifiers":{"15":[99,2],"77":["x"]}}]}`,
		`{"redirect":{"vehicle":{"make":"Dodge","model":"Ram"},"first":2010,"last":2011,"from":"rotor","to":"pads"}}`,
		`{"redirect":{"vehicle":{"make":"Dodge","model":"Ram"},"first":2010,"from":"pads","to":"rotor"}}`,
	)}
	_, files, err := openFiles(t, []file{partSpine, yard, cat})
	if err != nil {
		t.Fatal(err)
	}
	vpic := file{"c.jsonl", lines(vpicCatalog,
		`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"make":99999},"options":""},{"vehicle":{"model":88888},"options":""},{"vehicle":{"model":1938},"options":""},{"vehicle":{"make":476},"options":""}]}`,
		`{"fitment":{"type":"pads","number":"P2"},"applications":[{"vehicle":{"model":88888},"options":""}]}`,
	)}
	_, vpicFiles, err := openFiles(t, []file{partSpine, vpic})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		files []*File
		want  []string
	}{
		{"a vocabulary's catalog", files, []string{
			`2 vehicle /applications/1/vehicle: vocabulary "yard" has no entry {"make":"Dodge","model":"Nova"} (and 1 more)`,
			`2 vehicle /applications/2/vehicle: vocabulary "yard" lists {"make":"Dodge","model":"Lark"} without a mapping, so no translation reaches it`,
			`2 qualifier /applications/0/qualifiers/15/1: data month 2026_10 never decodes element 15 to attribute id 99 (and 1 more) (the file was built against 2026_09)`,
			`3 qualifier /applications/1/qualifiers/77: element 77 is not a public element of data month 2026_10 (the file was built against 2026_09)`,
			`5 redirect /redirect/to: part type "rotor" has no application for this vehicle in 2010 on`,
		}},
		{"a vpic catalog", vpicFiles, []string{
			`2 make /applications/0/vehicle/make: vPIC make 99999 is not in data month 2026_10`,
			`2 model /applications/1/vehicle/model: vPIC model 88888 is not in data month 2026_10 (and 1 more)`,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, d := range Drift(month{}, tc.files) {
				if d.Severity != SeverityDrift || !strings.HasSuffix(d.File, "c.jsonl") {
					t.Errorf("finding %+v", d)
				}
				got = append(got, strings.Join([]string{strconv.Itoa(d.Line), string(d.Drift), d.Detail}, " "))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("drift\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
		})
	}
}
