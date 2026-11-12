package vocab

import (
	"cmp"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
)

// A stored index is one File as Read made it, indexed and with its entries
// in list order, in a binary form that reads back without parsing the
// file's JSON again: a browser's Service Worker keeps one beside each file
// a visitor stores, so that its cold start costs what reading the index
// costs, not what parsing the file does (ADR 0013). Its bytes are read
// only by the release that wrote them, so the store keys an index by the
// file's blake3 and the release, and the format may change in any
// release; indexMagic and indexFormat guard against a mix-up.

const (
	indexMagic  = "nitrovin vocabulary index\n"
	indexFormat = 1
)

// WriteIndex writes f's stored index to w: everything ReadIndex needs to
// give back a File that answers as f does. A catalog has none: a browser
// compiles it and reads it in place. Nor has a connector's file: it runs
// on a server (ADR 0020). A stock file's rows are written as Read made
// them, before Link resolves their parts and vehicles, which a Set over
// the index resolves again.
func WriteIndex(w io.Writer, f *File) error {
	switch {
	case f.Kind == KindCatalog:
		return fmt.Errorf("%s: a catalog has no stored index", f.Path)
	case f.Connector != nil:
		return fmt.Errorf("%s: a connector's file has no stored index", f.Path)
	}
	e := indexWriter{at: map[string]int{}}
	e.b = append(e.b, f.Hash[:]...)
	for _, s := range []string{f.Path, string(f.Kind), f.Name, f.Version, f.DataMonth, string(f.Subject), f.Spine} {
		e.str(s)
	}
	e.strs(f.Fields)
	e.count(len(f.Entries), f.Entries == nil)
	for i := range f.Entries {
		en := &f.Entries[i]
		e.uint(uint64(en.Line))
		e.strs(en.Fields)
		e.count(len(en.Mappings), en.Mappings == nil)
		for j := range en.Mappings {
			mp := &en.Mappings[j]
			e.int(int64(mp.Make))
			e.int(int64(mp.Model))
			e.str(mp.Ext)
			e.str(mp.ID)
			e.year(mp.First)
			e.year(mp.Last)
			e.count(len(mp.Qualifiers), mp.Qualifiers == nil)
			for _, q := range mp.Qualifiers {
				e.int(int64(q.Element))
				e.count(len(q.Values), q.Values == nil)
				for _, v := range q.Values {
					if v.IsText {
						e.uint(1)
						e.str(v.Text)
					} else {
						e.uint(0)
						e.int(int64(v.Attribute))
					}
				}
			}
			e.str(mp.Origin)
			e.str(mp.Note)
		}
		e.str(en.Note)
	}
	if f.Kind == KindVocabulary {
		for _, se := range f.listOrder() {
			e.uint(uint64(se.at))
			for _, k := range se.key {
				e.str(k)
			}
		}
		// The entries mapping to each target, the targets in the order
		// of their first entry, so the same file writes the same bytes.
		targets := slices.SortedFunc(maps.Keys(f.targets), func(a, b Target) int {
			return cmp.Or(cmp.Compare(f.targets[a][0], f.targets[b][0]), cmp.Compare(a.Make, b.Make),
				cmp.Compare(a.Model, b.Model), cmp.Compare(a.Ext, b.Ext), cmp.Compare(a.ID, b.ID))
		})
		e.uint(uint64(len(targets)))
		for _, t := range targets {
			e.int(int64(t.Make))
			e.int(int64(t.Model))
			e.str(t.Ext)
			e.str(t.ID)
			idx := f.targets[t]
			e.uint(uint64(len(idx)))
			prev := 0
			for _, i := range idx {
				e.uint(uint64(i - prev))
				prev = i
			}
		}
	}
	e.count(len(f.Extensions), f.Extensions == nil)
	for _, x := range f.Extensions {
		e.uint(uint64(x.Line))
		e.str(x.ID)
		e.str(x.MakeName)
		e.int(int64(x.Make))
		e.str(x.MakeExt)
		e.str(x.Name)
	}
	e.count(len(f.SpineEntries), f.SpineEntries == nil)
	for _, s := range f.SpineEntries {
		e.uint(uint64(s.Line))
		e.str(s.ID)
		e.str(s.Name)
	}
	if f.Kind == KindStock {
		e.stock(f)
	}
	head := binary.AppendUvarint([]byte(indexMagic), indexFormat)
	head = binary.AppendUvarint(head, uint64(len(e.b)))
	for _, p := range [][]byte{head, e.b, e.text} {
		if _, err := w.Write(p); err != nil {
			return err
		}
	}
	return nil
}

