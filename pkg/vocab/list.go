package vocab

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
)

// The page sizes of a list (ADR 0012).
const (
	DefaultLimit = 100
	MaxLimit     = 1000
)

// ListQuery is one list request (ADR 0012). Without Of it asks for the
// catalog of what is loaded; with Of it pages through vPIC (Reserved) or
// one loaded file.
type ListQuery struct {
	Of string
	// Where filters by fields, matched after Normalize: a vocabulary's
	// fields, or vPIC's "make", a make id, to list that make's models.
	Where map[string]string
	// Limit is the page size, 1 to MaxLimit; 0 means DefaultLimit.
	Limit int
	// After is the Next of the previous page; "" starts at the first.
	After string
}

// CatalogEntry is one thing a translation can name: vPIC or a loaded file.
type CatalogEntry struct {
	Name    string
	Kind    Kind
	Version string
	// Subject is what its entries name: vehicles for vPIC, a vehicle
	// vocabulary and an extension set; parts for a declared spine and a
	// part vocabulary.
	Subject Subject
	// Fields are a vocabulary's.
	Fields []string
	// Spine is the spine a vocabulary or extension set routes through:
	// Reserved, or a declared spine's name. "" for a spine.
	Spine string
}

// Listing is one page of a list.
type Listing struct {
	// Catalog is the catalog, without Of: vPIC, then every file in load
	// order.
	Catalog []CatalogEntry
	// File is the file listed, nil for the catalog and vPIC.
	File *File
	// Entries are a vocabulary's entries, by their normalized fields.
	Entries []*Entry
	// Spine is vPIC's makes, or one make's models, by normalized name then
	// id; or a declared spine's entries or an extension set's extensions,
	// by id.
	Spine []Reached
	// Next is the After of the next page, "" on the last.
	Next string
}

// row is one listed entry and its list key.
type row struct {
	key   []string
	entry *Entry
	spine Reached
}

// vpicRow is a vPIC make or model, keyed by normalized name then id.
func vpicRow(ref Ref, id int32, name string, under *Ref) row {
	return row{key: []string{Normalize(name), fmt.Sprintf("%010d", id)}, spine: Reached{Ref: ref, Name: name, Under: under}}
}

func sortRows(rows []row) {
	slices.SortFunc(rows, func(a, b row) int { return compareKeys(a.key, b.key) })
}

// List answers q (ADR 0012). The order is stable for a given set of loaded
// files, and a page starts after the key its cursor holds, so paging
// loses and repeats nothing. The error is a *RequestError: an Of nothing
// loaded has, a Where field the listing lacks, a Limit out of range, or an
// After this listing never gave.
func (s *Set) List(q ListQuery) (*Listing, error) {
	limit := q.Limit
	switch {
	case limit == 0:
		limit = DefaultLimit
	case limit < 1 || limit > MaxLimit:
		return nil, refuse("/limit", "limit must be 1 to %d", MaxLimit)
	}
	l := &Listing{}
	if q.Of == "" {
		switch {
		case len(q.Where) > 0:
			return nil, refuse("/where", "where needs of: the catalog is not filtered")
		case q.After != "":
			return nil, refuse("/after", "after needs of: the catalog is not paged")
		case q.Limit != 0:
			return nil, refuse("/limit", "limit needs of: the catalog is not paged")
		}
		l.Catalog = s.catalog()
		return l, nil
	}
	rows, keyLen, err := s.rows(q, l)
	if err != nil {
		return nil, err
	}
	start := 0
	if q.After != "" {
		key, ok := decodeCursor(q.After)
		if !ok || len(key) != keyLen {
			return nil, refuse("/after", "%q is not a cursor of this list", q.After)
		}
		start = sort.Search(len(rows), func(i int) bool { return compareKeys(rows[i].key, key) > 0 })
	}
	end := min(start+limit, len(rows))
	for _, r := range rows[start:end] {
		if r.entry != nil {
			l.Entries = append(l.Entries, r.entry)
		} else {
			l.Spine = append(l.Spine, r.spine)
		}
	}
	if end < len(rows) {
		l.Next = encodeCursor(rows[end-1].key)
	}
	return l, nil
}

