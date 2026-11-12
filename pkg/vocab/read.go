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
// directly in it, in name order, and links them (Link). A file that begins
// with a compiled catalog's magic opens as one (ADR 0021): read in place,
// it stays open for the File's life. Each File's Path is absolute. The
// error is Faults: every fault of every file, which refuses them all.
func Open(paths ...string) ([]*File, error) {
	var files []*File
	var faults Faults
	read := func(path string) {
		f, err := openFile(path)
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

// openFile reads the file at path: a compiled catalog, kept open, or JSON
// Lines.
func openFile(path string) (*File, error) {
	osf, err := os.Open(path)
	var fi os.FileInfo
	if err == nil {
		fi, err = osf.Stat()
	}
	if err != nil {
		if osf != nil {
			osf.Close()
		}
		return nil, Faults{fault(path, 0, "%v", errorText(err))}
	}
	prefix := make([]byte, len(compiledMagic))
	if n, _ := osf.ReadAt(prefix, 0); isCompiled(prefix[:n]) {
		f, err := openCompiled(path, osf, fi.Size())
		if err != nil {
			osf.Close()
		}
		return f, err
	}
	osf.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, Faults{fault(path, 0, "%v", errorText(err))}
	}
	return parse(path, data)
}

// Read reads one file from r; name is its Path, the name findings carry.
// It checks the file alone: Link checks it against the others loaded with
// it. A compiled catalog is read into memory and read there. The error is
// Faults: every fault in the file.
func Read(name string, r io.Reader) (*File, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, Faults{fault(name, 0, "%v", err)}
	}
	if isCompiled(data) {
		return openCompiled(name, bytes.NewReader(data), int64(len(data)))
	}
	return parse(name, data)
}

// chunkSize is about how many bytes of lines one goroutine reads at a
// time.
const chunkSize = 256 << 10