// stock writes a stock file's header fields and rows.
func (e *indexWriter) stock(f *File) {
	for _, s := range []string{string(f.Holds), f.AsOf, f.StockCatalog, f.Release} {
		e.str(s)
	}
	e.count(len(f.Stock), f.Stock == nil)
	for i := range f.Stock {
		r := &f.Stock[i]
		e.uint(uint64(r.Line))
		e.str(r.ID)
		e.str(r.Location)
		e.present(r.Key != nil)
		if k := r.Key; k != nil {
			e.str(k.Type)
			e.str(k.Number)
			e.uint(uint64(k.Side))
		}
		e.present(r.Part != nil)
		if p := r.Part; p != nil {
			e.str(p.Spine)
			e.str(p.ID)
			e.str(p.Vocabulary)
			e.count(len(p.Entry), p.Entry == nil)
			for _, k := range slices.Sorted(maps.Keys(p.Entry)) {
				e.str(k)
				e.str(p.Entry[k])
			}
		}
		e.present(r.Decoded != nil)
		if d := r.Decoded; d != nil {
			e.int(int64(d.Make))
			e.int(int64(d.Model))
			e.int(int64(d.Year))
		}
		e.str(r.VIN)
		e.present(r.vehicle != nil)
		if r.vehicle != nil {
			e.str(string(r.vehicle))
		}
		e.year(r.Year)
		e.str(string(r.Fields))
	}
}

// present writes whether an optional value follows.
func (e *indexWriter) present(ok bool) {
	if ok {
		e.uint(1)
		return
	}
	e.uint(0)
}

// indexWriter builds an index: its body, and every string once in text,
// which the body refers to by offset and length.
type indexWriter struct {
	b    []byte
	text []byte
	at   map[string]int
}

func (e *indexWriter) uint(v uint64) { e.b = binary.AppendUvarint(e.b, v) }
func (e *indexWriter) int(v int64)   { e.b = binary.AppendVarint(e.b, v) }

// count writes a slice's length, telling a nil slice from an empty one:
// what was read is given back exactly.
func (e *indexWriter) count(n int, isNil bool) {
	if isNil {
		e.uint(0)
		return
	}
	e.uint(uint64(n) + 1)
}

func (e *indexWriter) str(s string) {
	off, ok := e.at[s]
	if !ok {
		off = len(e.text)
		e.text = append(e.text, s...)
		e.at[s] = off
	}
	e.uint(uint64(off))
	e.uint(uint64(len(s)))
}

func (e *indexWriter) strs(ss []string) {
	e.count(len(ss), ss == nil)
	for _, s := range ss {
		e.str(s)
	}
}

// year writes a model year bound, nil included.
func (e *indexWriter) year(y *int) {
	if y == nil {
		e.uint(0)
		return
	}
	e.uint(1)
	e.int(int64(*y))
}

// errIndex is an index ReadIndex cannot read: cut short, or not one this
// release wrote.
var errIndex = errors.New("vocabulary index: damaged")

// ReadIndex reads a stored index WriteIndex wrote, refusing one of another
// format or one it cannot read. The File it gives answers as the one
// written did. Its strings share one copy of data's text.
func ReadIndex(data []byte) (*File, error) {
	rest, ok := cutPrefix(data, indexMagic)
	if !ok {
		return nil, errors.New("vocabulary index: not an index")
	}
	format, n := binary.Uvarint(rest)
	if n <= 0 {
		return nil, errIndex
	}
	if format != indexFormat {
		return nil, fmt.Errorf("vocabulary index: format %d; this release reads format %d", format, indexFormat)
	}
	rest = rest[n:]
	size, n := binary.Uvarint(rest)
	if n <= 0 || size > uint64(len(rest)-n) {
		return nil, errIndex
	}
	rest = rest[n:]
	d := &indexReader{b: rest[:size], text: string(rest[size:])}
	f, err := d.file()
	if err != nil {
		return nil, err
	}
	if len(d.b) != 0 {
		return nil, errIndex
	}
	return f, nil
}

