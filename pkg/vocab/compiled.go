package vocab

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"slices"
	"sort"
	"strings"

	"github.com/zeebo/blake3"
)

// A compiled catalog (ADR 0021, ADR 0023) is a catalog Compile checked and
// wrote into one indexed file, in the artifact's family (ADR 0003). It is
// read in place through an io.ReaderAt, never a memory map, so it reads by
// byte range wherever a read is dear, as in a browser's store: opening it
// reads its header and its dictionary, which holds every part type and
// vehicle, the distinct sets of qualifiers, where each vehicle's block is
// and the first key of every page of fitments; each lookup then reads one
// or two runs of bytes. Its answers are its JSON Lines source's.
//
// Format 2 is clustered by vehicle. A vehicle's block holds its part types
// with their counts, its redirects, and a copy of every fitment with an
// application to it, so a vehicle's candidates, part types and related
// vehicles are one or two reads. Every fitment is also stored once in key
// order, in pages, for a number's lookup and for paging. A fitment with
// more than wideApplications applications is not copied whole into each
// of its vehicles' blocks, so the file grows with the applications, never
// with their square: each block holds its key, its applications to that
// vehicle and where it is in wide, a section of only such fitments, whole,
// so a vehicle's few are read together.
//
// # Layout
//
// All fixed-width integers are little-endian; offsets are from the start
// of the file.
//
//	header, 64 bytes
//	   0  [8]byte  magic "nvcatalg"
//	   8  uint32   format version (CompiledFormat)
//	  12  uint32   section count
//	  16  uint64   file size
//	  24  [8]byte  zero
//	  32  [32]byte blake3 of the file with these 32 bytes zero
//	directory, at 64: per section, uint64 offset and uint64 length
//	meta    the catalog's header, the source's blake3, the files it was
//	        linked against, the writer, the data month its drift was
//	        measured against and those findings, the drift kinds it did
//	        not measure, and its counts
//	dict    the part types, in byte order (a type's id is its index):
//	        each one's name and whether a fitment or redirect names it;
//	        the vehicles, in the order of the first line naming each (a
//	        vehicle's id is its index): each one's first naming and count,
//	        its fields or target, and its block's head and body lengths;
//	        the distinct sets of qualifiers (an application's set n is the
//	        nth, 0 for none); and the pages of keyed, each its first
//	        record's key and its length
//	keyed   every fitment and retired number in key order (part type,
//	        side, number), in pages of pageFitments fitments: a fitment
//	        whole, with the retired numbers it replaced; a retired number
//	        with its successor's key
//	wide    every fitment of more than wideApplications applications, in
//	        key order, whole, as keyed has it
//	blocks  per vehicle, in id order: its head, its part types (each its
//	        id, count of fitments and bytes of them in the body) and its
//	        redirects by part type from; then its body, a record per
//	        fitment, by part type, side and number
//
// Meta, dict and records are uvarints, varints, bytes and length-prefixed
// strings. A fitment record is its side, number, line, note and
// applications; an application its vehicle id, years (a flags byte,
// hasFirst and hasLast, then each as a varint), qualifier set and option
// text. Every section starts on an 8-byte boundary, padded with zeros. The
// bytes are a pure function of the source, the files it was linked
// against, the data month and the writer: rebuilding gives the same bytes.

// CompiledFormat is the compiled catalog format this package writes, and
// the only one it reads.
const CompiledFormat = 2

// compiledMagic begins a compiled catalog. No JSON Lines file begins so.
const compiledMagic = "nvcatalg"

// The sections, in file order.
const (
	secMeta = iota
	secDict
	secKeyed
	secWide
	secBlocks
	numSections
)

// headerSize is the header's bytes; the directory follows.
const headerSize = 64

// pageFitments is how many fitments a page of keyed holds.
const pageFitments = 64

// wideApplications is the most applications of a fitment copied whole
// into each of its vehicles' blocks.
const wideApplications = 16

// The kinds of record: in keyed, a fitment or a retired number; in a
// block, a fitment whole (near) or only its applications to the vehicle
// and where it is in keyed (far).
const (
	recFitment = 0
	recRetired = 1
	recNear    = 0
	recFar     = 1
)

// The bits of an application's or redirect's years flags.
const (
	hasFirst = 1 << iota
	hasLast
)

// Compiled is what a compiled catalog records beyond its catalog's header
// (ADR 0021).
type Compiled struct {
	// Format is the compiled format it is written in.
	Format int
	// SourceHash is the blake3 hash of the JSON Lines file it was
	// compiled from.
	SourceHash [32]byte
	// Links are the files it was linked against when compiled: its spine,
	// then its vehicle vocabulary unless its vehicles are vpic.
	Links []LinkedFile
	// Writer is the nitrovin version that compiled it.
	Writer string
	// DriftMonth is the data month its drift was measured against when it
	// was compiled, and Drift those findings, about the compiled file.
	// Loading it measures again only its vehicle drift, against the vehicle
	// vocabulary loaded (Drift); Expand measures all of it.
	DriftMonth string
	Drift      []Finding
	// Skipped are the drift kinds its compiling did not measure, because
	// the month it was given could not (ADR 0023): model-years without
	// vPIC models' years, model-qualifier without their model values. A
	// compile on a server skips none.
	Skipped []DriftKind
}

// LinkedFile is a file a compiled catalog was linked against.
type LinkedFile struct {
	Name, Version string
	// Hash is the blake3 hash of its bytes.
	Hash [32]byte
}

// Compiled is what f, a compiled catalog, records of its compiling; nil
// for any other file.
func (f *File) Compiled() *Compiled {
	if f.compiled == nil {
		return nil
	}
	return &f.compiled.meta
}

