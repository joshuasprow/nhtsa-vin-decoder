package bake

import (
	"bytes"
	"fmt"
	. "github.com/joshuasprow/nitrovin/prototypes/artifact-format/model"
	"unsafe"

	flatbuffers "github.com/google/flatbuffers/go"

	"github.com/joshuasprow/nitrovin/prototypes/artifact-format/nvfb"
)

// BuildFB writes the same rows with the generic Builder API in schema slot
// order (what flatc's generated builders do), strings shared, defaults
// omitted. The flatbuffers builder is deterministic for a given call order.
func BuildFB(d *Data) []byte {
	b := flatbuffers.NewBuilder(256 << 20)
	vecs := make([]flatbuffers.UOffsetT, len(Defs))
	for _, t := range d.Tables {
		slot := -1
		for i, def := range Defs {
			if def == t.Def {
				slot = i
			}
		}
		rowOffs := make([]flatbuffers.UOffsetT, t.N)
		strOffs := make([]flatbuffers.UOffsetT, len(t.Def.Cols))
		for i := range t.N {
			for ci, c := range t.Def.Cols {
				if c.Type == Str {
					strOffs[ci] = b.CreateSharedString(t.Cols[ci].S[i])
				}
			}
			b.StartObject(len(t.Def.Cols))
			for ci, c := range t.Def.Cols {
				col := &t.Cols[ci]
				switch c.Type {
				case I32:
					b.PrependInt32Slot(ci, col.I32[i], 0)
				case I64:
					b.PrependInt64Slot(ci, col.I64[i], 0)
				case Bool:
					b.PrependBoolSlot(ci, col.B[i], false)
				case Str:
					b.PrependUOffsetTSlot(ci, strOffs[ci], 0)
				}
			}
			rowOffs[i] = b.EndObject()
		}
		b.StartVector(4, t.N, 4)
		for i := t.N - 1; i >= 0; i-- {
			b.PrependUOffsetT(rowOffs[i])
		}
		vecs[slot] = b.EndVector(t.N)
	}
	b.StartObject(len(Defs))
	for i, v := range vecs {
		if v != 0 {
			b.PrependUOffsetTSlot(i, v, 0)
		}
	}
	root := b.EndObject()
	b.FinishWithFileIdentifier(root, []byte("NVFB"))
	return bytes.Clone(b.FinishedBytes())
}

type FB struct {
	a                      *nvfb.Artifact
	elements               []Element
	nWmi, nWS, nVS, nP, nL int
}

func bstr(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	return unsafe.String(&b[0], len(b))
}

// OpenFB reads in place; only the 161-row element table is copied out.
func OpenFB(buf []byte) (*FB, error) {
	if !nvfb.ArtifactBufferHasIdentifier(buf) {
		return nil, fmt.Errorf("not a flatbuffers artifact")
	}
	a := nvfb.GetRootAsArtifact(buf, 0)
	f := &FB{a: a, nWmi: a.WmiLength(), nWS: a.WmiVinschemaLength(), nVS: a.VinschemaLength(), nP: a.PatternLength(), nL: a.LookupLength()}
	var e nvfb.Element
	for i := range a.ElementLength() {
		a.Element(&e, i)
		for int(e.Id()) >= len(f.elements) {
			f.elements = append(f.elements, Element{})
		}
		f.elements[e.Id()] = Element{ID: e.Id(), Lookup: bstr(e.Lookuptable()), Private: e.Isprivate(), HasDecode: e.Hasdecode(), Present: true}
	}
	return f, nil
}

func (f *FB) Elements() []Element { return f.elements }

func (f *FB) Candidates(wmi, _ string, year int32, now int64, fn func(*PatternRow)) {
	a := f.a
	var w nvfb.Wmi
	var ws nvfb.WmiVinschema
	var vs nvfb.Vinschema
	var p nvfb.Pattern
	lo, hi := 0, f.nWmi
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		a.Wmi(&w, m)
		if bstr(w.Wmi()) < wmi {
			lo = m + 1
		} else {
			hi = m
		}
	}
	var row PatternRow
	for i := lo; i < f.nWmi; i++ {
		a.Wmi(&w, i)
		if bstr(w.Wmi()) != wmi {
			break
		}
		if w.Pad() > now {
			continue
		}
		wid := w.Id()
		b := lowerBound(f.nWS, func(m int) bool { a.WmiVinschema(&ws, m); return ws.Wmiid() < wid })
		for ; b < f.nWS; b++ {
			a.WmiVinschema(&ws, b)
			if ws.Wmiid() != wid {
				break
			}
			yf, yt := ws.Yearfrom(), ws.Yearto()
			if year != 0 && (year < yf || year > yt) {
				continue
			}
			sid := ws.Vinschemaid()
			v := lowerBound(f.nVS, func(m int) bool { a.Vinschema(&vs, m); return vs.Id() < sid })
			if v == f.nVS {
				continue
			}
			a.Vinschema(&vs, v)
			if vs.Id() != sid || vs.Tobeqced() {
				continue
			}
			row.Priority = yf
			for j := lowerBound(f.nP, func(m int) bool { a.Pattern(&p, m); return p.Vinschemaid() < sid }); j < f.nP; j++ {
				a.Pattern(&p, j)
				if p.Vinschemaid() != sid {
					break
				}
				row.ID, row.ElementID, row.Created = p.Id(), p.Elementid(), p.Created()
				row.Keys, row.Attr = bstr(p.Keys()), bstr(p.Attributeid())
				fn(&row)
			}
		}
	}
}

func lowerBound(n int, less func(int) bool) int {
	lo, hi := 0, n
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if less(m) {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

func (f *FB) Lookup(tag string, id int32) (string, bool) {
	var l nvfb.Lookup
	i := lowerBound(f.nL, func(m int) bool {
		f.a.Lookup(&l, m)
		t := bstr(l.Tag())
		return t < tag || (t == tag && l.Id() < id)
	})
	if i == f.nL {
		return "", false
	}
	f.a.Lookup(&l, i)
	if l.Id() != id || bstr(l.Tag()) != tag {
		return "", false
	}
	return bstr(l.Name()), true
}

func (f *FB) Close() error { return nil }