func cutPrefix(b []byte, prefix string) ([]byte, bool) {
	if len(b) < len(prefix) || string(b[:len(prefix)]) != prefix {
		return nil, false
	}
	return b[len(prefix):], true
}

// file reads the body: the File, then its indexes, rebuilt.
func (d *indexReader) file() (*File, error) {
	f := &File{}
	if len(d.b) < len(f.Hash) {
		return nil, errIndex
	}
	copy(f.Hash[:], d.b)
	d.b = d.b[len(f.Hash):]
	f.Path = d.str()
	f.Kind = Kind(d.str())
	f.Name = d.str()
	f.Version = d.str()
	f.DataMonth = d.str()
	f.Subject = Subject(d.str())
	f.Spine = d.str()
	f.Fields = d.strs(nil)
	nf := len(f.Fields)
	if n, ok := d.count(); ok {
		f.Entries = make([]Entry, n)
	}
	fields := make([]string, nf*len(f.Entries))
	var mappings slab[Mapping]
	var quals slab[Qualifier]
	var values slab[Value]
	var years slab[int]
	for i := range f.Entries {
		en := &f.Entries[i]
		en.Line = d.line()
		en.Fields = d.strs(fields[i*nf : i*nf : (i+1)*nf])
		if n, ok := d.count(); ok {
			en.Mappings = mappings.take(n)
		}
		for j := range en.Mappings {
			mp := &en.Mappings[j]
			mp.Make = d.int32()
			mp.Model = d.int32()
			mp.Ext = d.str()
			mp.ID = d.str()
			mp.First = d.year(&years)
			mp.Last = d.year(&years)
			if n, ok := d.count(); ok {
				mp.Qualifiers = quals.take(n)
			}
			for k := range mp.Qualifiers {
				q := &mp.Qualifiers[k]
				q.Element = d.int32()
				if n, ok := d.count(); ok {
					q.Values = values.take(n)
				}
				for v := range q.Values {
					val := &q.Values[v]
					switch d.uint() {
					case 0:
						val.Attribute = d.int32()
					case 1:
						val.IsText, val.Text = true, d.str()
					default:
						d.fail()
					}
				}
			}
			mp.Origin = d.str()
			mp.Note = d.str()
		}
		en.Note = d.str()
		if d.err != nil {
			return nil, d.err
		}
	}
	if f.Kind == KindVocabulary {
		keys := make([]string, nf*len(f.Entries))
		f.listed = make([]sortedEntry, len(f.Entries))
		seen := make([]bool, len(f.Entries))
		for i := range f.listed {
			j := d.uint()
			if j >= uint64(len(f.Entries)) || seen[j] {
				return nil, errIndex
			}
			seen[j] = true
			key := keys[i*nf : (i+1)*nf : (i+1)*nf]
			for k := range key {
				key[k] = d.str()
			}
			f.listed[i] = sortedEntry{&f.Entries[j], key, int(j)}
		}
		f.listOnce.Do(func() {})
		n := d.length()
		f.targets = make(map[Target][]int, n)
		var idxs slab[int]
		for range n {
			t := Target{Make: d.int32(), Model: d.int32(), Ext: d.str(), ID: d.str()}
			idx := idxs.take(d.length())
			prev := uint64(0)
			for k := range idx {
				prev += d.uint()
				if prev >= uint64(len(f.Entries)) || (k > 0 && prev == uint64(idx[k-1])) {
					return nil, errIndex
				}
				idx[k] = int(prev)
			}
			f.targets[t] = idx
		}
	}
	if n, ok := d.count(); ok {
		f.Extensions = make([]Extension, n)
	}
	for i := range f.Extensions {
		x := &f.Extensions[i]
		x.Line = d.line()
		x.ID = d.str()
		x.MakeName = d.str()
		x.Make = d.int32()
		x.MakeExt = d.str()
		x.Name = d.str()
	}
	if n, ok := d.count(); ok {
		f.SpineEntries = make([]SpineEntry, n)
	}
	for i := range f.SpineEntries {
		s := &f.SpineEntries[i]
		s.Line = d.line()
		s.ID = d.str()
		s.Name = d.str()
	}
	if f.Kind == KindStock {
		d.stock(f)
	}
	if d.err != nil {
		return nil, d.err
	}
	f.reindex()
	return f, nil
}

