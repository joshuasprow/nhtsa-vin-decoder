package vocab

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/zeebo/blake3"
)

// A compiled catalog (ADR 0021) is a catalog Compile checked and wrote into
// one indexed file, in the artifact's family (ADR 0003): fixed-width rows
// sorted by key, one string pool, and one sorted index per lookup of
// package fitment's Reader. It is read in place through an io.ReaderAt,
// never a memory map: opening it reads its header and its dictionary (part
// types, vehicles and qualifier sets), and each lookup reads the rows and
// strings it answers from. Its answers are its JSON Lines source's.
//
// # Layout
//
// All integers are little-endian; offsets are from the start of the file.
//
//	header, 64 bytes
//	   0  [8]byte  magic "nvcatalg"
//	   8  uint32   format version (CompiledFormat)
//	  12  uint32   section count
//	  16  uint64   file size
//	  24  [8]byte  zero
//	  32  [32]byte blake3 of the file with these 32 bytes zero
//	directory, at 64: per section, uint64 offset and uint64 length
//	meta         the catalog's header, the source's blake3, the files it
//	             was linked against, the writer, the data month its drift
//	             was measured against and those findings, and its counts
//	dict         per part type, in byte order: its name, whether a fitment
//	             or redirect names it, and its rows of fitments, spans,
//	             redirectsBy and retired; every vehicle, in the order of the
//	             first line naming it, with that line, the application on
//	             it (0 for a redirect, else its index plus one) and how many
//	             applications and redirects name it; every distinct set of
//	             qualifiers
//	fitments     36 bytes per fitment, in key order: its rank is its row
//	apps         28 bytes per application, the fitments' in rank order,
//	             each fitment's in file order
//	postings     20 bytes per application, by part type, vehicle, rank
//	             and application, with its side and years
//	spans        16 bytes per part type and vehicle, in that order: its
//	             rows of postings
//	redirects    36 bytes per redirect, in file order
//	redirectsBy  12 bytes per redirect, by part type from, vehicle and row
//	retired      40 bytes per retired number, in key order
//	strings      the string pool: a string is a uint32 offset into it and a
//	             uint32 length, each once
//
// Meta and dict are uvarints, varints and length-prefixed strings. Every
// section starts on an 8-byte boundary, padded with zeros. The bytes are a
// pure function of the source, the files it was linked against, the data
// month and the writer: rebuilding gives the same bytes.

// CompiledFormat is the compiled catalog format this package writes, and
// the only one it reads.
const CompiledFormat = 1

// compiledMagic begins a compiled catalog. No JSON Lines file begins so.
const compiledMagic = "nvcatalg"

// The sections, in file order.
const (
	secMeta = iota
	secDict
	secFitments
	secApps
	secPostings
	secSpans
	secRedirects
	secRedirectsBy
	secRetired
	secStrings
	numSections
)

// headerSize is the header's bytes; the directory follows.
const headerSize = 64

// The rows' widths, by section; 0 for a section of no rows.
var rowWidth = [numSections]int{secFitments: 36, secApps: 28, secPostings: 20, secSpans: 16, secRedirects: 36, secRedirectsBy: 12, secRetired: 40}

// The bits of a row's years byte.
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
	// qualifiers are the distinct sets of qualifiers: an application's
	// are qualifiers[q-1], none for q 0.
	qualifiers [][]Qualifier
	// years are the model years from yearBase that applications share.
	years [yearSpan]int
}

// compiledType is one part type: its name, and its rows in each section
// keyed by part type.
type compiledType struct {
	name string
	// named: a fitment or redirect names it, not only a retired number.
	named                            bool
	ranks, spans, redirects, retired span
}

