package ui

import (
	"net/url"
	"slices"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// fromKind is which from a lookup carries, the one answer to it in the
// package: the page's tab, the map's From card and heading, and the
// spine a lookup's targets are on each read it. The page carries it on
// its answer (data-from), so worker.js reads it rather than deriving it.
type fromKind string

const (
	fromNone  fromKind = ""
	fromVIN   fromKind = "vin"
	fromEntry fromKind = "entry"
	fromMake  fromKind = "make"
	fromModel fromKind = "model"
	fromExt   fromKind = "ext"
	fromSpine fromKind = "spine"
)

// queryFrom is the from query q carries, by the parameters it holds,
// empty or not: from.vin, from.vocabulary (an entry), from.spine,
// from.make, from.model or from.ext, in that order.
func queryFrom(q url.Values) fromKind {
	switch {
	case q.Has("from.vin"):
		return fromVIN
	case q.Has("from.vocabulary"):
		return fromEntry
	case q.Has("from.spine"):
		return fromSpine
	case q.Has("from.make"):
		return fromMake
	case q.Has("from.model"):
		return fromModel
	case q.Has("from.ext"):
		return fromExt
	}
	return fromNone
}

// translatedFrom is the from a translation answers.
func translatedFrom(f contract.TranslationFrom) fromKind {
	switch {
	case f.Vocabulary != "":
		return fromEntry
	case f.VIN != "":
		return fromVIN
	case f.Spine != "":
		return fromSpine
	case f.Make != 0:
		return fromMake
	case f.Model != 0:
		return fromModel
	case f.Ext != "":
		return fromExt
	}
	return fromNone
}

// spineOf is the spine a lookup of kind k in q is on, its targets'
// spine: vPIC's for a VIN, a vPIC id or an extension's, the from's for a
// declared spine's entry, and an entry's vocabulary's; false for none (no
// from, or no vocabulary in cat by the entry's name).
func spineOf(k fromKind, q url.Values, cat []contract.CatalogEntry) (string, bool) {
	switch k {
	case fromNone:
		return "", false
	case fromSpine:
		return q.Get("from.spine"), true
	case fromEntry:
		i := slices.IndexFunc(cat, func(c contract.CatalogEntry) bool {
			return c.Name == q.Get("from.vocabulary") && c.Kind == "vocabulary"
		})
		if i < 0 {
			return "", false
		}
		return cat[i].Spine, true
	}
	return vocab.Reserved, true
}