// compiled is a compiled catalog, open: its directory and dictionary in
// memory, the rest in its file.
type compiled struct {
	r    io.ReaderAt
	path string
	sec  [numSections]struct{ off, n int64 }

	meta Compiled
	info CatalogInfo

	types     []compiledType // in byte order: a type's id is its index
	typeAt    map[string]uint32
	vehicles  []*Vehicle // a vehicle's id is its index
	vehicleAt map[string]int32
	// uses are, by vehicle id, the line and application first naming it
	// and how many do: its drift's pointer and count.
	uses []use
	// blocks are, by vehicle id, where its block is in blocks.
	blocks []block
	// qualifiers are the distinct sets of qualifiers: an application's
	// are qualifiers[q-1], none for q 0.
	qualifiers [][]Qualifier
	// pages are keyed's pages, each its first record's key and offset in
	// keyed; a page ends where the next begins, the last at keyed's end.
	pages []keyPage
	// years are the model years from yearBase that applications share.
	years [yearSpan]int
}

// compiledType is one part type.
type compiledType struct {
	name string
	// named: a fitment or redirect names it, not only a retired number.
	named bool
}

// block is where a vehicle's block is in blocks: its head, then its body.
type block struct {
	off        int64
	head, body int64
}

// keyPage is one page of keyed.
type keyPage struct {
	typ    uint32
	side   Side
	number string
	off    int64
}

// isCompiled reports whether a file's first bytes are a compiled
// catalog's.
func isCompiled(prefix []byte) bool { return bytes.HasPrefix(prefix, []byte(compiledMagic)) }

// OpenCompiled opens the compiled catalog of size bytes r reads, named
// name, as Open opens one from a path: read in place through r, which
// stays in use for the File's life. It checks the file alone; Link checks
// it against the files loaded with it. A file in another compiled format,
// format 1 included, is refused with a fault saying to compile it again.
func OpenCompiled(name string, r io.ReaderAt, size int64) (*File, error) {
	return openCompiled(name, r, size)
}

// openCompiled opens the compiled catalog of size bytes r reads, loaded
// under path. Its faults are about the file as a whole, line 0.
func openCompiled(path string, r io.ReaderAt, size int64) (*File, error) {
	bad := func(format string, a ...any) error {
		return Faults{fault(path, 0, "not a readable compiled catalog: "+format, a...)}
	}
	var h [headerSize]byte
	if size < headerSize {
		return nil, bad("%d bytes, shorter than its header", size)
	}
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return nil, Faults{fault(path, 0, "%v", errorText(err))}
	}
	if !isCompiled(h[:]) {
		return nil, bad("no compiled catalog's magic: it is not a compiled catalog")
	}
	if v := binary.LittleEndian.Uint32(h[8:]); v != CompiledFormat {
		return nil, Faults{fault(path, 0, "compiled catalog format %d: this nitrovin reads format %d only; compile the catalog again with this release", v, CompiledFormat)}
	}
	if n := binary.LittleEndian.Uint32(h[12:]); n != numSections {
		return nil, bad("%d sections, not %d", n, numSections)
	}
	if n := binary.LittleEndian.Uint64(h[16:]); n != uint64(size) {
		return nil, bad("its header says %d bytes, and it has %d: it was cut short or added to", n, size)
	}
	c := &compiled{r: r, path: path, typeAt: map[string]uint32{}, vehicleAt: map[string]int32{}}
	dir := make([]byte, numSections*16)
	if _, err := r.ReadAt(dir, headerSize); err != nil {
		return nil, bad("%v", err)
	}
	at := int64(headerSize + len(dir))
	for i := range c.sec {
		off, n := binary.LittleEndian.Uint64(dir[i*16:]), binary.LittleEndian.Uint64(dir[i*16+8:])
		if off%8 != 0 || off < uint64(at) || off > uint64(size) || n > uint64(size)-off {
			return nil, bad("section %d is out of place", i)
		}
		c.sec[i].off, c.sec[i].n, at = int64(off), int64(n), int64(off+n)
	}
	for i := range c.years {
		c.years[i] = yearBase + i
	}
	f := &File{Path: path, Kind: KindCatalog, compiled: c}
	copy(f.Hash[:], h[32:])
	// Meta and dict lie side by side: one read.
	head := make([]byte, c.sec[secDict].off+c.sec[secDict].n-c.sec[secMeta].off)
	if _, err := r.ReadAt(head, c.sec[secMeta].off); err != nil {
		return nil, bad("%v", err)
	}
	if err := c.readMeta(f, head[:c.sec[secMeta].n]); err != nil {
		return nil, bad("%v", err)
	}
	if err := c.readDict(head[c.sec[secDict].off-c.sec[secMeta].off:]); err != nil {
		return nil, bad("%v", err)
	}
	return f, nil
}

// readMeta reads the meta section into f's header, c's meta and info.
func (c *compiled) readMeta(f *File, b []byte) error {
	d := &decoder{b: b}
	f.Name, f.Version, f.Release, f.DataMonth, f.Spine, f.Vehicles = d.str(), d.str(), d.str(), d.str(), d.str(), d.str()
	m := &c.meta
	m.Format = CompiledFormat
	m.SourceHash = d.hash()
	for range d.count() {
		m.Links = append(m.Links, LinkedFile{Name: d.str(), Version: d.str(), Hash: d.hash()})
	}
	m.Writer, m.DriftMonth = d.str(), d.str()
	for range d.count() {
		m.Drift = append(m.Drift, Finding{File: c.path, Line: int(d.uint()), Severity: SeverityDrift, Drift: DriftKind(d.str()), Detail: d.str()})
	}
	for range d.count() {
		m.Skipped = append(m.Skipped, DriftKind(d.str()))
	}
	c.info = CatalogInfo{
		Name: f.Name, Version: f.Version, Release: f.Release, DataMonth: f.DataMonth, Spine: f.Spine, Vehicles: f.Vehicles, Hash: m.SourceHash,
		Fitments: int(d.uint()), Applications: int(d.uint()), Redirects: int(d.uint()), Retired: int(d.uint()),
	}
	if d.err == nil && len(d.b) > 0 {
		d.fail("%d bytes after the meta", len(d.b))
	}
	return d.err
}

