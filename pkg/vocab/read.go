package vocab

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/zeebo/blake3"
)

// Open reads the files at paths, a directory meaning every *.jsonl file
// directly in it, in name order, and links them (Link). Each File's Path is
// absolute. The error is Faults: every fault of every file, which refuses
// them all.
func Open(paths ...string) ([]*File, error) {
	var files []*File
	var faults Faults
	read := func(path string) {
		data, err := os.ReadFile(path)
		if err != nil {
			faults = append(faults, fault(path, 0, "%v", errorText(err)))
			return
		}
		f, err := parse(path, data)
		if fs, ok := err.(Faults); ok {
			faults = append(faults, fs...)
			return
		}
		files = append(files, f)
	}
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			faults = append(faults, fault(p, 0, "%v", err))
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil {
			faults = append(faults, fault(abs, 0, "%v", errorText(err)))
			continue
		}
		if !fi.IsDir() {
			read(abs)
			continue
		}
		des, err := os.ReadDir(abs)
		if err != nil {
			faults = append(faults, fault(abs, 0, "%v", errorText(err)))
			continue
		}
		for _, de := range des {
			if !de.IsDir() && strings.HasSuffix(de.Name(), ".jsonl") {
				read(filepath.Join(abs, de.Name()))
			}
		}
	}
	if len(faults) > 0 {
		return nil, faults
	}
	if err := Link(files); err != nil {
		return nil, err
	}
	return files, nil
}

// errorText is a file system error without the path the finding already
// names.
func errorText(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Op + ": " + pe.Err.Error()
	}
	return err.Error()
}

// Read reads one file from r; name is its Path, the name findings carry.
// It checks the file alone: Link checks it against the others loaded with
// it. The error is Faults: every fault in the file.
func Read(name string, r io.Reader) (*File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Faults{fault(name, 0, "%v", err)}
	}
	return parse(name, data)
}

// chunkSize is about how many bytes of lines one goroutine reads at a
// time.
const chunkSize = 256 << 10

// parse reads one file's bytes: a header, then lines of its kind. Blank
// lines (nothing but spaces, tabs and a \r) are skipped and counted. The
// lines after the header are read in chunks, in parallel, then indexed in
// file order (index).
func parse(name string, data []byte) (*File, error) {
	f := &File{Path: name}
	hashed := make(chan struct{})
	go func() {
		f.Hash = blake3.Sum256(data)
		close(hashed)
	}()
	n, start := 0, 0
	for {
		if start > len(data) {
			return nil, Faults{fault(name, 1, "no header: the file is empty")}
		}
		end := lineEnd(data, start)
		line := data[start:end]
		n, start = n+1, end+1
		if blank(line) {
			continue
		}
		if err := f.header(line); err != nil {
			return nil, Faults{fault(name, n, "%v", err)}
		}
		break
	}
	var chunks []*chunk
	for start <= len(data) {
		end := len(data)
		if start+chunkSize < len(data) {
			if i := bytes.IndexByte(data[start+chunkSize:], '\n'); i >= 0 {
				end = start + chunkSize + i
			}
		}
		chunks = append(chunks, &chunk{data: data[start:end]})
		start = end + 1
	}
	if len(chunks) == 1 {
		newReader(f).read(chunks[0])
	} else {
		var next atomic.Int64
		var wg sync.WaitGroup
		for range min(runtime.GOMAXPROCS(0), len(chunks)) {
			wg.Go(func() {
				r := newReader(f)
				for i := next.Add(1) - 1; i < int64(len(chunks)); i = next.Add(1) - 1 {
					r.read(chunks[i])
				}
			})
		}
		wg.Wait()
	}
	if faults := f.index(n, chunks); len(faults) > 0 {
		return nil, faults
	}
	<-hashed
	return f, nil
}

// lineEnd is the index of the '\n' that ends the line starting at
// data[start], or len(data) for the last line.
func lineEnd(data []byte, start int) int {
	if i := bytes.IndexByte(data[start:], '\n'); i >= 0 {
		return start + i
	}
	return len(data)
}

// blank reports whether a line holds nothing but spaces, tabs and a \r.
func blank(line []byte) bool { return len(bytes.Trim(line, " \t\r")) == 0 }