// isCompiled reports whether a file's first bytes are a compiled
// catalog's.
func isCompiled(prefix []byte) bool { return bytes.HasPrefix(prefix, []byte(compiledMagic)) }

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
		if off%8 != 0 || off < uint64(at) || off > uint64(size) || n > uint64(size)-off || rowWidth[i] > 0 && n%uint64(rowWidth[i]) != 0 {
			return nil, bad("section %d is out of place", i)
		}
		c.sec[i].off, c.sec[i].n, at = int64(off), int64(n), int64(off+n)
	}
	if c.sec[secStrings].n > 1<<32 {
		return nil, bad("its strings are more than 4 GiB")
	}
	for i := range c.years {
		c.years[i] = yearBase + i
	}
	f := &File{Path: path, Kind: KindCatalog, compiled: c}
	copy(f.Hash[:], h[32:])
	meta, dict := make([]byte, c.sec[secMeta].n), make([]byte, c.sec[secDict].n)
	for i, b := range [][]byte{meta, dict} {
		if _, err := r.ReadAt(b, c.sec[secMeta+i].off); err != nil {
			return nil, bad("%v", err)
		}
	}
	if err := c.readMeta(f, meta); err != nil {
		return nil, bad("%v", err)
	}
	if err := c.readDict(dict); err != nil {
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
	c.info = CatalogInfo{
		Name: f.Name, Version: f.Version, Release: f.Release, DataMonth: f.DataMonth, Spine: f.Spine, Vehicles: f.Vehicles, Hash: m.SourceHash,
		Fitments: int(d.uint()), Applications: int(d.uint()), Redirects: int(d.uint()), Retired: int(d.uint()),
	}
	switch {
	case d.err != nil:
		return d.err
	case c.info.Fitments != c.rows(secFitments) || c.info.Applications != c.rows(secApps) || c.info.Applications != c.rows(secPostings) ||
		c.info.Redirects != c.rows(secRedirects) || c.info.Redirects != c.rows(secRedirectsBy) || c.info.Retired != c.rows(secRetired):
		return fmt.Errorf("its counts are not its rows'")
	}
	return nil
}

// readDict reads the dict section: part types, vehicles and qualifier
// sets.
func (c *compiled) readDict(b []byte) error {
	d := &decoder{b: b}
	rows := func(sec int) span {
		lo, hi := d.uint(), d.uint()
		if lo > hi || hi > uint64(c.rows(sec)) {
			d.fail("part type rows out of range")
			return span{}
		}
		return span{int32(lo), int32(hi)}
	}
	n := d.count()
	c.types = make([]compiledType, 0, n)
	for i := range n {
		t := compiledType{name: d.str(), named: d.uint() == 1}
		t.ranks, t.spans, t.redirects, t.retired = rows(secFitments), rows(secSpans), rows(secRedirectsBy), rows(secRetired)
		if i > 0 && t.name <= c.types[i-1].name {
			d.fail("part types out of order")
		}
		c.typeAt[t.name] = uint32(i)
		c.types = append(c.types, t)
	}
	n = d.count()
	c.vehicles, c.uses = make([]*Vehicle, 0, n), make([]use, 0, n)
	var key []byte
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
		key = appendVehicleKey(key[:0], v)
		v.key = string(key)
		c.vehicleAt[v.key] = v.id
		c.vehicles = append(c.vehicles, v)
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
	if d.err == nil && len(d.b) > 0 {
		d.fail("%d bytes after the dictionary", len(d.b))
	}
	return d.err
}

// rows is how many rows section sec holds.
func (c *compiled) rows(sec int) int { return int(c.sec[sec].n) / rowWidth[sec] }

// read fills b from the file at off. A Reader has no error to return, so a
// read that fails, the file cut short or replaced under a running process,
// panics.
func (c *compiled) read(b []byte, off int64) {
	if n, err := c.r.ReadAt(b, off); n < len(b) {
		panic(fmt.Errorf("compiled catalog %s: %v", c.path, err))
	}
}

// rowsAt reads n rows of section sec from row i.
func (c *compiled) rowsAt(sec, i, n int) []byte {
	w := rowWidth[sec]
	if i < 0 || n < 0 || i+n > c.rows(sec) {
		panic(fmt.Errorf("compiled catalog %s: row %d of section %d is out of range: the file is damaged", c.path, i+n, sec))
	}
	b := make([]byte, n*w)
	c.read(b, c.sec[sec].off+int64(i*w))
	return b
}

// u32 and i32 read a row's field at off.
func u32(b []byte, off int) uint32 { return binary.LittleEndian.Uint32(b[off:]) }
func i32(b []byte, off int) int32  { return int32(binary.LittleEndian.Uint32(b[off:])) }