// reindex rebuilds an extension set's, a declared spine's or a stock
// file's lookups from its lines, as index made them.
func (f *File) reindex() {
	switch f.Kind {
	case KindExtensionSet:
		f.extensions = make(map[string]int, len(f.Extensions))
		for i, x := range f.Extensions {
			f.extensions[x.ID] = i
		}
	case KindSpine:
		f.spine = make(map[string]int, len(f.SpineEntries))
		for i, s := range f.SpineEntries {
			f.spine[s.ID] = i
		}
	case KindStock:
		f.stockIDs = make(map[string]int, len(f.Stock))
		for i, r := range f.Stock {
			f.stockIDs[r.ID] = i
		}
	}
}

// stock reads a stock file's header fields and rows, as stock wrote them.
func (d *indexReader) stock(f *File) {
	f.Holds = StockHolds(d.str())
	f.AsOf = d.str()
	f.StockCatalog = d.str()
	f.Release = d.str()
	if n, ok := d.count(); ok {
		f.Stock = make([]StockRow, n)
	}
	var years slab[int]
	for i := range f.Stock {
		r := &f.Stock[i]
		r.Line = d.line()
		r.ID = d.str()
		r.Location = d.str()
		if d.present() {
			r.Key = &FitmentKey{Type: d.str(), Number: d.str()}
			if r.Key.Side = Side(d.uint()); r.Key.Side > SideBoth {
				d.fail()
			}
		}
		if d.present() {
			r.Part = &StockPart{Spine: d.str(), ID: d.str(), Vocabulary: d.str()}
			if n, ok := d.count(); ok {
				r.Part.Entry = make(map[string]string, n)
				for range n {
					k := d.str()
					r.Part.Entry[k] = d.str()
				}
			}
		}
		if d.present() {
			r.Decoded = &StockDecoded{Make: d.int32(), Model: d.int32(), Year: int(d.int())}
		}
		r.VIN = d.str()
		if d.present() {
			r.vehicle = []byte(d.str())
		}
		r.Year = d.year(&years)
		r.Fields = json.RawMessage(d.str())
		if d.err != nil {
			return
		}
	}
}

// present reads whether an optional value follows.
func (d *indexReader) present() bool {
	switch d.uint() {
	case 0:
		return false
	case 1:
		return true
	}
	d.fail()
	return false
}

// indexReader reads an index's body, its strings from text. Its first
// error sticks: every read after it gives zero values.
type indexReader struct {
	b    []byte
	text string
	err  error
}

func (d *indexReader) fail() {
	if d.err == nil {
		d.err = errIndex
	}
	d.b = nil
}

func (d *indexReader) uint() uint64 {
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *indexReader) int() int64 {
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail()
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *indexReader) int32() int32 {
	v := d.int()
	if int64(int32(v)) != v {
		d.fail()
	}
	return int32(v)
}

func (d *indexReader) line() int {
	v := d.uint()
	if v > 1<<31 {
		d.fail()
	}
	return int(v)
}

// count reads a slice's length, false for a nil slice.
func (d *indexReader) count() (int, bool) {
	n := d.length()
	if n == 0 {
		return 0, false
	}
	return n - 1, true
}

// length reads a length. One the rest of the body cannot hold, each
// element taking a byte at least, is a damaged index, so a bad length
// never allocates much.
func (d *indexReader) length() int {
	v := d.uint()
	if v > uint64(len(d.b))+1 {
		d.fail()
		return 0
	}
	return int(v)
}

func (d *indexReader) str() string {
	off, n := d.uint(), d.uint()
	if off > uint64(len(d.text)) || n > uint64(len(d.text))-off {
		d.fail()
		return ""
	}
	return d.text[off : off+n]
}

// strs reads a slice of strings into buf's capacity, or a new slice when
// it has too little.
func (d *indexReader) strs(buf []string) []string {
	n, ok := d.count()
	if !ok {
		return nil
	}
	if cap(buf) < n {
		buf = make([]string, 0, n)
	}
	buf = buf[:n]
	for i := range buf {
		buf[i] = d.str()
	}
	return buf
}

func (d *indexReader) year(years *slab[int]) *int {
	switch d.uint() {
	case 0:
		return nil
	case 1:
		y := &years.take(1)[0]
		*y = int(d.int())
		return y
	}
	d.fail()
	return nil
}
