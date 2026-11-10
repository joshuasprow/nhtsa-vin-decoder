// Package vocab reads, validates and indexes the files nitrovin translates
// with: vocabularies, extension sets and declared spines, each a JSON Lines
// file of docs/api/vocabulary.schema.json (ADR 0009, 0010, 0011).
//
//	files, err := vocab.Open("yard.jsonl", "vocabularies/") // a directory loads its *.jsonl
//	findings := vocab.Drift(month, files)                  // against a data month
//
// Read reads one file and refuses its faults; Open reads files and
// directories and also checks them against each other (Link): names
// unique, references resolved. Every fault is a Finding naming the file and
// line, and the error is Faults. Drift measures loaded files against a data
// month, reached through the Month interface, and is reported, never
// refused.
//
// The package reads no artifact: a Month can be backed by one (package
// nitrovin) or by anything else that knows the month's makes, models and
// element values, such as a bundle in a browser.
package vocab

import (
	"strconv"
	"strings"
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
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// key is the index key of an entry's field values: each normalized, length
// prefixed so no value can run into the next.
func key(values []string) string {
	var b strings.Builder
	for _, v := range values {
		v = Normalize(v)
		b.WriteString(strconv.Itoa(len(v)))
		b.WriteByte(':')
		b.WriteString(v)
	}
	return b.String()
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
