package vocab

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"

	"github.com/zeebo/blake3"
)

// Compile writes the one JSON Lines catalog among files as a compiled
// catalog (ADR 0021) to w, after checking it as `nitrovin vocab check`
// does: it links files (Link), refusing with Faults, then measures their
// drift against month m (Drift, and ModelDrift when m is a ModelMonth, as
// `vocab check` does) and records the catalog's, with m's name,
// beside the files it was linked against and writer, the nitrovin version
// writing it. It returns the drift it recorded, about the source. The same
// files, month and writer give the same bytes.
func Compile(w io.Writer, m Month, files []*File, writer string) ([]Finding, error) {
	var cat *File
	for _, f := range files {
		switch {
		case f.Kind != KindCatalog:
		case f.compiled != nil:
			return nil, fmt.Errorf("%s is compiled already: compile its JSON Lines source", f.Path)
		case cat != nil:
			return nil, fmt.Errorf("%s and %s are both catalogs: compile one at a time", cat.Path, f.Path)
		default:
			cat = f
		}
	}
	if cat == nil {
		return nil, fmt.Errorf("no catalog among the files to compile")
	}
	if err := Link(files); err != nil {
		return nil, err
	}
	measured := Drift(m, files)
	if mm, ok := m.(ModelMonth); ok {
		measured = append(measured, ModelDrift(mm, files)...)
	}
	var drift []Finding
	for _, d := range measured {
		if d.File == cat.Path {
			drift = append(drift, d)
		}
	}
	slices.SortStableFunc(drift, func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
	var links []LinkedFile
	for _, f := range files {
		if f.Name == cat.Spine || f.Name == cat.Vehicles && cat.Vehicles != Reserved {
			links = append(links, LinkedFile{Name: f.Name, Version: f.Version, Hash: f.Hash})
		}
	}
	// The spine first.
	slices.SortStableFunc(links, func(a, b LinkedFile) int {
		return cmp.Compare(b2i(a.Name != cat.Spine), b2i(b.Name != cat.Spine))
	})
	b, err := compileCatalog(cat, Compiled{Format: CompiledFormat, SourceHash: cat.Hash, Links: links, Writer: writer, DriftMonth: m.Name(), Drift: drift})
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	return drift, nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// compileCatalog is f, a linked JSON Lines catalog, as a compiled catalog's
// bytes, recording meta.
func compileCatalog(f *File, meta Compiled) ([]byte, error) {
	x := f.catalog
	var sec [numSections][]byte

	// Part types, every one a line names, in byte order.
	named := map[string]bool{}
	for i := range f.Fitments {
		named[f.Fitments[i].Key.Type] = true
	}
	for i := range f.Redirects {
		named[f.Redirects[i].From], named[f.Redirects[i].To] = true, true
	}
	for _, rt := range f.Retired {
		for _, t := range []string{rt.Key.Type, rt.To.Type} {
			named[t] = named[t] || false
		}
	}
	types := slices.Sorted(maps.Keys(named))
	typeID := make(map[string]uint32, len(types))
	for i, t := range types {
		typeID[t] = uint32(i)
	}
	ranges := make([][4]span, len(types)) // per type: its rows of fitments, spans, redirectsBy, retired

	// The string pool, each string once, in the order rows name them.
	var pool []byte
	refs := map[string]strRef{}
	var poolErr error
	str := func(s string) strRef {
		if s == "" {
			return strRef{}
		}
		if r, ok := refs[s]; ok {
			return r
		}
		if len(pool)+len(s) > math.MaxUint32 {
			poolErr = fmt.Errorf("%s: its strings are more than 4 GiB, more than a compiled catalog holds", f.Path)
			return strRef{}
		}
		r := strRef{uint32(len(pool)), uint32(len(s))}
		pool = append(pool, s...)
		refs[s] = r
		return r
	}
	putStr := func(b []byte, off int, s string) {
		r := str(s)
		binary.LittleEndian.PutUint32(b[off:], r.off)
		binary.LittleEndian.PutUint32(b[off+4:], r.n)
	}
	putYears := func(b []byte, off, flags int, first, last *int) {
		if first != nil {
			binary.LittleEndian.PutUint32(b[off:], uint32(int32(*first)))
			b[flags] |= hasFirst
		}
		if last != nil {
			binary.LittleEndian.PutUint32(b[off+4:], uint32(int32(*last)))
			b[flags] |= hasLast
		}
	}

	// Qualifier sets, each distinct one once, in the order applications
	// name them.
	var qsets []byte
	nq := 0
	qsetOf := map[string]uint32{}
	qsetAt := map[*Qualifier]uint32{} // a reader shares one slice among equal ones
	qset := func(qs []Qualifier) uint32 {
		if len(qs) == 0 {
			return 0
		}
		if q, ok := qsetAt[&qs[0]]; ok {
			return q
		}
		e := &encoder{}
		e.uint(uint64(len(qs)))
		for _, q := range qs {
			e.int(int64(q.Element))
			e.uint(uint64(len(q.Values)))
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
		q, ok := qsetOf[string(e.b)]
		if !ok {
			nq++
			q = uint32(nq)
			qsetOf[string(e.b)] = q
			qsets = append(qsets, e.b...)
		}
		qsetAt[&qs[0]] = q
		return q
	}

	// Fitments by rank, and their applications.
	fw, aw, pw := rowWidth[secFitments], rowWidth[secApps], rowWidth[secPostings]
	sec[secFitments] = make([]byte, len(x.order)*fw)
	sec[secApps] = make([]byte, x.applications*aw)
	type post struct {
		typ                uint32
		vehicle, rank, app int32
	}
	posts := make([]post, 0, x.applications)
	app := 0
	for rank, i := range x.order {
		fit := &f.Fitments[i]
		t := typeID[fit.Key.Type]
		if ranges[t][0].hi == 0 {
			ranges[t][0].lo = int32(rank)
		}
		ranges[t][0].hi = int32(rank + 1)
		row := sec[secFitments][rank*fw:]
		binary.LittleEndian.PutUint32(row[0:], t)
		putStr(row, 4, fit.Key.Number)
		putStr(row, 12, fit.Note)
		binary.LittleEndian.PutUint32(row[20:], uint32(fit.Line))
		binary.LittleEndian.PutUint32(row[24:], uint32(app))
		binary.LittleEndian.PutUint32(row[28:], uint32(len(fit.Applications)))
		row[32] = byte(fit.Key.Side)
		for j := range fit.Applications {
			a := &fit.Applications[j]
			ar := sec[secApps][app*aw:]
			binary.LittleEndian.PutUint32(ar[0:], uint32(a.Vehicle.id))
			putYears(ar, 4, 24, a.First, a.Last)
			putStr(ar, 12, a.Options)
			binary.LittleEndian.PutUint32(ar[20:], qset(a.Qualifiers))
			posts = append(posts, post{t, a.Vehicle.id, int32(rank), int32(j)})
			app++
		}
	}

	// Postings by part type (their ranks are in type order already), then
	// vehicle, rank and application; and a span per type and vehicle.
	slices.SortStableFunc(posts, func(a, b post) int { return cmp.Or(cmp.Compare(a.typ, b.typ), cmp.Compare(a.vehicle, b.vehicle)) })
	sec[secPostings] = make([]byte, len(posts)*pw)
	var spans []byte
	for i, p := range posts {
		fit := &f.Fitments[x.order[p.rank]]
		a := &fit.Applications[p.app]
		row := sec[secPostings][i*pw:]
		binary.LittleEndian.PutUint32(row[0:], uint32(p.rank))
		binary.LittleEndian.PutUint32(row[4:], uint32(p.app))
		putYears(row, 8, 17, a.First, a.Last)
		row[16] = byte(fit.Key.Side)
		t := p.typ
		n := len(spans) / rowWidth[secSpans]
		if i > 0 && posts[i-1].vehicle == p.vehicle && posts[i-1].typ == t {
			binary.LittleEndian.PutUint32(spans[len(spans)-4:], uint32(i+1))
			continue
		}
		if ranges[t][1].hi == 0 {
			ranges[t][1].lo = int32(n)
		}
		ranges[t][1].hi = int32(n + 1)
		spans = binary.LittleEndian.AppendUint32(spans, t)
		spans = binary.LittleEndian.AppendUint32(spans, uint32(p.vehicle))
		spans = binary.LittleEndian.AppendUint32(spans, uint32(i))
		spans = binary.LittleEndian.AppendUint32(spans, uint32(i+1))
	}
	sec[secSpans] = spans

	// Redirects in file order, and by part type from and vehicle.
	rw := rowWidth[secRedirects]
	sec[secRedirects] = make([]byte, len(f.Redirects)*rw)
	by := make([]int32, len(f.Redirects))
	for i := range f.Redirects {
		rd := &f.Redirects[i]
		row := sec[secRedirects][i*rw:]
		binary.LittleEndian.PutUint32(row[0:], uint32(rd.Line))
		binary.LittleEndian.PutUint32(row[4:], uint32(rd.Vehicle.id))
		putYears(row, 8, 32, rd.First, rd.Last)
		binary.LittleEndian.PutUint32(row[16:], typeID[rd.From])
		binary.LittleEndian.PutUint32(row[20:], typeID[rd.To])
		putStr(row, 24, rd.Options)
		by[i] = int32(i)
	}
	slices.SortFunc(by, func(a, b int32) int {
		ra, rb := &f.Redirects[a], &f.Redirects[b]
		return cmp.Or(cmp.Compare(typeID[ra.From], typeID[rb.From]), cmp.Compare(ra.Vehicle.id, rb.Vehicle.id), cmp.Compare(a, b))
	})
	for i, r := range by {
		rd := &f.Redirects[r]
		t := typeID[rd.From]
		if ranges[t][2].hi == 0 {
			ranges[t][2].lo = int32(i)
		}
		ranges[t][2].hi = int32(i + 1)
		sec[secRedirectsBy] = binary.LittleEndian.AppendUint32(sec[secRedirectsBy], t)
		sec[secRedirectsBy] = binary.LittleEndian.AppendUint32(sec[secRedirectsBy], uint32(rd.Vehicle.id))
		sec[secRedirectsBy] = binary.LittleEndian.AppendUint32(sec[secRedirectsBy], uint32(r))
	}

	// Retired numbers in key order.
	retired := make([]*Retired, len(f.Retired))
	for i := range f.Retired {
		retired[i] = &f.Retired[i]
	}
	slices.SortFunc(retired, func(a, b *Retired) int { return compareFitmentKeys(a.Key, b.Key) })
	tw := rowWidth[secRetired]
	sec[secRetired] = make([]byte, len(retired)*tw)
	for i, rt := range retired {
		t := typeID[rt.Key.Type]
		if ranges[t][3].hi == 0 {
			ranges[t][3].lo = int32(i)
		}
		ranges[t][3].hi = int32(i + 1)
		row := sec[secRetired][i*tw:]
		binary.LittleEndian.PutUint32(row[0:], uint32(rt.Line))
		binary.LittleEndian.PutUint32(row[4:], t)
		putStr(row, 8, rt.Key.Number)
		binary.LittleEndian.PutUint32(row[16:], typeID[rt.To.Type])
		putStr(row, 20, rt.To.Number)
		putStr(row, 28, rt.Note)
		row[36], row[37] = byte(rt.Key.Side), byte(rt.To.Side)
	}
	if poolErr != nil {
		return nil, poolErr
	}
	sec[secStrings] = pool

	// The dictionary.
	d := &encoder{}
	d.uint(uint64(len(types)))
	for i, t := range types {
		d.str(t)
		d.uint(uint64(b2i(named[t])))
		for _, r := range ranges[i] {
			d.uint(uint64(r.lo))
			d.uint(uint64(r.hi))
		}
	}
	// Each vehicle's first naming and how many name it, for its drift at
	// load.
	uses := make([]use, len(x.vehicleList))
	for i := range f.Fitments {
		fit := &f.Fitments[i]
		for j := range fit.Applications {
			uses[fit.Applications[j].Vehicle.id].see(fit.Line, j)
		}
	}
	for i := range f.Redirects {
		uses[f.Redirects[i].Vehicle.id].see(f.Redirects[i].Line, -1)
	}
	d.uint(uint64(len(x.vehicleList)))
	for _, v := range x.vehicleList {
		u := uses[v.id]
		d.uint(uint64(u.line))
		d.uint(uint64(u.app + 1))
		d.uint(uint64(u.n))
		switch {
		case v.Entry != nil:
			d.uint(0)
			d.uint(uint64(len(v.Entry)))
			for _, name := range slices.Sorted(maps.Keys(v.Entry)) {
				d.str(name)
				d.str(v.Entry[name])
			}
		case v.Make != 0:
			d.uint(1)
			d.uint(uint64(v.Make))
		case v.Model != 0:
			d.uint(2)
			d.uint(uint64(v.Model))
		default:
			d.uint(3)
			d.str(v.Ext)
		}
	}
	d.uint(uint64(nq))
	d.b = append(d.b, qsets...)
	sec[secDict] = d.b

	// The meta.
	e := &encoder{}
	for _, s := range []string{f.Name, f.Version, f.Release, f.DataMonth, f.Spine, f.Vehicles} {
		e.str(s)
	}
	e.b = append(e.b, meta.SourceHash[:]...)
	e.uint(uint64(len(meta.Links)))
	for _, l := range meta.Links {
		e.str(l.Name)
		e.str(l.Version)
		e.b = append(e.b, l.Hash[:]...)
	}
	e.str(meta.Writer)
	e.str(meta.DriftMonth)
	e.uint(uint64(len(meta.Drift)))
	for _, fd := range meta.Drift {
		e.uint(uint64(fd.Line))
		e.str(string(fd.Drift))
		e.str(fd.Detail)
	}
	for _, n := range []int{len(f.Fitments), x.applications, len(f.Redirects), len(f.Retired)} {
		e.uint(uint64(n))
	}
	sec[secMeta] = e.b

	// The header, the directory, then each section on an 8-byte boundary.
	size := headerSize + numSections*16
	var offs [numSections]int
	for i, s := range sec {
		size = (size + 7) &^ 7
		offs[i] = size
		size += len(s)
	}
	out := make([]byte, size)
	copy(out, compiledMagic)
	binary.LittleEndian.PutUint32(out[8:], CompiledFormat)
	binary.LittleEndian.PutUint32(out[12:], numSections)
	binary.LittleEndian.PutUint64(out[16:], uint64(size))
	for i, s := range sec {
		binary.LittleEndian.PutUint64(out[headerSize+i*16:], uint64(offs[i]))
		binary.LittleEndian.PutUint64(out[headerSize+i*16+8:], uint64(len(s)))
		copy(out[offs[i]:], s)
	}
	h := blake3.Sum256(out)
	copy(out[32:], h[:])
	return out, nil
}

// encoder writes meta and dict: uvarints, varints and length-prefixed
// strings.
type encoder struct{ b []byte }

func (e *encoder) uint(v uint64) { e.b = binary.AppendUvarint(e.b, v) }
func (e *encoder) int(v int64)   { e.b = binary.AppendVarint(e.b, v) }
func (e *encoder) str(s string) {
	e.uint(uint64(len(s)))
	e.b = append(e.b, s...)
}
