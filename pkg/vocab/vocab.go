// Package vocab reads, validates and indexes the files nitrovin translates
// with: vocabularies, extension sets and declared spines, each a JSON Lines
// file of docs/api/vocabulary.schema.json (ADR 0009, 0010, 0011).
//
//	files, err := vocab.Open("yard.jsonl", "vocabularies/") // a directory loads its *.jsonl
//	findings := vocab.Drift(month, files)                  // against a data month
//	set, err := vocab.NewSet(month, files)                 // to translate and list
//
// Read reads one file and refuses its faults; Open reads files and
// directories and also checks them against each other (Link): names
// unique, references resolved. Every fault is a Finding naming the file and
// line, and the error is Faults. Drift measures loaded files against a data
// month, reached through the Month interface, and is reported, never
// refused. A Set answers translations and lists (ADR 0012) over them:
// routing, year scope, qualifiers checked against a Decoded, paging.
//
// The package reads no artifact: a Month can be backed by one (package
// nitrovin) or by anything else that knows the month's makes, models and
// element values, such as a bundle in a browser.
package vocab

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Format is the file format this package reads: a header's format.
const Format = 1

// Kind is what a file holds, its header's kind.
type Kind string

// The kinds of file.
const (
	KindVocabulary   Kind = "vocabulary"
	KindExtensionSet Kind = "extension-set"
	KindSpine        Kind = "spine"
)

// noun is the kind in a sentence: "a vocabulary".
func (k Kind) noun() string {
	switch k {
	case KindVocabulary:
		return "a vocabulary"
	case KindExtensionSet:
		return "an extension set"
	}
	return "a spine"
}

// Subject is what a vocabulary's entries name, and so what its mappings
// point at.
type Subject string

// The subjects of a vocabulary.
const (
	// SubjectVehicle: mappings name vPIC makes, vPIC models or extensions.
	SubjectVehicle Subject = "vehicle"
	// SubjectPart: mappings name entries of the vocabulary's declared
	// spine.
	SubjectPart Subject = "part"
)

// Reserved is the name no file may take: the vehicle spine's, vPIC
// (ADR 0012).
const Reserved = "vpic"

// File is one loaded file: its header, and the lines of its kind, in file
// order and indexed. A File never changes once read.
type File struct {
	// Path is the name the file was read under: the path as Open made it
	// absolute, or the name given to Read.
	Path string
	// Hash is the blake3 hash of the file's bytes.
	Hash [32]byte

	Kind    Kind
	Name    string
	Version string
	// DataMonth is the data month the file says it was built against, ""
	// when it says none.
	DataMonth string

	// Subject, Fields and Spine are a vocabulary's: what its mappings name,
	// the fields that name an entry in order, and the declared spine a part
	// vocabulary maps onto ("" for a vehicle vocabulary).
	Subject Subject
	Fields  []string
	Spine   string

	// Entries are a vocabulary's lines.
	Entries []Entry
	// Extensions are an extension set's lines.
	Extensions []Extension
	// SpineEntries are a declared spine's lines.
	SpineEntries []SpineEntry

	entries    map[string]int   // entry key → index in Entries
	targets    map[Target][]int // mapping target → indexes in Entries, ascending
	extensions map[string]int   // extension id → index in Extensions
	spine      map[string]int   // spine entry id → index in SpineEntries
}

// Entry is one line of a vocabulary: an entry's fields and every mapping it
// has, none for an unmapped entry.
type Entry struct {
	// Line is the entry's line in its file, from 1.
	Line int
	// Fields are the entry's field values in the order of its file's
	// Fields, as written.
	Fields   []string
	Mappings []Mapping
	Note     string
}

// Target is the spine entry a mapping names: exactly one of a vPIC make
// id, a vPIC model id, an extension reference (ext:<set>/<id>) or an entry
// id of a declared spine.
type Target struct {
	Make  int32
	Model int32
	Ext   string
	ID    string
}

// Mapping is one spine entry an entry maps to, with the model years and
// qualifiers it holds for (ADR 0009).
type Mapping struct {
	Target
	// First and Last bound the model years the mapping holds for,
	// inclusive; nil is unbounded.
	First, Last *int
	// Qualifiers are the mapping's conditions in ascending element id;
	// none on an extension mapping.
	Qualifiers []Qualifier
	// Origin ("derived", "curated", "seed") and Note are passed through,
	// never interpreted; "" when absent.
	Origin string
	Note   string
}

// Qualifier is a condition on a mapping: a vPIC element and the values it
// holds for, any one satisfying it.
type Qualifier struct {
	Element int32
	Values  []Value
}