// strRef is a string in the pool.
type strRef struct{ off, n uint32 }

func refAt(b []byte, off int) strRef { return strRef{u32(b, off), u32(b, off+4)} }

// want is a string to read into dst.
type want struct {
	ref strRef
	dst *string
}

// fill reads every string wants names, one read for strings that lie
// within a few KiB of each other: those of fitments of neighbouring ranks
// do.
func (c *compiled) fill(wants []want) {
	const gap = 4 << 10
	slices.SortFunc(wants, func(a, b want) int { return cmp.Compare(a.ref.off, b.ref.off) })
	for i := 0; i < len(wants); {
		if wants[i].ref.n == 0 {
			*wants[i].dst = ""
			i++
			continue
		}
		lo, hi := int64(wants[i].ref.off), int64(wants[i].ref.off)+int64(wants[i].ref.n)
		j := i + 1
		for ; j < len(wants) && int64(wants[j].ref.off) <= hi+gap; j++ {
			hi = max(hi, int64(wants[j].ref.off)+int64(wants[j].ref.n))
		}
		if hi > c.sec[secStrings].n {
			panic(fmt.Errorf("compiled catalog %s: a string out of range: the file is damaged", c.path))
		}
		b := make([]byte, hi-lo)
		c.read(b, c.sec[secStrings].off+lo)
		s := string(b)
		for _, w := range wants[i:j] {
			at := int64(w.ref.off) - lo
			*w.dst = s[at : at+int64(w.ref.n)]
		}
		i = j
	}
}

// year is y as an application's First or Last: shared within the years
// from yearBase.
func (c *compiled) year(y int32) *int {
	if i := int(y) - yearBase; i >= 0 && i < yearSpan {
		return &c.years[i]
	}
	return new(int(y))
}

// years reads a row's first and last years at off, their flags at flags.
func (c *compiled) yearsAt(b []byte, off, flags int) (first, last *int) {
	if b[flags]&hasFirst != 0 {
		first = c.year(i32(b, off))
	}
	if b[flags]&hasLast != 0 {
		last = c.year(i32(b, off+4))
	}
	return first, last
}

// rowHolds reports whether year falls within a row's years at off, their
// flags at flags.
func rowHolds(b []byte, off, flags int, year *int) bool {
	return year == nil || (b[flags]&hasFirst == 0 || *year >= int(i32(b, off))) && (b[flags]&hasLast == 0 || *year <= int(i32(b, off+4)))
}

// typeName is part type t's name.
func (c *compiled) typeName(t uint32) string {
	if int(t) >= len(c.types) {
		panic(fmt.Errorf("compiled catalog %s: part type %d is out of range: the file is damaged", c.path, t))
	}
	return c.types[t].name
}

// vehicle is vehicle id's Vehicle.
func (c *compiled) vehicle(id uint32) *Vehicle {
	if int(id) >= len(c.vehicles) {
		panic(fmt.Errorf("compiled catalog %s: vehicle %d is out of range: the file is damaged", c.path, id))
	}
	return c.vehicles[id]
}