// chunk is a run of whole lines after the header, and what reading them
// made: the lines of the file's kind that read, and the faults of the rest.
// Its lines and faults count lines from 1 at the chunk's first.
type chunk struct {
	data       []byte
	lines      int // how many lines data holds
	entries    []Entry
	keys       []string // each entry's key
	extensions []Extension
	spine      []SpineEntry
	faults     Faults
}

// reader reads lines of one file, a chunk at a time, on one goroutine. A
// line's slices come from slabs, larger allocations it shares with the
// lines after it, rather than allocations of their own.
type reader struct {
	f        *File
	line     [3][]byte // an entry line's members, entryNames
	entry    [][]byte  // its entry's fields, the file's Fields
	mapping  [9][]byte // a mapping's members, mappingNames
	mappings [][]byte
	key      []byte

	strings  slab[string]
	mappingS slab[Mapping]
	years    slab[int]
}

func newReader(f *File) *reader {
	return &reader{f: f, entry: make([][]byte, len(f.Fields))}
}

// slab hands out short slices of one larger allocation.
type slab[T any] []T

// take is the next n elements, its capacity n.
func (s *slab[T]) take(n int) []T {
	if len(*s) < n {
		*s = make([]T, max(n, 1024))
	}
	out := (*s)[:n:n]
	*s = (*s)[n:]
	return out
}

// read reads c's lines.
func (r *reader) read(c *chunk) {
	size := bytes.Count(c.data, []byte("\n")) + 1
	switch r.f.Kind {
	case KindVocabulary:
		c.entries, c.keys = make([]Entry, 0, size), make([]string, 0, size)
	case KindExtensionSet:
		c.extensions = make([]Extension, 0, size)
	case KindSpine:
		c.spine = make([]SpineEntry, 0, size)
	}
	for start := 0; start <= len(c.data); {
		end := lineEnd(c.data, start)
		line := c.data[start:end]
		start = end + 1
		c.lines++
		if blank(line) {
			continue
		}
		n := c.lines
		var err error
		switch r.f.Kind {
		case KindVocabulary:
			var e Entry
			var key string
			if e, key, err = r.readEntry(n, line); err == nil {
				c.entries = append(c.entries, e)
				c.keys = append(c.keys, key)
			}
		case KindExtensionSet:
			var x Extension
			if x, err = extension(n, line); err == nil {
				c.extensions = append(c.extensions, x)
			}
		case KindSpine:
			var e SpineEntry
			if e, err = spineEntry(n, line); err == nil {
				c.spine = append(c.spine, e)
			}
		}
		if err != nil {
			c.faults = append(c.faults, fault(r.f.Path, n, "%v", err))
		}
	}
	c.data = nil
}

// index puts the chunks' lines in f in file order, indexed, and returns
// every fault in line order: the chunks' and those of lines that repeat an
// earlier line's entry or id. base is the header's line, the line before
// the first chunk's.
func (f *File) index(base int, chunks []*chunk) Faults {
	total := 0
	for _, c := range chunks {
		total += len(c.entries) + len(c.extensions) + len(c.spine)
	}
	switch f.Kind { // slices.Grow leaves a file without lines nil
	case KindVocabulary:
		f.Entries = slices.Grow(f.Entries, total)
		f.entries = make(map[string]int, total)
		f.targets = map[Target][]int{}
	case KindExtensionSet:
		f.Extensions = slices.Grow(f.Extensions, total)
		f.extensions = make(map[string]int, total)
	case KindSpine:
		f.SpineEntries = slices.Grow(f.SpineEntries, total)
		f.spine = make(map[string]int, total)
	}
	var faults Faults
	for _, c := range chunks {
		next := 0 // c.faults[next:] are still to add
		// at adds the faults of c's lines before line, then line's own if
		// err is one.
		at := func(line int, err error) {
			for ; next < len(c.faults) && c.faults[next].Line < line; next++ {
				ft := c.faults[next]
				ft.Line += base
				faults = append(faults, ft)
			}
			if err != nil {
				faults = append(faults, fault(f.Path, base+line, "%v", err))
			}
		}
		for i := range c.entries {
			e := &c.entries[i]
			line := e.Line
			e.Line += base
			at(line, f.addEntry(e, c.keys[i]))
		}
		for i := range c.extensions {
			x := &c.extensions[i]
			line := x.Line
			x.Line += base
			at(line, f.addExtension(x))
		}
		for i := range c.spine {
			e := &c.spine[i]
			line := e.Line
			e.Line += base
			at(line, f.addSpineEntry(e))
		}
		at(math.MaxInt, nil)
		base += c.lines
		*c = chunk{}
	}
	return faults
}

