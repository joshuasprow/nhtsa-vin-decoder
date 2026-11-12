// Package stock is how a fit reads stock (ADR 0019): one interface,
// Source, whatever stands behind it. A stock file loaded with --vocab is a
// Source (FileSource, over package vocab's stock file); a request-time
// connector (ADR 0020, package pg below this one) implements the same
// interface as a Live source, so a fit never changes with where its stock
// lives.
//
// A source is named, in the one namespace of loaded files, and holds one
// kind of stock: shelf parts, which name a catalog's fitments, or yard
// vehicles, which name vPIC models and the catalog's vehicles. It answers
// three lookups, each the rows of several lists at once, so a connector
// answers every list of one kind in one answer with one query:
//
//	shelf, by fitment keys            ShelfByFitment
//	shelf, by part type and vehicle   ShelfByVehicle
//	yard, by vPIC model or catalog vehicle, and years   Yard
//
// A source answers only its own kind's lookups; the others' results are
// empty. Availability is the source's rule: it supplies only rows it would
// offer, and nitrovin passes their fields through uninterpreted.
package stock

import (
	"context"
	"encoding/json"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Holds is what a source holds: shelf parts or yard vehicles. A stock
// file's header names it as its source.
type Holds = vocab.StockHolds

// What sources hold.
const (
	Shelf = vocab.StockShelf
	Yard  = vocab.StockYard
)

// Kind is where a source's rows come from, as an answer's source block
// says: a file loaded once, or a live database read at request time.
type Kind string

// The kinds of source.
const (
	KindFile Kind = "file"
	KindLive Kind = "live"
)

// Info is what a source is, fixed for its life: never read from its
// rows, so it does no I/O.
type Info struct {
	Name  string
	Holds Holds
	Kind  Kind
	// Catalog is a shelf's: the catalog its numbers belong to.
	Catalog string
	// Connector and Relation are a live source's: how it is read
	// (vocab.ConnectorPostgres) and the relation its rows are in. Never a
	// connection detail.
	Connector, Relation string
}

// Meta is what one lookup read beside its rows: the catalog release a
// shelf's numbers are under, and the time every list's rows are as of
// (a file's own), each "" when the source has none. A live source reads
// both with its rows, so they describe exactly the rows returned. A
// source that keeps a time per row (Row.AsOf) leaves AsOf "": each list's
// block then says the oldest of its own rows' times.
type Meta struct {
	Release, AsOf string
}

// Page is the part of each list a lookup returns: rows at Location (any
// one of them; none for every location), after the first Offset, at most
// Limit (0: none, only the count).
type Page struct {
	Location      []string
	Offset, Limit int
}

// Result is one list's page: its rows, and how many the whole list holds.
type Result struct {
	Rows  []Row
	Total int
}

// Matched is how a row matched what a list asked for.
type Matched string

// The ways a row matches.
const (
	// MatchedFitment: a shelf row's number is the fitment's, or a retired
	// number it replaced.
	MatchedFitment Matched = "fitment"
	// MatchedVehicle: a shelf row with no number, by its part type and its
	// exact vehicle and year.
	MatchedVehicle Matched = "vehicle"
	// MatchedVIN: a yard row by its vPIC model and year, decoded by the
	// source or from its VIN.
	MatchedVIN Matched = "vin"
	// MatchedCatalogVehicle: a yard row by the catalog vehicle and year it
	// names.
	MatchedCatalogVehicle Matched = "catalog-vehicle"
)

// Row is one row of a source as a lookup returns it.
type Row struct {
	ID, Location string
	Matched      Matched
	// Fitment is a shelf row's key as the row names it: the fitment's own
	// number, or a retired number that joins it.
	Fitment *vocab.FitmentKey
	// PartType is a shelf row's part type when it has no number: the one
	// the lookup asked for.
	PartType string
	// Vehicle and Year are the catalog vehicle and year a row names, when
	// it names one.
	Vehicle *vocab.Vehicle
	Year    *int
	// VIN and Decoded are a yard row's, as the source has them.
	VIN     string
	Decoded *vocab.StockDecoded
	// AsOf is the row's own time, when the source keeps one per row
	// (ADR 0020's as_of column); "" otherwise.
	AsOf string
	// Fields are the row's own fields, verbatim: a JSON object.
	Fields json.RawMessage
}

// ShelfVehicle asks for the shelf rows with no number of one of PartTypes,
// for one of Vehicles (the catalog's own) in Year (nil: any year).
type ShelfVehicle struct {
	PartTypes []string
	Vehicles  []vocab.Vehicle
	Year      *int
}

// ModelYears is a vPIC model in model years First to Last (nil:
// unbounded).
type ModelYears struct {
	Model       int32
	First, Last *int
}

// VehicleYears is one of the catalog's vehicles in model years First to
// Last (nil: unbounded).
type VehicleYears struct {
	Vehicle     vocab.Vehicle
	First, Last *int
}

// YardQuery asks for the yard rows matched by their vPIC model (a
// precomputed decode, else a decoded VIN) to one of Models, or, for a row
// with neither, by their catalog vehicle to one of Vehicles, each in its
// years. A row's own route decides which half can match it, so the halves
// never share a row. Qualifiers are not checked: a yard row carries none.
type YardQuery struct {
	Models   []ModelYears
	Vehicles []VehicleYears
}

// Unavailable is the error a Source returns when it cannot answer. Reason
// is served to every caller in the source's block, so it names no
// connection detail: no DSN, host, user or database. Any other error is
// served as a fixed reason, never its text.
type Unavailable struct {
	Reason string
}

func (u *Unavailable) Error() string { return "stock source unavailable: " + u.Reason }

// Health is what a live source's latest connection found.
type Health struct {
	// Checked is false until the source first connects: it connects on
	// its first lookup, never at load.
	Checked bool
	// Down says why the source is unavailable; nil when it answered.
	Down *Unavailable
}

// Live is a Source read at request time, whose availability changes while
// nitrovin runs. Health reads what it last found, with no I/O. Close
// releases what it holds open (connections, background work); a lookup
// after it is unavailable, and calling it again does nothing.
type Live interface {
	Source
	Health() Health
	Close()
}

// Declared is what a connector's stock file declares (ADR 0020), as
// Connect receives it: File, linked, with its Connector; Catalog, the
// catalog its rows' vehicles are the vehicles of (a shelf's own, a yard's
// the one loaded; nil when none is); and Vehicles, that catalog's vehicle
// vocabulary (nil when its vehicles are vpic, or none is loaded).
type Declared struct {
	File, Catalog, Vehicles *vocab.File
}

// Connect opens the live source d declares. It does no I/O: a source
// connects on its first lookup, and one that cannot is unavailable, never
// a fault.
type Connect func(d Declared) Live

// Down is a live source that is always unavailable for reason: a
// connector's file loaded where no Connect is, such as the C library.
func Down(info Info, reason string) Live { return down{info, &Unavailable{Reason: reason}} }

type down struct {
	info Info
	err  *Unavailable
}

func (d down) Info() Info     { return d.info }
func (d down) Health() Health { return Health{Checked: true, Down: d.err} }
func (d down) Close()         {}

func (d down) ShelfByFitment(context.Context, [][]vocab.FitmentKey, Page) ([]Result, Meta, error) {
	return nil, Meta{}, d.err
}

func (d down) ShelfByVehicle(context.Context, []ShelfVehicle, Page) ([]Result, Meta, error) {
	return nil, Meta{}, d.err
}

func (d down) Yard(context.Context, []YardQuery, Page) ([]Result, Meta, error) {
	return nil, Meta{}, d.err
}

// Source is one stock source. Each lookup answers several lists at once,
// one Result per list in order, each list's rows once each in the
// source's order, paged by p, with the Meta it read. Its error makes every
// list of the call unavailable; return *Unavailable to say why. ctx bounds
// the lookup: a source must return promptly when ctx ends, and a fit
// that stops waiting answers it unavailable. One fit runs its lookups of
// different list kinds concurrently, so a Source is safe for concurrent
// use; it must not keep or change the slices it is given.
type Source interface {
	// Info describes the source.
	Info() Info
	// ShelfByFitment answers, per list, the shelf rows whose fitment key
	// is one of the list's keys: a fitment's, and the retired numbers it
	// replaced.
	ShelfByFitment(ctx context.Context, lists [][]vocab.FitmentKey, p Page) ([]Result, Meta, error)
	// ShelfByVehicle answers, per list, the shelf rows with no number for
	// its part types, vehicles and year.
	ShelfByVehicle(ctx context.Context, lists []ShelfVehicle, p Page) ([]Result, Meta, error)
	// Yard answers, per list, the yard rows its query matches.
	Yard(ctx context.Context, lists []YardQuery, p Page) ([]Result, Meta, error)
}
