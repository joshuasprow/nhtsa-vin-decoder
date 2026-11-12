package nitrovin

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/fitment"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// TestCatalogLoads: testdata/vocab/fitment loads beside testdata/vocab
// with no drift; Info lists the catalog with its release and counts, and
// the other files without them, as decode.schema.json's Info says; and a
// translation to the catalog's vehicle vocabulary reaches its fitments
// through fitment.Reader.
func TestCatalogLoads(t *testing.T) {
	d := embeddedDecoder(t)
	files, err := vocab.Open("../../testdata/vocab", "../../testdata/vocab/fitment")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	if drift := tr.Drift(); len(drift) > 0 {
		t.Errorf("the catalog fixture drifts: %v", drift)
	}
	info := tr.Info()
	b := conforms(t, "Info", info)
	var catalogs []contract.VocabularyFile
	for _, v := range info.Vocabularies {
		switch {
		case v.Kind == "catalog":
			catalogs = append(catalogs, v)
		case v.Release != "" || v.CatalogCounts != nil:
			t.Errorf("%s, a %s, carries a catalog's release or counts: %+v", v.Name, v.Kind, v)
		}
	}
	want := contract.CatalogCounts{Fitments: 5, Applications: 7, Redirects: 1, Retired: 1}
	if len(catalogs) != 1 || catalogs[0].Name != "parts-catalog" || catalogs[0].Release != "7.02" || catalogs[0].CatalogCounts == nil || *catalogs[0].CatalogCounts != want {
		t.Fatalf("info's catalog: %+v", catalogs)
	}
	if !strings.Contains(string(b), `"release":"7.02","blake3":`) || !strings.Contains(string(b), `"drift":[],"fitments":5,"applications":7,"redirects":1,"retired":1}`) {
		t.Errorf("info's JSON does not list the catalog's release and counts:\n%s", b)
	}

	cat := tr.Catalog()
	if cat == nil || cat.Info().Name != "parts-catalog" {
		t.Fatalf("Catalog %v", cat)
	}
	// The seam a fit request crosses: vPIC's model 1938 in 2010, through
	// the spine, to the catalog's own vehicle, then its mirrors.
	route, err := tr.core.Set.Translate(vocab.Query{From: vocab.From{Ref: vocab.Ref{Target: vocab.Target{Model: 1938}}}, To: []string{cat.Info().Vehicles}, ModelYear: new(2010)})
	if err != nil {
		t.Fatal(err)
	}
	vehicles := fitment.Vehicles(cat.Info(), route)
	if len(vehicles) != 1 || vehicles[0].Entry["model"] != "Ram 1500" {
		t.Fatalf("vehicles reached: %+v", vehicles)
	}
	var got []string
	for _, c := range cat.ByVehicle("mirror", vehicles, new(2010), nil) {
		got = append(got, c.Fitment.Key.Number+" "+c.Fitment.Key.Side.String()+" "+strconv.Itoa(len(c.Applications)))
	}
	if want := []string{"MR-1001 left 2", "MR-1001 right 1"}; !slices.Equal(got, want) {
		t.Errorf("mirrors for model 1938 in 2010: %v; want %v", got, want)
	}

	// Without a catalog, none.
	none, err := NewTranslator(d)
	if err != nil {
		t.Fatal(err)
	}
	if none.Catalog() != nil {
		t.Errorf("Catalog with nothing loaded: %v", none.Catalog())
	}
}