// addEntry adds e, whose key is k, refusing it when an earlier entry has
// the key.
func (f *File) addEntry(e *Entry, k string) error {
	if j, dup := f.entries[k]; dup {
		return errAt("/entry", "duplicate entry: line %d names it too, after normalization", f.Entries[j].Line)
	}
	i := len(f.Entries)
	f.entries[k] = i
	for _, mp := range e.Mappings {
		idx := f.targets[mp.Target]
		if len(idx) == 0 || idx[len(idx)-1] != i {
			f.targets[mp.Target] = append(idx, i)
		}
	}
	f.Entries = append(f.Entries, *e)
	return nil
}

// addExtension adds x, refusing it when an earlier extension has its id.
func (f *File) addExtension(x *Extension) error {
	if j, dup := f.extensions[x.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", x.ID, f.Extensions[j].Line)
	}
	f.extensions[x.ID] = len(f.Extensions)
	f.Extensions = append(f.Extensions, *x)
	return nil
}

// addSpineEntry adds e, refusing it when an earlier spine entry has its
// id.
func (f *File) addSpineEntry(e *SpineEntry) error {
	if j, dup := f.spine[e.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", e.ID, f.SpineEntries[j].Line)
	}
	f.spine[e.ID] = len(f.SpineEntries)
	f.SpineEntries = append(f.SpineEntries, *e)
	return nil
}

// lineError is what is wrong with a line: at a JSON Pointer into it, or
// with the line as a whole.
type lineError struct{ ptr, msg string }

func (e *lineError) Error() string {
	if e.ptr == "" {
		return e.msg
	}
	return e.ptr + ": " + e.msg
}

func errAt(ptr, format string, a ...any) error {
	return &lineError{ptr, fmt.Sprintf(format, a...)}
}

// under moves err, a lineError about a part of the line, under prefix, the
// part's pointer.
func under(prefix string, err error) error {
	e := err.(*lineError)
	e.ptr = prefix + e.ptr
	return e
}

// lineValue checks that a line is JSON, and is its value without the white
// space around it.
func lineValue(line []byte) ([]byte, error) {
	if !json.Valid(line) {
		var v any
		err := json.Unmarshal(line, &v)
		return nil, errAt("", "malformed JSON: %v", err)
	}
	return trimSpace(line), nil
}

func isNull(raw []byte) bool { return string(bytes.TrimSpace(raw)) == "null" }

// stringAt reads raw, at ptr, as a string.
func stringAt(raw []byte, ptr string) (string, error) {
	s, ok := str(raw)
	if !ok {
		return "", errAt(ptr, "must be a string")
	}
	return s, nil
}

// nonEmptyAt reads raw, at ptr, as a non-empty string.
func nonEmptyAt(raw []byte, ptr string) (string, error) {
	s, err := stringAt(raw, ptr)
	if err == nil && s == "" {
		err = errAt(ptr, "must not be empty")
	}
	return s, err
}

// integer reads raw as a JSON integer: digits, no fraction or exponent.
func integer(raw []byte) (int64, bool) {
	raw = bytes.TrimSpace(raw)
	digits := bytes.TrimPrefix(raw, []byte("-"))
	if len(digits) == 0 || len(digits) > 18 { // 18 digits never overflow
		return parseInt(raw)
	}
	var n int64
	for _, c := range digits {
		if c < '0' || c > '9' {
			return parseInt(raw)
		}
		n = n*10 + int64(c-'0')
	}
	if len(digits) < len(raw) {
		n = -n
	}
	return n, true
}

func parseInt(raw []byte) (int64, bool) {
	n, err := strconv.ParseInt(string(raw), 10, 64)
	return n, err == nil
}