// readDict reads the dict section: part types, vehicles, qualifier sets
// and pages.
func (c *compiled) readDict(b []byte) error {
	d := &decoder{b: b}
	n := d.count()
	c.types = make([]compiledType, 0, n)
	for i := range n {
		t := compiledType{name: d.str(), named: d.uint() == 1}
		if i > 0 && t.name <= c.types[i-1].name {
			d.fail("part types out of order")
		}
		c.typeAt[t.name] = uint32(i)
		c.types = append(c.types, t)
	}
	n = d.count()
	c.vehicles, c.uses, c.blocks = make([]*Vehicle, 0, n), make([]use, 0, n), make([]block, 0, n)
	var key []byte
	at := int64(0)
	for i := range n {
		v := &Vehicle{id: int32(i)}
		c.uses = append(c.uses, use{line: int(d.uint()), app: int(d.uint()) - 1, n: int(d.uint())})
		switch d.uint() {
		case 0:
			fields := d.count()
			v.Entry = make(map[string]string, fields)
			for range fields {
				name := d.str()
				v.Entry[name] = d.str()
			}
		case 1:
			v.Make = int32(d.uint())
		case 2:
			v.Model = int32(d.uint())
		case 3:
			v.Ext = d.str()
		default:
			d.fail("a vehicle of no kind")
		}
		bl := block{off: at, head: int64(d.uint()), body: int64(d.uint())}
		if bl.head > c.sec[secBlocks].n-at || bl.body > c.sec[secBlocks].n-at-bl.head {
			d.fail("vehicle %d's block is out of range", i)
			break
		}
		at += bl.head + bl.body
		c.blocks = append(c.blocks, bl)
		key = appendVehicleKey(key[:0], v)
		v.key = string(key)
		c.vehicleAt[v.key] = v.id
		c.vehicles = append(c.vehicles, v)
	}
	if d.err == nil && at != c.sec[secBlocks].n {
		d.fail("the vehicles' blocks are %d bytes of %d", at, c.sec[secBlocks].n)
	}
	n = d.count()
	c.qualifiers = make([][]Qualifier, 0, n)
	for range n {
		qs := make([]Qualifier, d.count())
		for i := range qs {
			qs[i].Element = int32(d.int())
			qs[i].Values = make([]Value, d.count())
			for j := range qs[i].Values {
				if d.uint() == 1 {
					qs[i].Values[j] = Value{IsText: true, Text: d.str()}
				} else {
					qs[i].Values[j].Attribute = int32(d.int())
				}
			}
		}
		c.qualifiers = append(c.qualifiers, qs)
	}
	n = d.count()
	c.pages = make([]keyPage, 0, n)
	at = 0
	for i := range n {
		p := keyPage{typ: uint32(d.uint()), off: at}
		if len(d.b) > 0 {
			p.side, d.b = Side(d.b[0]), d.b[1:]
		}
		p.number = d.str()
		size := int64(d.uint())
		switch {
		case int(p.typ) >= len(c.types):
			d.fail("page %d's part type is out of range", i)
		case size == 0 || size > c.sec[secKeyed].n-at:
			d.fail("page %d is out of range", i)
		case i > 0 && comparePages(c.pages[i-1], p) >= 0:
			d.fail("pages out of order")
		}
		if d.err != nil {
			break
		}
		at += size
		c.pages = append(c.pages, p)
	}
	switch {
	case d.err != nil:
	case at != c.sec[secKeyed].n:
		d.fail("the pages are %d bytes of %d", at, c.sec[secKeyed].n)
	case len(d.b) > 0:
		d.fail("%d bytes after the dictionary", len(d.b))
	}
	return d.err
}

func comparePages(a, b keyPage) int {
	return cmp.Or(cmp.Compare(a.typ, b.typ), cmp.Compare(a.side, b.side), strings.Compare(a.number, b.number))
}

// damaged panics: a Reader has no error to return, so a file whose bytes
// contradict themselves, cut short or replaced under a running process,
// ends a lookup in this panic.
func (c *compiled) damaged(format string, a ...any) {
	panic(fmt.Errorf("compiled catalog %s: %s: the file is damaged", c.path, fmt.Sprintf(format, a...)))
}

// read reads n bytes of section sec from off, as one string.
func (c *compiled) read(sec int, off, n int64) string {
	if off < 0 || n < 0 || off > c.sec[sec].n || n > c.sec[sec].n-off {
		c.damaged("bytes %d to %d of section %d are out of range", off, off+n, sec)
	}
	b := make([]byte, n)
	if got, err := c.r.ReadAt(b, c.sec[sec].off+off); got < len(b) {
		c.damaged("%v", err)
	}
	return string(b)
}

// cursor reads records from bytes read: uvarints, varints, bytes and
// length-prefixed strings, each string a slice of what was read. Its
// first failure panics, about a damaged file.
type cursor struct {
	c  *compiled
	s  string
	at int
}

func (r *cursor) more() bool { return r.at < len(r.s) }

func (r *cursor) uint() uint64 {
	var v uint64
	for shift := uint(0); ; shift += 7 {
		if r.at >= len(r.s) || shift > 63 {
			r.c.damaged("a number cut short")
		}
		b := r.s[r.at]
		r.at++
		v |= uint64(b&0x7f) << shift
		if b < 0x80 {
			return v
		}
	}
}

