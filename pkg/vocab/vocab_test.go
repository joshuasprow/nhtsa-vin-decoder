package vocab

import (
	"errors"
	"iter"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/zeebo/blake3"
)

// Headers of the files the fault cases are built from.
const (
	vehicleHeader = `{"format":1,"kind":"vocabulary","name":"yard","version":"1","subject":"vehicle","fields":["make","model"]}`
	partHeader    = `{"format":1,"kind":"vocabulary","name":"listing","version":"1","subject":"part","fields":["category"],"spine":"parts"}`
	setHeader     = `{"format":1,"kind":"extension-set","name":"classics","version":"1"}`
	spineHeader   = `{"format":1,"kind":"spine","name":"parts","version":"1"}`
)

// file is one file of a fault case: its name in the case's directory and
// its lines.
type file struct {
	name  string
	lines []string
}

func lines(l ...string) []string { return l }

// TestFaults refuses one file per fault class, each at the line it is on:
// every fault ADR 0011 lists, and the schema's rules a line can break.
func TestFaults(t *testing.T) {
	entry := `{"entry":{"make":"Dodge","model":"Ram 1500"},"mappings":[]}`
	for _, tc := range []struct {
		name   string
		files  []file
		file   string // the faulty file
		line   int
		detail string // a substring of the fault's detail
	}{
		{"malformed line", []file{{"a.jsonl", lines(vehicleHeader, entry, `{"entry":`)}}, "a.jsonl", 3, "malformed JSON"},
		{"line not an object", []file{{"a.jsonl", lines(vehicleHeader, `[1]`)}}, "a.jsonl", 2, "must be an object"},
		{"empty file", []file{{"a.jsonl", lines("", " ")}}, "a.jsonl", 1, "no header"},
		{"unknown format", []file{{"a.jsonl", lines(strings.Replace(spineHeader, `"format":1`, `"format":2`, 1))}}, "a.jsonl", 1, "unknown format 2"},
		{"missing format", []file{{"a.jsonl", lines(`{"kind":"spine","name":"parts","version":"1"}`)}}, "a.jsonl", 1, `missing field "format"`},
		{"unknown kind", []file{{"a.jsonl", lines(strings.Replace(spineHeader, `"spine"`, `"taxonomy"`, 1))}}, "a.jsonl", 1, `unknown kind "taxonomy"`},
		{"unknown header field", []file{{"a.jsonl", lines(strings.Replace(spineHeader, `}`, `,"owner":"x"}`, 1))}}, "a.jsonl", 1, `unknown field "owner"`},
		{"field of another kind", []file{{"a.jsonl", lines(strings.Replace(spineHeader, `}`, `,"data_month":"2026_10"}`, 1))}}, "a.jsonl", 1, `unknown field "data_month"`},
		{"missing version", []file{{"a.jsonl", lines(`{"format":1,"kind":"spine","name":"parts"}`)}}, "a.jsonl", 1, `missing field "version"`},
		{"bad name", []file{{"a.jsonl", lines(strings.Replace(spineHeader, `"parts"`, `"Parts"`, 1))}}, "a.jsonl", 1, "is not a name"},
		{"reserved name", []file{{"a.jsonl", lines(strings.Replace(spineHeader, `"parts"`, `"vpic"`, 1))}}, "a.jsonl", 1, "reserved"},
		{"bad data month", []file{{"a.jsonl", lines(strings.Replace(setHeader, `}`, `,"data_month":"2026-10"}`, 1))}}, "a.jsonl", 1, "not a data month"},
		{"unknown subject", []file{{"a.jsonl", lines(strings.Replace(vehicleHeader, `"vehicle"`, `"boat"`, 1))}}, "a.jsonl", 1, `unknown subject "boat"`},
		{"no fields", []file{{"a.jsonl", lines(strings.Replace(vehicleHeader, `["make","model"]`, `[]`, 1))}}, "a.jsonl", 1, "at least one field"},
		{"duplicate field", []file{{"a.jsonl", lines(strings.Replace(vehicleHeader, `["make","model"]`, `["make","make"]`, 1))}}, "a.jsonl", 1, `duplicate field "make"`},
		{"part vocabulary without spine", []file{{"a.jsonl", lines(strings.Replace(partHeader, `,"spine":"parts"`, ``, 1))}}, "a.jsonl", 1, `missing field "spine"`},
		{"vehicle vocabulary with spine", []file{{"a.jsonl", lines(strings.Replace(vehicleHeader, `}`, `,"spine":"parts"}`, 1))}}, "a.jsonl", 1, "names no spine"},
		{"unknown entry line field", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[],"aliases":[]}`)}}, "a.jsonl", 2, `unknown field "aliases"`},
		{"missing mappings", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"}}`)}}, "a.jsonl", 2, `missing field "mappings"`},
		{"undeclared entry field", []file{{"a.jsonl", lines(vehicleHeader, entry, `{"entry":{"make":"Dodge","model":"Ram","trim":"SLT"},"mappings":[]}`)}}, "a.jsonl", 3, `/entry: unknown field "trim"`},
		{"missing entry field", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge"},"mappings":[]}`)}}, "a.jsonl", 2, `/entry: missing field "model"`},
		{"entry field not a string", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":1500},"mappings":[]}`)}}, "a.jsonl", 2, "/entry/model: must be a string"},
		{"duplicate entry", []file{{"a.jsonl", lines(vehicleHeader, entry, `{"entry":{"make":" DODGE ","model":"ram  1500"},"mappings":[]}`)}}, "a.jsonl", 3, "duplicate entry: line 2"},
		{"unknown mapping field", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"tonnage":1500}]}`)}}, "a.jsonl", 2, `/mappings/0: unknown field "tonnage"`},
		{"mapping to nothing", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"first":2000}]}`)}}, "a.jsonl", 2, "names no spine entry"},
		{"mapping to two", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"make":476,"model":1938}]}`)}}, "a.jsonl", 2, "more than one spine entry: make, model"},
		{"vehicle mapping to a spine id", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"id":"x"}]}`)}}, "a.jsonl", 2, "not id"},
		{"part mapping to a model", []file{{"a.jsonl", lines(partHeader, `{"entry":{"category":"Pads"},"mappings":[{"model":1938}]}`)}}, "a.jsonl", 2, "not model"},
		{"id not an integer", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":"1938"}]}`)}}, "a.jsonl", 2, "/mappings/0/model: must be an integer"},
		{"bad extension reference", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"ext":"classics/corvair"}]}`)}}, "a.jsonl", 2, "not an extension reference"},
		{"first after last", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"first":2014,"last":2012}]}`)}}, "a.jsonl", 2, "first year 2014 is after last year 2012"},
		{"qualifiers on an extension mapping", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Chevrolet","model":"Corvair"},"mappings":[{"ext":"ext:classics/corvair","qualifiers":{"34":["Monza"]}}]}`)}}, "a.jsonl", 2, "an extension mapping takes no qualifiers"},
		{"empty qualifiers", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"qualifiers":{}}]}`)}}, "a.jsonl", 2, "at least one element"},
		{"qualifier not an element id", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"qualifiers":{"Series":["1500"]}}]}`)}}, "a.jsonl", 2, `"Series" is not an element id`},
		{"qualifier value of no type", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"qualifiers":{"34":[true]}}]}`)}}, "a.jsonl", 2, "/mappings/0/qualifiers/34/0: must be an attribute id"},
		{"duplicate qualifier value", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"qualifiers":{"15":[2,2]}}]}`)}}, "a.jsonl", 2, "duplicate value"},
		{"unknown origin", []file{{"a.jsonl", lines(vehicleHeader, `{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"origin":"guessed"}]}`)}}, "a.jsonl", 2, `unknown origin "guessed"`},
		{"extension both make and model", []file{{"a.jsonl", lines(setHeader, `{"id":"x","make_name":"Packard","make":467,"name":"Clipper"}`)}}, "a.jsonl", 2, "not both"},
		{"extension neither", []file{{"a.jsonl", lines(setHeader, `{"id":"x","name":"Clipper"}`)}}, "a.jsonl", 2, "missing make_name"},
		{"extension id with a slash", []file{{"a.jsonl", lines(setHeader, `{"id":"a/b","make_name":"Packard"}`)}}, "a.jsonl", 2, "neither '/'"},
		{"duplicate extension id", []file{{"a.jsonl", lines(setHeader, `{"id":"packard","make_name":"Packard"}`, `{"id":"packard","make":467,"name":"Packard"}`)}}, "a.jsonl", 3, `duplicate id "packard": line 2`},
		{"unknown spine entry field", []file{{"a.jsonl", lines(spineHeader, `{"id":"pads","parent":"brakes"}`)}}, "a.jsonl", 2, `unknown field "parent"`},
		{"duplicate spine id", []file{{"a.jsonl", lines(spineHeader, `{"id":"pads"}`, `{"id":"rotor"}`, `{"id":"pads","name":"Pads"}`)}}, "a.jsonl", 4, `duplicate id "pads": line 2`},
		{"unreadable file", []file{{"a.jsonl", nil}}, "missing.jsonl", 0, "no such file"},

		// Faults between files (Link).
		{"two files with one name", []file{{"a.jsonl", lines(spineHeader)}, {"b.jsonl", lines(strings.Replace(setHeader, `"classics"`, `"parts"`, 1))}}, "b.jsonl", 1, `"parts" is also the name of`},
		{"extension set not loaded", []file{{"a.jsonl", lines(vehicleHeader, entry, `{"entry":{"make":"Chevrolet","model":"Corvair"},"mappings":[{"ext":"ext:classics/corvair"}]}`)}}, "a.jsonl", 3, `extension set "classics" is not loaded`},
		{"extension not defined", []file{{"a.jsonl", lines(setHeader, `{"id":"packard","make_name":"Packard"}`)}, {"b.jsonl", lines(vehicleHeader, `{"entry":{"make":"Chevrolet","model":"Corvair"},"mappings":[{"ext":"ext:classics/corvair"}]}`)}}, "b.jsonl", 2, `extension set "classics" has no id "corvair"`},
		{"extension reference to another kind", []file{{"a.jsonl", lines(spineHeader)}, {"b.jsonl", lines(vehicleHeader, `{"entry":{"make":"Chevrolet","model":"Corvair"},"mappings":[{"ext":"ext:parts/corvair"}]}`)}}, "b.jsonl", 2, `"parts" is a spine, not an extension set`},
		{"extension model under an extension model", []file{{"a.jsonl", lines(setHeader, `{"id":"packard","make_name":"Packard"}`, `{"id":"clipper","make":"ext:classics/packard","name":"Clipper"}`, `{"id":"custom","make":"ext:classics/clipper","name":"Custom"}`)}}, "a.jsonl", 4, "is an extension model, not a make"},
		{"spine not loaded", []file{{"a.jsonl", lines(partHeader, `{"entry":{"category":"Pads"},"mappings":[{"id":"pads"}]}`)}}, "a.jsonl", 1, `spine "parts" is not loaded`},
		{"spine of another kind", []file{{"a.jsonl", lines(strings.Replace(setHeader, `"classics"`, `"parts"`, 1))}, {"b.jsonl", lines(partHeader)}}, "b.jsonl", 1, `"parts" is an extension set, not a spine`},
		{"spine id not defined", []file{{"a.jsonl", lines(spineHeader, `{"id":"pads"}`)}, {"b.jsonl", lines(partHeader, `{"entry":{"category":"Pads"},"mappings":[{"id":"pads"}]}`, `{"entry":{"category":"Rotors"},"mappings":[{"id":"rotor"}]}`)}}, "b.jsonl", 3, `spine "parts" has no id "rotor"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var paths []string
			for _, f := range tc.files {
				p := filepath.Join(dir, f.name)
				if f.lines != nil {
					if err := os.WriteFile(p, []byte(strings.Join(f.lines, "\n")+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					p = filepath.Join(dir, tc.file)
				}
				paths = append(paths, p)
			}
			files, err := Open(paths...)
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

// TestFaultsAll: Open reports every fault of every file, in order.
func TestFaultsAll(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.jsonl")
	b := filepath.Join(dir, "b.jsonl")
	os.WriteFile(a, []byte(spineHeader+"\n{\"id\":\"\"}\n\n{}\n"), 0o644)
	os.WriteFile(b, []byte("not json\n"), 0o644)
	_, err := Open(dir)
	faults, _ := errors.AsType[Faults](err)
	var got []string
	for _, f := range faults {
		got = append(got, filepath.Base(f.File)+":"+strconv.Itoa(f.Line))
	}
	if want := []string{"a.jsonl:2", "a.jsonl:4", "b.jsonl:1"}; !slices.Equal(got, want) {
		t.Fatalf("faults at %v (%v); want %v", got, err, want)
	}
	if !strings.HasSuffix(err.Error(), "(and 2 more)") {
		t.Errorf("error %q does not count the rest", err)
	}
}

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"  Ram   1500 ":      "ram 1500",
		"MERCEDES-BENZ":      "mercedes-benz",
		"Citroën\tDS":        "citroën ds",
		"\u00a0ÉCLAIR\n":     "éclair",
		"Brake  Pads, Front": "brake pads, front",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q; want %q", in, got, want)
		}
	}
}

// TestFixture reads the synthetic fixture: four files, in name order,
// indexed.
func TestFixture(t *testing.T) {
	files, err := Open("../../testdata/vocab")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*File{}
	var names []string
	for _, f := range files {
		byName[f.Name] = f
		names = append(names, f.Name)
		if !filepath.IsAbs(f.Path) {
			t.Errorf("%s: path %q is not absolute", f.Name, f.Path)
		}
	}
	if want := []string{"classics", "listing", "parts", "yard"}; !slices.Equal(names, want) {
		t.Fatalf("files %v; want %v", names, want)
	}

	yard := byName["yard"]
	e, ok := yard.Entry(map[string]string{"make": " DODGE", "model": "ram   1500"})
	if !ok || e.Line != 2 || len(e.Mappings) != 2 {
		t.Fatalf("yard Dodge Ram 1500: %+v, %t; want line 2's two mappings", e, ok)
	}
	if m := e.Mappings[0]; m.Model != 1938 || m.First != nil || m.Last == nil || *m.Last != 2012 ||
		len(m.Qualifiers) != 1 || m.Qualifiers[0].Element != 34 || !slices.Contains(m.Qualifiers[0].Values, Value{IsText: true, Text: "DS (1500)"}) || m.Origin != "curated" {
		t.Errorf("Dodge Ram 1500's first mapping: %+v", m)
	}
	if m := e.Mappings[1]; m.Model != 13620 || m.First == nil || *m.First != 2013 || m.Last != nil || m.Note == "" {
		t.Errorf("Dodge Ram 1500's second mapping: %+v", m)
	}
	if _, ok := yard.Entry(map[string]string{"make": "Dodge"}); ok {
		t.Error("an entry found by part of its fields")
	}
	if _, ok := yard.Entry(map[string]string{"make": "Dodge", "model": "Caravan"}); ok {
		t.Error("an unknown entry found")
	}
	if e, ok := yard.Entry(map[string]string{"make": "studebaker", "model": "lark"}); !ok || len(e.Mappings) != 0 || e.Note == "" {
		t.Errorf("the unmapped entry: %+v, %t", e, ok)
	}
	var lines []int
	for _, e := range yard.EntriesFor(Target{Model: 13620}) {
		lines = append(lines, e.Line)
	}
	if want := []int{2, 3}; !slices.Equal(lines, want) {
		t.Errorf("entries for model 13620 on lines %v; want %v", lines, want)
	}
	if es := yard.EntriesFor(Target{Ext: "ext:classics/corvair"}); len(es) != 1 || es[0].Fields[1] != "Corvair" {
		t.Errorf("entries for the Corvair extension: %v", es)
	}

	listing := byName["listing"]
	if listing.Subject != SubjectPart || listing.Spine != "parts" || len(listing.EntriesFor(Target{ID: "brake-pads"})) != 2 {
		t.Errorf("listing: subject %q, spine %q, %d entries for brake-pads", listing.Subject, listing.Spine, len(listing.EntriesFor(Target{ID: "brake-pads"})))
	}
	e, _ = listing.Entry(map[string]string{"category": "brake pads"})
	if q := e.Mappings[1].Qualifiers; len(q) != 1 || q[0].Element != 39 || !slices.Equal(q[0].Values, []Value{{Attribute: 1}}) {
		t.Errorf("motorcycle brake pads' qualifiers: %+v", q)
	}

	classics := byName["classics"]
	if x, ok := classics.Extension("packard-clipper"); !ok || x.IsMake() || x.MakeExt != "ext:classics/packard" || x.Name != "Clipper" {
		t.Errorf("packard-clipper: %+v, %t", x, ok)
	}
	if x, ok := classics.Extension("packard"); !ok || !x.IsMake() {
		t.Errorf("packard: %+v, %t", x, ok)
	}

	parts := byName["parts"]
	if s, ok := parts.SpineEntry("300"); !ok || s.Name != "" || s.Line != 7 {
		t.Errorf("spine entry 300: %+v, %t", s, ok)
	}
}

// TestRead: a file read from a reader carries the name given and the hash
// of its bytes.
func TestRead(t *testing.T) {
	data := spineHeader + "\n" + `{"id":"pads","name":"Pads"}` + "\n"
	f, err := Read("parts", strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if f.Path != "parts" || f.Hash != blake3.Sum256([]byte(data)) || f.Kind != KindSpine || len(f.SpineEntries) != 1 {
		t.Errorf("Read: %+v", f)
	}
	_, err = Read("parts", strings.NewReader(spineHeader+"\n{}\n"))
	if faults, ok := errors.AsType[Faults](err); !ok || faults[0].String() != `parts:2: missing field "id"` {
		t.Errorf("Read of a faulty file: %v", err)
	}
}

// month is a Month without an artifact: Dodge (476) with its Ram (1938),
// lookup element 15 producing attribute 2, text element 34 producing
// "1500".
type month struct{}

func (month) Name() string { return "2026_10" }
func (month) Make(id int32) (string, bool) {
	if id == 476 {
		return "Dodge", true
	}
	return "", false
}
func (month) Model(id int32) (int32, string, bool) {
	if id == 1938 {
		return 476, "Ram", true
	}
	return 0, "", false
}
func (m month) Makes() iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) { yield(476, "Dodge") }
}
func (m month) Models(make int32) iter.Seq2[int32, string] {
	return func(yield func(int32, string) bool) {
		if make == 476 {
			yield(1938, "Ram")
		}
	}
}
func (month) Element(id int32) (bool, bool) {
	switch id {
	case 15:
		return true, true
	case 34:
		return false, true
	}
	return false, false
}
func (month) Attribute(element, id int32) bool     { return element == 15 && id == 2 }
func (month) Text(element int32, text string) bool { return element == 34 && text == "1500" }

// TestDrift measures files against a Month that is not an artifact.
func TestDrift(t *testing.T) {
	yard, err := Read("yard", strings.NewReader(strings.Join(lines(
		strings.Replace(vehicleHeader, `}`, `,"data_month":"2026_09"}`, 1),
		`{"entry":{"make":"Dodge","model":"Ram"},"mappings":[{"model":1938,"qualifiers":{"15":[2],"34":["1500"]}},{"make":476}]}`,
		`{"entry":{"make":"Dodge","model":"Ram 4x4"},"mappings":[{"model":1938,"qualifiers":{"15":["4WD"],"34":[1500],"999":[1]}}]}`,
		`{"entry":{"make":"Dodge","model":"Gone"},"mappings":[{"model":7},{"make":8}]}`,
	), "\n")))
	if err != nil {
		t.Fatal(err)
	}
	set, err := Read("classics", strings.NewReader(strings.Join(lines(
		setHeader,
		`{"id":"dodge","make_name":"DODGE "}`,
		`{"id":"ram","make":476,"name":"ram"}`,
		`{"id":"gone","make":8,"name":"Gone"}`,
		`{"id":"ok","make":476,"name":"Coronet"}`,
	), "\n")))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range Drift(month{}, []*File{yard, set}) {
		if f.Severity != SeverityDrift {
			t.Errorf("%v: severity %q", f, f.Severity)
		}
		got = append(got, string(f.Drift)+" "+f.String())
	}
	want := []string{
		`qualifier yard:3: /mappings/0/qualifiers/15/0: element 15 is a lookup element in data month 2026_10: its values are attribute ids, not "4WD" (the file was built against 2026_09)`,
		`qualifier yard:3: /mappings/0/qualifiers/34/0: element 34 is a text element in data month 2026_10: its values are strings, not 1500 (the file was built against 2026_09)`,
		`qualifier yard:3: /mappings/0/qualifiers/999: element 999 is not a public element of data month 2026_10 (the file was built against 2026_09)`,
		`model yard:4: /mappings/0/model: vPIC model 7 is not in data month 2026_10 (the file was built against 2026_09)`,
		`make yard:4: /mappings/1/make: vPIC make 8 is not in data month 2026_10 (the file was built against 2026_09)`,
		`extension classics:2: extension "dodge" is named like vPIC make 476 (Dodge) in data month 2026_10`,
		`extension classics:3: extension "ram" is named like vPIC model 1938 (Ram) under make 476 (Dodge) in data month 2026_10`,
		`make classics:4: /make: vPIC make 8 is not in data month 2026_10`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("drift:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
