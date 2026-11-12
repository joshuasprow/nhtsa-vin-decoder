package vocab

import (
	"cmp"
	"fmt"
	"slices"
	"sync"
)

// Set is files loaded together beside a data month, indexed to translate
// and list (ADR 0012). It never changes once made and is safe for
// concurrent use.
type Set struct {
	month  Month
	files  []*File
	byName map[string]*File
	// lists holds, per declared spine and extension set, its entries in
	// list order, built on first use.
	lists map[*File]func() []row
	// makes is vPIC's makes in list order, built on first use.
	makes func() []row
	// modelRows holds, per vPIC make id, its models in list order, built
	// on first use. It holds only makes that have models.
	modelRows sync.Map
}

// sortedEntry is a vocabulary entry with its normalized fields, its list
// key.
type sortedEntry struct {
	entry *Entry
	key   []string
	// at is entry's index in its file's Entries.
	at int
}

// listOrder is a vocabulary's entries in list order, by their normalized
// fields: made once per file, since it is the file's alone.
func (f *File) listOrder() []sortedEntry {
	f.listOnce.Do(func() {
		// Every entry's key in one allocation, the normalized fields that
		// are not already normal in shared blocks.
		n := len(f.Fields)
		keys := make([]string, n*len(f.Entries))
		var text texts
		es := make([]sortedEntry, len(f.Entries))
		for i := range f.Entries {
			key := keys[i*n : (i+1)*n : (i+1)*n]
			for j, v := range f.Entries[i].Fields {
				key[j] = text.normalized(v)
			}
			es[i] = sortedEntry{&f.Entries[i], key, i}
		}
		slices.SortFunc(es, func(a, b sortedEntry) int { return compareKeys(a.key, b.key) })
		f.listed = es
	})
	return f.listed
}

// NewSet links files (Link), refusing with Faults, and indexes them beside
// data month m.
func NewSet(m Month, files []*File) (*Set, error) {
	if err := Link(files); err != nil {
		return nil, err
	}
	s := &Set{month: m, files: files, byName: map[string]*File{}, lists: map[*File]func() []row{}}
	for _, f := range files {
		s.byName[f.Name] = f
		switch f.Kind {
		case KindVocabulary:
			f.listOrder()
		case KindSpine:
			s.lists[f] = sync.OnceValue(func() []row {
				var rows []row
				for _, e := range f.SpineEntries {
					rows = append(rows, row{key: []string{e.ID}, spine: Reached{Ref: Ref{Spine: f.Name, Target: Target{ID: e.ID}}, Name: e.Name}})
				}
				sortRows(rows)
				return rows
			})
		case KindExtensionSet:
			s.lists[f] = sync.OnceValue(func() []row {
				var rows []row
				for _, x := range f.Extensions {
					it := Reached{Ref: Ref{Target: Target{Ext: "ext:" + f.Name + "/" + x.ID}}}
					s.describe(&it)
					rows = append(rows, row{key: []string{x.ID}, spine: it})
				}
				sortRows(rows)
				return rows
			})
		}
	}
	s.makes = sync.OnceValue(func() []row {
		var rows []row
		for id, name := range m.Makes() {
			rows = append(rows, vpicRow(Ref{Target: Target{Make: id}}, id, name, nil))
		}
		sortRows(rows)
		return rows
	})
	return s, nil
}

// models is vPIC make mk's models in list order.
func (s *Set) models(mk int32) []row {
	if rows, ok := s.modelRows.Load(mk); ok {
		return rows.([]row)
	}
	under := &Ref{Target: Target{Make: mk}}
	var rows []row
	for id, name := range s.month.Models(mk) {
		r := vpicRow(Ref{Target: Target{Model: id}}, id, name, under)
		r.spine.Years = modelYears(s.month, id)
		rows = append(rows, r)
	}
	if rows == nil {
		return nil
	}
	sortRows(rows)
	got, _ := s.modelRows.LoadOrStore(mk, rows)
	return got.([]row)
}

// Month is the data month the files are indexed beside.
func (s *Set) Month() Month { return s.month }

// Files are the files, in load order.
func (s *Set) Files() []*File { return s.files }

// normalized is values, each normalized.
func normalized(values []string) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = Normalize(v)
	}
	return out
}

// compareKeys orders list keys: field by field, bytewise.
func compareKeys(a, b []string) int {
	for i := range min(len(a), len(b)) {
		if c := cmp.Compare(a[i], b[i]); c != 0 {
			return c
		}
	}
	return cmp.Compare(len(a), len(b))
}

// spineOf is the spine a vocabulary routes through: its declared spine, or
// vPIC's for a vehicle vocabulary.
func spineOf(f *File) string {
	if f.Subject == SubjectPart {
		return f.Spine
	}
	return Reserved
}

// RequestError is a translate or list request the loaded files refuse
// (ADR 0012): an unknown vocabulary, entry fields other than the
// vocabulary's, a target on another spine. Pointer is a JSON Pointer into
// the request at the offending value.
type RequestError struct {
	Pointer string
	Detail  string
}

func (e *RequestError) Error() string { return e.Pointer + ": " + e.Detail }

func refuse(ptr, format string, a ...any) error {
	return &RequestError{ptr, fmt.Sprintf(format, a...)}
}

// vocabulary finds the loaded vocabulary name, refusing at ptr a name
// nothing loaded has, or one that is not a vocabulary.
func (s *Set) vocabulary(name, ptr string) (*File, error) {
	if name == Reserved {
		return nil, refuse(ptr, "%q is the vehicle spine, not a vocabulary", name)
	}
	f := s.byName[name]
	switch {
	case f == nil:
		return nil, refuse(ptr, "no vocabulary %q is loaded", name)
	case f.Kind != KindVocabulary:
		return nil, refuse(ptr, "%q is %s, not a vocabulary", name, f.Kind.noun())
	}
	return f, nil
}