func (r *cursor) int() int64 {
	u := r.uint()
	return int64(u>>1) ^ -int64(u&1)
}

func (r *cursor) byte() byte {
	if r.at >= len(r.s) {
		r.c.damaged("a record cut short")
	}
	b := r.s[r.at]
	r.at++
	return b
}

// count is how many of something follow, each at least one byte.
func (r *cursor) count() int {
	n := r.uint()
	if n > uint64(len(r.s)-r.at) {
		r.c.damaged("a count of %d with %d bytes left", n, len(r.s)-r.at)
	}
	return int(n)
}

func (r *cursor) str() string {
	n := r.count()
	s := r.s[r.at : r.at+n]
	r.at += n
	return s
}

// id reads a part type's or vehicle's id, below n.
func (r *cursor) id(n int, what string) uint32 {
	v := r.uint()
	if v >= uint64(n) {
		r.c.damaged("%s %d is out of range", what, v)
	}
	return uint32(v)
}

// years reads an application's or redirect's years.
func (r *cursor) years() (first, last *int) {
	flags := r.byte()
	if flags&hasFirst != 0 {
		first = r.c.year(r.int())
	}
	if flags&hasLast != 0 {
		last = r.c.year(r.int())
	}
	return first, last
}

// holds reads past years, reporting whether they hold year (nil: any).
func (r *cursor) holds(year *int) bool {
	flags := r.byte()
	ok := true
	if flags&hasFirst != 0 {
		if y := r.int(); year != nil && int64(*year) < y {
			ok = false
		}
	}
	if flags&hasLast != 0 {
		if y := r.int(); year != nil && int64(*year) > y {
			ok = false
		}
	}
	return ok
}

// year is y as an application's First or Last: shared within the years
// from yearBase.
func (c *compiled) year(y int64) *int {
	if i := y - yearBase; i >= 0 && i < yearSpan {
		return &c.years[i]
	}
	return new(int(y))
}

// side reads a side.
func (r *cursor) side() Side {
	s := Side(r.byte())
	if s > SideBoth {
		r.c.damaged("side %d", s)
	}
	return s
}

// fitment reads a fitment record's body, after its part type: its side,
// number, line, note and applications, taking the applications from apps,
// or allocating them for nil.
func (r *cursor) fitment(typ string, apps *slab[Application]) Fitment {
	fit := Fitment{Key: FitmentKey{Type: typ}}
	fit.Key.Side, fit.Key.Number, fit.Line, fit.Note = r.side(), r.str(), int(r.uint()), r.str()
	switch n := r.count(); {
	case n > 0 && apps == nil:
		fit.Applications = make([]Application, n)
	case n > 0:
		fit.Applications = apps.take(n)
	}
	for i := range fit.Applications {
		a := &fit.Applications[i]
		a.Vehicle = r.c.vehicles[r.id(len(r.c.vehicles), "vehicle")]
		r.application(a)
	}
	return fit
}

// matching reads past a fitment record's body, after its part type,
// returning its side and number and the applications to vehicle whose
// years hold year.
func (r *cursor) matching(vehicle uint32, year *int, into []int32) (Side, string, []int32) {
	side, number := r.side(), r.str()
	r.uint()
	r.str()
	n := r.count()
	for i := range n {
		v := r.id(len(r.c.vehicles), "vehicle")
		if r.holds(year) && v == vehicle {
			into = append(into, int32(i))
		}
		r.uint()
		r.str()
	}
	return side, number, into
}

// partial reads a fitment record's body, after its part type, as matched
// applications: its line and note, and the applications of keep,
// ascending, each with its index.
func (r *cursor) partial(keep []int32, into []indexedApp) (int, string, []indexedApp) {
	r.side()
	r.str()
	line, note := int(r.uint()), r.str()
	n := r.count()
	for i := range n {
		v := r.c.vehicles[r.id(len(r.c.vehicles), "vehicle")]
		if len(keep) == 0 || keep[0] != int32(i) {
			r.holds(nil)
			r.uint()
			r.str()
			continue
		}
		keep = keep[1:]
		a := indexedApp{i: int32(i), app: Application{Vehicle: v}}
		r.application(&a.app)
		into = append(into, a)
	}
	if len(keep) > 0 {
		r.c.damaged("application %d of a fitment of %d is out of range", keep[0], n)
	}
	return line, note, into
}

// application reads an application's years, qualifiers and option text
// into a.
func (r *cursor) application(a *Application) {
	a.First, a.Last = r.years()
	if q := r.uint(); q > 0 {
		if q > uint64(len(r.c.qualifiers)) {
			r.c.damaged("qualifier set %d is out of range", q)
		}
		a.Qualifiers = r.c.qualifiers[q-1]
	}
	a.Options = r.str()
}

// indexedApp is an application and its index in its fitment.
type indexedApp struct {
	i   int32
	app Application
}

// appSlab is a slab for the applications of about n fitments: a
// catalog's average is 2.4 (ADR 0017).
func appSlab(n int) slab[Application] { return make(slab[Application], 3*n) }

// typeName is part type t's name.
func (c *compiled) typeName(t uint32) string { return c.types[t].name }

// hit is a fitment one of a request's vehicles reached: where its record
// is, and the applications of it that matched.
type hit struct {
	typ    uint32
	side   Side
	number string
	// rec is its fitment record's body, after its part type: in a block,
	// or, for a far record's fitment read whole, in wide.
	rec  cursor
	keep []int32 // ascending
	// far: its block holds its line, note and its applications to the
	// vehicle, those that matched in own; the whole fitment is n bytes at
	// off in wide.
	far    bool
	line   int
	note   string
	own    []indexedApp
	off, n int64
}

