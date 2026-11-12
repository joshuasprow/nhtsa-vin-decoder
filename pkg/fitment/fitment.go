// Package fitment is how every consumer reads a loaded catalog (ADR 0016,
// ADR 0017): one interface, Reader, whatever form the catalog was loaded
// in. Requests (fit, ADR 0018) are written against Reader alone, so a
// compiled catalog (ADR 0021), or a connector, can stand behind them
// without a request changing.
//
// The JSON Lines form is package vocab's: `--vocab` loads a catalog file
// with the other files, vocab.Link checks it against them, and its
// *vocab.Catalog (vocab.Set's Catalog) is a Reader. The catalog's lines
// are vocab's types, as a vocabulary's are.
//
// vocab.Set.Catalog returns the concrete *vocab.Catalog, not a Reader:
// this package imports vocab for those types, so vocab cannot name
// Reader. That type stays. The compiled form (ADR 0021), which `--vocab`
// recognises by its magic, becomes a second backing of *vocab.Catalog
// inside package vocab, so Set.Catalog's type never changes; a reader
// that is not loaded through `--vocab`, such as a connector, implements
// Reader on its own.
//
// A Reader answers in the catalog's own terms: its part types (entries of
// its declared spine) and its vehicles (entries of its vehicle vocabulary,
// or vPIC makes and models and extensions when its vehicles are vpic).
// Reaching those from what a request gives is a translation, not a
// lookup, and it stays outside the reader:
//
//	set := translator's vocab.Set
//	cat := set.Catalog()                              // a Reader
//	tr, _ := set.Translate(vocab.Query{From: …, To: []string{cat.Info().Vehicles}})
//	vehicles := fitment.Vehicles(cat.Info(), tr)      // the seam
//	candidates := cat.ByVehicle(partType, vehicles, year, nil)
package fitment

import (
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Reader reads one loaded catalog. Implementations never change once
// loaded and are safe for concurrent use.
type Reader interface {
	// ByVehicle returns the candidates for part type partType: each
	// fitment with an application to one of vehicles (the catalog's own;
	// see Vehicles) whose years hold year, nil for any year, and whose
	// side is *side, nil for any side, with those applications. They come
	// ordered by side, then number.
	ByVehicle(partType string, vehicles []vocab.Vehicle, year *int, side *vocab.Side) []vocab.Candidate
	// ByNumber returns the fitment with that part type, number and side,
	// directly, or through the retired number it replaced, which retired
	// then is. fitment is nil when there is neither.
	ByNumber(partType, number string, side vocab.Side) (fitment *vocab.Fitment, retired *vocab.Retired)
	// Redirect returns the redirects from part type partType for one of
	// vehicles (as ByVehicle takes them) whose years hold year (nil: any),
	// in file order, each once: each names the part type the catalog lists
	// partType under for its vehicle. A request follows one hop at most
	// (ADR 0016).
	Redirect(partType string, vehicles []vocab.Vehicle, year *int) []*vocab.Redirect
	// Fitments pages through the catalog's fitments in key order (part
	// type, side, number): those of part type partType, "" for every type,
	// whose keys come after *after, nil from the first, at most limit of
	// them, limit < 1 for every one. The last one's key is the next page's
	// after. It serves `list` of a catalog (ADR 0018) and any pass over
	// every application, such as a compiled catalog's drift check
	// (ADR 0021).
	Fitments(partType string, after *vocab.FitmentKey, limit int) []*vocab.Fitment
	// Info is the catalog's header and counts.
	Info() vocab.CatalogInfo
}

// The JSON Lines form.
var _ Reader = (*vocab.Catalog)(nil)

// Vehicles is the seam between a request and a Reader: the catalog's own
// vehicles a translation reached, which ByVehicle and Redirect take.
//
// t is a translation to the catalog's vehicle vocabulary (To naming
// info.Vehicles). Its vehicles are that vocabulary's entries the
// translation reached, or the from entry itself when it is one of that
// vocabulary's. When the catalog's vehicles are vpic, they are the spine
// entries the translation reached: vPIC makes and models, and extensions.
// Qualifiers and years on the route are the translation's to check; the
// reader checks only the catalog's own years.
func Vehicles(info vocab.CatalogInfo, t *vocab.Translation) []vocab.Vehicle {
	var out []vocab.Vehicle
	if info.Vehicles == vocab.Reserved {
		for _, r := range t.Spine {
			if r.Ref.ID == "" { // a declared spine's entry is no vehicle
				out = append(out, vocab.Vehicle{Target: r.Ref.Target})
			}
		}
		return out
	}
	if t.Vocabulary != nil && t.Vocabulary.Name == info.Vehicles && t.Entry != nil {
		return []vocab.Vehicle{{Entry: t.Vocabulary.FieldsOf(t.Entry)}}
	}
	for _, to := range t.To {
		if to.Vocabulary.Name != info.Vehicles {
			continue
		}
		for _, m := range to.Matches {
			out = append(out, vocab.Vehicle{Entry: to.Vocabulary.FieldsOf(m.Entry)})
		}
	}
	return out
}