// Value is one value a qualifier holds for: an attribute id for a lookup
// element (Drive Type, Body Class), or the exact text a text element
// decodes to (Series, Trim).
type Value struct {
	// IsText says which of Attribute and Text the value is.
	IsText    bool
	Attribute int32
	Text      string
}

// Extension is one line of an extension set: a make vPIC lacks (MakeName),
// or a model (Name) under a vPIC make (Make) or an extension make (MakeExt).
type Extension struct {
	// Line is the extension's line in its file, from 1.
	Line     int
	ID       string
	MakeName string
	Make     int32
	MakeExt  string
	Name     string
}

// IsMake reports whether the extension is a make rather than a model.
func (x *Extension) IsMake() bool { return x.MakeName != "" }

// SpineEntry is one line of a declared spine: an entry id and, optionally,
// its name.
type SpineEntry struct {
	// Line is the entry's line in its file, from 1.
	Line int
	ID   string
	Name string
}

// Normalize is the one normalization entry fields are matched after:
// trimmed, every run of white space collapsed to one space, and Unicode
// lower-cased.
func Normalize(s string) string {
	if normalASCII(s) {
		return s
	}
	return string(appendNormalized(make([]byte, 0, len(s)), s))
}

// normalASCII reports whether s is ASCII that Normalize leaves as it is.
func normalASCII(s string) bool {
	for i := range len(s) {
		switch c := s[i]; {
		case c >= utf8.RuneSelf, 'A' <= c && c <= 'Z', c != ' ' && asciiSpace(c):
			return false
		case c == ' ' && (i == 0 || i == len(s)-1 || s[i+1] == ' '):
			return false
		}
	}
	return true
}

// appendNormalized appends Normalize(s) to b: byte by byte when s is
// ASCII, through Unicode's white space and case otherwise.
func appendNormalized(b []byte, s string) []byte {
	start, space := len(b), false
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= utf8.RuneSelf:
			return append(b[:start], strings.ToLower(strings.Join(strings.Fields(s), " "))...)
		case asciiSpace(c):
			space = len(b) > start
			continue
		case 'A' <= c && c <= 'Z':
			c += 'a' - 'A'
		}
		if space {
			b, space = append(b, ' '), false
		}
		b = append(b, c)
	}
	return b
}

// asciiSpace reports whether c is white space, as unicode.IsSpace has it,
// for an ASCII byte.
func asciiSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

// key is the index key of an entry's field values: each normalized, length
// prefixed so no value can run into the next.
func key(values []string) string { return string(appendKey(nil, values)) }

// appendKey appends key(values) to b.
func appendKey(b []byte, values []string) []byte {
	for _, v := range values {
		at := len(b)
		b = appendNormalized(b, v)
		n := len(b) - at
		var buf [21]byte
		prefix := append(strconv.AppendInt(buf[:0], int64(n), 10), ':')
		b = append(b, prefix...)             // room for the prefix
		copy(b[at+len(prefix):], b[at:at+n]) // the value, after it
		copy(b[at:], prefix)
	}
	return b
}

// Entry finds a vocabulary's entry by its fields, matched after Normalize.
// fields must name exactly the vocabulary's Fields.
func (f *File) Entry(fields map[string]string) (*Entry, bool) {
	if f.Kind != KindVocabulary || len(fields) != len(f.Fields) {
		return nil, false
	}
	values := make([]string, len(f.Fields))
	for i, name := range f.Fields {
		v, ok := fields[name]
		if !ok {
			return nil, false
		}
		values[i] = v
	}
	i, ok := f.entries[key(values)]
	if !ok {
		return nil, false
	}
	return &f.Entries[i], true
}

// FieldsOf is a vocabulary entry's fields by name, as written.
func (f *File) FieldsOf(e *Entry) map[string]string {
	m := make(map[string]string, len(f.Fields))
	for i, name := range f.Fields {
		m[name] = e.Fields[i]
	}
	return m
}

// EntriesFor returns, in file order, a vocabulary's entries with a mapping
// to t.
func (f *File) EntriesFor(t Target) []*Entry {
	idx := f.targets[t]
	out := make([]*Entry, len(idx))
	for i, j := range idx {
		out[i] = &f.Entries[j]
	}
	return out
}

// Extension finds an extension set's extension by id.
func (f *File) Extension(id string) (*Extension, bool) {
	i, ok := f.extensions[id]
	if !ok {
		return nil, false
	}
	return &f.Extensions[i], true
}

// SpineEntry finds a declared spine's entry by id.
func (f *File) SpineEntry(id string) (*SpineEntry, bool) {
	i, ok := f.spine[id]
	if !ok {
		return nil, false
	}
	return &f.SpineEntries[i], true
}