func compareHits(a, b *hit) int {
	return cmp.Or(cmp.Compare(a.typ, b.typ), cmp.Compare(a.side, b.side), strings.Compare(a.number, b.number))
}

// byVehicle is Catalog.ByVehicle, every application of each fitment, when
// whole, and Catalog.Matching otherwise; cat is the compiled catalog's.
// Every part type for partType "": a vehicle's whole block, one read; one
// part type: its head, then that type's records. Only ByVehicle reads
// wide, for a far record's fitment.
func (c *compiled) byVehicle(cat *Catalog, partType string, vehicles []Vehicle, year *int, side *Side, whole bool) []Candidate {
	t, ok := uint32(0), true
	if partType != "" {
		if t, ok = c.typeAt[partType]; !ok {
			return nil
		}
	}
	var hits []*hit
	for _, id := range c.ids(cat, vehicles) {
		bl := c.blocks[id]
		var head, body cursor
		types := []typeRun{}
		if partType == "" {
			s := c.read(secBlocks, bl.off, bl.head+bl.body)
			head, body = cursor{c, s[:bl.head], 0}, cursor{c, s, int(bl.head)}
			types = c.typeRuns(&head)
		} else {
			head = cursor{c, c.read(secBlocks, bl.off, bl.head), 0}
			var at int64
			for _, r := range c.typeRuns(&head) {
				if r.typ == t {
					body = cursor{c, c.read(secBlocks, bl.off+bl.head+at, r.bytes), 0}
					types = append(types, typeRun{typ: t, n: r.n, bytes: r.bytes})
					break
				}
				at += r.bytes
			}
		}
		for _, r := range types {
			end := body.at + int(r.bytes)
			if r.bytes < 0 || end > len(body.s) {
				c.damaged("vehicle %d's part type %d runs past its block", id, r.typ)
			}
			for range r.n {
				h := c.blockHit(&body, r.typ, uint32(id), year, side)
				if h != nil {
					hits = append(hits, h)
				}
			}
			if body.at != end {
				c.damaged("vehicle %d's part type %d is not its bytes", id, r.typ)
			}
		}
	}
	if len(hits) == 0 {
		return nil
	}
	if whole {
		return c.wholeCandidates(hits)
	}
	return c.matchedCandidates(hits)
}

// typeRun is a part type's run of a vehicle's body.
type typeRun struct {
	typ   uint32
	n     int
	bytes int64
}

// typeRuns reads a head's part types, leaving it at its redirects.
func (c *compiled) typeRuns(head *cursor) []typeRun {
	out := make([]typeRun, head.count())
	for i := range out {
		out[i] = typeRun{typ: head.id(len(c.types), "part type"), n: int(head.uint()), bytes: int64(head.uint())}
	}
	return out
}

// blockHit reads one record of a vehicle's body, of part type typ: a hit
// when it has an application to vehicle whose years hold year, and its
// side is *side.
func (c *compiled) blockHit(body *cursor, typ, vehicle uint32, year *int, side *Side) *hit {
	switch kind := body.byte(); kind {
	case recNear:
		start := *body
		s, number, keep := body.matching(vehicle, year, nil)
		if len(keep) == 0 || side != nil && s != *side {
			return nil
		}
		return &hit{typ: typ, side: s, number: number, rec: start, keep: keep}
	case recFar:
		h := &hit{typ: typ, far: true}
		h.side, h.number, h.line, h.note = body.side(), body.str(), int(body.uint()), body.str()
		for range body.count() {
			i := body.uint()
			if i > math.MaxInt32 {
				c.damaged("application %d of a far record is out of range", i)
			}
			a := indexedApp{i: int32(i), app: Application{Vehicle: c.vehicles[vehicle]}}
			body.application(&a.app)
			if holds(a.app.First, a.app.Last, year) {
				h.keep, h.own = append(h.keep, a.i), append(h.own, a)
			}
		}
		h.off, h.n = int64(body.uint()), int64(body.uint())
		if h.off < 0 || h.n <= 0 || h.off > c.sec[secWide].n || h.n > c.sec[secWide].n-h.off {
			c.damaged("vehicle %d's far record is out of range", vehicle)
		}
		if len(h.keep) == 0 || side != nil && h.side != *side {
			return nil
		}
		return h
	default:
		c.damaged("a block record of kind %d", kind)
		return nil
	}
}

// groups calls f with each run of hits of one fitment, in key order.
func groups(hits []*hit, f func(run []*hit)) {
	slices.SortStableFunc(hits, compareHits)
	for i := 0; i < len(hits); {
		j := i + 1
		for ; j < len(hits) && compareHits(hits[j], hits[i]) == 0; j++ {
		}
		f(hits[i:j])
		i = j
	}
}

// wholeCandidates reads the fitments of hits whole, in key order, each
// once with the applications any hit of it matched.
func (c *compiled) wholeCandidates(hits []*hit) []Candidate {
	// Far hits carry where their fitment is in wide: read those, nearby
	// ones together.
	var far []*hit
	for _, h := range hits {
		if h.far {
			far = append(far, h)
		}
	}
	c.readFar(far)
	out := make([]Candidate, 0, len(hits))
	fits := make([]Fitment, 0, len(hits))
	apps := appSlab(len(hits))
	groups(hits, func(run []*hit) {
		keep := run[0].keep
		if len(run) > 1 {
			keep = nil
			for _, h := range run {
				keep = append(keep, h.keep...)
			}
			slices.Sort(keep)
			keep = slices.Compact(keep)
		}
		fits = append(fits, run[0].rec.fitment(c.typeName(run[0].typ), &apps))
		fit := &fits[len(fits)-1]
		cand := Candidate{Fitment: fit, Applications: make([]*Application, len(keep))}
		for k, a := range keep {
			if uint(a) >= uint(len(fit.Applications)) {
				c.damaged("application %d of %s is out of range", a, fit.Key)
			}
			cand.Applications[k] = &fit.Applications[a]
		}
		out = append(out, cand)
	})
	return out
}