// idAt reads raw, at ptr, as a vPIC id: an integer from 1 to 2^31-1.
func idAt(raw []byte, ptr string) (int32, error) {
	n, ok := integer(raw)
	if !ok || n < 1 || n > math.MaxInt32 {
		return 0, errAt(ptr, "must be an integer from 1 to %d", math.MaxInt32)
	}
	return int32(n), nil
}

var (
	namePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)
	monthPattern = regexp.MustCompile(`^[0-9]{4}_(0[1-9]|1[0-2])$`)
	extPattern   = regexp.MustCompile(`^ext:([a-z0-9][a-z0-9._-]*)/([^/\s]+)$`)
	idPattern    = regexp.MustCompile(`^[^/\s]+$`)
	// elementPattern is a qualifier's element id.
	elementPattern = regexp.MustCompile(`^[1-9][0-9]*$`)
)

// nameAt reads raw, at ptr, as a file name.
func nameAt(raw []byte, ptr string) (string, error) {
	s, err := stringAt(raw, ptr)
	switch {
	case err != nil:
		return "", err
	case !namePattern.MatchString(s):
		return "", errAt(ptr, "%q is not a name: lower-case letters, digits, '.', '_' and '-', starting with a letter or digit", s)
	case s == Reserved:
		return "", errAt(ptr, "%q is reserved for the vehicle spine", s)
	}
	return s, nil
}

// headerNames are a header's fields: a spine's are the first four, an
// extension set's the first five, a vocabulary's all of them.
var headerNames = []string{"format", "kind", "name", "version", "data_month", "subject", "fields", "spine"}

// header reads line 1.
func (f *File) header(line []byte) error {
	raw, err := lineValue(line)
	if err != nil {
		return err
	}
	var v, scratch [8][]byte
	if _, _, ok := members(raw, headerNames, v[:]); !ok {
		return errAt("", "must be an object")
	}
	if v[0] == nil {
		return errAt("", "missing field \"format\"")
	}
	if n, ok := integer(v[0]); !ok || n != Format {
		return errAt("/format", "unknown format %s: this nitrovin reads format %d", bytes.TrimSpace(v[0]), Format)
	}
	if v[1] == nil {
		return errAt("", "missing field \"kind\"")
	}
	kind, err := stringAt(v[1], "/kind")
	if err != nil {
		return err
	}
	f.Kind = Kind(kind)
	allowed := 4
	switch f.Kind {
	case KindVocabulary:
		allowed = 8
	case KindExtensionSet:
		allowed = 5
	case KindSpine:
	default:
		return errAt("/kind", "unknown kind %q: vocabulary, extension-set or spine", kind)
	}
	if unknown, has, _ := members(raw, headerNames[:allowed], scratch[:allowed]); has {
		return errAt("", "unknown field %q", unknown)
	}
	required := []int{2, 3} // name, version
	if f.Kind == KindVocabulary {
		required = append(required, 5, 6) // subject, fields
	}
	for _, i := range required {
		if v[i] == nil {
			return errAt("", "missing field %q", headerNames[i])
		}
	}
	if f.Name, err = nameAt(v[2], "/name"); err != nil {
		return err
	}
	if f.Version, err = nonEmptyAt(v[3], "/version"); err != nil {
		return err
	}
	if v[4] != nil {
		if f.DataMonth, err = stringAt(v[4], "/data_month"); err != nil {
			return err
		}
		if !monthPattern.MatchString(f.DataMonth) {
			return errAt("/data_month", "%q is not a data month, YYYY_MM", f.DataMonth)
		}
	}
	if f.Kind != KindVocabulary {
		return nil
	}
	subject, err := stringAt(v[5], "/subject")
	if err != nil {
		return err
	}
	f.Subject = Subject(subject)
	if f.Subject != SubjectVehicle && f.Subject != SubjectPart {
		return errAt("/subject", "unknown subject %q: vehicle or part", subject)
	}
	fields, ok := items(v[6], nil)
	if !ok {
		return errAt("/fields", "must be an array of strings")
	}
	if len(fields) == 0 {
		return errAt("/fields", "must name at least one field")
	}
	for i, raw := range fields {
		ptr := "/fields/" + strconv.Itoa(i)
		s, err := nonEmptyAt(raw, ptr)
		if err != nil {
			return err
		}
		if slices.Contains(f.Fields, s) {
			return errAt(ptr, "duplicate field %q", s)
		}
		f.Fields = append(f.Fields, s)
	}
	hasSpine := v[7] != nil
	switch {
	case f.Subject == SubjectPart && !hasSpine:
		return errAt("", "missing field \"spine\": a part vocabulary names the spine it maps onto")
	case f.Subject == SubjectVehicle && hasSpine:
		return errAt("/spine", "a vehicle vocabulary maps onto vPIC and names no spine")
	case hasSpine:
		if f.Spine, err = nameAt(v[7], "/spine"); err != nil {
			return err
		}
	}
	return nil
}

