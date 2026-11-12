package vocab

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// stockFixture is the synthetic catalog with testdata/vocab/stock's shelf
// and yard sources.
func stockFixture(t *testing.T) []*File {
	t.Helper()
	files, err := Open("../../testdata/vocab/fitment", "../../testdata/vocab/stock", "../../testdata/vocab/stock/drift")
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func fileNamed(files []*File, name string) *File {
	for _, f := range files {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// TestStockLoads: the fixture's shelf and yard read every row form, and
// Link resolves a part vocabulary's entry and a spine entry to part types
// and each catalog vehicle; the Set keeps them out of translate and list.
func TestStockLoads(t *testing.T) {
	files := stockFixture(t)
	shelf, yard := fileNamed(files, "shelf-stock"), fileNamed(files, "yard-stock")
	if shelf == nil || yard == nil {
		t.Fatal("the fixture has no shelf-stock or yard-stock")
	}
	if shelf.Holds != StockShelf || shelf.StockCatalog != "parts-catalog" || shelf.Release != "7.01" || shelf.AsOf != "2026-11-01T08:00:00Z" || len(shelf.Stock) != 5 {
		t.Errorf("shelf header %q %q %q %q, %d rows", shelf.Holds, shelf.StockCatalog, shelf.Release, shelf.AsOf, len(shelf.Stock))
	}
	byID := map[string]*StockRow{}
	for _, f := range []*File{shelf, yard} {
		for i := range f.Stock {
			byID[f.Stock[i].ID] = &f.Stock[i]
		}
	}
	if r := byID["S-3"]; r.Key == nil || r.Key.Number != "BR-1999" {
		t.Errorf("S-3's key %v", r.Key)
	}
	if r := byID["S-4"]; !slices.Equal(r.PartTypes, []string{"mirror"}) || r.Vehicle == nil || r.Vehicle.Entry["model"] != "Ram 1500" || *r.Year != 2010 {
		t.Errorf("S-4, a part vocabulary's entry: %v %v %v", r.PartTypes, r.Vehicle, r.Year)
	}
	if r := byID["S-5"]; !slices.Equal(r.PartTypes, []string{"engine"}) {
		t.Errorf("S-5, a spine entry: %v", r.PartTypes)
	}
	if r := byID["Y-1"]; r.Decoded == nil || r.Decoded.Model != 1938 || r.Decoded.Year != 2010 || r.VIN == "" {
		t.Errorf("Y-1, decoded with a VIN: %+v", r)
	}
	if r := byID["Y-3"]; r.Vehicle == nil || r.Vehicle.Entry["make"] != "Chevrolet" {
		t.Errorf("Y-3, a catalog vehicle: %+v", r)
	}
	if got := string(byID["Y-1"].Fields); !strings.Contains(got, `"photos":["y100-1.jpg","y100-2.jpg"]`) {
		t.Errorf("Y-1's fields are not verbatim: %s", got)
	}
	set, err := NewSet(fixtureMonth{}, files)
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Stock()) != 4 || slices.ContainsFunc(set.Files(), func(f *File) bool { return f.Kind == KindStock }) {
		t.Errorf("Stock %d; Files hold stock", len(set.Stock()))
	}
	if _, err := set.List(ListQuery{Of: "shelf-stock"}); err == nil || !strings.Contains(err.Error(), "is a stock source") {
		t.Errorf("list of a stock source: %v", err)
	}
}

// TestStockFaults: a malformed row or header is refused with its line and
// pointer, and so is a shelf whose catalog is not loaded, a part or
// vehicle the catalog's files lack, and a name another file has.
func TestStockFaults(t *testing.T) {
	const shelf = `{"format":1,"kind":"stock","name":"s","version":"1","source":"shelf","as_of":"2026-11-01","catalog":"parts-catalog"}`
	const yard = `{"format":1,"kind":"stock","name":"y","version":"1","source":"yard","as_of":"2026-11-01T00:00:00Z"}`
	for _, tc := range []struct{ name, file, want string }{
		{"no source", `{"format":1,"kind":"stock","name":"s","version":"1","as_of":"2026-11-01"}`, `:1: missing field "source"`},
		{"no catalog", `{"format":1,"kind":"stock","name":"s","version":"1","source":"shelf","as_of":"2026-11-01"}`, `:1: missing field "catalog"`},
		{"yard catalog", `{"format":1,"kind":"stock","name":"s","version":"1","source":"yard","as_of":"2026-11-01","catalog":"c"}`, `:1: /catalog: a yard source names no catalog`},
		{"as_of", `{"format":1,"kind":"stock","name":"s","version":"1","source":"yard","as_of":"Nov 1"}`, `:1: /as_of: "Nov 1" is not a time`},
		{"no id", shelf + "\n" + `{"location":"a","type":"mirror","number":"M"}`, `:2: missing field "id"`},
		{"both forms", shelf + "\n" + `{"id":"1","location":"a","type":"mirror","number":"M","part":{"spine":"part-types","id":"mirror"}}`, `:2: a shelf row names its part by its fitment key`},
		{"no number", shelf + "\n" + `{"id":"1","location":"a","type":"mirror"}`, `:2: missing field "number"`},
		{"no vehicle", shelf + "\n" + `{"id":"1","location":"a","part":{"spine":"part-types","id":"mirror"},"year":2010}`, `:2: missing field "vehicle"`},
		{"not flat", shelf + "\n" + `{"id":"1","location":"a","type":"mirror","number":"M","fields":{"photos":[]}}`, `:2: /fields: a shelf row's fields are flat`},
		{"yard field", shelf + "\n" + `{"id":"1","location":"a","vin":"X"}`, `:2: unknown field "vin": a shelf row has`},
		{"duplicate id", shelf + "\n" + `{"id":"1","location":"a","type":"mirror","number":"M"}` + "\n" + `{"id":"1","location":"b","type":"mirror","number":"N"}`, `:3: /id: duplicate id "1": line 2 has it too`},
		{"no route", yard + "\n" + `{"id":"1","location":"a"}`, `:2: names no vehicle`},
		{"decoded", yard + "\n" + `{"id":"1","location":"a","decoded":{"make":476,"model":1938}}`, `:2: /decoded: missing field "year"`},
		{"vehicle without year", yard + "\n" + `{"id":"1","location":"a","vehicle":{"make":"Dodge","model":"Ram 1500"}}`, `:2: missing field "year"`},
		{"part not on spine", shelf + "\n" + `{"id":"1","location":"a","part":{"spine":"part-types","id":"seat"},"vehicle":{"make":"Dodge","model":"Ram 1500"},"year":2010}`, `:2: /part/id: spine "part-types" has no id "seat"`},
		{"vehicle fields", shelf + "\n" + `{"id":"1","location":"a","part":{"spine":"part-types","id":"mirror"},"vehicle":{"model":"Ram 1500"},"year":2010}`, `:2: /vehicle: fields "model" are not vocabulary "catalog-models"'s: "make", "model"`},
		{"catalog not loaded", strings.Replace(shelf, "parts-catalog", "other-catalog", 1), `:1: /catalog: catalog "other-catalog" is not loaded`},
		{"vehicle not in vocabulary", shelf + "\n" + `{"id":"1","location":"a","part":{"spine":"part-types","id":"mirror"},"vehicle":{"make":"Dodge","model":"Ram 9999"},"year":2010}`, `:2: /vehicle: vocabulary "catalog-models" has no entry {"make":"Dodge","model":"Ram 9999"}, so the row could never match`},
		{"yard vehicle not in vocabulary", `{"format":1,"kind":"stock","name":"y","version":"1","source":"yard","as_of":"2026-11-01"}` + "\n" + `{"id":"1","location":"a","vehicle":{"make":"Dodge","model":"Ram 9999"},"year":2010}`, `:2: /vehicle: vocabulary "catalog-models" has no entry`},
		{"name taken", strings.Replace(shelf, `"name":"s"`, `"name":"parts-catalog"`, 1), `:1: /name: "parts-catalog" is also the name of`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "stock.jsonl"), []byte(tc.file+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := Open("../../testdata/vocab/fitment", dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v\nwant a fault with %q", err, tc.want)
			}
		})
	}
}

// TestStockDrift: an orphan shelf row, whose number the catalog lacks
// current or retired, is drift, and a retired number is not; a field name
// that looks personal (any of the five words in its name, nested fields
// and arrays included) is drift once, at its first row, counting the rest.
func TestStockDrift(t *testing.T) {
	files := stockFixture(t)
	var got []string
	for _, d := range Drift(fixtureMonth{}, files) {
		if d.Drift == DriftOrphan || d.Drift == DriftPersonal {
			got = append(got, filepath.Base(d.File)+":"+d.String()[strings.LastIndex(d.String(), ".jsonl:")+7:])
		}
	}
	want := []string{`shelf-orphans.jsonl:2: /number: catalog "parts-catalog" has no fitment {"type":"mirror","number":"MR-0000","side":"right"}, current or retired`}
	if !slices.Equal(got, want) {
		t.Errorf("drift\n%q\nwant\n%q", got, want)
	}

	dir := t.TempDir()
	data := `{"format":1,"kind":"stock","name":"y","version":"1","source":"yard","as_of":"2026-11-01"}
{"id":"1","location":"a","vin":"X","fields":{"sellerPhone":"…","stock_number":"1"}}
{"id":"2","location":"a","vin":"Y","fields":{"sellerPhone":"…","owner_name":"…","phones":["…"],"phonenumber":"…","seller":{"email":"…","rating":5},"photos":[{"url":"a.jpg","license":"cc"},{"url":"b.jpg","license":"cc"}]}}
`
	if err := os.WriteFile(filepath.Join(dir, "y.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	for _, d := range Drift(fixtureMonth{}, files) {
		got = append(got, string(d.Drift)+" "+d.Detail)
	}
	want = []string{
		`personal-field /fields/sellerPhone: "sellerPhone" looks like personal data, which a stock source never supplies (and 1 more)`,
		`personal-field /fields/owner_name: "owner_name" looks like personal data, which a stock source never supplies`,
		`personal-field /fields/phones: "phones" looks like personal data, which a stock source never supplies`,
		`personal-field /fields/phonenumber: "phonenumber" looks like personal data, which a stock source never supplies`,
		`personal-field /fields/seller/email: "email" looks like personal data, which a stock source never supplies`,
		`personal-field /fields/photos/0/license: "license" looks like personal data, which a stock source never supplies (and 1 more)`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("drift\n%q\nwant\n%q", got, want)
	}
}