// matchedCandidates reads of the fitments of hits only what matched, in
// key order: each fitment's key, line and note and the applications any
// hit of it matched, from the blocks alone.
func (c *compiled) matchedCandidates(hits []*hit) []Candidate {
	out := make([]Candidate, 0, len(hits))
	fits := make([]Fitment, 0, len(hits))
	apps := appSlab(len(hits))
	var matched []indexedApp
	groups(hits, func(run []*hit) {
		fit := Fitment{Key: FitmentKey{Type: c.typeName(run[0].typ), Side: run[0].side, Number: run[0].number}}
		matched = matched[:0]
		for _, h := range run {
			if h.far {
				fit.Line, fit.Note = h.line, h.note
				matched = append(matched, h.own...)
				continue
			}
			r := h.rec
			fit.Line, fit.Note, matched = r.partial(h.keep, matched)
		}
		slices.SortStableFunc(matched, func(a, b indexedApp) int { return cmp.Compare(a.i, b.i) })
		matched = slices.CompactFunc(matched, func(a, b indexedApp) bool { return a.i == b.i })
		fit.Applications = apps.take(len(matched))
		cand := Candidate{Applications: make([]*Application, len(matched))}
		for k := range matched {
			fit.Applications[k] = matched[k].app
			cand.Applications[k] = &fit.Applications[k]
		}
		fits = append(fits, fit)
		cand.Fitment = &fits[len(fits)-1]
		out = append(out, cand)
	})
	return out
}

// farGap is the most bytes between two far fitments' records read
// together, not apart: about what a browser's store moves in the time one
// read costs (ADR 0023: 0.2-0.4 ms a read, about 1 GB/s), so reading the
// bytes between them costs no more than a second read would. farSpan is
// the most one read of them spans, so a request's bytes stay bounded
// however many far fitments lie near each other.
const (
	farGap  = 256 << 10
	farSpan = 1 << 20
)

// readFar points each far hit's rec at its fitment record in wide, read
// in runs of records within farGap of each other and farSpan in all.
func (c *compiled) readFar(hits []*hit) {
	hits = slices.Clone(hits)
	slices.SortFunc(hits, func(a, b *hit) int { return cmp.Compare(a.off, b.off) })
	for i := 0; i < len(hits); {
		lo, hi := hits[i].off, hits[i].off+hits[i].n
		j := i + 1
		for ; j < len(hits) && hits[j].off <= hi+farGap && max(hi, hits[j].off+hits[j].n)-lo <= farSpan; j++ {
			hi = max(hi, hits[j].off+hits[j].n)
		}
		s := c.read(secWide, lo, hi-lo)
		for _, h := range hits[i:j] {
			r := cursor{c, s[h.off-lo : h.off-lo+h.n], 0}
			rec := c.next(&r)
			if rec.kind != recFitment || rec.typ != h.typ || rec.side != h.side || rec.number != h.number {
				c.damaged("a far record names another fitment")
			}
			r.at -= rec.back()
			h.rec = r
		}
		i = j
	}
}

