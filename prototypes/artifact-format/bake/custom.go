package bake

import (
	"bytes"
	"encoding/binary"
	"fmt"
	. "github.com/joshuasprow/nitrovin/prototypes/artifact-format/model"
	"strings"
	"unicode/utf8"
	"unsafe"
)

// Custom indexed binary, little-endian, everything 8-byte aligned:
//
//	"NVPROTO1" u32 version u32 ntables
//	u64 arenaOffsetsOff u64 arenaCount u64 arenaBytesOff u64 arenaBytesLen
//	per table: u16 len, name, u32 nrows, u16 ncols, per col: u16 len, name, u8 type, u64 off
//	columns: int32[n] | int64[n] | u8[n] | u32 string id[n]
//	arena: u32 offsets[count+1], bytes
//
// Rows are in the table's sort order, so every index is a binary search over
// a column; strings are interned first-seen over Defs order → bytes are a pure
// function of the rows.
const customMagic = "NVPROTO1"

func BuildCustom(d *Data) []byte {
	intern := map[string]uint32{}
	var strs []string
	id := func(s string) uint32 {
		if v, ok := intern[s]; ok {
			return v
		}
		v := uint32(len(strs))
		intern[s] = v
		strs = append(strs, s)
		return v
	}
	var body bytes.Buffer
	align := func() {
		for body.Len()%8 != 0 {
			body.WriteByte(0)
		}
	}
	colOff := make([][]uint64, len(d.Tables))
	for ti, t := range d.Tables {
		for ci, c := range t.Def.Cols {
			align()
			colOff[ti] = append(colOff[ti], uint64(body.Len()))
			col := &t.Cols[ci]
			switch c.Type {
			case I32:
				binary.Write(&body, binary.LittleEndian, col.I32)
			case I64:
				binary.Write(&body, binary.LittleEndian, col.I64)
			case Bool:
				for _, b := range col.B {
					body.WriteByte(byte(B2i(b)))
				}
			case Str:
				ids := make([]uint32, t.N)
				for i, s := range col.S {
					ids[i] = id(s)
				}
				binary.Write(&body, binary.LittleEndian, ids)
			}
		}
	}
	align()
	arenaOffsetsOff := uint64(body.Len())
	var off uint32
	offs := make([]uint32, 0, len(strs)+1)
	for _, s := range strs {
		offs = append(offs, off)
		off += uint32(len(s))
	}
	offs = append(offs, off)
	binary.Write(&body, binary.LittleEndian, offs)
	arenaBytesOff := uint64(body.Len())
	for _, s := range strs {
		body.WriteString(s)
	}

	header := func(base uint64) []byte {
		var h bytes.Buffer
		le := binary.LittleEndian
		h.WriteString(customMagic)
		binary.Write(&h, le, uint32(1))
		binary.Write(&h, le, uint32(len(d.Tables)))
		binary.Write(&h, le, []uint64{base + arenaOffsetsOff, uint64(len(strs)), base + arenaBytesOff, uint64(off)})
		for ti, t := range d.Tables {
			binary.Write(&h, le, uint16(len(t.Def.Name)))
			h.WriteString(t.Def.Name)
			binary.Write(&h, le, uint32(t.N))
			binary.Write(&h, le, uint16(len(t.Def.Cols)))
			for ci, c := range t.Def.Cols {
				binary.Write(&h, le, uint16(len(c.Name)))
				h.WriteString(c.Name)
				h.WriteByte(byte(c.Type))
				binary.Write(&h, le, base+colOff[ti][ci])
			}
		}
		for h.Len()%8 != 0 {
			h.WriteByte(0)
		}
		return h.Bytes()
	}
	base := uint64(len(header(0)))
	return append(header(base), body.Bytes()...)
}

type ccol struct {
	typ ColType
	i32 []int32
	i64 []int64
	b   []byte
	s   []uint32
}

type ctable struct {
	name string
	n    int
	cols []ccol
	def  []Col
}

func (t *ctable) col(name string) *ccol {
	for i, c := range t.def {
		if c.Name == name {
			return &t.cols[i]
		}
	}
	panic("no column " + t.name + "." + name)
}