// fitments reads the fitments of ranks, ascending, each run of
// neighbouring ranks in one read, and their applications likewise.
func (c *compiled) fitments(ranks []int32) []Fitment {
	out := make([]Fitment, len(ranks))
	if len(ranks) == 0 {
		return out
	}
	rows := make([][]byte, len(ranks)) // each fitment's row
	for i := 0; i < len(ranks); {
		j := i + 1
		for j < len(ranks) && ranks[j] == ranks[j-1]+1 {
			j++
		}
		b := c.rowsAt(secFitments, int(ranks[i]), j-i)
		for k := i; k < j; k++ {
			rows[k] = b[(k-i)*rowWidth[secFitments]:][:rowWidth[secFitments]]
		}
		i = j
	}
	total := 0
	for _, r := range rows {
		total += int(u32(r, 28))
	}
	// Before allocating by it: a damaged count must end in this panic,
	// never in an allocation no memory holds.
	if total > c.rows(secApps) {
		panic(fmt.Errorf("compiled catalog %s: %d applications for %d fitments, more than it holds: the file is damaged", c.path, total, len(ranks)))
	}
	apps := make([]Application, total)
	wants := make([]want, 0, 2*len(ranks)+total)
	at := 0 // the next of apps
	for i := 0; i < len(rows); {
		// A run of fitments whose applications neighbour each other.
		first, n := int(u32(rows[i], 24)), int(u32(rows[i], 28))
		j := i + 1
		for j < len(rows) && int(u32(rows[j], 24)) == first+n {
			n += int(u32(rows[j], 28))
			j++
		}
		b := c.rowsAt(secApps, first, n)
		for k := i; k < j; k++ {
			r, fit := rows[k], &out[k]
			fit.Line = int(u32(r, 20))
			fit.Key = FitmentKey{Type: c.typeName(u32(r, 0)), Side: Side(r[32])}
			wants = append(wants, want{refAt(r, 4), &fit.Key.Number}, want{refAt(r, 12), &fit.Note})
			if m := int(u32(r, 28)); m > 0 {
				fit.Applications = apps[at : at+m : at+m]
				at += m
			}
			for a := range fit.Applications {
				ar := b[:rowWidth[secApps]]
				b = b[rowWidth[secApps]:]
				app := &fit.Applications[a]
				app.Vehicle = c.vehicle(u32(ar, 0))
				app.First, app.Last = c.yearsAt(ar, 4, 24)
				if q := u32(ar, 20); q > 0 {
					if int(q) > len(c.qualifiers) {
						panic(fmt.Errorf("compiled catalog %s: qualifier set %d is out of range: the file is damaged", c.path, q))
					}
					app.Qualifiers = c.qualifiers[q-1]
				}
				wants = append(wants, want{refAt(ar, 12), &app.Options})
			}
		}
		i = j
	}
	c.fill(wants)
	return out
}

// search finds, within rows [lo, hi) of section sec, the first row that
// cmp, given the row, does not order before what is sought: as
// slices.BinarySearchFunc, reading one row a probe.
func (c *compiled) search(sec int, lo, hi int, cmp func(row []byte) int) (int, bool) {
	found := false
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		switch r := cmp(c.rowsAt(sec, m, 1)); {
		case r < 0:
			lo = m + 1
		default:
			hi, found = m, r == 0
		}
	}
	return lo, found
}

// compareKeyAt orders the key in a row of fitments or retired numbers (its
// side at side, its number at number) against side and number, of the
// row's part type: by side, then number, read only when the sides are one.
func (c *compiled) compareKeyAt(row []byte, side, number int, s Side, n string) int {
	if r := cmp.Compare(Side(row[side]), s); r != 0 {
		return r
	}
	var got string
	c.fill([]want{{refAt(row, number), &got}})
	return strings.Compare(got, n)
}

// rank finds the rank of the fitment with key k.
func (c *compiled) rank(k FitmentKey) (int32, bool) {
	t, ok := c.typeAt[k.Type]
	if !ok {
		return 0, false
	}
	r := c.types[t].ranks
	i, ok := c.search(secFitments, int(r.lo), int(r.hi), func(row []byte) int { return c.compareKeyAt(row, 32, 4, k.Side, k.Number) })
	return int32(i), ok
}

func (c *compiled) byNumber(k FitmentKey) (*Fitment, *Retired) {
	if rank, ok := c.rank(k); ok {
		return &c.fitments([]int32{rank})[0], nil
	}
	t, ok := c.typeAt[k.Type]
	if !ok {
		return nil, nil
	}
	r := c.types[t].retired
	i, ok := c.search(secRetired, int(r.lo), int(r.hi), func(row []byte) int { return c.compareKeyAt(row, 36, 8, k.Side, k.Number) })
	if !ok {
		return nil, nil
	}
	row := c.rowsAt(secRetired, i, 1)
	rt := &Retired{Line: int(u32(row, 0)), Key: FitmentKey{Type: c.typeName(u32(row, 4)), Side: Side(row[36])}, To: FitmentKey{Type: c.typeName(u32(row, 16)), Side: Side(row[37])}}
	c.fill([]want{{refAt(row, 8), &rt.Key.Number}, {refAt(row, 20), &rt.To.Number}, {refAt(row, 28), &rt.Note}})
	rank, ok := c.rank(rt.To)
	if !ok {
		return nil, rt
	}
	return &c.fitments([]int32{rank})[0], rt
}