// entryNames are a vocabulary line's fields.
var entryNames = []string{"entry", "mappings", "note"}

// readEntry reads a vocabulary's line n, and its key.
func (r *reader) readEntry(n int, line []byte) (Entry, string, error) {
	f := r.f
	raw, err := lineValue(line)
	if err != nil {
		return Entry{}, "", err
	}
	v := r.line[:]
	unknown, has, ok := members(raw, entryNames, v)
	switch {
	case !ok:
		return Entry{}, "", errAt("", "must be an object")
	case has:
		return Entry{}, "", errAt("", "unknown field %q", unknown)
	case v[0] == nil:
		return Entry{}, "", errAt("", "missing field \"entry\"")
	case v[1] == nil:
		return Entry{}, "", errAt("", "missing field \"mappings\"")
	}
	unknown, has, ok = members(v[0], f.Fields, r.entry)
	switch {
	case !ok:
		return Entry{}, "", errAt("/entry", "must be an object")
	case has:
		return Entry{}, "", errAt("/entry", "unknown field %q: the vocabulary's fields are %s", unknown, quoted(f.Fields))
	}
	e := Entry{Line: n, Fields: r.strings.take(len(f.Fields))}
	for i, k := range f.Fields {
		raw := r.entry[i]
		if raw == nil {
			return Entry{}, "", errAt("/entry", "missing field %q", k)
		}
		if e.Fields[i], ok = str(raw); !ok {
			return Entry{}, "", errAt("/entry/"+pointerToken(k), "must be a string")
		}
	}
	if r.mappings, ok = items(v[1], r.mappings[:0]); !ok {
		return Entry{}, "", errAt("/mappings", "must be an array")
	}
	if len(r.mappings) > 0 {
		e.Mappings = r.mappingS.take(len(r.mappings))
		for i, raw := range r.mappings {
			if err := r.readMapping(raw, &e.Mappings[i]); err != nil {
				return Entry{}, "", under("/mappings/"+strconv.Itoa(i), err)
			}
		}
	}
	if v[2] != nil {
		if e.Note, err = stringAt(v[2], "/note"); err != nil {
			return Entry{}, "", err
		}
	}
	r.key = appendKey(r.key[:0], e.Fields)
	return e, string(r.key), nil
}

// mappingNames are a mapping's fields: the four targets first.
var mappingNames = []string{"make", "model", "ext", "id", "first", "last", "qualifiers", "origin", "note"}

// origins are a mapping's origins.
var origins = []string{"derived", "curated", "seed"}