// ids are vehicles' ids in the catalog, each once, ascending.
func (c *compiled) ids(cat *Catalog, vehicles []Vehicle) []int32 {
	var out []int32
	for i := range vehicles {
		if id, ok := cat.vehicleID(&vehicles[i]); ok {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// redirect is Catalog.Redirect, cat being the compiled catalog's: each
// vehicle's head, one read.
func (c *compiled) redirect(cat *Catalog, partType string, vehicles []Vehicle, year *int) []*Redirect {
	t, ok := uint32(0), true
	if partType != "" {
		if t, ok = c.typeAt[partType]; !ok {
			return nil
		}
	}
	var out []*Redirect
	for _, id := range c.ids(cat, vehicles) {
		bl := c.blocks[id]
		head := cursor{c, c.read(secBlocks, bl.off, bl.head), 0}
		c.typeRuns(&head)
		for range head.count() {
			rd := c.redirectAt(&head, id)
			if (partType == "" || c.typeAt[rd.From] == t) && holds(rd.First, rd.Last, year) {
				out = append(out, rd)
			}
		}
		if head.more() {
			c.damaged("vehicle %d's head is not its bytes", id)
		}
	}
	slices.SortFunc(out, func(a, b *Redirect) int { return cmp.Compare(a.Line, b.Line) })
	return out
}

// redirectAt reads a redirect of vehicle id's head.
func (c *compiled) redirectAt(head *cursor, id int32) *Redirect {
	rd := &Redirect{Line: int(head.uint()), Vehicle: c.vehicles[id]}
	rd.From, rd.To = c.typeName(head.id(len(c.types), "part type")), c.typeName(head.id(len(c.types), "part type"))
	rd.First, rd.Last = head.years()
	rd.Options = head.str()
	return rd
}

// pageOf is the page that holds key (t, side, number) if any does: the
// last whose first key is not after it; -1 when every page's is.
func (c *compiled) pageOf(t uint32, side Side, number string) int {
	k := keyPage{typ: t, side: side, number: number}
	return sort.Search(len(c.pages), func(i int) bool { return comparePages(c.pages[i], k) > 0 }) - 1
}

// pageEnd is where page i ends in keyed.
func (c *compiled) pageEnd(i int) int64 {
	if i+1 < len(c.pages) {
		return c.pages[i+1].off
	}
	return c.sec[secKeyed].n
}

// keyedRecord is the key of one record of keyed.
type keyedRecord struct {
	kind   byte
	typ    uint32
	side   Side
	number string
}

// next reads a keyed record's kind and key, leaving r at its line; for a
// fitment, r.at is then rewound by back to read its body (cursor.fitment)
// from its side.
func (c *compiled) next(r *cursor) keyedRecord {
	k := keyedRecord{kind: r.byte()}
	if k.kind > recRetired {
		c.damaged("a keyed record of kind %d", k.kind)
	}
	k.typ = r.id(len(c.types), "part type")
	k.side, k.number = r.side(), r.str()
	return k
}

// back is how many bytes before a record's line its side is.
func (k keyedRecord) back() int { return 1 + uvarintLen(uint64(len(k.number))) + len(k.number) }

// skip reads past the rest of a record next read the key of.
func (c *compiled) skip(r *cursor, k keyedRecord) {
	r.uint()
	r.str()
	if k.kind == recRetired {
		r.id(len(c.types), "part type")
		r.side()
		r.str()
		return
	}
	for range r.count() {
		r.uint()
		r.holds(nil)
		r.uint()
		r.str()
	}
	for range r.count() {
		r.uint()
		r.uint()
		r.side()
		r.str()
		r.str()
	}
}

// keyed finds key k's record: its page, one read, and the cursor at the
// record's line (as next leaves it); ok false when there is none.
func (c *compiled) keyed(k FitmentKey) (keyedRecord, cursor, bool) {
	t, ok := c.typeAt[k.Type]
	if !ok {
		return keyedRecord{}, cursor{}, false
	}
	p := c.pageOf(t, k.Side, k.Number)
	if p < 0 {
		return keyedRecord{}, cursor{}, false
	}
	r := cursor{c, c.read(secKeyed, c.pages[p].off, c.pageEnd(p)-c.pages[p].off), 0}
	for r.more() {
		rec := c.next(&r)
		switch cmp.Or(cmp.Compare(rec.typ, t), cmp.Compare(rec.side, k.Side), strings.Compare(rec.number, k.Number)) {
		case 0:
			return rec, r, true
		case 1:
			return keyedRecord{}, cursor{}, false
		}
		c.skip(&r, rec)
	}
	return keyedRecord{}, cursor{}, false
}

// fitmentAt reads the rest of a fitment record next read rec of.
func (c *compiled) fitmentAt(r *cursor, rec keyedRecord) *Fitment {
	r.at -= rec.back()
	fit := r.fitment(c.typeName(rec.typ), nil)
	return &fit
}

// uvarintLen is how many bytes v takes as a uvarint.
func uvarintLen(v uint64) int {
	n := 1
	for ; v >= 0x80; v >>= 7 {
		n++
	}
	return n
}

// retiredAt reads the rest of a retired record next read rec of.
func (c *compiled) retiredAt(r *cursor, rec keyedRecord) *Retired {
	rt := &Retired{Key: FitmentKey{Type: c.typeName(rec.typ), Side: rec.side, Number: rec.number}}
	rt.Line, rt.Note = int(r.uint()), r.str()
	rt.To.Type = c.typeName(r.id(len(c.types), "part type"))
	rt.To.Side, rt.To.Number = r.side(), r.str()
	return rt
}

func (c *compiled) byNumber(k FitmentKey) (*Fitment, *Retired) {
	rec, r, ok := c.keyed(k)
	switch {
	case !ok:
		return nil, nil
	case rec.kind == recFitment:
		return c.fitmentAt(&r, rec), nil
	}
	rt := c.retiredAt(&r, rec)
	succ, r, ok := c.keyed(rt.To)
	if !ok || succ.kind != recFitment {
		return nil, rt
	}
	return c.fitmentAt(&r, succ), rt
}

// retiredTo returns the retired numbers fitment key k replaced, in line
// order: its record lists them.
func (c *compiled) retiredTo(k FitmentKey) []*Retired {
	rec, r, ok := c.keyed(k)
	if !ok || rec.kind != recFitment {
		return nil
	}
	r.uint()
	r.str()
	for range r.count() {
		r.uint()
		r.holds(nil)
		r.uint()
		r.str()
	}
	var out []*Retired
	for range r.count() {
		rt := &Retired{Line: int(r.uint()), To: k}
		rt.Key.Type = c.typeName(r.id(len(c.types), "part type"))
		rt.Key.Side, rt.Key.Number, rt.Note = r.side(), r.str(), r.str()
		out = append(out, rt)
	}
	return out
}

// page is Catalog.Fitments: the pages from the first key after *after
// that hold limit fitments more, one read.
func (c *compiled) page(partType string, after *FitmentKey, limit int) []*Fitment {
	out := []*Fitment{}
	if len(c.pages) == 0 {
		return out
	}
	var t uint32
	if partType != "" {
		var ok bool
		if t, ok = c.typeAt[partType]; !ok {
			return out
		}
	}
	// from is the first page to read, to the page after the last.
	from, to := 0, len(c.pages)
	if partType != "" {
		from = max(c.pageOf(t, SideNone, ""), 0)
		to = c.pageOf(t+1, SideNone, "") + 1
	}
	if after != nil {
		if at, ok := c.typeAt[after.Type]; ok {
			from = max(from, c.pageOf(at, after.Side, after.Number))
		} else if i := sort.Search(len(c.types), func(i int) bool { return c.types[i].name > after.Type }); i < len(c.types) {
			from = max(from, c.pageOf(uint32(i), SideNone, ""))
		} else {
			return out
		}
	}
	if limit > 0 {
		to = min(to, from+(limit+pageFitments-1)/pageFitments+1)
	}
	if from >= to {
		return out
	}
	r := cursor{c, c.read(secKeyed, c.pages[from].off, c.pageEnd(to-1)-c.pages[from].off), 0}
	apps := appSlab((to - from) * pageFitments)
	for r.more() && (limit < 1 || len(out) < limit) {
		rec := c.next(&r)
		switch {
		case rec.kind != recFitment,
			partType != "" && rec.typ < t,
			after != nil && compareFitmentKeys(FitmentKey{c.typeName(rec.typ), rec.number, rec.side}, *after) <= 0:
			c.skip(&r, rec)
			continue
		case partType != "" && rec.typ > t:
			return out
		}
		r.at -= rec.back()
		fit := r.fitment(c.typeName(rec.typ), &apps)
		for range r.count() { // the retired numbers it replaced
			r.uint()
			r.uint()
			r.side()
			r.str()
			r.str()
		}
		out = append(out, &fit)
	}
	return out
}

// Expand reads a compiled catalog in full, as its JSON Lines source loads:
// every fitment, redirect and retired number, in file order, indexed, with
// its own vehicles. Link then checks it line by line, and Drift measures
// it again, as `nitrovin vocab check` does. Expand first checks the file's
// blake3 against its header, and refuses a damaged file. Any other file
// is returned as it is.
func (f *File) Expand() (g *File, err error) {
	c := f.compiled
	if c == nil {
		return f, nil
	}
	h := blake3.New()
	b := make([]byte, 1<<20)
	size := c.sec[numSections-1].off + c.sec[numSections-1].n
	for off := int64(0); off < size; off += int64(len(b)) {
		chunk := b[:min(int64(len(b)), size-off)]
		if n, err := c.r.ReadAt(chunk, off); n < len(chunk) {
			return nil, Faults{fault(f.Path, 0, "%v", err)}
		}
		if off == 0 {
			clear(chunk[32:64])
		}
		h.Write(chunk)
	}
	if !bytes.Equal(h.Sum(nil), f.Hash[:]) {
		return nil, Faults{fault(f.Path, 0, "its bytes do not hash to the blake3 its header records: the file is damaged")}
	}
	defer func() {
		// A file that hashes as its header says and still contradicts
		// itself was written so.
		if r := recover(); r != nil {
			e, ok := r.(error)
			if !ok {
				panic(r)
			}
			g, err = nil, Faults{fault(f.Path, 0, "%v", e)}
		}
	}()
	g = &File{Path: f.Path, Hash: f.Hash, Kind: f.Kind, Name: f.Name, Version: f.Version, DataMonth: f.DataMonth, Spine: f.Spine, Vehicles: f.Vehicles, Release: f.Release}
	// Its own vehicles: indexLines numbers them.
	own := make(map[*Vehicle]*Vehicle, len(c.vehicles))
	for _, v := range c.vehicles {
		w := *v
		own[v] = &w
	}
	r := cursor{c, c.read(secKeyed, 0, c.sec[secKeyed].n), 0}
	var apps slab[Application]
	for r.more() {
		rec := c.next(&r)
		if rec.kind == recRetired {
			g.Retired = append(g.Retired, *c.retiredAt(&r, rec))
			continue
		}
		r.at -= rec.back()
		fit := r.fitment(c.typeName(rec.typ), &apps)
		for i := range fit.Applications {
			fit.Applications[i].Vehicle = own[fit.Applications[i].Vehicle]
		}
		g.Fitments = append(g.Fitments, fit)
		for range r.count() {
			r.uint()
			r.uint()
			r.side()
			r.str()
			r.str()
		}
	}
	for id, bl := range c.blocks {
		head := cursor{c, c.read(secBlocks, bl.off, bl.head), 0}
		c.typeRuns(&head)
		for range head.count() {
			rd := c.redirectAt(&head, int32(id))
			rd.Vehicle = own[rd.Vehicle]
			g.Redirects = append(g.Redirects, *rd)
		}
	}
	slices.SortFunc(g.Fitments, func(a, b Fitment) int { return cmp.Compare(a.Line, b.Line) })
	slices.SortFunc(g.Redirects, func(a, b Redirect) int { return cmp.Compare(a.Line, b.Line) })
	slices.SortFunc(g.Retired, func(a, b Retired) int { return cmp.Compare(a.Line, b.Line) })
	if faults := g.indexLines(true); len(faults) > 0 {
		return nil, faults
	}
	return g, nil
}

// decoder reads meta and dict: uvarints, varints and length-prefixed
// strings, its error the first that went wrong.
type decoder struct {
	b   []byte
	err error
}

func (d *decoder) fail(format string, a ...any) {
	if d.err == nil {
		d.err = fmt.Errorf(format, a...)
	}
	d.b = nil
}

func (d *decoder) uint() uint64 {
	v, n := binary.Uvarint(d.b)
	if n <= 0 {
		d.fail("a number cut short")
		return 0
	}
	d.b = d.b[n:]
	return v
}

func (d *decoder) int() int64 {
	v, n := binary.Varint(d.b)
	if n <= 0 {
		d.fail("a number cut short")
		return 0
	}
	d.b = d.b[n:]
	return v
}

// count is how many of something follow, each at least one byte.
func (d *decoder) count() int {
	n := d.uint()
	if n > uint64(len(d.b)) {
		d.fail("a count of %d with %d bytes left", n, len(d.b))
		return 0
	}
	return int(n)
}

func (d *decoder) str() string {
	n := d.uint()
	if n > uint64(len(d.b)) {
		d.fail("a string cut short")
		return ""
	}
	s := string(d.b[:n])
	d.b = d.b[n:]
	return s
}

func (d *decoder) hash() (h [32]byte) {
	if len(d.b) < len(h) {
		d.fail("a hash cut short")
		return h
	}
	copy(h[:], d.b)
	d.b = d.b[len(h):]
	return h
}
