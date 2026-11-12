package nitrovin

import (
	"os"
	"path/filepath"
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