// readMapping reads one of a vocabulary entry's mappings into mp. Its
// error's pointer is into the mapping.
func (r *reader) readMapping(raw []byte, mp *Mapping) error {
	f := r.f
	v := r.mapping[:]
	unknown, has, ok := members(raw, mappingNames, v)
	switch {
	case !ok:
		return errAt("", "must be an object")
	case has:
		return errAt("", "unknown field %q", unknown)
	}
	t, targets := 0, 0
	for i := range 4 {
		if v[i] != nil {
			t, targets = i, targets+1
		}
	}
	switch {
	case targets == 0:
		return errAt("", "names no spine entry: one of make, model, ext or id")
	case targets > 1:
		var named []string
		for i := range 4 {
			if v[i] != nil {
				named = append(named, mappingNames[i])
			}
		}
		return errAt("", "names more than one spine entry: %s", strings.Join(named, ", "))
	}
	switch {
	case f.Subject == SubjectVehicle && t == 3:
		return errAt("", "a vehicle vocabulary maps to make, model or ext, not id")
	case f.Subject == SubjectPart && t != 3:
		return errAt("", "a part vocabulary maps to an id of spine %q, not %s", f.Spine, mappingNames[t])
	}
	var err error
	switch t {
	case 0:
		mp.Make, err = idAt(v[0], "/make")
	case 1:
		mp.Model, err = idAt(v[1], "/model")
	case 2:
		mp.Ext, err = extRefAt(v[2], "/ext")
	case 3:
		mp.ID, err = nonEmptyAt(v[3], "/id")
	}
	if err != nil {
		return err
	}
	for i, ptr := range []string{"/first", "/last"} {
		raw := v[4+i]
		if raw == nil {
			continue
		}
		y, ok := integer(raw)
		if !ok || y < math.MinInt32 || y > math.MaxInt32 {
			return errAt(ptr, "must be an integer year")
		}
		year := r.years.take(1)
		year[0] = int(y)
		if i == 0 {
			mp.First = &year[0]
		} else {
			mp.Last = &year[0]
		}
	}
	if mp.First != nil && mp.Last != nil && *mp.First > *mp.Last {
		return errAt("", "first year %d is after last year %d", *mp.First, *mp.Last)
	}
	if v[6] != nil {
		if mp.Ext != "" {
			return errAt("/qualifiers", "an extension mapping takes no qualifiers: no decode lands on an extension")
		}
		if mp.Qualifiers, err = qualifiersAt(v[6], "/qualifiers"); err != nil {
			return err
		}
	}
	if v[7] != nil {
		if s, ok := plainString(v[7]); ok {
			for _, o := range origins {
				if string(s) == o {
					mp.Origin = o // shared, not a copy per mapping
				}
			}
		}
		if mp.Origin == "" {
			if mp.Origin, err = stringAt(v[7], "/origin"); err != nil {
				return err
			}
			if !slices.Contains(origins, mp.Origin) {
				return errAt("/origin", "unknown origin %q: derived, curated or seed", mp.Origin)
			}
		}
	}
	if v[8] != nil {
		if mp.Note, err = stringAt(v[8], "/note"); err != nil {
			return err
		}
	}
	return nil
}

// qualifiersAt reads a mapping's qualifiers, at ptr: element id → values.
func qualifiersAt(raw []byte, ptr string) ([]Qualifier, error) {
	type member struct {
		key   string
		value []byte
	}
	var ms []member
	if !eachMember(raw, func(key, value []byte) { ms = append(ms, member{keyString(key), value}) }) {
		return nil, errAt(ptr, "must be an object")
	}
	// As encoding/json reads them into a map: in key order, the last value
	// of a repeated key.
	slices.SortStableFunc(ms, func(a, b member) int { return strings.Compare(a.key, b.key) })
	kept := ms[:0]
	for i, m := range ms {
		if i+1 == len(ms) || ms[i+1].key != m.key {
			kept = append(kept, m)
		}
	}
	if len(kept) == 0 {
		return nil, errAt(ptr, "must name at least one element: omit qualifiers instead")
	}
	var qs []Qualifier
	for _, m := range kept {
		el, err := strconv.ParseInt(m.key, 10, 32)
		if !elementPattern.MatchString(m.key) || err != nil {
			return nil, errAt(ptr, "%q is not an element id", m.key)
		}
		vp := ptr + "/" + m.key
		values, ok := items(m.value, nil)
		if !ok {
			return nil, errAt(vp, "must be an array of attribute ids and strings")
		}
		if len(values) == 0 {
			return nil, errAt(vp, "must list at least one value")
		}
		q := Qualifier{Element: int32(el)}
		for i, raw := range values {
			ip := vp + "/" + strconv.Itoa(i)
			var v Value
			if n, ok := integer(raw); ok {
				if n < math.MinInt32 || n > math.MaxInt32 {
					return nil, errAt(ip, "attribute id %d is out of range", n)
				}
				v.Attribute = int32(n)
			} else if s, err := stringAt(raw, ip); err == nil {
				v = Value{IsText: true, Text: s}
			} else {
				return nil, errAt(ip, "must be an attribute id (an integer) or a string")
			}
			if slices.Contains(q.Values, v) {
				return nil, errAt(ip, "duplicate value")
			}
			q.Values = append(q.Values, v)
		}
		qs = append(qs, q)
	}
	slices.SortFunc(qs, func(a, b Qualifier) int { return cmp.Compare(a.Element, b.Element) }) // "10" sorts before "9"
	return qs, nil
}