// byVehicle is Catalog.ByVehicle, cat being the compiled catalog's.
func (c *compiled) byVehicle(cat *Catalog, partType string, vehicles []Vehicle, year *int, side *Side) []Candidate {
	t, ok := c.typeAt[partType]
	if !ok {
		return nil
	}
	sp := c.types[t].spans
	var ps []posting
	for i := range vehicles {
		id, ok := cat.vehicleID(&vehicles[i])
		if !ok {
			continue
		}
		s, ok := c.search(secSpans, int(sp.lo), int(sp.hi), func(row []byte) int { return cmp.Compare(i32(row, 4), id) })
		if !ok {
			continue
		}
		row := c.rowsAt(secSpans, s, 1)
		lo, hi := int(u32(row, 8)), int(u32(row, 12))
		if lo > hi {
			panic(fmt.Errorf("compiled catalog %s: a span out of order: the file is damaged", c.path))
		}
		b := c.rowsAt(secPostings, lo, hi-lo)
		for ; len(b) > 0; b = b[rowWidth[secPostings]:] {
			if side != nil && Side(b[16]) != *side || !rowHolds(b, 8, 17, year) {
				continue
			}
			ps = append(ps, posting{i32(b, 0), i32(b, 4)})
		}
	}
	if len(vehicles) > 1 {
		slices.SortFunc(ps, func(a, b posting) int { return cmp.Or(cmp.Compare(a.rank, b.rank), cmp.Compare(a.app, b.app)) })
		ps = slices.Compact(ps)
	}
	if len(ps) == 0 {
		return nil
	}
	ranks := make([]int32, 0, len(ps))
	for _, p := range ps {
		if n := len(ranks); n == 0 || ranks[n-1] != p.rank {
			ranks = append(ranks, p.rank)
		}
	}
	fits := c.fitments(ranks)
	out := make([]Candidate, 0, len(ranks))
	k := -1
	for _, p := range ps {
		if k < 0 || ranks[k] != p.rank {
			k++
			out = append(out, Candidate{Fitment: &fits[k]})
		}
		if uint(p.app) >= uint(len(fits[k].Applications)) {
			panic(fmt.Errorf("compiled catalog %s: application %d is out of range: the file is damaged", c.path, p.app))
		}
		out[k].Applications = append(out[k].Applications, &fits[k].Applications[p.app])
	}
	return out
}

// redirect is Catalog.Redirect, cat being the compiled catalog's.
func (c *compiled) redirect(cat *Catalog, partType string, vehicles []Vehicle, year *int) []*Redirect {
	t, ok := c.typeAt[partType]
	if !ok {
		return nil
	}
	sp := c.types[t].redirects
	var idx []int32
	for i := range vehicles {
		id, ok := cat.vehicleID(&vehicles[i])
		if !ok {
			continue
		}
		j, _ := c.search(secRedirectsBy, int(sp.lo), int(sp.hi), func(row []byte) int { return cmp.Compare(i32(row, 4), id) })
		for ; j < int(sp.hi); j++ {
			row := c.rowsAt(secRedirectsBy, j, 1)
			if i32(row, 4) != id {
				break
			}
			idx = append(idx, i32(row, 8))
		}
	}
	if len(vehicles) > 1 {
		slices.Sort(idx)
		idx = slices.Compact(idx)
	}
	var out []*Redirect
	var wants []want
	for _, i := range idx {
		row := c.rowsAt(secRedirects, int(i), 1)
		if !rowHolds(row, 8, 32, year) {
			continue
		}
		rd := &Redirect{Line: int(u32(row, 0)), Vehicle: c.vehicle(u32(row, 4)), From: c.typeName(u32(row, 16)), To: c.typeName(u32(row, 20))}
		rd.First, rd.Last = c.yearsAt(row, 8, 32)
		wants = append(wants, want{refAt(row, 24), &rd.Options})
		out = append(out, rd)
	}
	c.fill(wants)
	return out
}