// catalog is vPIC, then every loaded file in load order.
func (s *Set) catalog() []CatalogEntry {
	out := []CatalogEntry{{Name: Reserved, Kind: KindSpine, Version: s.month.Name(), Subject: SubjectVehicle}}
	for _, f := range s.files {
		c := CatalogEntry{Name: f.Name, Kind: f.Kind, Version: f.Version}
		switch f.Kind {
		case KindVocabulary:
			c.Subject, c.Fields, c.Spine = f.Subject, f.Fields, spineOf(f)
		case KindExtensionSet:
			c.Subject, c.Spine = SubjectVehicle, Reserved
		case KindSpine:
			c.Subject = SubjectPart
		}
		out = append(out, c)
	}
	return out
}

// rows is every entry q's Of and Where select, in list order, and the
// length of their keys. It sets l.File.
func (s *Set) rows(q ListQuery, l *Listing) ([]row, int, error) {
	where := slices.Sorted(maps.Keys(q.Where))
	if q.Of == Reserved {
		for _, k := range where {
			if k != "make" {
				return nil, 0, refuse("/where/"+pointerToken(k), "vPIC's entries take where.make, a vPIC make id, and no other field")
			}
		}
		v, ok := q.Where["make"]
		if !ok {
			return s.makes(), 2, nil
		}
		mk, err := strconv.ParseInt(v, 10, 32)
		if err != nil || mk < 1 {
			return nil, 0, refuse("/where/make", "%q is not a vPIC make id", v)
		}
		under := &Ref{Target: Target{Make: int32(mk)}}
		var rows []row
		for id, name := range s.month.Models(int32(mk)) {
			rows = append(rows, vpicRow(Ref{Target: Target{Model: id}}, id, name, under))
		}
		sortRows(rows)
		return rows, 2, nil
	}
	f := s.byName[q.Of]
	if f == nil {
		return nil, 0, refuse("/of", "nothing named %q is loaded: list without of names what is", q.Of)
	}
	l.File = f
	if f.Kind != KindVocabulary && len(where) > 0 {
		return nil, 0, refuse("/where/"+pointerToken(where[0]), "%q is %s, which takes no where", f.Name, f.Kind.noun())
	}
	var rows []row
	switch f.Kind {
	case KindVocabulary:
		idx := make([]int, len(where))
		values := make([]string, len(where))
		for i, k := range where {
			if idx[i] = slices.Index(f.Fields, k); idx[i] < 0 {
				return nil, 0, refuse("/where/"+pointerToken(k), "vocabulary %q has no field %q: its fields are %s", f.Name, k, quoted(f.Fields))
			}
			values[i] = Normalize(q.Where[k])
		}
	entries:
		for _, se := range s.sorted[f] {
			for i, j := range idx {
				if se.key[j] != values[i] {
					continue entries
				}
			}
			rows = append(rows, row{key: se.key, entry: se.entry})
		}
		return rows, len(f.Fields), nil
	case KindSpine:
		for _, e := range f.SpineEntries {
			rows = append(rows, row{key: []string{e.ID}, spine: Reached{Ref: Ref{Spine: f.Name, Target: Target{ID: e.ID}}, Name: e.Name}})
		}
	case KindExtensionSet:
		for _, x := range f.Extensions {
			it := Reached{Ref: Ref{Target: Target{Ext: "ext:" + f.Name + "/" + x.ID}}}
			s.describe(&it)
			rows = append(rows, row{key: []string{x.ID}, spine: it})
		}
	}
	sortRows(rows)
	return rows, 1, nil
}

// encodeCursor makes a list key a cursor: opaque to clients, its JSON in
// unpadded base64url.
func encodeCursor(key []string) string {
	b, _ := json.Marshal(key)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeCursor(cursor string) ([]string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return nil, false
	}
	var key []string
	if json.Unmarshal(b, &key) != nil {
		return nil, false
	}
	return key, true
}