// extRefAt reads raw, at ptr, as an extension reference, ext:<set>/<id>.
func extRefAt(raw []byte, ptr string) (string, error) {
	s, err := stringAt(raw, ptr)
	if err == nil && !extPattern.MatchString(s) {
		err = errAt(ptr, "%q is not an extension reference, ext:<set>/<id>", s)
	}
	return s, err
}

// splitExt splits a valid extension reference into its set and id.
func splitExt(ref string) (set, id string) {
	m := extPattern.FindStringSubmatch(ref)
	return m[1], m[2]
}

// extensionNames are an extension set line's fields.
var extensionNames = []string{"id", "make_name", "make", "name"}

// extension reads an extension set's line n.
func extension(n int, line []byte) (Extension, error) {
	raw, err := lineValue(line)
	if err != nil {
		return Extension{}, err
	}
	var v [4][]byte
	unknown, has, ok := members(raw, extensionNames, v[:])
	switch {
	case !ok:
		return Extension{}, errAt("", "must be an object")
	case has:
		return Extension{}, errAt("", "unknown field %q", unknown)
	case v[0] == nil:
		return Extension{}, errAt("", "missing field \"id\"")
	}
	x := Extension{Line: n}
	if x.ID, err = nonEmptyAt(v[0], "/id"); err != nil {
		return Extension{}, err
	}
	if !idPattern.MatchString(x.ID) {
		return Extension{}, errAt("/id", "%q may contain neither '/' nor white space", x.ID)
	}
	hasMakeName, hasMake, hasName := v[1] != nil, v[2] != nil, v[3] != nil
	switch {
	case hasMakeName && (hasMake || hasName):
		return Extension{}, errAt("", "an extension is a make (make_name) or a model (make and name), not both")
	case hasMakeName:
		if x.MakeName, err = nonEmptyAt(v[1], "/make_name"); err != nil {
			return Extension{}, err
		}
	case !hasMake || !hasName:
		return Extension{}, errAt("", "missing make_name, for an extension make, or make and name, for an extension model")
	default:
		if _, ok := integer(v[2]); ok {
			x.Make, err = idAt(v[2], "/make")
		} else if x.MakeExt, err = extRefAt(v[2], "/make"); err != nil {
			err = errAt("/make", "must be a vPIC make id or an extension reference, ext:<set>/<id>")
		}
		if err != nil {
			return Extension{}, err
		}
		if x.Name, err = nonEmptyAt(v[3], "/name"); err != nil {
			return Extension{}, err
		}
	}
	return x, nil
}

// spineNames are a declared spine line's fields.
var spineNames = []string{"id", "name"}

// spineEntry reads a declared spine's line n.
func spineEntry(n int, line []byte) (SpineEntry, error) {
	raw, err := lineValue(line)
	if err != nil {
		return SpineEntry{}, err
	}
	var v [2][]byte
	unknown, has, ok := members(raw, spineNames, v[:])
	switch {
	case !ok:
		return SpineEntry{}, errAt("", "must be an object")
	case has:
		return SpineEntry{}, errAt("", "unknown field %q", unknown)
	case v[0] == nil:
		return SpineEntry{}, errAt("", "missing field \"id\"")
	}
	e := SpineEntry{Line: n}
	if e.ID, err = nonEmptyAt(v[0], "/id"); err != nil {
		return SpineEntry{}, err
	}
	if v[1] != nil {
		if e.Name, err = nonEmptyAt(v[1], "/name"); err != nil {
			return SpineEntry{}, err
		}
	}
	return e, nil
}

// quoted lists names as "a", "b".
func quoted(names []string) string {
	q := make([]string, len(names))
	for i, s := range names {
		q[i] = strconv.Quote(s)
	}
	return strings.Join(q, ", ")
}

// pointerToken escapes a JSON Pointer reference token (RFC 6901).
func pointerToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}