// page is Catalog.Fitments.
func (c *compiled) page(partType string, after *FitmentKey, limit int) []*Fitment {
	lo, hi := 0, c.rows(secFitments)
	if partType != "" {
		t, ok := c.typeAt[partType]
		if !ok {
			return []*Fitment{}
		}
		lo, hi = int(c.types[t].ranks.lo), int(c.types[t].ranks.hi)
	}
	if after != nil {
		// The first rank whose key comes after *after.
		lo, _ = c.search(secFitments, lo, hi, func(row []byte) int {
			if r := strings.Compare(c.typeName(u32(row, 0)), after.Type); r != 0 {
				return r
			}
			if c.compareKeyAt(row, 32, 4, after.Side, after.Number) <= 0 {
				return -1
			}
			return 1
		})
	}
	if limit > 0 && hi-lo > limit {
		hi = lo + limit
	}
	ranks := make([]int32, hi-lo)
	for i := range ranks {
		ranks[i] = int32(lo + i)
	}
	fits := c.fitments(ranks)
	out := make([]*Fitment, len(fits))
	for i := range fits {
		out[i] = &fits[i]
	}
	return out
}

// Expand reads a compiled catalog in full, as its JSON Lines source loads:
// every fitment, redirect and retired number, in file order, indexed, with
// its own vehicles. Link then checks it line by line, and Drift measures
// it again, as `nitrovin vocab check` does. Expand first checks the file's
// blake3 against its header, and refuses a damaged file. Any other file
// is returned as it is.
func (f *File) Expand() (*File, error) {
	c := f.compiled
	if c == nil {
		return f, nil
	}
	h := blake3.New()
	b := make([]byte, 1<<20)
	size := c.sec[secStrings].off + c.sec[secStrings].n
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
	g := &File{Path: f.Path, Hash: f.Hash, Kind: f.Kind, Name: f.Name, Version: f.Version, DataMonth: f.DataMonth, Spine: f.Spine, Vehicles: f.Vehicles, Release: f.Release}
	// Its own vehicles: indexLines numbers them.
	own := make(map[*Vehicle]*Vehicle, len(c.vehicles))
	for _, v := range c.vehicles {
		w := *v
		own[v] = &w
	}
	ranks := make([]int32, c.rows(secFitments))
	for i := range ranks {
		ranks[i] = int32(i)
	}
	g.Fitments = c.fitments(ranks)
	slices.SortFunc(g.Fitments, func(a, b Fitment) int { return cmp.Compare(a.Line, b.Line) })
	for i := range g.Fitments {
		for j := range g.Fitments[i].Applications {
			a := &g.Fitments[i].Applications[j]
			a.Vehicle = own[a.Vehicle]
		}
	}
	g.Redirects = make([]Redirect, c.rows(secRedirects))
	var wants []want
	rows := c.rowsAt(secRedirects, 0, len(g.Redirects))
	for i := range g.Redirects {
		row := rows[i*rowWidth[secRedirects]:]
		rd := &g.Redirects[i]
		*rd = Redirect{Line: int(u32(row, 0)), Vehicle: own[c.vehicle(u32(row, 4))], From: c.typeName(u32(row, 16)), To: c.typeName(u32(row, 20))}
		rd.First, rd.Last = c.yearsAt(row, 8, 32)
		wants = append(wants, want{refAt(row, 24), &rd.Options})
	}
	c.fill(wants)
	n := c.rows(secRetired)
	g.Retired = make([]Retired, n)
	wants = wants[:0]
	rows = c.rowsAt(secRetired, 0, n)
	for i := range g.Retired {
		row := rows[i*rowWidth[secRetired]:]
		rt := &g.Retired[i]
		*rt = Retired{Line: int(u32(row, 0)), Key: FitmentKey{Type: c.typeName(u32(row, 4)), Side: Side(row[36])}, To: FitmentKey{Type: c.typeName(u32(row, 16)), Side: Side(row[37])}}
		wants = append(wants, want{refAt(row, 8), &rt.Key.Number}, want{refAt(row, 20), &rt.To.Number}, want{refAt(row, 28), &rt.Note})
	}
	c.fill(wants)
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
