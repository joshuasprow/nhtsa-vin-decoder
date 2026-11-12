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
	pager, keyLen, err := s.pager(q, l)
	if err != nil {
		return nil, err
	}
	var after []string
	if q.After != "" {
		key, ok := decodeCursor(q.After)
		if !ok || len(key) != keyLen {
			return nil, refuse("/after", "%q is not a cursor of this list", q.After)
		}
		after = key
	}
	l.Next = pager(after, limit)
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

// A pager adds to a listing the page after cursor key after (nil: the
// first page), of at most limit rows, and is the Next of that page.
type pager func(after []string, limit int) string

// pager pages through what q's Of and Where select, into l, and is the
// length of its list keys. It sets l.File. Every list is already in list
// order, so a page costs its cursor's binary search and its own rows.
func (s *Set) pager(q ListQuery, l *Listing) (pager, int, error) {
	where := slices.Sorted(maps.Keys(q.Where))
	addSpine := func(r *row) { l.Spine = append(l.Spine, r.spine) }
	if q.Of == Reserved {
		for _, k := range where {
			if k != "make" && k != "year" {
				return nil, 0, refuse("/where/"+pointerToken(k), "vPIC's entries take where.make, a vPIC make id, and with it where.year, and no other field")
			}
		}
		v, ok := q.Where["make"]
		if !ok {
			if _, ok := q.Where["year"]; ok {
				return nil, 0, refuse("/where/year", "where.year needs where.make: it filters one make's models")
			}
			return rowPager(s.makes(), addSpine), 2, nil
		}
		mk, err := strconv.ParseInt(v, 10, 32)
		if err != nil || mk < 1 {
			return nil, 0, refuse("/where/make", "%q is not a vPIC make id", v)
		}
		keep, err := yearFilter(q.Where)
		if err != nil {
			return nil, 0, err
		}
		rows := s.models(int32(mk))
		return func(after []string, limit int) string {
			return page(rows, func(r *row) []string { return r.key }, keep, after, limit, addSpine)
		}, 2, nil
	}
	f := s.byName[q.Of]
	if f == nil {
		return nil, 0, refuse("/of", "nothing named %q is loaded: list without of names what is", q.Of)
	}
	l.File = f
	if f.Kind != KindVocabulary && len(where) > 0 {
		return nil, 0, refuse("/where/"+pointerToken(where[0]), "%q is %s, which takes no where", f.Name, f.Kind.noun())
	}
	if f.Kind != KindVocabulary {
		return rowPager(s.lists[f](), addSpine), 1, nil
	}
	idx := make([]int, len(where))
	values := make([]string, len(where))
	for i, k := range where {
		if idx[i] = slices.Index(f.Fields, k); idx[i] < 0 {
			return nil, 0, refuse("/where/"+pointerToken(k), "vocabulary %q has no field %q: its fields are %s", f.Name, k, quoted(f.Fields))
		}
		values[i] = Normalize(q.Where[k])
	}
	es := f.listOrder()
	// The entries a where on leading fields selects are one run of the
	// sorted entries: narrow to it, and filter by the other fields.
	var prefix []string
	for j := range f.Fields {
		i := slices.Index(idx, j)
		if i < 0 {
			break
		}
		prefix = append(prefix, values[i])
	}
	if p := len(prefix); p > 0 {
		lo := sort.Search(len(es), func(i int) bool { return compareKeys(es[i].key[:p], prefix) >= 0 })
		hi := lo + sort.Search(len(es)-lo, func(i int) bool { return compareKeys(es[lo+i].key[:p], prefix) > 0 })
		es = es[lo:hi]
	}
	keep := func(se *sortedEntry) bool {
		for i, j := range idx[len(prefix):] {
			if se.key[j] != values[len(prefix)+i] {
				return false
			}
		}
		return true
	}
	return func(after []string, limit int) string {
		return page(es, func(se *sortedEntry) []string { return se.key }, keep, after, limit, func(se *sortedEntry) { l.Entries = append(l.Entries, se.entry) })
	}, len(f.Fields), nil
}

// rowPager pages through rows, every one, by add.
func rowPager(rows []row, add func(*row)) pager {
	return func(after []string, limit int) string {
		return page(rows, func(r *row) []string { return r.key }, func(*row) bool { return true }, after, limit, add)
	}
}

// page adds, by add, up to limit of rows that keep takes, from the first
// whose key is after after (nil: from the first), and is the cursor of the
// next page: the last added row's key, or "" when keep takes no later row.
// rows are in list order.
func page[T any](rows []T, key func(*T) []string, keep func(*T) bool, after []string, limit int, add func(*T)) string {
	i := 0
	if after != nil {
		i = sort.Search(len(rows), func(i int) bool { return compareKeys(key(&rows[i]), after) > 0 })
	}
	var last *T
	for n := 0; i < len(rows); i++ {
		r := &rows[i]
		if !keep(r) {
			continue
		}
		if n == limit {
			return encodeCursor(key(last))
		}
		add(r)
		last, n = r, n+1
	}
	return ""
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
