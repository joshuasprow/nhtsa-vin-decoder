package stock

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// TestYardRoutes: a yard row takes its first route that gives a vPIC model
// and a year. A VIN that decodes a model and no year falls to the row's
// catalog vehicle and year when it names one, and otherwise matches the
// model in every year; a VIN that decodes no model falls to the vehicle.
func TestYardRoutes(t *testing.T) {
	dir := t.TempDir()
	data := `{"format":1,"kind":"stock","name":"y","version":"1","source":"yard","as_of":"2026-11-01"}
{"id":"no-year-vehicle","location":"a","vin":"NOYEAR","vehicle":{"make":"Chevrolet","model":"Silverado 1500"},"year":2010}
{"id":"no-year","location":"a","vin":"NOYEAR"}
{"id":"no-model","location":"b","vin":"NOMODEL","vehicle":{"make":"Chevrolet","model":"Silverado 1500"},"year":2011}
{"id":"decoded","location":"b","decoded":{"make":467,"model":1850,"year":2012},"vin":"NOMODEL"}
`
	if err := os.WriteFile(filepath.Join(dir, "y.jsonl"), []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open("../../testdata/vocab/fitment", dir)
	if err != nil {
		t.Fatal(err)
	}
	var f *vocab.File
	for _, g := range files {
		if g.Name == "y" {
			f = g
		}
	}
	src := NewFile(f, func(vin string) (int32, *int, bool) {
		if vin == "NOYEAR" {
			return 1850, nil, true
		}
		return 0, nil, false
	})
	silverado := vocab.Vehicle{Entry: map[string]string{"make": "Chevrolet", "model": "Silverado 1500"}}
	ids := func(q YardQuery, p Page) []string {
		t.Helper()
		rs, meta, err := src.Yard(context.Background(), []YardQuery{q}, p)
		if err != nil || len(rs) != 1 || meta.AsOf != "2026-11-01" || meta.Release != "" {
			t.Fatalf("Yard: %v, %d results, %+v", err, len(rs), meta)
		}
		var out []string
		for _, r := range rs[0].Rows {
			out = append(out, r.ID+"/"+string(r.Matched))
		}
		return out
	}
	y := func(n int) *int { return &n }
	if got, want := ids(YardQuery{Models: []ModelYears{{Model: 1850, First: y(2012), Last: y(2012)}}}, Page{Limit: 10}), []string{"no-year/vin", "decoded/vin"}; !slices.Equal(got, want) {
		t.Errorf("model 1850 in 2012: %q, want %q", got, want)
	}
	if got, want := ids(YardQuery{Vehicles: []VehicleYears{{Vehicle: silverado}}}, Page{Limit: 10}), []string{"no-year-vehicle/catalog-vehicle", "no-model/catalog-vehicle"}; !slices.Equal(got, want) {
		t.Errorf("the Silverado by vehicle: %q, want %q", got, want)
	}
	both := YardQuery{Models: []ModelYears{{Model: 1850}}, Vehicles: []VehicleYears{{Vehicle: silverado}}}
	if got, want := ids(both, Page{Location: []string{"b"}, Offset: 1, Limit: 10}), []string{"decoded/vin"}; !slices.Equal(got, want) {
		t.Errorf("both routes at b, after the first: %q, want %q", got, want)
	}
	if rs, _, _ := src.ShelfByFitment(context.Background(), [][]vocab.FitmentKey{{{Type: "mirror", Number: "MR-1001"}}}, Page{Limit: 10}); len(rs) != 1 || rs[0].Total != 0 || rs[0].Rows == nil {
		t.Errorf("a yard's shelf lookup: %+v", rs)
	}
}