type Custom struct {
	buf    []byte
	offs   []uint32
	arena  []byte
	tables []*ctable

	wmiS, wmiID        []uint32
	wmiPad             []int64
	wmiIDs             []int32
	wsW, wsS, wsF, wsT []int32
	vsID               []int32
	vsQC               []byte
	pS, pID, pEl       []int32
	pKeys, pAttr       []uint32
	pCreated           []int64
	lTag, lName        []uint32
	lID                []int32
	elements           []Element
}

func sliceOf[T any](b []byte, off uint64, n int) []T {
	if n == 0 {
		return nil
	}
	return unsafe.Slice((*T)(unsafe.Pointer(&b[off])), n)
}

// OpenCustom reads the table directory and points typed slices into buf; no
// row is copied. buf may be an embedded []byte, an mmap or a decompressed heap
// buffer. Little-endian hosts only (prototype).
func OpenCustom(buf []byte) (*Custom, error) {
	if len(buf) < 48 || string(buf[:8]) != customMagic {
		return nil, fmt.Errorf("not a custom artifact")
	}
	le := binary.LittleEndian
	c := &Custom{buf: buf}
	ntab := int(le.Uint32(buf[12:]))
	aoff, acount, aboff, alen := le.Uint64(buf[16:]), le.Uint64(buf[24:]), le.Uint64(buf[32:]), le.Uint64(buf[40:])
	c.offs = sliceOf[uint32](buf, aoff, int(acount)+1)
	c.arena = buf[aboff : aboff+alen]
	p := 48
	rd16 := func() int { v := int(le.Uint16(buf[p:])); p += 2; return v }
	rdName := func() string { n := rd16(); s := string(buf[p : p+n]); p += n; return s }
	for range ntab {
		t := &ctable{name: rdName()}
		t.n = int(le.Uint32(buf[p:]))
		p += 4
		ncol := rd16()
		for range ncol {
			name := rdName()
			typ := ColType(buf[p])
			off := le.Uint64(buf[p+1:])
			p += 9
			cc := ccol{typ: typ}
			switch typ {
			case I32:
				cc.i32 = sliceOf[int32](buf, off, t.n)
			case I64:
				cc.i64 = sliceOf[int64](buf, off, t.n)
			case Bool:
				cc.b = buf[off : off+uint64(t.n)]
			case Str:
				cc.s = sliceOf[uint32](buf, off, t.n)
			}
			t.cols = append(t.cols, cc)
			t.def = append(t.def, Col{Name: name, Type: typ})
		}
		c.tables = append(c.tables, t)
	}
	w := c.table("wmi")
	c.wmiS, c.wmiIDs, c.wmiPad = w.col("wmi").s, w.col("id").i32, w.col("pad").i64
	ws := c.table("wmi_vinschema")
	c.wsW, c.wsS, c.wsF, c.wsT = ws.col("wmiid").i32, ws.col("vinschemaid").i32, ws.col("yearfrom").i32, ws.col("yearto").i32
	vs := c.table("vinschema")
	c.vsID, c.vsQC = vs.col("id").i32, vs.col("tobeqced").b
	pt := c.table("pattern")
	c.pS, c.pID, c.pEl, c.pKeys, c.pAttr, c.pCreated = pt.col("vinschemaid").i32, pt.col("id").i32, pt.col("elementid").i32, pt.col("keys").s, pt.col("attributeid").s, pt.col("created").i64
	lk := c.table("lookup")
	c.lTag, c.lID, c.lName = lk.col("tag").s, lk.col("id").i32, lk.col("name").s
	el := c.table("element")
	ids, lts, priv, hd := el.col("id").i32, el.col("lookuptable").s, el.col("isprivate").b, el.col("hasdecode").b
	c.elements = make([]Element, ids[len(ids)-1]+1)
	for i, id := range ids {
		c.elements[id] = Element{ID: id, Lookup: c.str(lts[i]), Private: priv[i] == 1, HasDecode: hd[i] == 1, Present: true}
	}
	return c, nil
}

