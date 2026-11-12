package vocab

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/zeebo/blake3"
)

// CompileOptions are what Compile is given beyond its catalog, the files
// beside it and its scratch storage.
type CompileOptions struct {
	// Writer is the nitrovin version compiling, which the file records.
	Writer string
}

// Compile writes the JSON Lines catalog src, named name (the path its
// findings carry), as a compiled catalog (ADR 0021, ADR 0023) to w, after
// checking it as `nitrovin vocab check` does, against files, the files
// loaded beside it (its spine and vehicle vocabulary among them): every
// fault refuses it (Faults), and its drift against month m is measured
// (Drift, and ModelDrift when m is a ModelMonth) and recorded, with m's
// name, the files it was linked against and opt.Writer. It returns that
// drift, about src. The same source, files, month and writer give the same
// bytes.
//
// It streams: src is read once, front to back, and its fitments, redirects
// and retired numbers are sorted in scratch, about twice the compiled file,
// in runs of at most runBytes held in memory, two and a half of them at
// once, so its memory grows with the vehicles, part types and distinct sets
// of qualifiers the catalog names (thousands), not with its lines. The kinds of drift m cannot measure, model-years without vPIC
// models' years (YearsMonth) and model-qualifier without their model
// values (ModelMonth), are recorded as skipped.
func Compile(w io.Writer, m Month, files []*File, src io.Reader, name string, scratch Scratch, opt CompileOptions) ([]Finding, error) {
	if scratch == nil {
		return nil, errors.New("compile: no scratch storage")
	}
	byName := map[string]*File{}
	for _, f := range files {
		if f.Kind == KindCatalog {
			if f.compiled != nil {
				return nil, fmt.Errorf("%s is compiled already: compile its JSON Lines source", f.Path)
			}
			return nil, fmt.Errorf("%s and %s are both catalogs: compile one at a time", f.Path, name)
		}
		byName[f.Name] = f
	}
	c := &compiler{
		name: name, m: m, files: files, byName: byName, scratch: scratch, opt: opt,
		vehicleAt: map[string]int32{}, types: map[string]bool{}, qsetOf: map[string]uint32{},
		// The retired numbers' sorters hold a quarter of a run each: they
		// are a release's changes, seldom more than a few MiB.
		byKey: newSorter(scratch, 1), byVehicle: newSorter(scratch, 1), byRetired: newSorter(scratch, 4), bySuccessor: newSorter(scratch, 4),
	}
	c.mm, _ = m.(ModelMonth)
	defer c.close()
	if err := c.readSource(src); err != nil {
		return nil, err
	}
	if err := c.mergeKeys(); err != nil {
		return nil, err
	}
	if faults := append(c.readFaults, c.indexFaults...); len(faults) > 0 {
		slices.SortStableFunc(faults, func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
		return nil, faults
	}
	if faults := c.link.faults; len(faults) > 0 {
		slices.SortStableFunc(faults, func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
		return nil, faults
	}
	if err := c.mergeVehicles(); err != nil {
		return nil, err
	}
	var drift []Finding
	for _, g := range c.drift {
		g.report()
		drift = append(drift, g.d.out...)
	}
	slices.SortStableFunc(drift, func(a, b Finding) int { return cmp.Compare(a.Line, b.Line) })
	if err := c.write(w, drift); err != nil {
		return nil, err
	}
	return drift, nil
}

// sourceBatch is how many bytes of its source Compile reads at a time, or
// more for a longer line.
var sourceBatch = 4 << 20

// compiler is one Compile's state.
type compiler struct {
	name    string
	m       Month
	mm      ModelMonth // m, when it is one
	files   []*File
	byName  map[string]*File
	scratch Scratch
	opt     CompileOptions
	hdr     *File // the catalog's header; its lines are never held
	hash    [32]byte

	link                    *catalogLink
	drift                   []*catalogDrift // Drift's, and ModelDrift's when mm is set
	readFaults, indexFaults Faults

	vehicles                                   []*Vehicle // a vehicle's id is its index
	vehicleAt                                  map[string]int32
	types                                      map[string]bool // every part type a line names: whether a fitment or redirect does
	typeID                                     map[string]uint32
	typeNames                                  []string          // by id
	qsetOf                                     map[string]uint32 // a set of qualifiers' encoding: its id
	qsets                                      []byte            // every set's encoding, in id order
	fitments, applications, redirects, retired int

	// byKey has every fitment by key; byVehicle, per vehicle, its
	// redirects by part type from, then its fitments by key; byRetired
	// every retired number by its key, and bySuccessor by its successor's.
	byKey, byVehicle, byRetired, bySuccessor *sorter
	// keyed is the keyed section as written, and pages its pages; wideSec
	// the wide section, and wide where in it each wide fitment is, by
	// line.
	keyed   *spill
	pages   []keyPage
	wideSec *spill
	wide    map[int]span64
	// heads and bodies are the vehicles' blocks as written, apart;
	// blocks their lengths, by vehicle.
	heads, bodies *spill
	blocks        []block

	buf, key, val, pay []byte // reused encodings
	ids                []int32
	qs                 []uint32
}

// close releases the scratch files still open.
func (c *compiler) close() {
	for _, s := range []*spill{c.keyed, c.wideSec, c.heads, c.bodies} {
		if s != nil {
			s.f.Close()
		}
	}
	for _, s := range []*sorter{c.byKey, c.byVehicle, c.byRetired, c.bySuccessor} {
		if s.out != nil {
			s.out.f.Close()
			s.out = nil
		}
	}
}

// readSource reads src once: its header, then its lines a batch at a
// time, each batch read in parallel as Read reads a file, then seen in
// line order (fitment, redirect, retiredLine).
func (c *compiler) readSource(src io.Reader) error {
	h := blake3.New()
	data := make([]byte, 0, sourceBatch)
	line := 0 // the lines before data
	for eof := false; !eof; {
		n, err := io.ReadFull(src, data[len(data):cap(data)])
		h.Write(data[len(data) : len(data)+n])
		data = data[:len(data)+n]
		switch {
		case err == io.EOF || err == io.ErrUnexpectedEOF:
			eof = true
		case err != nil:
			return Faults{fault(c.name, 0, "%v", errorText(err))}
		}
		end := len(data)
		if !eof {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 { // a line longer than data holds
				data = slices.Grow(data, cap(data))
				continue
			}
			end = i
		}
		if err := c.batch(data[:end], &line); err != nil {
			return err
		}
		if !eof {
			data = data[:copy(data, data[end+1:])]
		}
	}
	if c.hdr == nil {
		return Faults{fault(c.name, 1, "no header: the file is empty")}
	}
	copy(c.hash[:], h.Sum(nil))
	return nil
}

// batch reads lines, whole lines without the last one's end, after line
// lines; line becomes the number of the last of them.
func (c *compiler) batch(lines []byte, line *int) error {
	for c.hdr == nil {
		end := lineEnd(lines, 0)
		text, last := lines[:end], end == len(lines)
		*line++
		if !last {
			lines = lines[end+1:]
		}
		if !blank(text) {
			if err := c.header(text, *line); err != nil {
				return err
			}
		}
		if last {
			return nil
		}
	}
	first := *line + 1 // the first line's number
	var chunks []*chunk
	slots := 0
	for start := 0; start <= len(lines); {
		end := len(lines)
		if start+chunkSize < len(lines) {
			if i := bytes.IndexByte(lines[start+chunkSize:], '\n'); i >= 0 {
				end = start + chunkSize + i
			}
		}
		ch := &chunk{data: lines[start:end], first: slots}
		ch.lines = bytes.Count(ch.data, []byte("\n")) + 1
		slots += ch.lines
		chunks = append(chunks, ch)
		start = end + 1
	}
	*line += slots
	f := &File{Path: c.name, Kind: KindCatalog, Vehicles: c.hdr.Vehicles, Fitments: make([]Fitment, slots)}
	inParallel(chunks, func() func(*chunk) {
		r := newReader(f)
		return func(ch *chunk) { r.read(ch, first-1, nil) }
	})
	for _, ch := range chunks {
		c.readFaults = append(c.readFaults, ch.faults...)
		rd, rt := ch.redirects, ch.retired
		for slot := ch.first; slot < ch.first+ch.lines; slot++ {
			n := first + slot
			switch {
			case f.Fitments[slot].Line == n:
				c.fitment(&f.Fitments[slot])
			case len(rd) > 0 && rd[0].Line == n:
				c.redirect(&rd[0])
				rd = rd[1:]
			case len(rt) > 0 && rt[0].Line == n:
				c.retiredLine(&rt[0])
				rt = rt[1:]
			}
		}
	}
	return nil
}

// header reads the catalog's header, on line n, and checks it against the
// files beside it.
func (c *compiler) header(text []byte, n int) error {
	f := &File{Path: c.name}
	if err := f.header(text); err != nil {
		return Faults{fault(c.name, n, "%v", err)}
	}
	if f.Kind != KindCatalog {
		return fmt.Errorf("%s is %s, not a catalog: compile a catalog", c.name, f.Kind.noun())
	}
	c.hdr = f
	ext := func(ref string) (*Extension, string) { return resolveExt(c.byName, ref) }
	c.link = newCatalogLink(f, c.byName, ext)
	if g, dup := c.byName[f.Name]; dup {
		c.link.faults = append(c.link.faults, fault(c.name, 1, "/name: %q is also the name of %s", f.Name, g.Path))
	}
	var vehicles *File
	if f.Vehicles != Reserved {
		vehicles = c.byName[f.Vehicles]
	}
	d := newDrifts(c.m, nil)
	d.f = f
	c.drift = append(c.drift, d.catalogDrift(vehicles))
	if c.mm != nil {
		dm := newDrifts(c.m, c.mm)
		dm.f = f
		c.drift = append(c.drift, dm.catalogDrift(vehicles))
	}
	return nil
}

// canon is the catalog's one Vehicle for v, numbered on first sight.
func (c *compiler) canon(v *Vehicle) *Vehicle {
	if id, ok := c.vehicleAt[v.key]; ok {
		return c.vehicles[id]
	}
	v.id = int32(len(c.vehicles))
	c.vehicleAt[v.key] = v.id
	c.vehicles = append(c.vehicles, v)
	c.link.vehicle(v)
	for _, g := range c.drift {
		g.vehicle(v)
	}
	return v
}

// qset is the id of a set of qualifiers, 0 for none: each distinct set
// numbered from 1 in the order applications name them.
func (c *compiler) qset(qs []Qualifier) uint32 {
	if len(qs) == 0 {
		return 0
	}
	e := &encoder{b: c.buf[:0]}
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
	c.buf = e.b
	if id, ok := c.qsetOf[string(e.b)]; ok {
		return id
	}
	id := uint32(len(c.qsetOf) + 1)
	c.qsetOf[string(e.b)] = id
	c.qsets = append(c.qsets, e.b...)
	return id
}

// fitment sees a fitment line: checks it, measures its drift, and adds it
// to both sorters, whole by key, and per vehicle it names, whole or, when
// it is wide, as its applications to that vehicle.
func (c *compiler) fitment(fit *Fitment) {
	for j := range fit.Applications {
		a := &fit.Applications[j]
		a.Vehicle = c.canon(a.Vehicle)
	}
	c.link.fitment(fit)
	c.fitments++
	c.applications += len(fit.Applications)
	c.types[fit.Key.Type] = true

	// The record: side, number, line, note, applications.
	b := append(c.val[:0], byte(fit.Key.Side))
	b = appendStr(b, fit.Key.Number)
	b = binary.AppendUvarint(b, uint64(fit.Line))
	b = appendStr(b, fit.Note)
	b = binary.AppendUvarint(b, uint64(len(fit.Applications)))
	ids, qs := c.ids[:0], c.qs[:0]
	for j := range fit.Applications {
		a := &fit.Applications[j]
		q := c.qset(a.Qualifiers)
		for _, g := range c.drift {
			g.application(fit.Line, j, a, qualifierKey{id: q})
		}
		b = binary.AppendUvarint(b, uint64(a.Vehicle.id))
		b = appendApplication(b, a, q)
		ids, qs = append(ids, a.Vehicle.id), append(qs, q)
	}
	c.qs = qs
	c.val = b

	k := appendOrdered(c.key[:0], fit.Key.Type)
	k = append(k, byte(fit.Key.Side))
	k = appendOrdered(k, fit.Key.Number)
	k = binary.BigEndian.AppendUint32(k, uint32(fit.Line))
	c.key = k
	p := appendStr(c.pay[:0], fit.Key.Type)
	c.byKey.add(k, append(p, b...))

	slices.Sort(ids)
	ids = slices.Compact(ids)
	c.ids = ids
	wide := len(fit.Applications) > wideApplications
	for _, id := range ids {
		vk := binary.BigEndian.AppendUint32(c.buf[:0], uint32(id))
		vk = append(vk, 1)
		vk = append(vk, k...)
		c.buf = vk
		p = appendStr(p[:0], fit.Key.Type)
		if !wide {
			p = append(append(p, recNear), b...)
		} else {
			// What answers for this vehicle: the key, line and note, and
			// its own applications whole; wide holds the rest.
			p = append(p, recFar, byte(fit.Key.Side))
			p = appendStr(p, fit.Key.Number)
			p = binary.AppendUvarint(p, uint64(fit.Line))
			p = appendStr(p, fit.Note)
			own := 0
			for j := range fit.Applications {
				if fit.Applications[j].Vehicle.id == id {
					own++
				}
			}
			p = binary.AppendUvarint(p, uint64(own))
			for j := range fit.Applications {
				if a := &fit.Applications[j]; a.Vehicle.id == id {
					p = binary.AppendUvarint(p, uint64(j))
					p = appendApplication(p, a, qs[j])
				}
			}
		}
		c.byVehicle.add(vk, p)
	}
	c.pay = p
}

// redirect sees a redirect line.
func (c *compiler) redirect(rd *Redirect) {
	rd.Vehicle = c.canon(rd.Vehicle)
	c.link.redirect(rd)
	for _, g := range c.drift {
		g.redirect(rd)
	}
	c.redirects++
	c.types[rd.From], c.types[rd.To] = true, true
	k := binary.BigEndian.AppendUint32(c.key[:0], uint32(rd.Vehicle.id))
	k = append(k, 0)
	k = appendOrdered(k, rd.From)
	k = binary.BigEndian.AppendUint32(k, uint32(rd.Line))
	c.key = k
	p := appendStr(c.val[:0], rd.From)
	p = appendStr(p, rd.To)
	p = binary.AppendUvarint(p, uint64(rd.Line))
	p = appendYears(p, rd.First, rd.Last)
	p = appendStr(p, rd.Options)
	c.val = p
	c.byVehicle.add(k, p)
}

// retiredLine sees a retired line, and adds it to its two sorters: by its
// key, and by its successor's.
func (c *compiler) retiredLine(rt *Retired) {
	for _, t := range []string{rt.Key.Type, rt.To.Type} {
		c.types[t] = c.types[t] || false
	}
	c.retired++
	for i, ks := range [2][2]FitmentKey{{rt.Key, rt.To}, {rt.To, rt.Key}} {
		k := appendFitmentKey(c.key[:0], ks[0])
		k = binary.BigEndian.AppendUint32(k, uint32(rt.Line))
		c.key = k
		p := appendKeyFields(c.val[:0], ks[0])
		p = binary.AppendUvarint(p, uint64(rt.Line))
		p = appendKeyFields(p, ks[1])
		p = appendStr(p, rt.Note)
		c.val = p
		[]*sorter{c.byRetired, c.bySuccessor}[i].add(k, p)
	}
}

// appendFitmentKey appends fitment key k as a sorter's key: ordered as
// compareFitmentKeys orders keys.
func appendFitmentKey(b []byte, k FitmentKey) []byte {
	b = appendOrdered(b, k.Type)
	b = append(b, byte(k.Side))
	return appendOrdered(b, k.Number)
}

// appendKeyFields appends fitment key k as a payload's fields.
func appendKeyFields(b []byte, k FitmentKey) []byte {
	b = appendStr(b, k.Type)
	b = append(b, byte(k.Side))
	return appendStr(b, k.Number)
}

// retiredAt reads a retired number as retiredLine added it to sorter
// byRetired (succ false) or bySuccessor (succ true).
func retiredAt(payload []byte, succ bool) Retired {
	r := &bcursor{b: payload}
	key := func() FitmentKey {
		k := FitmentKey{Type: r.str()}
		k.Side, k.Number = Side(r.byte()), r.str()
		return k
	}
	var rt Retired
	first := key()
	rt.Line = int(r.uint())
	second := key()
	rt.Note = r.str()
	if succ {
		rt.To, rt.Key = first, second
	} else {
		rt.Key, rt.To = first, second
	}
	return rt
}

// pull is a merge read a record ahead: a retired number, ok false after
// the last.
type pull struct {
	m    *merge
	succ bool
	rt   Retired
	ok   bool
}

func (p *pull) next() error {
	_, payload, ok, err := p.m.next()
	p.ok = ok
	if ok {
		p.rt = retiredAt(payload, p.succ)
	}
	return err
}

// mergeKeys writes keyed from the fitments in key order and the retired
// numbers among them, each fitment with the retired numbers it replaced;
// and finds the faults only the whole catalog shows (indexLines'): a
// repeated fitment key, and a retired number repeated, live, or whose
// successor is no fitment, the last two only when every line read.
func (c *compiler) mergeKeys() error {
	c.typeNames = slices.Sorted(maps.Keys(c.types))
	c.typeID = make(map[string]uint32, len(c.typeNames))
	for i, t := range c.typeNames {
		c.typeID[t] = uint32(i)
	}
	var err error
	if c.keyed, err = newSpill(c.scratch); err != nil {
		return err
	}
	if c.wideSec, err = newSpill(c.scratch); err != nil {
		return err
	}
	c.wide = map[int]span64{}
	pageFits := 0
	record := func(fitment bool, k FitmentKey) {
		if len(c.pages) == 0 || fitment && pageFits == pageFitments {
			c.pages = append(c.pages, keyPage{typ: c.typeID[k.Type], side: k.Side, number: k.Number, off: c.keyed.size})
			pageFits = 0
		}
		if fitment {
			pageFits++
		}
	}
	// The retired numbers, by key and by successor, read beside the
	// fitments by key.
	pulls := [2]*pull{{succ: false}, {succ: true}}
	for i, s := range []*sorter{c.byRetired, c.bySuccessor} {
		if pulls[i].m, err = s.sorted(); err != nil {
			return err
		}
		defer pulls[i].m.close()
		if err := pulls[i].next(); err != nil {
			return err
		}
	}
	byKey, bySucc := pulls[0], pulls[1]
	// The faults about retired numbers, by line: a repeated one's alone,
	// else a live one's before a missing successor's, as indexLines finds
	// them.
	type retiredFault struct {
		line, order int
		f           Finding
	}
	var faults []retiredFault
	repeated := map[int]bool{} // the lines of retired numbers retired again
	var firstKey FitmentKey    // the last retired key seen, and its first line
	firstLine := 0
	// retired writes the retired number byKey is at, after finding whether
	// it repeats an earlier one.
	retired := func(live int) error {
		rt := &byKey.rt
		if firstLine > 0 && rt.Key == firstKey {
			repeated[rt.Line] = true
			faults = append(faults, retiredFault{rt.Line, 0, duplicateRetired(c.name, rt, firstLine)})
		} else {
			firstKey, firstLine = rt.Key, rt.Line
			if live > 0 {
				faults = append(faults, retiredFault{rt.Line, 1, liveRetired(c.name, rt, live)})
			}
		}
		record(false, rt.Key)
		b := append(c.buf[:0], recRetired)
		b = binary.AppendUvarint(b, uint64(c.typeID[rt.Key.Type]))
		b = append(b, byte(rt.Key.Side))
		b = appendStr(b, rt.Key.Number)
		b = binary.AppendUvarint(b, uint64(rt.Line))
		b = appendStr(b, rt.Note)
		b = binary.AppendUvarint(b, uint64(c.typeID[rt.To.Type]))
		b = append(b, byte(rt.To.Side))
		b = appendStr(b, rt.To.Number)
		c.buf = b
		if _, err := c.keyed.Write(b); err != nil {
			return err
		}
		return byKey.next()
	}
	missing := func() error {
		rt := &bySucc.rt
		faults = append(faults, retiredFault{rt.Line, 2, noSuccessor(c.name, rt)})
		return bySucc.next()
	}
	var prev FitmentKey
	prevLine := 0
	var replaced []Retired
	err = c.byKey.each(func(_, payload []byte) error {
		r := &bcursor{b: payload}
		typ := r.str()
		body := r.b
		side := Side(r.byte())
		number := r.str()
		line := int(r.uint())
		r.str() // note
		napps := int(r.uint())
		k := FitmentKey{Type: typ, Number: number, Side: side}
		for byKey.ok && compareFitmentKeys(byKey.rt.Key, k) <= 0 {
			live := 0
			if byKey.rt.Key == k {
				live = line
			}
			if err := retired(live); err != nil {
				return err
			}
		}
		if prevLine > 0 && prev == k {
			c.indexFaults = append(c.indexFaults, duplicateKey(c.name, line, prevLine))
		}
		for bySucc.ok && compareFitmentKeys(bySucc.rt.To, k) < 0 {
			if err := missing(); err != nil {
				return err
			}
		}
		replaced = replaced[:0]
		for bySucc.ok && bySucc.rt.To == k && prev != k {
			replaced = append(replaced, bySucc.rt)
			if err := bySucc.next(); err != nil {
				return err
			}
		}
		prev, prevLine = k, line
		record(true, k)
		b := append(c.buf[:0], recFitment)
		b = binary.AppendUvarint(b, uint64(c.typeID[typ]))
		b = append(b, body...)
		b = binary.AppendUvarint(b, uint64(len(replaced)))
		for i := range replaced {
			rt := &replaced[i]
			b = binary.AppendUvarint(b, uint64(rt.Line))
			b = binary.AppendUvarint(b, uint64(c.typeID[rt.Key.Type]))
			b = append(b, byte(rt.Key.Side))
			b = appendStr(b, rt.Key.Number)
			b = appendStr(b, rt.Note)
		}
		c.buf = b
		if _, err := c.keyed.Write(b); err != nil {
			return err
		}
		if napps > wideApplications {
			c.wide[line] = span64{c.wideSec.size, int64(len(b))}
			if _, err := c.wideSec.Write(b); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for byKey.ok {
		if err := retired(0); err != nil {
			return err
		}
	}
	for bySucc.ok {
		if err := missing(); err != nil {
			return err
		}
	}
	read := len(c.readFaults) == 0
	slices.SortStableFunc(faults, func(a, b retiredFault) int { return cmp.Or(cmp.Compare(a.line, b.line), cmp.Compare(a.order, b.order)) })
	for _, f := range faults {
		// A repeated number's fault is its only one; a fitment that did
		// not read is not missing.
		if f.order == 0 || read && !repeated[f.line] {
			c.indexFaults = append(c.indexFaults, f.f)
		}
	}
	if err := c.wideSec.done(); err != nil {
		return err
	}
	return c.keyed.done()
}

// mergeVehicles writes each vehicle's block, its head to heads and its
// body to bodies, from its redirects and fitments in order; and finds the
// redirects no application of their target part type answers.
func (c *compiler) mergeVehicles() error {
	var err error
	if c.heads, err = newSpill(c.scratch); err != nil {
		return err
	}
	if c.bodies, err = newSpill(c.scratch); err != nil {
		return err
	}
	c.blocks = make([]block, 0, len(c.vehicles))
	var (
		runs  []typeRun
		reds  []pendingRedirect
		cur   = int32(-1)
		start = int64(0) // the vehicle's body's start in bodies
	)
	flush := func() error {
		b := binary.AppendUvarint(c.buf[:0], uint64(len(runs)))
		for _, r := range runs {
			b = binary.AppendUvarint(b, uint64(r.typ))
			b = binary.AppendUvarint(b, uint64(r.n))
			b = binary.AppendUvarint(b, uint64(r.bytes))
		}
		b = binary.AppendUvarint(b, uint64(len(reds)))
		for i := range reds {
			p := &reds[i]
			b = binary.AppendUvarint(b, uint64(p.rd.Line))
			b = binary.AppendUvarint(b, uint64(p.from))
			b = binary.AppendUvarint(b, uint64(p.to))
			b = appendYears(b, p.rd.First, p.rd.Last)
			b = appendStr(b, p.rd.Options)
			if !p.answered {
				rd := p.rd
				for _, g := range c.drift {
					g.unanswered(&rd)
				}
			}
		}
		c.buf = b
		if _, err := c.heads.Write(b); err != nil {
			return err
		}
		c.blocks = append(c.blocks, block{head: int64(len(b)), body: c.bodies.size - start})
		start = c.bodies.size
		runs, reds = runs[:0], reds[:0]
		return nil
	}
	err = c.byVehicle.each(func(key, payload []byte) error {
		id := int32(binary.BigEndian.Uint32(key))
		for cur < id {
			if cur >= 0 {
				if err := flush(); err != nil {
					return err
				}
			}
			cur++
		}
		r := &bcursor{b: payload}
		typ := c.typeID[string(r.bytes())]
		if key[4] == 0 { // a redirect
			p := pendingRedirect{from: typ, to: c.typeID[string(r.bytes())]}
			p.rd = Redirect{Line: int(r.uint()), Vehicle: c.vehicles[id], From: c.typeNames[typ], To: c.typeNames[p.to]}
			p.rd.First, p.rd.Last = r.years()
			p.rd.Options = r.str()
			reds = append(reds, p)
			return nil
		}
		if n := len(runs); n == 0 || runs[n-1].typ != typ {
			runs = append(runs, typeRun{typ: typ})
		}
		run := &runs[len(runs)-1]
		run.n++
		rec := r.b
		if rec[0] == recFar {
			r.byte()
			r.byte()
			r.bytes()
			line := int(r.uint())
			at, ok := c.wide[line]
			if !ok {
				return fmt.Errorf("compile: the wide fitment on line %d is not in wide", line)
			}
			rec = binary.AppendUvarint(slices.Clone(rec), uint64(at.off))
			rec = binary.AppendUvarint(rec, uint64(at.n))
		}
		if _, err := c.bodies.Write(rec); err != nil {
			return err
		}
		run.bytes += int64(len(rec))
		answer(reds, typ, rec, id)
		return nil
	})
	if err != nil {
		return err
	}
	for int(cur) < len(c.vehicles) {
		if cur >= 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		cur++
	}
	if err := c.heads.done(); err != nil {
		return err
	}
	return c.bodies.done()
}

// pendingRedirect is a redirect of the vehicle whose block is being
// written: whether an application of its target part type to the vehicle
// answers it is known once the block is.
type pendingRedirect struct {
	rd       Redirect
	from, to uint32
	answered bool
}

// answer marks the redirects of reds to part type typ that rec, a block
// record of vehicle id of that type, answers: an application of it to the
// vehicle in years that overlap the redirect's (catalogIndex.answers).
func answer(reds []pendingRedirect, typ uint32, rec []byte, id int32) {
	if !slices.ContainsFunc(reds, func(p pendingRedirect) bool { return p.to == typ && !p.answered }) {
		return
	}
	r := &bcursor{b: rec}
	kind := r.byte()
	r.byte()  // side
	r.bytes() // number
	r.uint()  // line
	r.bytes() // note
	for range r.uint() {
		v := id
		if kind == recNear {
			v = int32(r.uint())
		} else {
			r.uint() // its index
		}
		first, last := r.years()
		r.uint()  // qualifiers
		r.bytes() // options
		if v != id {
			continue
		}
		for i := range reds {
			p := &reds[i]
			if p.to == typ && (first == nil || p.rd.Last == nil || *first <= *p.rd.Last) && (p.rd.First == nil || last == nil || *p.rd.First <= *last) {
				p.answered = true
			}
		}
	}
}

// write writes the compiled file to w, its blake3 found first: the
// header, the directory, meta and dict, then keyed and each vehicle's
// block, its head and body put together.
func (c *compiler) write(w io.Writer, drift []Finding) error {
	meta, dict := c.meta(drift), c.dict()
	var offs, lens [numSections]int64
	lens[secMeta], lens[secDict], lens[secKeyed], lens[secWide] = int64(len(meta)), int64(len(dict)), c.keyed.size, c.wideSec.size
	lens[secBlocks] = c.heads.size + c.bodies.size
	size := int64(headerSize + numSections*16)
	for i := range offs {
		size = (size + 7) &^ 7
		offs[i] = size
		size += lens[i]
	}
	put := func(w io.Writer, hash []byte) error {
		bw := bufio.NewWriterSize(w, 1<<20)
		b := make([]byte, headerSize+numSections*16)
		copy(b, compiledMagic)
		binary.LittleEndian.PutUint32(b[8:], CompiledFormat)
		binary.LittleEndian.PutUint32(b[12:], numSections)
		binary.LittleEndian.PutUint64(b[16:], uint64(size))
		copy(b[32:], hash)
		for i := range offs {
			binary.LittleEndian.PutUint64(b[headerSize+i*16:], uint64(offs[i]))
			binary.LittleEndian.PutUint64(b[headerSize+i*16+8:], uint64(lens[i]))
		}
		at := int64(len(b))
		bw.Write(b)
		pad := func(i int) {
			bw.Write(make([]byte, offs[i]-at))
			at = offs[i] + lens[i]
		}
		pad(secMeta)
		bw.Write(meta)
		pad(secDict)
		bw.Write(dict)
		pad(secKeyed)
		if err := c.keyed.copyTo(bw, 0, c.keyed.size); err != nil {
			return err
		}
		pad(secWide)
		if err := c.wideSec.copyTo(bw, 0, c.wideSec.size); err != nil {
			return err
		}
		pad(secBlocks)
		heads := bufio.NewReaderSize(io.NewSectionReader(c.heads.f, 0, c.heads.size), 256<<10)
		bodies := bufio.NewReaderSize(io.NewSectionReader(c.bodies.f, 0, c.bodies.size), 1<<20)
		for _, bl := range c.blocks {
			if _, err := io.CopyN(bw, heads, bl.head); err != nil {
				return err
			}
			if _, err := io.CopyN(bw, bodies, bl.body); err != nil {
				return err
			}
		}
		return bw.Flush()
	}
	h := blake3.New()
	if err := put(h, make([]byte, 32)); err != nil {
		return err
	}
	return put(w, h.Sum(nil))
}

// meta is the meta section: the catalog's header, the source's hash, the
// files it was linked against, the writer, the drift and its month, the
// kinds skipped, and the counts.
func (c *compiler) meta(drift []Finding) []byte {
	f := c.hdr
	e := &encoder{}
	for _, s := range []string{f.Name, f.Version, f.Release, f.DataMonth, f.Spine, f.Vehicles} {
		e.str(s)
	}
	e.b = append(e.b, c.hash[:]...)
	var links []*File // the spine first
	for _, name := range []string{f.Spine, f.Vehicles} {
		if g := c.byName[name]; g != nil {
			links = append(links, g)
		}
	}
	e.uint(uint64(len(links)))
	for _, l := range links {
		e.str(l.Name)
		e.str(l.Version)
		e.b = append(e.b, l.Hash[:]...)
	}
	e.str(c.opt.Writer)
	e.str(c.m.Name())
	e.uint(uint64(len(drift)))
	for _, fd := range drift {
		e.uint(uint64(fd.Line))
		e.str(string(fd.Drift))
		e.str(fd.Detail)
	}
	skipped := c.skipped()
	e.uint(uint64(len(skipped)))
	for _, k := range skipped {
		e.str(string(k))
	}
	for _, n := range []int{c.fitments, c.applications, c.redirects, c.retired} {
		e.uint(uint64(n))
	}
	return e.b
}

// skipped are the drift kinds c's month cannot measure.
func (c *compiler) skipped() []DriftKind {
	var out []DriftKind
	if _, ok := c.m.(YearsMonth); !ok {
		out = append(out, DriftModelYears)
	}
	if c.mm == nil {
		out = append(out, DriftModelQualifier)
	}
	return out
}

// dict is the dict section: part types, vehicles with their blocks'
// lengths, qualifier sets and pages.
func (c *compiler) dict() []byte {
	d := &encoder{}
	d.uint(uint64(len(c.typeNames)))
	for _, t := range c.typeNames {
		d.str(t)
		d.uint(uint64(b2i(c.types[t])))
	}
	uses := c.drift[0].firsts
	d.uint(uint64(len(c.vehicles)))
	for i, v := range c.vehicles {
		u := uses[i]
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
		d.uint(uint64(c.blocks[i].head))
		d.uint(uint64(c.blocks[i].body))
	}
	d.uint(uint64(len(c.qsetOf)))
	d.b = append(d.b, c.qsets...)
	d.uint(uint64(len(c.pages)))
	for i, p := range c.pages {
		end := c.keyed.size
		if i+1 < len(c.pages) {
			end = c.pages[i+1].off
		}
		d.uint(uint64(p.typ))
		d.b = append(d.b, byte(p.side))
		d.str(p.number)
		d.uint(uint64(end - p.off))
	}
	return d.b
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// encoder writes meta and dict: uvarints, varints and length-prefixed
// strings.
type encoder struct{ b []byte }

func (e *encoder) uint(v uint64) { e.b = binary.AppendUvarint(e.b, v) }
func (e *encoder) int(v int64)   { e.b = binary.AppendVarint(e.b, v) }
func (e *encoder) str(s string)  { e.b = appendStr(e.b, s) }

// appendStr appends s, length-prefixed.
func appendStr(b []byte, s string) []byte {
	return append(binary.AppendUvarint(b, uint64(len(s))), s...)
}

// appendApplication appends an application's years, qualifier set q and
// option text.
func appendApplication(b []byte, a *Application, q uint32) []byte {
	b = appendYears(b, a.First, a.Last)
	b = binary.AppendUvarint(b, uint64(q))
	return appendStr(b, a.Options)
}

// appendYears appends years first and last: their flags, then each set.
func appendYears(b []byte, first, last *int) []byte {
	var flags byte
	if first != nil {
		flags |= hasFirst
	}
	if last != nil {
		flags |= hasLast
	}
	b = append(b, flags)
	if first != nil {
		b = binary.AppendVarint(b, int64(*first))
	}
	if last != nil {
		b = binary.AppendVarint(b, int64(*last))
	}
	return b
}

// bcursor reads back the records compiling wrote.
type bcursor struct{ b []byte }

func (r *bcursor) byte() byte {
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *bcursor) uint() uint64 {
	v, n := binary.Uvarint(r.b)
	r.b = r.b[n:]
	return v
}

func (r *bcursor) int() int64 {
	v, n := binary.Varint(r.b)
	r.b = r.b[n:]
	return v
}

func (r *bcursor) bytes() []byte {
	n := r.uint()
	s := r.b[:n]
	r.b = r.b[n:]
	return s
}

func (r *bcursor) str() string { return string(r.bytes()) }

func (r *bcursor) years() (first, last *int) {
	flags := r.byte()
	if flags&hasFirst != 0 {
		first = new(int(r.int()))
	}
	if flags&hasLast != 0 {
		last = new(int(r.int()))
	}
	return first, last
}
