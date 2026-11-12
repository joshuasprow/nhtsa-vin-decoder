package contract

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The kinds of stock a fit asks for (ADR 0019).
const (
	StockShelf = string(stock.Shelf)
	StockYard  = string(stock.Yard)
)

// StockFirst is how many rows a candidate's stock lists carry: a fit
// from the fitment's key with the same stock pages the whole list.
const StockFirst = 10

// StockList is one list of stock rows (ADR 0019): the rows of every loaded
// source of its kind, each tagged with its source, how many the whole
// list holds, and every source's block.
type StockList struct {
	Rows    []StockRow    `json:"rows"`
	Total   int           `json:"total"`
	Sources []StockSource `json:"sources"`
}

// StockSource is a stock list's source block: its name, where its rows
// come from, how old they are, and whether it answered. What it holds is
// the list's.
type StockSource struct {
	Name string `json:"name"`
	// Kind is file or live.
	Kind string `json:"kind"`
	// Connector and Relation are a live source's (ADR 0020): how it is
	// read ("postgres") and the relation its rows are read from; absent
	// for a file.
	Connector string `json:"connector,omitempty"`
	Relation  string `json:"relation,omitempty"`
	// AsOf is the time the lookup's rows are as of: a file's own, or what
	// a live source read with them; absent when it has none.
	AsOf string `json:"as_of,omitempty"`
	// Catalog and Release are a shelf's: its catalog, and the release its
	// numbers are under as the lookup read it; CatalogMismatch is true
	// when that release is not the loaded catalog's.
	Catalog         string `json:"catalog,omitempty"`
	Release         string `json:"release,omitempty"`
	CatalogMismatch bool   `json:"catalog_mismatch,omitempty"`
	// Status is available or unavailable; Reason says why a source is
	// unavailable, and its rows are then absent from the list.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// The statuses of a stock source.
const (
	StockAvailable   = "available"
	StockUnavailable = "unavailable"
)

// StockFailed is the reason served for a source that failed with any
// error but *stock.Unavailable, whose text could name a connection detail.
const StockFailed = "the source failed"

// StockCancelled is the reason served for a source whose lookup had not
// answered when the fit's context ended: the caller went away, or its
// deadline passed.
const StockCancelled = "the request ended before the source answered"

// StockRow is one row of a stock list: what nitrovin interprets of it,
// how it matched, and its own fields verbatim.
type StockRow struct {
	// Source is the name of the source it came from.
	Source   string `json:"source"`
	ID       string `json:"id"`
	Location string `json:"location"`
	// Matched is fitment or vehicle for a shelf row, vin or
	// catalog-vehicle for a yard row.
	Matched string `json:"matched"`
	// Fitment is a shelf row's key as the row names it; Reached is retired
	// when that is a retired number the fitment replaced.
	Fitment *FitmentKey `json:"fitment,omitempty"`
	Reached FitReach    `json:"reached,omitempty"`
	// Part is a shelf row's part type when it has no number.
	Part string `json:"part,omitempty"`
	// Vehicle and Year are the catalog vehicle and year a row names.
	Vehicle *CatalogVehicle `json:"vehicle,omitempty"`
	Year    Null[int32]     `json:"year,omitzero"`
	// VIN and Decoded are a yard row's, as its source has them.
	VIN     string        `json:"vin,omitempty"`
	Decoded *StockDecoded `json:"decoded,omitempty"`
	// AsOf is the row's own time, when its source keeps one per row.
	AsOf   string          `json:"as_of,omitempty"`
	Fields json.RawMessage `json:"fields"`
}

// StockDecoded is a yard vehicle's vPIC make, model and year as its source
// decoded them.
type StockDecoded struct {
	Make  int32 `json:"make"`
	Model int32 `json:"model"`
	Year  int   `json:"year"`
}

// stockCall is one lookup of one source, answering n lists with page p.
type stockCall func(ctx context.Context, src stock.Source, p stock.Page) ([]stock.Result, stock.Meta, error)

// stockLists gathers n lists of what holds from every loaded source
// holding it, one lookup per source: of each list, the rows after the
// first offset, at most limit, and its total across every source. One list
// pages exactly across the sources, in load order; with more than one,
// offset is 0. into names the fitment each list is of, nil for none, to
// mark a row under a retired number.
func (f Fitter) stockLists(ctx context.Context, holds stock.Holds, n, offset, limit int, location []string, into []*FitmentKey, call stockCall) []*StockList {
	if n > 1 && offset != 0 {
		panic("stock: several lists page from their first row")
	}
	out := make([]*StockList, n)
	for i := range out {
		out[i] = &StockList{Rows: []StockRow{}, Sources: []StockSource{}}
	}
	if n == 0 {
		return out
	}
	var release string
	if f.Catalog != nil {
		release = f.Catalog.Info().Release
	}
	skip := offset    // a single list's rows of offset left to skip
	var asOf []string // each list's oldest row time from the source at hand
	for _, src := range f.Stock {
		info := src.Info()
		if info.Holds != holds {
			continue
		}
		block := StockSource{Name: info.Name, Kind: string(info.Kind), Connector: info.Connector, Relation: info.Relation, Catalog: info.Catalog, Status: StockAvailable}
		p := stock.Page{Location: location, Limit: limit}
		if n == 1 {
			p.Offset, p.Limit = skip, limit-len(out[0].Rows)
		}
		rs, meta, err := lookup(ctx, src, p, call)
		switch {
		case errors.Is(err, errCancelled):
			block.Status, block.Reason = StockUnavailable, StockCancelled
		case err != nil:
			block.Status, block.Reason = StockUnavailable, StockFailed
			if u, ok := errors.AsType[*stock.Unavailable](err); ok {
				block.Reason = u.Reason
			}
		case len(rs) != n:
			panic(fmt.Sprintf("stock: source %q answered %d lists of %d", info.Name, len(rs), n))
		default:
			block.AsOf, block.Release = meta.AsOf, meta.Release
			block.CatalogMismatch = holds == stock.Shelf && meta.Release != "" && meta.Release != release
			// A source with a time per row gives each list's block the
			// oldest of the rows that list carries from it: none when it
			// carries none.
			asOf = asOf[:0]
			for i, r := range rs {
				out[i].Total += r.Total
				oldest := ""
				for _, row := range r.Rows {
					if len(out[i].Rows) < limit {
						out[i].Rows = append(out[i].Rows, f.stockRow(info.Name, row, into[i]))
						if row.AsOf != "" && (oldest == "" || row.AsOf < oldest) {
							oldest = row.AsOf
						}
					}
				}
				asOf = append(asOf, oldest)
			}
			if n == 1 {
				skip = max(0, skip-rs[0].Total)
			}
		}
		for i := range out {
			b := block
			if err == nil && asOf[i] != "" {
				b.AsOf = asOf[i]
			}
			out[i].Sources = append(out[i].Sources, b)
		}
	}
	return out
}

// errCancelled is lookup's error when ctx ended before the source
// answered.
var errCancelled = errors.New("the fit's context ended")

// lookup is call's answer from src, or errCancelled once ctx is done
// before it answers: a source that ignores ctx cannot hold a fit past its
// caller. The abandoned call finishes on its own. A file source answers
// from memory, so it is always asked, and waited for, ctx or not: one
// slow live source does not empty the files' lists after it.
func lookup(ctx context.Context, src stock.Source, p stock.Page, call stockCall) ([]stock.Result, stock.Meta, error) {
	if src.Info().Kind == stock.KindFile {
		return call(ctx, src, p)
	}
	if ctx.Err() != nil {
		return nil, stock.Meta{}, errCancelled
	}
	type answer struct {
		rs    []stock.Result
		meta  stock.Meta
		err   error
		panic *LookupPanic
	}
	// The goroutine may outlive this call: it gets its own copy of the
	// page, so a caller reusing its location slice cannot race it.
	p.Location = slices.Clone(p.Location)
	done := make(chan answer, 1)
	go func() {
		// A panic is the caller's while it waits; once ctx has ended and
		// it no longer does, it is recovered and dropped with the answer.
		defer func() {
			if v := recover(); v != nil {
				done <- answer{panic: lookupPanic(v)}
			}
		}()
		rs, meta, err := call(ctx, src, p)
		done <- answer{rs, meta, err, nil}
	}()
	select {
	case a := <-done:
		if a.panic != nil {
			panic(a.panic)
		}
		if a.err != nil && ctx.Err() != nil {
			return nil, stock.Meta{}, errCancelled // the source stopped because ctx did
		}
		return a.rs, a.meta, a.err
	case <-ctx.Done():
		return nil, stock.Meta{}, errCancelled
	}
}

// stockRow is a source's row as a list carries it; into is the fitment
// the list is of, nil for none.
func (f Fitter) stockRow(source string, r stock.Row, into *FitmentKey) StockRow {
	out := StockRow{Source: source, ID: r.ID, Location: r.Location, Matched: string(r.Matched), Part: r.PartType, VIN: r.VIN, Fields: r.Fields}
	if r.Fitment != nil {
		k := fitmentKey(*r.Fitment)
		out.Fitment, out.Reached = &k, ReachedDirect
		if into != nil && k != *into {
			out.Reached = ReachedRetired
		}
	}
	if r.Vehicle != nil {
		v := catalogVehicle(r.Vehicle)
		out.Vehicle = &v
	}
	if r.Year != nil {
		out.Year = Value(int32(*r.Year))
	}
	out.AsOf = r.AsOf
	if r.Decoded != nil {
		out.Decoded = &StockDecoded{Make: r.Decoded.Make, Model: r.Decoded.Model, Year: r.Decoded.Year}
	}
	if out.Fields == nil {
		out.Fields = json.RawMessage("{}")
	}
	return out
}

// shelfKeys are the keys whose shelf rows are fitment k's: its own, and
// the retired numbers it replaced.
func (f Fitter) shelfKeys(k vocab.FitmentKey) []vocab.FitmentKey {
	keys := []vocab.FitmentKey{k}
	for _, rt := range f.Catalog.RetiredTo(k) {
		keys = append(keys, rt.Key)
	}
	return keys
}

// yardOf is what a yard row matches to be among applications: each
// application's vehicle in its years, and the vPIC models it maps to in
// the years both hold.
func (f Fitter) yardOf(apps []*vocab.Application) ([]stock.ModelYears, []stock.VehicleYears) {
	var models []stock.ModelYears
	var vehicles []stock.VehicleYears
	for _, a := range apps {
		vehicles = append(vehicles, stock.VehicleYears{Vehicle: *a.Vehicle, First: a.First, Last: a.Last})
		models = append(models, f.vpicModels(a.Vehicle, a.First, a.Last)...)
	}
	return models, vehicles
}

// vpicModels are the vPIC models catalog vehicle v is in years first to
// last: itself, a vPIC model, when the catalog's vehicles are vpic, or the
// models its vehicle vocabulary entry maps to, each in the years the
// mapping and the range share. A make or an extension is no model.
func (f Fitter) vpicModels(v *vocab.Vehicle, first, last *int) []stock.ModelYears {
	if v.Entry == nil {
		if v.Model != 0 {
			return []stock.ModelYears{{Model: v.Model, First: first, Last: last}}
		}
		return nil
	}
	g := f.Set.File(f.Catalog.Info().Vehicles)
	if g == nil {
		return nil
	}
	e, ok := g.Entry(v.Entry)
	if !ok {
		return nil
	}
	var out []stock.ModelYears
	for _, mp := range e.Mappings {
		if mp.Model == 0 || !overlaps(first, last, mp.First, mp.Last) {
			continue
		}
		out = append(out, stock.ModelYears{Model: mp.Model, First: later(first, mp.First), Last: earlier(last, mp.Last)})
	}
	return out
}

func later(a, b *int) *int {
	if a == nil || b != nil && *b > *a {
		return b
	}
	return a
}

func earlier(a, b *int) *int {
	if a == nil || b != nil && *b < *a {
		return b
	}
	return a
}

// shelfLists are the shelf lists of fitments fits, each its own key's and
// its retired numbers' rows.
func (f Fitter) shelfLists(ctx context.Context, fits []*vocab.Fitment, offset, limit int, location []string) []*StockList {
	keys := make([][]vocab.FitmentKey, len(fits))
	into := make([]*FitmentKey, len(fits))
	for i, fit := range fits {
		keys[i] = f.shelfKeys(fit.Key)
		k := fitmentKey(fit.Key)
		into[i] = &k
	}
	return f.stockLists(ctx, stock.Shelf, len(fits), offset, limit, location, into, func(ctx context.Context, src stock.Source, p stock.Page) ([]stock.Result, stock.Meta, error) {
		return src.ShelfByFitment(ctx, keys, p)
	})
}

// yardLists are the yard lists of application sets apps: the yard
// vehicles among each set's vehicles and years.
func (f Fitter) yardLists(ctx context.Context, apps [][]*vocab.Application, offset, limit int, location []string) []*StockList {
	qs := make([]stock.YardQuery, len(apps))
	for i, as := range apps {
		qs[i].Models, qs[i].Vehicles = f.yardOf(as)
	}
	return f.stockLists(ctx, stock.Yard, len(apps), offset, limit, location, make([]*FitmentKey, len(apps)), func(ctx context.Context, src stock.Source, p stock.Page) ([]stock.Result, stock.Meta, error) {
		return src.Yard(ctx, qs, p)
	})
}

// shelfByVehicle is the shelf list of rows with no number of part types
// types for the vehicle v, paged by pg.
func (f Fitter) shelfByVehicle(ctx context.Context, types []partType, v *reached, location []string, pg pager) *StockList {
	q := stock.ShelfVehicle{Vehicles: v.vehicles, Year: v.year}
	for _, t := range types {
		q.PartTypes = append(q.PartTypes, t.id)
	}
	return f.stockLists(ctx, stock.Shelf, 1, pg.after, pg.limit, location, []*FitmentKey{nil}, func(ctx context.Context, src stock.Source, p stock.Page) ([]stock.Result, stock.Meta, error) {
		return src.ShelfByVehicle(ctx, []stock.ShelfVehicle{q}, p)
	})[0]
}

// applicationsOf is every application of fit.
func applicationsOf(fit *vocab.Fitment) []*vocab.Application {
	out := make([]*vocab.Application, len(fit.Applications))
	for i := range fit.Applications {
		out[i] = &fit.Applications[i]
	}
	return out
}

// wants reports whether req asks for stock of kind.
func (r FitRequest) wants(kind string) bool { return slices.Contains(r.Stock, kind) }

// candidateStock gives each candidate the stock lists req asks for, its
// first StockFirst rows of each.
func (f Fitter) candidateStock(ctx context.Context, req FitRequest, cs []FitCandidate) {
	if len(req.Stock) == 0 {
		return
	}
	fits := make([]*vocab.Fitment, len(cs))
	apps := make([][]*vocab.Application, len(cs))
	for i, c := range cs {
		fits[i], _ = f.Catalog.ByNumber(c.Fitment.Type, c.Fitment.Number, sideOf(c.Fitment.Side))
		apps[i] = applicationsOf(fits[i])
	}
	var shelf, yard []*StockList
	concurrently(
		func() {
			if req.wants(StockShelf) {
				shelf = f.shelfLists(ctx, fits, 0, StockFirst, req.Location)
			}
		},
		func() {
			if req.wants(StockYard) {
				yard = f.yardLists(ctx, apps, 0, StockFirst, req.Location)
			}
		})
	for i := range cs {
		if shelf != nil {
			cs[i].Shelf = shelf[i]
		}
		if yard != nil {
			cs[i].Yard = yard[i]
		}
	}
}

// concurrently runs fns at once and returns when every one has: one
// answer's independent stock lookups (its shelf, its yard, its rows by
// vehicle) wait on their slowest source, not on the sum of them.
//
// A panic in one of fns, which on its own goroutine no caller's recover
// could see, is recovered there and, once every one has returned, panics
// again on the calling goroutine as a *LookupPanic: the first one's, by
// fns's order.
func concurrently(fns ...func()) {
	var wg sync.WaitGroup
	panics := make([]*LookupPanic, len(fns))
	for i, fn := range fns {
		wg.Go(func() {
			defer func() {
				if v := recover(); v != nil {
					panics[i] = lookupPanic(v)
				}
			}()
			fn()
		})
	}
	wg.Wait()
	for _, p := range panics {
		if p != nil {
			panic(p)
		}
	}
}

// LookupPanic is a panic in one of a fit's stock lookups, which run on
// goroutines of their own (a fault in a stock.Source, or in nitrovin),
// carried to the goroutine that called Fit and panicked again there, so
// its caller's recover sees it. Value is the original panic's value and
// Stack the stack of the goroutine it happened on, for a log. Its String
// and Error are Value's, so an #internal problem reads as the original
// panic would, and it unwraps to Value when that is an error (a damaged
// compiled catalog's, say), so errors.Is and errors.As see through it.
type LookupPanic struct {
	Value any
	Stack []byte
}

func (p *LookupPanic) String() string { return fmt.Sprint(p.Value) }

func (p *LookupPanic) Error() string { return p.String() }

// Unwrap is Value when it is an error, else nil.
func (p *LookupPanic) Unwrap() error {
	if err, ok := p.Value.(error); ok {
		return err
	}
	return nil
}

// lookupPanic is v, a recovered panic, as a *LookupPanic, with the stack
// of the goroutine recovering it, unless it is one already.
func lookupPanic(v any) *LookupPanic {
	if p, ok := v.(*LookupPanic); ok {
		return p
	}
	return &LookupPanic{Value: v, Stack: debug.Stack()}
}

// fitmentStock gives a fitment's answer the stock lists req asks for,
// paged with its vehicles: apps are the applications it lists, before
// paging.
func (f Fitter) fitmentStock(ctx context.Context, req FitRequest, fit *vocab.Fitment, apps []*vocab.Application, out *Fit, pg pager) {
	concurrently(
		func() {
			if req.wants(StockShelf) {
				out.Shelf = f.shelfLists(ctx, []*vocab.Fitment{fit}, pg.after, pg.limit, req.Location)[0]
			}
		},
		func() {
			if req.wants(StockYard) {
				out.Yard = f.yardLists(ctx, [][]*vocab.Application{apps}, pg.after, pg.limit, req.Location)[0]
			}
		})
	pageStock(out, pg, out.Shelf, out.Yard)
}

// pageStock sets the answer's next while any of lists, paged with its own
// list, has more after pg's page.
func pageStock(out *Fit, pg pager, lists ...*StockList) {
	for _, l := range lists {
		if l != nil && out.Next == "" && pg.after+pg.limit < l.Total {
			out.Next = encodeFitCursor(pg.kind, pg.after+pg.limit)
		}
	}
}

// StockDecode decodes a stock file's yard VINs (stock.NewFile) with t's
// Decode as of now: each VIN's vPIC model and model year.
func (t Translator) StockDecode(now time.Time) stock.Decode {
	return func(vin string) (int32, *int, bool) {
		d, err := t.Decode(vin, Null[int32]{}, now)
		if err != nil {
			return 0, nil, false
		}
		dec := vocab.NewDecoded(t.Set.Month(), vocabItems(d.Items))
		return dec.Model, dec.ModelYear, dec.Model != 0
	}
}
