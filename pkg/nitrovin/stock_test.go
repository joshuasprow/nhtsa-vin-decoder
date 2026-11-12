package nitrovin

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// stockTranslator loads testdata/vocab, the synthetic catalog and the
// synthetic shelf and yard in testdata/vocab/stock.
func stockTranslator(t *testing.T) *Translator {
	t.Helper()
	files, err := vocab.Open("../../testdata/vocab", "../../testdata/vocab/fitment", "../../testdata/vocab/stock", "../../testdata/vocab/stock/drift")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTranslator(embeddedDecoder(t), files...)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

// stockText writes a list as "total: row row …", each row its id, how it
// matched, and how its number reached the fitment when not directly.
func stockText(l *contract.StockList) string {
	if l == nil {
		return "absent"
	}
	var rows []string
	for _, r := range l.Rows {
		s := r.ID + "/" + r.Matched
		if r.Reached == contract.ReachedRetired {
			s += "/retired"
		}
		rows = append(rows, s)
	}
	return fmt.Sprintf("%d: %s", l.Total, strings.Join(rows, " "))
}

// sourcesText writes a list's sources: name, kind, status and mismatch.
func sourcesText(l *contract.StockList) string {
	var out []string
	for _, s := range l.Sources {
		t := s.Name + " " + s.Kind + " " + s.Status
		if s.CatalogMismatch {
			t += " mismatch"
		}
		if s.Reason != "" {
			t += " (" + s.Reason + ")"
		}
		out = append(out, t)
	}
	return strings.Join(out, ", ")
}

// TestFitStock: every row form answers through fit. A candidate's shelf
// holds the rows of its number and of the retired number it replaced, from
// every shelf; its yard the vehicles among its applications, by a
// precomputed decode, a decoded VIN or a catalog vehicle; a shelf numbered
// under another catalog release is marked, and one naming no release is
// not; an orphan row appears under no candidate; rows with no number
// answer as shelf_by_vehicle; and related vehicles count their yard.
func TestFitStock(t *testing.T) {
	tr := stockTranslator(t)
	const ram = `"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010`

	f, err := fit(t, tr, `{`+ram+`,"part":{"vocabulary":"part-codes","entry":{"code":"ROT"}},"stock":["shelf","yard"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Candidates) != 1 || f.Candidates[0].Fitment.Number != "BR-2001" {
		t.Fatalf("candidates %+v", f.Candidates)
	}
	c := f.Candidates[0]
	if got, want := stockText(c.Shelf), "4: A-1/fitment S-2/fitment S-3/fitment/retired O-2/fitment/retired"; got != want {
		t.Errorf("BR-2001's shelf %s, want %s", got, want)
	}
	if got, want := sourcesText(c.Shelf), "shelf-annex file available, shelf-stock file available mismatch, shelf-orphans file available"; got != want {
		t.Errorf("BR-2001's shelf sources %s, want %s", got, want)
	}
	if s := c.Shelf.Sources[1]; s.Release != "7.01" || s.Catalog != "parts-catalog" || s.AsOf != "2026-11-01T08:00:00Z" {
		t.Errorf("the shelf's block %+v", s)
	}
	if s := c.Shelf.Sources[0]; s.Release != "" || s.CatalogMismatch || s.AsOf != "2026-10-30" {
		t.Errorf("the annex's block, no release: %+v", s)
	}
	if got, want := stockText(c.Yard), "3: Y-1/vin Y-2/vin Y-3/catalog-vehicle"; got != want {
		t.Errorf("BR-2001's yard %s, want %s", got, want)
	}
	if got, want := sourcesText(c.Yard), "yard-stock file available"; got != want {
		t.Errorf("BR-2001's yard sources %s, want %s", got, want)
	}
	if got := string(c.Yard.Rows[0].Fields); !strings.Contains(got, `"photos":["y100-1.jpg","y100-2.jpg"]`) {
		t.Errorf("Y-1's fields are not verbatim: %s", got)
	}
	if f.ShelfByVehicle == nil || f.ShelfByVehicle.Total != 0 {
		t.Errorf("rotors' shelf_by_vehicle %s", stockText(f.ShelfByVehicle))
	}

	f, err = fit(t, tr, `{`+ram+`,"part":{"vocabulary":"part-codes","entry":{"code":"MIR"}},"stock":["shelf"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var mirrors []string
	for _, c := range f.Candidates {
		mirrors = append(mirrors, c.Fitment.Side+" "+stockText(c.Shelf)+" yard "+stockText(c.Yard))
	}
	if want := []string{"left 1: S-1/fitment yard absent", "right 0:  yard absent"}; !slices.Equal(mirrors, want) {
		t.Errorf("mirrors %q, want %q: the orphan O-1 is under none", mirrors, want)
	}
	if got, want := stockText(f.ShelfByVehicle), "2: A-2/vehicle S-4/vehicle"; got != want {
		t.Errorf("mirrors' shelf_by_vehicle %s, want %s", got, want)
	}
	if r := f.ShelfByVehicle.Rows[1]; r.Part != "mirror" || r.Vehicle == nil || r.Year != contract.Value(int32(2010)) {
		t.Errorf("S-4 as answered %+v", r)
	}

	f, err = fit(t, tr, `{"from":{"model":1850},"model_year":2010,"related":true,"stock":["yard"]}`)
	if err != nil {
		t.Fatal(err)
	}
	var related []string
	for _, r := range f.Related {
		related = append(related, vehicleText(r.Vehicle)+" "+stockText(r.Yard))
	}
	if want := []string{"Dodge/Ram 1500 2: "}; !slices.Equal(related, want) {
		t.Errorf("related %q, want %q", related, want)
	}
	if _, err := tr.Fit(context.Background(), contract.FitRequest{From: contract.FitFrom{SpineRef: contract.SpineRef{Model: 1850}}, Related: true, Stock: []string{"shelf"}}); contract.AsProblem(err).Pointer != "/stock" {
		t.Errorf("related with stock shelf: %v, want a refusal at /stock", err)
	}

	// The engine is in stock with no number, for a Silverado.
	f, err = fit(t, tr, `{"from":{"vocabulary":"catalog-models","entry":{"make":"Chevrolet","model":"Silverado 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"engine"},"stock":["shelf"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stockText(f.ShelfByVehicle), "1: S-5/vehicle"; got != want {
		t.Errorf("a Silverado's engines by vehicle %s, want %s", got, want)
	}
}

// TestFitStockLocation: location keeps the rows at its locations and drops
// the others, in every list and across pages.
func TestFitStockLocation(t *testing.T) {
	tr := stockTranslator(t)
	const ram = `"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010`
	f, err := fit(t, tr, `{`+ram+`,"part":{"vocabulary":"part-codes","entry":{"code":"ROT"}},"stock":["shelf","yard"],"location":["north","east"]}`)
	if err != nil {
		t.Fatal(err)
	}
	c := f.Candidates[0]
	if got, want := stockText(c.Shelf)+" | "+stockText(c.Yard), "2: A-1/fitment S-3/fitment/retired | 1: Y-1/vin"; got != want {
		t.Errorf("BR-2001 at north and east: %s, want %s", got, want)
	}
	f, err = fit(t, tr, `{`+ram+`,"part":{"vocabulary":"part-codes","entry":{"code":"MIR"}},"stock":["shelf"],"location":["north"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stockText(f.Candidates[0].Shelf)+" | "+stockText(f.ShelfByVehicle), "1: S-1/fitment | 1: S-4/vehicle"; got != want {
		t.Errorf("mirrors at north: %s, want %s", got, want)
	}
	pages := stockPages(t, tr, `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"stock":["shelf","yard"],"location":["north","east"],"limit":1`)
	want := []string{
		"1 vehicles, shelf 2: A-1/fitment, yard 1: Y-1/vin",
		"1 vehicles, shelf 2: S-3/fitment/retired, yard 1: ",
	}
	if !slices.Equal(pages, want) {
		t.Errorf("pages at north and east\n%q\nwant\n%q", pages, want)
	}
}

// stockPages fits request (a JSON object without its closing brace) page
// by page, writing each page's vehicles count and stock lists.
func stockPages(t *testing.T, tr *Translator, request string) []string {
	t.Helper()
	var pages []string
	after := ""
	for range 10 {
		req := request
		if after != "" {
			req += `,"after":"` + after + `"`
		}
		f, err := fit(t, tr, req+`}`)
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case f.Shelf != nil:
			pages = append(pages, fmt.Sprintf("%d vehicles, shelf %s, yard %s", len(f.Vehicles), stockText(f.Shelf), stockText(f.Yard)))
		default:
			var cs []string
			for _, c := range f.Candidates {
				cs = append(cs, c.Fitment.Number+" "+c.Fitment.Side)
			}
			pages = append(pages, fmt.Sprintf("candidates %q, by vehicle %s", cs, stockText(f.ShelfByVehicle)))
		}
		if after = f.Next; after == "" {
			break
		}
	}
	return pages
}

// TestFitStockPaging: from a fitment key, after and limit page the
// fitment's shelf and yard with its vehicles; with part, they page
// shelf_by_vehicle with the candidates; and next goes on while any list
// has more.
func TestFitStockPaging(t *testing.T) {
	tr := stockTranslator(t)
	pages := stockPages(t, tr, `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-1999"}},"stock":["shelf","yard"],"limit":1`)
	want := []string{
		"1 vehicles, shelf 4: A-1/fitment, yard 3: Y-1/vin",
		"1 vehicles, shelf 4: S-2/fitment, yard 3: Y-2/vin",
		"0 vehicles, shelf 4: S-3/fitment/retired, yard 3: Y-3/catalog-vehicle",
		"0 vehicles, shelf 4: O-2/fitment/retired, yard 3: ",
	}
	if !slices.Equal(pages, want) {
		t.Errorf("pages\n%q\nwant\n%q", pages, want)
	}
	pages = stockPages(t, tr, `{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"mirror"},"stock":["shelf"],"limit":1`)
	want = []string{
		`candidates ["MR-1001 left"], by vehicle 2: A-2/vehicle`,
		`candidates ["MR-1001 right"], by vehicle 2: S-4/vehicle`,
	}
	if !slices.Equal(pages, want) {
		t.Errorf("candidate pages\n%q\nwant\n%q", pages, want)
	}
	pages = stockPages(t, tr, `{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"engine"},"stock":["shelf"],"limit":1`)
	if want := []string{`candidates ["EN-57 "], by vehicle 0: `}; !slices.Equal(pages, want) {
		t.Errorf("engine pages %q, want %q", pages, want)
	}
}

// failing is a stock source that is down, holding holds, failing every
// lookup with err.
type failing struct {
	name  string
	holds stock.Holds
	err   error
}

func (s failing) Info() stock.Info {
	return stock.Info{Name: s.name, Holds: s.holds, Kind: stock.KindLive}
}
func (s failing) ShelfByFitment(context.Context, [][]vocab.FitmentKey, stock.Page) ([]stock.Result, stock.Meta, error) {
	return nil, stock.Meta{}, s.err
}
func (s failing) ShelfByVehicle(context.Context, []stock.ShelfVehicle, stock.Page) ([]stock.Result, stock.Meta, error) {
	return nil, stock.Meta{}, s.err
}
func (s failing) Yard(context.Context, []stock.YardQuery, stock.Page) ([]stock.Result, stock.Meta, error) {
	return nil, stock.Meta{}, s.err
}

// TestFitStockUnavailable: a source that fails answers unavailable, with
// its *stock.Unavailable reason or, for any other error, a fixed reason
// that never shows the error's text, and no rows, in every list a fit
// gives: candidates', shelf_by_vehicle, related counts and a fitment's.
// The rest of the answer stands.
func TestFitStockUnavailable(t *testing.T) {
	tr := stockTranslator(t)
	tr.core.Stock = append(tr.core.Stock,
		failing{"live-shelf", stock.Shelf, &stock.Unavailable{Reason: "the database is not answering"}},
		failing{"live-yard", stock.Yard, errors.New("dial tcp 10.0.0.5:5432: user=stock password=secret")})
	const liveShelf = "live-shelf live unavailable (the database is not answering)"
	const liveYard = "live-yard live unavailable (the source failed)"

	f, err := fit(t, tr, `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-2001"}},"stock":["shelf","yard"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stockText(f.Yard), "3: Y-1/vin Y-2/vin Y-3/catalog-vehicle"; got != want {
		t.Errorf("yard %s, want %s", got, want)
	}
	if got, want := sourcesText(f.Yard), "yard-stock file available, "+liveYard; got != want {
		t.Errorf("yard sources %s, want %s", got, want)
	}
	if got := sourcesText(f.Shelf); !strings.HasSuffix(got, ", "+liveShelf) || f.Shelf.Total != 4 || f.Status != contract.FitFound || len(f.Vehicles) != 2 {
		t.Errorf("shelf %s (%s), status %s, %d vehicles", stockText(f.Shelf), got, f.Status, len(f.Vehicles))
	}

	f, err = fit(t, tr, `{"from":{"vocabulary":"catalog-models","entry":{"make":"Dodge","model":"Ram 1500"}},"model_year":2010,"part":{"spine":"part-types","id":"mirror"},"stock":["shelf","yard"]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Candidates {
		if !strings.HasSuffix(sourcesText(c.Shelf), liveShelf) || !strings.HasSuffix(sourcesText(c.Yard), liveYard) {
			t.Errorf("%s %s: shelf %s, yard %s", c.Fitment.Number, c.Fitment.Side, sourcesText(c.Shelf), sourcesText(c.Yard))
		}
	}
	if got := sourcesText(f.ShelfByVehicle); !strings.HasSuffix(got, liveShelf) || f.ShelfByVehicle.Total != 2 {
		t.Errorf("shelf_by_vehicle %s (%s)", stockText(f.ShelfByVehicle), got)
	}

	f, err = fit(t, tr, `{"from":{"model":1850},"model_year":2010,"related":true,"stock":["yard"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.Related[0].Yard; r.Total != 2 || !strings.HasSuffix(sourcesText(r), liveYard) {
		t.Errorf("related yard %s (%s)", stockText(r), sourcesText(r))
	}
}

// TestInfoStock: Info lists each stock source with what it holds and its
// rows, and counts its orphans, with examples, as drift.
func TestInfoStock(t *testing.T) {
	info := stockTranslator(t).Info()
	conforms(t, "Info", info)
	var got []string
	for _, v := range info.Vocabularies {
		if v.Kind != string(vocab.KindStock) {
			continue
		}
		s := fmt.Sprintf("%s %s %s %q %d rows", v.Name, v.Source, v.Catalog, v.Release, v.Rows)
		for _, d := range v.Drift {
			s += fmt.Sprintf(", %d %s at line %d", d.Count, d.Kind, d.Examples[0].Line)
		}
		got = append(got, s)
	}
	want := []string{
		`shelf-annex shelf parts-catalog "" 2 rows`,
		`shelf-stock shelf parts-catalog "7.01" 5 rows`,
		`yard-stock yard  "" 4 rows`,
		`shelf-orphans shelf parts-catalog "7.02" 2 rows, 1 orphan at line 2`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("stock in Info\n%q\nwant\n%q", got, want)
	}
}

// blocking is a stock source that never answers by itself: it waits for
// its lookup's ctx, or, when deaf, for nothing it is given.
type blocking struct {
	name  string
	holds stock.Holds
	deaf  chan struct{} // nil: the source honours ctx
}

func (s blocking) Info() stock.Info {
	return stock.Info{Name: s.name, Holds: s.holds, Kind: stock.KindLive}
}
func (s blocking) wait(ctx context.Context) ([]stock.Result, stock.Meta, error) {
	if s.deaf != nil {
		<-s.deaf
		return nil, stock.Meta{}, errors.New("released")
	}
	<-ctx.Done()
	return nil, stock.Meta{}, ctx.Err()
}
func (s blocking) ShelfByFitment(ctx context.Context, _ [][]vocab.FitmentKey, _ stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.wait(ctx)
}
func (s blocking) ShelfByVehicle(ctx context.Context, _ []stock.ShelfVehicle, _ stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.wait(ctx)
}
func (s blocking) Yard(ctx context.Context, _ []stock.YardQuery, _ stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.wait(ctx)
}

// TestFitStockCancelled: a stock lookup still running when the fit's
// context ends answers its source unavailable, with the fixed reason
// contract.StockCancelled, whether the source stops with ctx or ignores
// it; the fit returns, and the file sources, which answer from memory,
// still give their rows. A context already ended asks no live source.
func TestFitStockCancelled(t *testing.T) {
	tr := stockTranslator(t)
	deaf := make(chan struct{})
	defer close(deaf)
	tr.core.Stock = append(tr.core.Stock, blocking{"slow-shelf", stock.Shelf, nil}, blocking{"deaf-yard", stock.Yard, deaf})
	req := contract.FitRequest{From: contract.FitFrom{Fitment: &contract.FitmentKey{Type: "brake-rotor-front", Number: "BR-2001"}}, Stock: []string{"shelf", "yard"}, Now: fitNow}
	const cancelled = "unavailable (" + contract.StockCancelled + ")"

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	f, err := tr.Fit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the fit took %s after its deadline", d)
	}
	if got := sourcesText(f.Shelf); !strings.HasSuffix(got, "slow-shelf live "+cancelled) || f.Shelf.Total != 4 {
		t.Errorf("shelf %s (%s)", stockText(f.Shelf), got)
	}
	if got := sourcesText(f.Yard); !strings.HasSuffix(got, "deaf-yard live "+cancelled) || f.Yard.Total != 3 {
		t.Errorf("yard %s (%s)", stockText(f.Yard), got)
	}

	done, stop := context.WithCancel(context.Background())
	stop()
	fits, err := tr.FitBatch(done, []contract.FitRequest{req})
	if err != nil {
		t.Fatal(err)
	}
	if got := sourcesText(fits[0].Yard); got != "yard-stock file available, deaf-yard live "+cancelled || fits[0].Yard.Total != 3 || fits[0].Status != contract.FitFound {
		t.Errorf("an ended context: yard %s, status %s", got, fits[0].Status)
	}
}

// rendezvous is a live stock source whose lookups answer empty only once
// every source sharing its group has started one: the lookups of one fit
// must run at once for any of them to answer.
type rendezvous struct {
	name  string
	holds stock.Holds
	group *sync.WaitGroup
}

func (s rendezvous) Info() stock.Info {
	return stock.Info{Name: s.name, Holds: s.holds, Kind: stock.KindLive}
}
func (s rendezvous) meet(ctx context.Context, n int) ([]stock.Result, stock.Meta, error) {
	s.group.Done()
	met := make(chan struct{})
	go func() { s.group.Wait(); close(met) }()
	select {
	case <-met:
		out := make([]stock.Result, n)
		for i := range out {
			out[i].Rows = []stock.Row{}
		}
		return out, stock.Meta{AsOf: "2026-11-12"}, nil
	case <-ctx.Done():
		return nil, stock.Meta{}, ctx.Err()
	}
}
func (s rendezvous) ShelfByFitment(ctx context.Context, lists [][]vocab.FitmentKey, _ stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.meet(ctx, len(lists))
}
func (s rendezvous) ShelfByVehicle(ctx context.Context, lists []stock.ShelfVehicle, _ stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.meet(ctx, len(lists))
}
func (s rendezvous) Yard(ctx context.Context, lists []stock.YardQuery, _ stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.meet(ctx, len(lists))
}

// TestFitStockConcurrent: one answer's lookups of different list kinds run
// at once: a candidate's shelf, its yard and shelf_by_vehicle each answer
// only once all three have started, and do within the deadline.
func TestFitStockConcurrent(t *testing.T) {
	tr := stockTranslator(t)
	var group sync.WaitGroup
	group.Add(3) // the candidates' shelf, their yard, and shelf_by_vehicle
	tr.core.Stock = append(tr.core.Stock, rendezvous{"live-shelf", stock.Shelf, &group}, rendezvous{"live-yard", stock.Yard, &group})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	f, err := tr.Fit(ctx, contract.FitRequest{
		From: contract.FitFrom{Vocabulary: "catalog-models", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}, ModelYear: contract.Value[int32](2010),
		Part: &contract.FitPart{Spine: "part-types", ID: "brake-rotor-front"}, Stock: []string{"shelf", "yard"}, Now: fitNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := f.Candidates[0]
	for _, l := range []*contract.StockList{c.Shelf, c.Yard, f.ShelfByVehicle} {
		if got := sourcesText(l); !strings.HasSuffix(got, " live available") {
			t.Errorf("sources %s: the lookups did not run at once", got)
		}
	}
}

// panicking is a stock source with a fault: every lookup panics, once
// release (when not nil) is closed, closing panicked as it does.
type panicking struct {
	name     string
	holds    stock.Holds
	kind     stock.Kind
	release  chan struct{}
	panicked chan struct{}
}

func (s panicking) Info() stock.Info {
	return stock.Info{Name: s.name, Holds: s.holds, Kind: s.kind}
}
func (s panicking) fail() ([]stock.Result, stock.Meta, error) {
	if s.release != nil {
		<-s.release
		defer close(s.panicked)
	}
	panic("a fault in " + s.name)
}
func (s panicking) ShelfByFitment(context.Context, [][]vocab.FitmentKey, stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.fail()
}
func (s panicking) ShelfByVehicle(context.Context, []stock.ShelfVehicle, stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.fail()
}
func (s panicking) Yard(context.Context, []stock.YardQuery, stock.Page) ([]stock.Result, stock.Meta, error) {
	return s.fail()
}

// TestFitStockPanic: a panic in a stock lookup, on a goroutine of its own,
// reaches Fit's caller, who can recover it, as a *contract.LookupPanic
// with the original value and the stack it happened on, whether the
// source is a file, looked up on concurrently's goroutine, or live, on
// the lookup's own. A live lookup that panics after the fit stopped
// waiting for it is dropped, and the process lives on.
func TestFitStockPanic(t *testing.T) {
	req := contract.FitRequest{
		From: contract.FitFrom{Vocabulary: "catalog-models", Entry: map[string]string{"make": "Dodge", "model": "Ram 1500"}}, ModelYear: contract.Value[int32](2010),
		Part: &contract.FitPart{Spine: "part-types", ID: "brake-rotor-front"}, Stock: []string{"shelf", "yard"}, Now: fitNow,
	}
	for _, kind := range []stock.Kind{stock.KindFile, stock.KindLive} {
		t.Run(string(kind), func(t *testing.T) {
			tr := stockTranslator(t)
			tr.core.Stock = append(tr.core.Stock, panicking{name: "faulty-yard", holds: stock.Yard, kind: kind})
			var v any
			func() {
				defer func() { v = recover() }()
				tr.Fit(context.Background(), req)
			}()
			p, ok := v.(*contract.LookupPanic)
			if !ok || p.Value != "a fault in faulty-yard" || fmt.Sprint(p) != "a fault in faulty-yard" || !strings.Contains(string(p.Stack), "panicking.fail") {
				t.Fatalf("recovered %#v; want a *contract.LookupPanic of the source's panic, with its stack", v)
			}
		})
	}

	tr := stockTranslator(t)
	late := panicking{name: "late-yard", holds: stock.Yard, kind: stock.KindLive, release: make(chan struct{}), panicked: make(chan struct{})}
	tr.core.Stock = append(tr.core.Stock, late)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	f, err := tr.Fit(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if got := sourcesText(f.Candidates[0].Yard); !strings.HasSuffix(got, "late-yard live unavailable ("+contract.StockCancelled+")") {
		t.Errorf("the late yard's block: %s", got)
	}
	close(late.release)
	<-late.panicked
	time.Sleep(100 * time.Millisecond) // a panic unrecovered would end the test binary here
}