// parse reads one file's bytes: a header, then lines of its kind. Blank
// lines (nothing but spaces, tabs and a \r) are skipped and counted. The
// lines after the header are read in chunks, in parallel, each line into
// its own slot of the file's lines, then indexed in file order (index).
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
	inParallel(chunks, func() func(*chunk) {
		return func(c *chunk) { c.lines = bytes.Count(c.data, []byte("\n")) + 1 }
	})
	slots := 0
	for _, c := range chunks {
		c.first, slots = slots, slots+c.lines
	}
	var keys []string // each entry slot's key
	switch f.Kind {
	case KindVocabulary:
		f.Entries, keys = make([]Entry, slots), make([]string, slots)
	case KindExtensionSet:
		f.Extensions = make([]Extension, slots)
	case KindSpine:
		f.SpineEntries = make([]SpineEntry, slots)
	case KindCatalog:
		f.Fitments = make([]Fitment, slots)
	case KindStock:
		f.Stock = make([]StockRow, slots)
	}
	inParallel(chunks, func() func(*chunk) {
		r := newReader(f)
		return func(c *chunk) { r.read(c, n, keys) }
	})
	if faults := f.index(chunks, keys); len(faults) > 0 {
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

// chunk is a run of whole lines after the header: its lines are the
// file's lines' slots first to first+lines-1. faults are those of its
// lines that did not read, in line order.
type chunk struct {
	data         []byte
	first, lines int
	faults       Faults
	// redirects and retired are a catalog's lines of those kinds, in line
	// order; its fitments go to the file's slots.
	redirects []Redirect
	retired   []Retired
}

// inParallel calls work with every chunk, on up to GOMAXPROCS goroutines,
// each with its own work, from worker.
func inParallel(chunks []*chunk, worker func() func(*chunk)) {
	if len(chunks) == 1 {
		worker()(chunks[0])
		return
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for range min(runtime.GOMAXPROCS(0), len(chunks)) {
		wg.Go(func() {
			work := worker()
			for i := next.Add(1) - 1; i < int64(len(chunks)); i = next.Add(1) - 1 {
				work(chunks[i])
			}
		})
	}
	wg.Wait()
}

// reader reads lines of one file, a chunk at a time, on one goroutine. A
// line's slices and entry strings come from slabs and blocks, larger
// allocations it shares with the lines after it, rather than allocations
// of their own.
type reader struct {
	f        *File
	line     [3][]byte // an entry line's members, entryNames
	entry    [][]byte  // its entry's fields, the file's Fields
	mapping  [9][]byte // a mapping's members, mappingNames
	mappings [][]byte
	key      []byte

	text     texts
	strings  slab[string]
	mappingS slab[Mapping]
	years    slab[int]

	catalog catalogReader // a catalog's lines
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

// read reads c's lines into their slots, and an entry's key into its slot
// of keys; header is the header's line.
func (r *reader) read(c *chunk, header int, keys []string) {
	f := r.f
	slot := c.first
	for start := 0; start <= len(c.data); slot++ {
		end := lineEnd(c.data, start)
		line := c.data[start:end]
		start = end + 1
		if blank(line) {
			continue
		}
		n := header + 1 + slot
		var err error
		switch f.Kind {
		case KindVocabulary:
			var e Entry
			var key string
			if e, key, err = r.readEntry(n, line); err == nil {
				f.Entries[slot], keys[slot] = e, key
			}
		case KindExtensionSet:
			var x Extension
			if x, err = extension(n, line); err == nil {
				f.Extensions[slot] = x
			}
		case KindSpine:
			var e SpineEntry
			if e, err = spineEntry(n, line); err == nil {
				f.SpineEntries[slot] = e
			}
		case KindCatalog:
			err = r.readCatalogLine(c, slot, n, line)
		case KindStock:
			var row StockRow
			if row, err = f.stockRow(n, line); err == nil {
				f.Stock[slot] = row
			}
		}
		if err != nil {
			c.faults = append(c.faults, fault(f.Path, n, "%v", err))
		}
	}
	c.data = nil
}

// index keeps, of f's lines, those that read and repeat no earlier line's
// entry or id, indexed, and returns every fault in line order: the
// chunks', and the repeats'. keys are the entry slots' keys, needed only
// here: an entry is found by its list order (Entry).
func (f *File) index(chunks []*chunk, keys []string) Faults {
	var faults Faults
	switch f.Kind {
	case KindVocabulary:
		seen := make(map[string]int, len(f.Entries))
		f.targets = map[Target][]int{}
		f.Entries, faults = keep(f.Path, chunks, f.Entries, func(e *Entry) int { return e.Line },
			func(e *Entry, i, slot int) error { return f.addEntry(seen, e, i, keys[slot]) })
	case KindExtensionSet:
		f.extensions = make(map[string]int, len(f.Extensions))
		f.Extensions, faults = keep(f.Path, chunks, f.Extensions, func(x *Extension) int { return x.Line }, f.addExtension)
	case KindSpine:
		f.spine = make(map[string]int, len(f.SpineEntries))
		f.SpineEntries, faults = keep(f.Path, chunks, f.SpineEntries, func(e *SpineEntry) int { return e.Line }, f.addSpineEntry)
	case KindCatalog:
		faults = f.indexCatalog(chunks)
	case KindStock:
		f.stockIDs = make(map[string]int, len(f.Stock))
		f.Stock, faults = keep(f.Path, chunks, f.Stock, func(r *StockRow) int { return r.Line }, f.addStockRow)
	}
	return faults
}

// keep compacts items, the slots of the chunks' lines, to those that read
// (line is not 0) and that add accepts, given the index each takes; nil
// when none do. It returns every fault in line order: the chunks', and
// those of add, about the file at path.
func keep[T any](path string, chunks []*chunk, items []T, line func(*T) int, add func(t *T, i, slot int) error) ([]T, Faults) {
	var faults Faults
	i := 0
	for _, c := range chunks {
		fs := c.faults
		for slot := c.first; slot < c.first+c.lines; slot++ {
			t := &items[slot]
			n := line(t)
			if n == 0 {
				continue // a blank line, or one of fs
			}
			for len(fs) > 0 && fs[0].Line < n {
				faults, fs = append(faults, fs[0]), fs[1:]
			}
			if err := add(t, i, slot); err != nil {
				faults = append(faults, fault(path, n, "%v", err))
				continue
			}
			if i != slot {
				items[i] = *t
			}
			i++
		}
		faults = append(faults, fs...)
		c.faults = nil
	}
	if i == 0 {
		return nil, faults
	}
	clear(items[i:])
	return items[:i], faults
}

// addEntry indexes e, about to be entry i, under its key k in seen,
// refusing it when an earlier entry has the key.
func (f *File) addEntry(seen map[string]int, e *Entry, i int, k string) error {
	if j, dup := seen[k]; dup {
		return errAt("/entry", "duplicate entry: line %d names it too, after normalization", f.Entries[j].Line)
	}
	seen[k] = i
	for _, mp := range e.Mappings {
		idx := f.targets[mp.Target]
		if len(idx) == 0 || idx[len(idx)-1] != i {
			f.targets[mp.Target] = append(idx, i)
		}
	}
	return nil
}

// addExtension indexes x, about to be extension i, refusing it when an
// earlier extension has its id.
func (f *File) addExtension(x *Extension, i, _ int) error {
	if j, dup := f.extensions[x.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", x.ID, f.Extensions[j].Line)
	}
	f.extensions[x.ID] = i
	return nil
}

// addSpineEntry indexes e, about to be spine entry i, refusing it when an
// earlier spine entry has its id.
func (f *File) addSpineEntry(e *SpineEntry, i, _ int) error {
	if j, dup := f.spine[e.ID]; dup {
		return errAt("/id", "duplicate id %q: line %d has it too", e.ID, f.SpineEntries[j].Line)
	}
	f.spine[e.ID] = i
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

// headerNames are a header's fields; headerFields picks each kind's.
var headerNames = []string{"format", "kind", "name", "version", "data_month", "subject", "fields", "spine", "vehicles", "release", "source", "as_of", "catalog"}

// headerFields are the indexes in headerNames of each kind's header
// fields.
var headerFields = map[Kind][]int{
	KindSpine:        {0, 1, 2, 3},
	KindExtensionSet: {0, 1, 2, 3, 4},
	KindVocabulary:   {0, 1, 2, 3, 4, 5, 6, 7},
	KindCatalog:      {0, 1, 2, 3, 4, 7, 8, 9},
	KindStock:        {0, 1, 2, 3, 9, 10, 11, 12},
}

// header reads line 1.
func (f *File) header(line []byte) error {
	raw, err := lineValue(line)
	if err != nil {
		return err
	}
	var v, scratch [13][]byte
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
	names, known := headerFields[f.Kind]
	if !known {
		return errAt("/kind", "unknown kind %q: vocabulary, extension-set, spine, catalog or stock", kind)
	}
	allowed := make([]string, len(names))
	for i, j := range names {
		allowed[i] = headerNames[j]
	}
	if unknown, has, _ := members(raw, allowed, scratch[:len(allowed)]); has {
		return errAt("", "unknown field %q", unknown)
	}
	required := []int{2, 3} // name, version
	switch f.Kind {
	case KindVocabulary:
		required = append(required, 5, 6) // subject, fields
	case KindCatalog:
		required = append(required, 7, 8) // spine, vehicles
	case KindStock:
		required = append(required, 10, 11) // source, as_of
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
	switch f.Kind {
	case KindCatalog:
		return f.catalogHeader(v[7], v[8], v[9])
	case KindStock:
		return f.stockHeader(v[10], v[11], v[12], v[9])
	case KindVocabulary:
	default:
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
		if e.Fields[i], ok = r.str(raw); !ok {
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
	return e, r.text.of(r.key), nil
}

// str reads raw as a JSON string, into r's text blocks when it needs no
// unescaping.
func (r *reader) str(raw []byte) (string, bool) {
	if s, ok := plainString(raw); ok {
		return r.text.of(s), true
	}
	return str(raw)
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
	if !eachMember(raw, func(key, value []byte) {
		k, _ := str(key)
		ms = append(ms, member{k, value})
	}) {
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
