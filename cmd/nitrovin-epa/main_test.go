package main

import (
	"archive/zip"
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// testdata/vehicles.csv is 78 rows of EPA's vehicles.csv (fueleconomy.gov,
// fetched 2026-11-10; US government work, public domain), chosen to reach
// every rule and outcome; testdata/curated.jsonl overrides some of them.
// testdata/vehicles.jsonl is what they build against the data month its
// header names; on any other month the build must differ only in that name.

var update = flag.Bool("update", false, "rewrite testdata/vehicles.jsonl")

// embeddedDecoder is the embedded data. The curated ids and the sample's
// models are long-lived, so the tests run on whatever month is embedded; one
// that fails names the month, so a refresh that breaks them says which.
func embeddedDecoder(t *testing.T) *nitrovin.Decoder {
	t.Helper()
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("on the embedded data month %s", embedded.Month)
		}
	})
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func runBuild(t *testing.T, args ...string) (out []byte, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	if err := run(args, &o, &e); err != nil {
		t.Fatalf("nitrovin-epa %s: %v\n%s", strings.Join(args, " "), err, e.String())
	}
	return o.Bytes(), e.String()
}

// TestBuild: the same inputs give the same bytes, from the CSV or its zip;
// they are the golden file's, but for the data month it names; the
// vocabulary loads against the embedded month with no drift, and every rule
// and outcome is in it.
func TestBuild(t *testing.T) {
	d := embeddedDecoder(t)
	out, stderr := runBuild(t, "-curated", "testdata/curated.jsonl", "testdata/vehicles.csv")
	again, _ := runBuild(t, "-curated", "testdata/curated.jsonl", "testdata/vehicles.csv")
	if !bytes.Equal(out, again) {
		t.Fatal("two builds of the same inputs differ")
	}
	zipped := filepath.Join(t.TempDir(), "vehicles.csv.zip")
	writeZip(t, zipped, "testdata/vehicles.csv")
	if fromZip, _ := runBuild(t, "-curated", "testdata/curated.jsonl", zipped); !bytes.Equal(out, fromZip) {
		t.Error("the zip builds other bytes than the CSV")
	}

	const golden = "testdata/vehicles.jsonl"
	if *update {
		if err := os.WriteFile(golden, out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	g, err := vocab.Read(golden, bytes.NewReader(want))
	if err != nil {
		t.Fatal(err)
	}
	// The month is named in the header alone: the version's suffix and
	// data_month.
	if !bytes.Equal(out, bytes.ReplaceAll(want, []byte(g.DataMonth), []byte(embedded.Month))) {
		t.Errorf("the build differs from %s, built against %s, in more than the month (go test -update rewrites it)", golden, g.DataMonth)
	}

	f, err := vocab.Read("vehicles.jsonl", bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if f.Name != Name || f.Version != "2026-06-09+"+embedded.Month || f.DataMonth != embedded.Month || !slices.Equal(f.Fields, []string{"make", "model"}) {
		t.Errorf("header: %s %s %s %v", f.Name, f.Version, f.DataMonth, f.Fields)
	}
	tr, err := nitrovin.NewTranslator(d, f)
	if err != nil {
		t.Fatal(err)
	}
	if drift := tr.Drift(); len(drift) > 0 {
		t.Errorf("the build drifts from %s: %v", embedded.Month, drift)
	}

	notes := map[string]bool{}
	for _, e := range f.Entries {
		notes[e.Note] = true
		for _, m := range e.Mappings {
			notes[m.Origin+": "+m.Note] = true
			if m.First == nil || m.Last == nil {
				t.Errorf("line %d: a mapping without years", e.Line)
			}
		}
	}
	for _, r := range []string{ruleExact, ruleStripped, rulePrefix, ruleBase, ruleFolded, ruleMakeName, ruleSlash} {
		if !notes["derived: rule: "+r] {
			t.Errorf("no mapping by rule %q", r)
		}
	}
	for _, n := range []string{
		"curated: vPIC names it Sierra Limited",
		"curated: a postal Jeep DJ",
		"a postal van vPIC has no model for",
		`vPIC has no make named "Aurora Cars Ltd"`,
		"no rule matched a vPIC model under Chevrolet",
		"for curation: the longest vPIC model prefix and baseModel name different models",
	} {
		if !notes[n] {
			t.Errorf("no note %q", n)
		}
	}
	if !strings.Contains(stderr, "1 curated entries are not in vehicles.csv") {
		t.Errorf("the stale override is not reported:\n%s", stderr)
	}
}

// TestEntry checks lines of the build that pin the rules down: slash
// alternatives map to several models, qualifiers come from the drive
// column and the model's schemas, curated lines win and keep what they
// say, the rest of their years coming from the EPA rows.
func TestEntry(t *testing.T) {
	embeddedDecoder(t)
	out, _ := runBuild(t, "-curated", "testdata/curated.jsonl", "testdata/vehicles.csv")
	f, err := vocab.Read("vehicles.jsonl", bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	get := func(make, model string) *vocab.Entry {
		t.Helper()
		e, ok := f.Entry(map[string]string{"make": make, "model": model})
		if !ok {
			t.Fatalf("no entry %s / %s", make, model)
		}
		return e
	}
	for _, c := range []struct {
		make, model string
		want        string // mappings, as model:first-last[qualifiers]origin
	}{
		{"Acura", "ADX AWD", "35664:2025-2026[15:2,3]derived"},
		{"Acura", "ADX FWD", "35664:2025-2026[15:7]derived"},
		{"Audi", "80/90", "14342:1988-1988[]derived 8056:1988-1988[]derived"},
		{"GMC", "Sierra LTD K15 4WD", "24910:2019-2019[15:2 34:1500]curated"},
		{"AM General", "DJ Po Vehicle 2WD", "make606:1983-1984[]curated"},
		{"Jaguar", "XJ8 4.2 Litre", ""},
		{"Nissan", "ARIYA FWD 63kWh", "36667:2025-2026[15:7]curated"},
		{"Nissan", "ARIYA ENGAGE+/EVOLVE+ e-4ORCE 87kWh", "30884:2024-2024[]curated 36667:2025-2025[]curated"},
		{"Toyota", "Matrix AWD", "2466:2002-2005[15:2]curated"},
		{"Volvo", "740/760", "14119:1986-1986[]curated 14121:1986-1986[]curated"},
	} {
		if got := describe(get(c.make, c.model)); got != c.want {
			t.Errorf("%s / %s: %s; want %s", c.make, c.model, got, c.want)
		}
	}
}

// describe is an entry's mappings in TestEntry's notation.
func describe(e *vocab.Entry) string {
	var parts []string
	for _, m := range e.Mappings {
		var b strings.Builder
		if m.Make != 0 {
			b.WriteString("make")
			b.WriteString(itoa(int(m.Make)))
		} else {
			b.WriteString(itoa(int(m.Model)))
		}
		b.WriteString(":" + itoa(*m.First) + "-" + itoa(*m.Last) + "[")
		for i, q := range m.Qualifiers {
			if i > 0 {
				b.WriteByte(' ')
			}
			b.WriteString(itoa(int(q.Element)) + ":")
			for j, v := range q.Values {
				if j > 0 {
					b.WriteByte(',')
				}
				if v.IsText {
					b.WriteString(v.Text)
				} else {
					b.WriteString(itoa(int(v.Attribute)))
				}
			}
		}
		b.WriteString("]" + m.Origin)
		parts = append(parts, b.String())
	}
	return strings.Join(parts, " ")
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestCuratedLoadsClean: the built-in overrides read without a fault and
// name only ids the embedded month has.
func TestCuratedLoadsClean(t *testing.T) {
	d := embeddedDecoder(t)
	f, err := readCurated("")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d, f)
	if err != nil {
		t.Fatal(err)
	}
	if drift := tr.Drift(); len(drift) > 0 {
		t.Errorf("%s drifts from %s: %v", curatedName, embedded.Month, drift)
	}
	for _, e := range f.Entries {
		for _, m := range e.Mappings {
			if m.Ext != "" {
				t.Errorf("line %d: an extension mapping", e.Line)
			}
		}
	}
}

func TestCandidates(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"stanza wagon 4wd", []string{"stanza wagon 4wd", "stanza wagon", "stanza"}},
		{"explorer 4wd ffv", []string{"explorer 4wd ffv", "explorer ffv", "explorer"}},
		{"ex90 (21 inch wheels)", []string{"ex90 (21 inch wheels)", "ex90"}},
		{"q8 quattro", []string{"q8 quattro", "q8"}},
		{"wagon", []string{"wagon"}},
	} {
		if got := candidates(c.in); !slices.Equal(got, c.want) {
			t.Errorf("candidates(%q) = %q; want %q", c.in, got, c.want)
		}
	}
}

func TestTonnage(t *testing.T) {
	for in, want := range map[string]string{
		"silverado c15 2wd":        "1500",
		"sierra k2500 4wd":         "2500",
		"ram 3500 pickup":          "3500",
		"silverado 2500hd 4wd":     "2500",
		"c10 pickup 2wd":           "1500",
		"v20 pickup 4wd":           "2500",
		"d100/d150 pickup 2wd":     "1500",
		"w250 pickup 4wd":          "2500",
		"express 1500/2500 2wd":    "1500",
		"tahoe 2wd":                "",
		"b150/b250 van 2wd":        "",
		"silverado 15 hybrid 2wd":  "",
		"c1500 pickup 2wd (cargo)": "1500",
	} {
		if got := tonnage(in); got != want {
			t.Errorf("tonnage(%q) = %q; want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		text, class string
		want        bool
	}{
		{"1500", "1500", true},
		{"1500 (1/2 Ton)", "1500", true},
		{"Luxury (1/2 Ton)", "1500", true},
		{"DS (1500)", "1500", true},
		{"3500W", "3500", true},
		{"2500 HD", "2500", true},
		{"1 Ton", "3500", true},
		{"1/2 Ton", "3500", false},
		{"15000", "1500", false},
		{"4500 Cutaway", "1500", false},
	} {
		if got := seriesClass(c.text, c.class); got != c.want {
			t.Errorf("seriesClass(%q, %q) = %v", c.text, c.class, got)
		}
	}
}

func writeZip(t *testing.T, path, csv string) {
	t.Helper()
	data, err := os.ReadFile(csv)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := zip.NewWriter(f)
	w, err := z.Create("vehicles.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
}