// TestCatalogDrift: a catalog's drift is reported beside the files' and
// counted in Info by its own kinds, after the month's.
func TestCatalogDrift(t *testing.T) {
	d := embeddedDecoder(t)
	dir := t.TempDir()
	fixture, err := os.ReadFile("../../testdata/vocab/fitment/catalog.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	catalog := string(fixture) +
		`{"fitment":{"type":"engine","number":"EN-99"},"applications":[{"vehicle":{"make":"Dodge","model":"Dakota"},"options":""},{"vehicle":{"make":"Dodge","model":"Ram 1500"},"options":"","qualifiers":{"15":[99]}}]}` + "\n" +
		`{"redirect":{"vehicle":{"make":"Dodge","model":"Ram 1500"},"first":2013,"from":"engine","to":"mirror"}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "catalog.jsonl"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open("../../testdata/vocab/fitment/catalog-models.jsonl", "../../testdata/vocab/fitment/part-types.jsonl", dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range tr.Drift() {
		got = append(got, string(f.Drift)+" "+strconv.Itoa(f.Line)+": "+f.Detail)
	}
	want := []string{
		`vehicle 9: /applications/0/vehicle: vocabulary "catalog-models" has no entry {"make":"Dodge","model":"Dakota"}`,
		driftDetail(`qualifier 9: /applications/1/qualifiers/15/0: data month %[1]s never decodes element 15 to attribute id 99`),
		`redirect 10: /redirect/to: part type "mirror" has no application for this vehicle in 2013 on`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("drift:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	info := tr.Info()
	conforms(t, "Info", info)
	var kinds []string
	for _, v := range info.Vocabularies {
		if v.Kind == "catalog" {
			for _, c := range v.Drift {
				kinds = append(kinds, c.Kind+"="+strconv.Itoa(c.Count))
			}
		}
	}
	if want := []string{"qualifier=1", "vehicle=1", "redirect=1"}; !slices.Equal(kinds, want) {
		t.Errorf("catalog drift counts %v; want %v", kinds, want)
	}
}

// TestCatalogModelDrift: a catalog whose vehicles are vPIC's has its
// applications' qualifiers tested against their model's values (ADR 0015),
// as a mapping's are: off load, in AllDrift and Info, once per value at its
// first application. A make vehicle keeps the month-wide test.
func TestCatalogModelDrift(t *testing.T) {
	d := embeddedDecoder(t)
	dir := t.TempDir()
	// "2500 Cargo" is a Series only the Express van line decodes to.
	catalog := `{"format":1,"kind":"catalog","name":"vpic-parts","version":"1","data_month":"2026_10","spine":"part-types","vehicles":"vpic"}` + "\n" +
		`{"fitment":{"type":"mirror","number":"MR-1","side":"left"},"applications":[{"vehicle":{"model":1850},"options":"","qualifiers":{"34":["2500 Cargo"]}},{"vehicle":{"model":1850},"first":2010,"options":"","qualifiers":{"34":["2500 Cargo"]}}]}` + "\n" +
		`{"fitment":{"type":"mirror","number":"MR-2","side":"left"},"applications":[{"vehicle":{"make":467},"options":"","qualifiers":{"34":["2500 Cargo"]}}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "catalog.jsonl"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open("../../testdata/vocab/fitment/part-types.jsonl", dir)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	if drift := tr.Drift(); len(drift) > 0 {
		t.Errorf("load drift %v; want none, model-qualifier is off load", drift)
	}
	var got []string
	for _, f := range tr.AllDrift() {
		got = append(got, string(f.Drift)+" "+strconv.Itoa(f.Line)+": "+f.Detail)
	}
	want := []string{
		driftDetail(`model-qualifier 2: /applications/0/qualifiers/34/0: vPIC model 1850 never decodes element 34 to "2500 Cargo" in data month %[1]s (and 1 more)`),
	}
	if !slices.Equal(got, want) {
		t.Errorf("all drift:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	var kinds []string
	for _, v := range tr.Info().Vocabularies {
		if v.Kind == "catalog" {
			for _, c := range v.Drift {
				kinds = append(kinds, c.Kind+"="+strconv.Itoa(c.Count))
			}
		}
	}
	if want := []string{"model-qualifier=1"}; !slices.Equal(kinds, want) {
		t.Errorf("catalog drift counts %v; want %v", kinds, want)
	}

	// Compiled (ADR 0021): its compiling measured model-qualifier with the
	// rest and stored it. Load still reports none of it, and LoadInfo
	// counts none; AllDrift and Info have it, as the source's do.
	var b bytes.Buffer
	if _, err := compileCatalog(t, &b, d.month, files); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.compiled")
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := vocab.Open("../../testdata/vocab/fitment/part-types.jsonl", path)
	if err != nil {
		t.Fatal(err)
	}
	ctr, err := NewTranslator(d, loaded...)
	if err != nil {
		t.Fatal(err)
	}
	if drift := ctr.Drift(); len(drift) > 0 {
		t.Errorf("compiled load drift %v; want none, model-qualifier is off load", drift)
	}
	if c := ctr.LoadInfo().Vocabularies[1]; len(c.Drift) != 0 {
		t.Errorf("compiled LoadInfo drift %+v; want none", c.Drift)
	}
	all := slices.Clone(tr.AllDrift())
	for i := range all {
		all[i].File = strings.Replace(all[i].File, filepath.Join(dir, "catalog.jsonl"), path, 1)
	}
	if got := ctr.AllDrift(); !reflect.DeepEqual(got, all) {
		t.Errorf("compiled all drift\n%+v\nwant\n%+v", got, all)
	}
	if got, want := ctr.Info().Vocabularies[1].Drift, tr.Info().Vocabularies[1].Drift; !reflect.DeepEqual(got, want) {
		t.Errorf("compiled Info drift %+v; want %+v", got, want)
	}
	conforms(t, "Info", ctr.Info())
}

// renamed is a data month under another name: the month a catalog was
// compiled against, when it is not the loaded one.
type renamed struct {
	*month
	name string
}

func (r renamed) Name() string { return r.name }

// TestCompiledCatalog: a compiled catalog (ADR 0021) loads beside its
// spine and vehicle vocabulary as its JSON Lines source does, answers the
// same, and Info lists it with its source's hash, its writer, and the
// drift its compiling stored with that drift's month, saying when the
// month is not the loaded one; beside another vehicle vocabulary, it says
// links_differ and its vehicle drift is measured against that one.
func TestCompiledCatalog(t *testing.T) {
	d := embeddedDecoder(t)
	dir := t.TempDir()
	fixture, err := os.ReadFile("../../testdata/vocab/fitment/catalog.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	// One drift of the vehicle vocabulary's, one of the month's.
	catalog := string(fixture) + `{"fitment":{"type":"engine","number":"EN-99"},"applications":[{"vehicle":{"make":"Dodge","model":"Dakota"},"options":""},{"vehicle":{"make":"Dodge","model":"Ram 1500"},"options":"","qualifiers":{"15":[99]}}]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "catalog.jsonl"), []byte(catalog), 0o644); err != nil {
		t.Fatal(err)
	}
	links := []string{"../../testdata/vocab/fitment/part-types.jsonl", "../../testdata/vocab/fitment/catalog-models.jsonl"}
	files, err := vocab.Open(append(links, dir)...)
	if err != nil {
		t.Fatal(err)
	}
	jsonl, err := NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		month   string
		differs bool
	}{{d.month.Name(), false}, {"2026_01", true}} {
		t.Run(tc.month, func(t *testing.T) {
			var b bytes.Buffer
			if _, err := compileCatalog(t, &b, renamed{d.month, tc.month}, files); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "catalog.compiled")
			if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
				t.Fatal(err)
			}
			loaded, err := vocab.Open(append(links, path)...)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := NewTranslator(d, loaded...)
			if err != nil {
				t.Fatal(err)
			}
			info := tr.Info()
			conforms(t, "Info", info)
			got, want := info.Vocabularies[2], jsonl.Info().Vocabularies[2]
			source := want.Blake3
			wantCompiled := contract.CompiledCatalog{Format: vocab.CompiledFormat, SourceBlake3: source, Writer: Version(), DriftMonth: tc.month, DriftMonthDiffers: tc.differs, LinksDiffer: false, SkippedDrift: []string{}}
			if got.Compiled == nil || !reflect.DeepEqual(*got.Compiled, wantCompiled) || got.Blake3 == source || got.Path != path {
				t.Errorf("info's compiled catalog: %+v, compiled %+v; want %+v", got, got.Compiled, wantCompiled)
			}
			if want.Compiled != nil {
				t.Errorf("info's JSON Lines catalog is compiled: %+v", want.Compiled)
			}
			kinds := func(cs []contract.DriftCount) (out []string) {
				for _, c := range cs {
					out = append(out, c.Kind+"="+strconv.Itoa(c.Count))
				}
				return out
			}
			if g, w := kinds(got.Drift), kinds(want.Drift); !slices.Equal(g, w) || len(w) != 2 || *got.CatalogCounts != *want.CatalogCounts || got.Release != want.Release {
				t.Errorf("compiled catalog's drift %v, counts %+v; want %v, %+v", g, got.CatalogCounts, w, want.CatalogCounts)
			}
			if q := got.Drift[0]; !strings.Contains(q.Examples[0].Detail, "data month "+tc.month+" never decodes") {
				t.Errorf("stored qualifier drift %+v; want it measured against %s", q, tc.month)
			}

			cat, ram := tr.Catalog(), []vocab.Vehicle{{Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}}
			if cat.Info() != jsonl.Catalog().Info() {
				t.Errorf("Catalog().Info() %+v; want %+v", cat.Info(), jsonl.Catalog().Info())
			}
			var numbers []string
			for _, c := range cat.ByVehicle("mirror", ram, new(2010), nil) {
				numbers = append(numbers, c.Fitment.Key.Number+" "+c.Fitment.Key.Side.String()+" "+strconv.Itoa(len(c.Applications)))
			}
			if want := []string{"MR-1001 left 2", "MR-1001 right 1"}; !slices.Equal(numbers, want) {
				t.Errorf("mirrors for the Ram 1500 in 2010: %v; want %v", numbers, want)
			}
		})
	}

	// Another vehicle vocabulary beside it, which no longer maps the
	// Silverado: links_differ, and the vehicle drift measured against it,
	// as its source's is.
	t.Run("another vehicle vocabulary", func(t *testing.T) {
		var b bytes.Buffer
		if _, err := compileCatalog(t, &b, d.month, files); err != nil {
			t.Fatal(err)
		}
		tmp := t.TempDir()
		path := filepath.Join(tmp, "catalog.compiled")
		if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		models, err := os.ReadFile(links[1])
		if err != nil {
			t.Fatal(err)
		}
		models = bytes.Replace(models, []byte(`"mappings":[{"model":1850}]`), []byte(`"mappings":[]`), 1)
		models = bytes.Replace(models, []byte(`"version":"2026.11"`), []byte(`"version":"2026.12"`), 1)
		other := filepath.Join(tmp, "catalog-models.jsonl")
		if err := os.WriteFile(other, models, 0o644); err != nil {
			t.Fatal(err)
		}
		load := func(catalog string) contract.VocabularyFile {
			t.Helper()
			fs, err := vocab.Open(links[0], other, catalog)
			if err != nil {
				t.Fatal(err)
			}
			tr, err := NewTranslator(d, fs...)
			if err != nil {
				t.Fatal(err)
			}
			conforms(t, "Info", tr.Info())
			return tr.Info().Vocabularies[2]
		}
		got, want := load(path), load(dir)
		if got.Compiled == nil || !got.Compiled.LinksDiffer || got.Compiled.DriftMonthDiffers {
			t.Errorf("compiled %+v; want links_differ", got.Compiled)
		}
		if !reflect.DeepEqual(got.Drift, want.Drift) || len(want.Drift) != 2 || want.Drift[1].Count != 2 {
			t.Errorf("drift beside another vocabulary %+v; want %+v", got.Drift, want.Drift)
		}
	})
}
