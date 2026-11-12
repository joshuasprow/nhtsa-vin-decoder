package stock

import (
	"context"
	"slices"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Decode is how a file source decodes a yard row's VIN: its vPIC model and
// model year (nil when none decodes), ok false when it decodes to no
// model.
type Decode func(vin string) (model int32, year *int, ok bool)

// FileSource is a stock file (a vocab.File of kind stock, linked) as a
// Source: its rows indexed, and its yard rows' VINs decoded, once when it
// is made, for the life of the process (ADR 0019). It never fails.
type FileSource struct {
	f *vocab.File

	byKey     map[vocab.FitmentKey][]int // shelf rows by their number
	byPart    map[string][]int           // shelf rows with no number by part type
	byModel   map[int32][]int            // yard rows by vPIC model
	byVehicle map[string][]int           // yard rows by catalog vehicle (vocab.VehicleKey)
	// years are the model years of the yard rows byModel holds, nil for a
	// row whose decode gives none.
	years map[int]*int
}

var _ Source = (*FileSource)(nil)

// NewFile is f, a linked stock file, as a Source. decode decodes its yard
// rows' VINs (nil: none can be). A yard row takes the first of its routes
// that gives a vPIC model and year: its precomputed decode, then its VIN.
// A VIN that decodes to no model, or to a model but no year when the row
// names its catalog vehicle and year, falls to that vehicle; a VIN that
// gives a model and no year, on a row naming no vehicle, matches the model
// in every year.
func NewFile(f *vocab.File, decode Decode) *FileSource {
	s := &FileSource{f: f, byKey: map[vocab.FitmentKey][]int{}, byPart: map[string][]int{}, byModel: map[int32][]int{}, byVehicle: map[string][]int{}, years: map[int]*int{}}
	for i := range f.Stock {
		r := &f.Stock[i]
		switch {
		case r.Key != nil:
			s.byKey[*r.Key] = append(s.byKey[*r.Key], i)
			continue
		case r.Part != nil:
			for _, t := range r.PartTypes {
				s.byPart[t] = append(s.byPart[t], i)
			}
			continue
		case r.Decoded != nil:
			s.byModel[r.Decoded.Model] = append(s.byModel[r.Decoded.Model], i)
			s.years[i] = new(r.Decoded.Year)
			continue
		case r.VIN != "" && decode != nil:
			if m, y, ok := decode(r.VIN); ok && (y != nil || r.Vehicle == nil) {
				s.byModel[m] = append(s.byModel[m], i)
				s.years[i] = y
				continue
			}
		}
		if r.Vehicle != nil {
			k := vocab.VehicleKey(r.Vehicle)
			s.byVehicle[k] = append(s.byVehicle[k], i)
		}
	}
	return s
}

// Info describes the file's source.
func (s *FileSource) Info() Info {
	return Info{Name: s.f.Name, Holds: s.f.Holds, Kind: KindFile, Catalog: s.f.StockCatalog}
}

// meta is the file's: its header's release and as_of.
func (s *FileSource) meta() Meta { return Meta{Release: s.f.Release, AsOf: s.f.AsOf} }

// ShelfByFitment answers the shelf rows of each list's keys.
func (s *FileSource) ShelfByFitment(_ context.Context, lists [][]vocab.FitmentKey, p Page) ([]Result, Meta, error) {
	return answer(s, lists, p, Shelf, func(keys []vocab.FitmentKey, add func(int, string)) {
		for _, k := range keys {
			for _, i := range s.byKey[k] {
				add(i, "")
			}
		}
	}), s.meta(), nil
}

// ShelfByVehicle answers the shelf rows with no number of each list's part
// types, vehicles and year.
func (s *FileSource) ShelfByVehicle(_ context.Context, lists []ShelfVehicle, p Page) ([]Result, Meta, error) {
	return answer(s, lists, p, Shelf, func(q ShelfVehicle, add func(int, string)) {
		keys := make([]string, len(q.Vehicles))
		for j := range q.Vehicles {
			keys[j] = vocab.VehicleKey(&q.Vehicles[j])
		}
		for _, t := range q.PartTypes {
			for _, i := range s.byPart[t] {
				r := &s.f.Stock[i]
				if slices.Contains(keys, vocab.VehicleKey(r.Vehicle)) && (q.Year == nil || *r.Year == *q.Year) {
					add(i, t)
				}
			}
		}
	}), s.meta(), nil
}

// Yard answers the yard rows of each list's vPIC models and catalog
// vehicles, in their years; a row whose decode gives no year is in every
// year of its model.
func (s *FileSource) Yard(_ context.Context, lists []YardQuery, p Page) ([]Result, Meta, error) {
	return answer(s, lists, p, Yard, func(q YardQuery, add func(int, string)) {
		for _, m := range q.Models {
			for _, i := range s.byModel[m.Model] {
				if y := s.years[i]; y == nil || within(*y, m.First, m.Last) {
					add(i, "")
				}
			}
		}
		for _, v := range q.Vehicles {
			for _, i := range s.byVehicle[vocab.VehicleKey(&v.Vehicle)] {
				if within(*s.f.Stock[i].Year, v.First, v.Last) {
					add(i, "")
				}
			}
		}
	}), s.meta(), nil
}

func within(y int, first, last *int) bool {
	return (first == nil || y >= *first) && (last == nil || y <= *last)
}

// answer answers each list of a lookup of what holds's rows, which find
// adds (with the part type it matched, for a shelf row with no number):
// each row once, in file order, at p's locations, p's page of them. A
// source holding the other kind answers every list empty.
func answer[Q any](s *FileSource, lists []Q, p Page, holds Holds, find func(Q, func(int, string))) []Result {
	out := make([]Result, len(lists))
	for li := range out {
		out[li].Rows = []Row{}
	}
	if s.f.Holds != holds {
		return out
	}
	for li, q := range lists {
		types := map[int]string{}
		var idx []int
		find(q, func(i int, t string) {
			if _, seen := types[i]; !seen {
				types[i] = t
				idx = append(idx, i)
			}
		})
		slices.Sort(idx)
		if len(p.Location) > 0 {
			idx = slices.DeleteFunc(idx, func(i int) bool { return !slices.Contains(p.Location, s.f.Stock[i].Location) })
		}
		out[li].Total = len(idx)
		lo := min(p.Offset, len(idx))
		hi := min(lo+max(p.Limit, 0), len(idx))
		for _, i := range idx[lo:hi] {
			out[li].Rows = append(out[li].Rows, s.row(i, types[i]))
		}
	}
	return out
}

// row is row i as a lookup returns it; partType is the part type it
// matched, for a shelf row with no number.
func (s *FileSource) row(i int, partType string) Row {
	r := &s.f.Stock[i]
	out := Row{ID: r.ID, Location: r.Location, Fitment: r.Key, PartType: partType, Vehicle: r.Vehicle, Year: r.Year, VIN: r.VIN, Decoded: r.Decoded, Fields: r.Fields}
	_, byModel := s.years[i]
	switch {
	case s.f.Holds == Shelf && r.Key != nil:
		out.Matched = MatchedFitment
	case s.f.Holds == Shelf:
		out.Matched = MatchedVehicle
	case byModel:
		out.Matched = MatchedVIN
	default:
		out.Matched = MatchedCatalogVehicle
	}
	return out
}
