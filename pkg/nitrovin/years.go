package nitrovin

import (
	"cmp"
	"slices"
	"strconv"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// elementModel is vPIC's Model element.
const elementModel = 28

var _ vocab.YearsMonth = (*month)(nil)

// ModelYears is the model years vPIC model id's VIN schemas cover (ADR
// 0022), nil when it has no Model pattern: unknown.
func (m *month) ModelYears(id int32) []vocab.YearRange { return m.years()[id] }

// modelYears derives every model's years: the union of the wmi_vinschema
// bands of every VIN schema with a Model (28) pattern naming the model,
// across all the schema's WMIs, merged into ranges. Gaps are kept, a band
// with no yearto gives an open range, and every schema and WMI counts.
func (m *month) modelYears() map[int32][]vocab.YearRange {
	p := m.a.Table("pattern")
	schemas, values := p.Int32s("vinschemaid"), p.Strings("attributeid")
	type link struct{ schema, model int32 }
	var links []link
	for r, el := range p.Int32s("elementid") {
		if el != elementModel {
			continue
		}
		v, _ := values.At(r)
		if id, err := strconv.Atoi(v); err == nil {
			links = append(links, link{schemas[r], int32(id)})
		}
	}
	slices.SortFunc(links, func(a, b link) int { return cmp.Or(cmp.Compare(a.model, b.model), cmp.Compare(a.schema, b.schema)) })
	links = slices.Compact(links)

	b := m.a.Table("wmi_vinschema")
	bandSchema, from, to := b.Int32s("vinschemaid"), b.Int32s("yearfrom"), b.Int32s("yearto")
	bands := map[int32][]vocab.YearRange{}
	for r, s := range bandSchema {
		last := 0 // open
		if to[r] != artifact.NullInt32 {
			last = int(to[r])
		}
		bands[s] = append(bands[s], vocab.YearRange{First: int(from[r]), Last: last})
	}

	out := map[int32][]vocab.YearRange{}
	var all []vocab.YearRange
	for i := 0; i < len(links); {
		model := links[i].model
		all = all[:0]
		for ; i < len(links) && links[i].model == model; i++ {
			all = append(all, bands[links[i].schema]...)
		}
		if len(all) > 0 {
			out[model] = mergeYears(all)
		}
	}
	return out
}

// mergeYears merges ranges that overlap or abut into a new ascending
// slice; an open range swallows every range after its first.
func mergeYears(rs []vocab.YearRange) []vocab.YearRange {
	slices.SortFunc(rs, func(a, b vocab.YearRange) int { return cmp.Compare(a.First, b.First) })
	var out []vocab.YearRange
	for _, r := range rs {
		n := len(out)
		if n > 0 && (out[n-1].Last == 0 || r.First <= out[n-1].Last+1) {
			if out[n-1].Last != 0 && (r.Last == 0 || r.Last > out[n-1].Last) {
				out[n-1].Last = r.Last
			}
			continue
		}
		out = append(out, r)
	}
	return slices.Clip(out)
}
