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
	// sorted holds, per vocabulary, its entries in list order: by their
	// normalized fields.
	sorted map[*File][]sortedEntry
	// makes is vPIC's makes in list order, built on first use.
	makes func() []row
}

// sortedEntry is a vocabulary entry with its normalized fields, its list
// key.
type sortedEntry struct {
	entry *Entry
	key   []string
}

// NewSet links files (Link), refusing with Faults, and indexes them beside
// data month m.
func NewSet(m Month, files []*File) (*Set, error) {
	if err := Link(files); err != nil {
		return nil, err
	}
	s := &Set{month: m, files: files, byName: map[string]*File{}, sorted: map[*File][]sortedEntry{}}
	for _, f := range files {
		s.byName[f.Name] = f
		if f.Kind != KindVocabulary {
			continue
		}
		es := make([]sortedEntry, len(f.Entries))
		for i := range f.Entries {
			es[i] = sortedEntry{&f.Entries[i], normalized(f.Entries[i].Fields)}
		}
		slices.SortFunc(es, func(a, b sortedEntry) int { return compareKeys(a.key, b.key) })
		s.sorted[f] = es
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