// Verify is the one-time check a release reader would run before trusting
// unsafe.String views: arena offsets monotonic and in range, arena UTF-8.
func (c *Custom) Verify() error {
	for i := 1; i < len(c.offs); i++ {
		if c.offs[i] < c.offs[i-1] || int(c.offs[i]) > len(c.arena) {
			return fmt.Errorf("arena offset %d out of order", i)
		}
	}
	if !utf8.Valid(c.arena) {
		return fmt.Errorf("arena not UTF-8")
	}
	return nil
}

func (c *Custom) table(name string) *ctable {
	for _, t := range c.tables {
		if t.name == name {
			return t
		}
	}
	panic("no table " + name)
}

func (c *Custom) str(id uint32) string {
	a, b := c.offs[id], c.offs[id+1]
	if a == b {
		return ""
	}
	return unsafe.String(&c.arena[a], b-a)
}

func lowerI32(s []int32, v int32) int {
	lo, hi := 0, len(s)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if s[m] < v {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

func (c *Custom) Elements() []Element { return c.elements }

func (c *Custom) Candidates(wmi, _ string, year int32, now int64, fn func(*PatternRow)) {
	lo, hi := 0, len(c.wmiS)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if c.str(c.wmiS[m]) < wmi {
			lo = m + 1
		} else {
			hi = m
		}
	}
	var row PatternRow
	for w := lo; w < len(c.wmiS) && c.str(c.wmiS[w]) == wmi; w++ {
		if c.wmiPad[w] > now {
			continue
		}
		wid := c.wmiIDs[w]
		for b := lowerI32(c.wsW, wid); b < len(c.wsW) && c.wsW[b] == wid; b++ {
			if year != 0 && (year < c.wsF[b] || year > c.wsT[b]) {
				continue
			}
			sid := c.wsS[b]
			if v := lowerI32(c.vsID, sid); v == len(c.vsID) || c.vsID[v] != sid || c.vsQC[v] == 1 {
				continue
			}
			row.Priority = c.wsF[b]
			for p := lowerI32(c.pS, sid); p < len(c.pS) && c.pS[p] == sid; p++ {
				row.ID, row.ElementID, row.Created = c.pID[p], c.pEl[p], c.pCreated[p]
				row.Keys, row.Attr = c.str(c.pKeys[p]), c.str(c.pAttr[p])
				fn(&row)
			}
		}
	}
}

func (c *Custom) Lookup(tag string, id int32) (string, bool) {
	lo, hi := 0, len(c.lTag)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		t := c.str(c.lTag[m])
		if t < tag || (t == tag && c.lID[m] < id) {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(c.lTag) && c.lID[lo] == id && c.str(c.lTag[lo]) == tag {
		return c.str(c.lName[lo]), true
	}
	return "", false
}

func (c *Custom) Close() error { return nil }

// Dump prints a table's directory entry and the rows whose column equals val:
// the debugging surface a custom format has to bring itself.
func (c *Custom) Dump(table, colName, val string, limit int) string {
	var sb strings.Builder
	if table == "" {
		for _, t := range c.tables {
			fmt.Fprintf(&sb, "%-26s %9d rows  ", t.name, t.n)
			for _, d := range t.def {
				sb.WriteString(d.Name + " ")
			}
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "%-26s %9d strings, %d bytes\n", "(arena)", len(c.offs)-1, len(c.arena))
		return sb.String()
	}
	t := c.table(table)
	cell := func(cc *ccol, i int) string {
		switch cc.typ {
		case I32:
			return fmt.Sprint(cc.i32[i])
		case I64:
			return fmt.Sprint(cc.i64[i])
		case Bool:
			return fmt.Sprint(cc.b[i] == 1)
		default:
			return c.str(cc.s[i])
		}
	}
	for _, d := range t.def {
		sb.WriteString(d.Name + "\t")
	}
	sb.WriteByte('\n')
	n := 0
	for i := 0; i < t.n && n < limit; i++ {
		if colName != "" && cell(t.col(colName), i) != val {
			continue
		}
		for ci := range t.cols {
			sb.WriteString(cell(&t.cols[ci], i) + "\t")
		}
		sb.WriteByte('\n')
		n++
	}
	return sb.String()
}
