package decoder

import (
	"slices"
	"strings"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// patternIndex is one VinSchema's patterns as the core's two pattern passes
// read them. The pattern pass (C2, spvindecode_core.sql:75-105) tests each
// distinct key once and keeps every row under it; the formula pass (C7,
// :150-174) tests every row whose key holds '#'. Rows are indices into the
// artifact's pattern table, which is ordered by (vinschemaid, id), so
// ascending rows are ascending Pattern.Id.
type patternIndex struct {
	groups  []keyGroup
	rows    []int32 // the groups' rows, each group's contiguous and ascending
	formula []formulaRow
}

type keyGroup struct {
	m      keyMatcher
	lo, hi int32 // the group's rows: rows[lo:hi]
}

type formulaRow struct {
	row int32
	m   keyMatcher
}

// excludedElement reports the elements both passes skip (`not P.ElementId
// in (26, 27, 29, 39)`): Make, Manufacturer Name, Model Year and Vehicle
// Type, which the procedure decodes from the WMI and the year instead.
func excludedElement(id int32) bool {
	return id == 26 || id == 27 || id == 29 || id == 39
}

// patternElements is, by element id, whether the pattern pass keeps an
// element's rows: not excluded, Decode not NULL, and not private, the
// procedure's default includePrivate = false (the API has no private
// switch).
func patternElements(a *artifact.Artifact) []bool {
	t := a.Table("element")
	ids := t.Int32s("id")
	decode := t.Strings("decode")
	private := t.Bools("isprivate")
	ok := make([]bool, slices.Max(ids)+1)
	for i, id := range ids {
		_, hasDecode := decode.At(i)
		isPrivate, _ := private.At(i)
		ok[id] = !excludedElement(id) && hasDecode && !isPrivate
	}
	return ok
}

// newPatternIndex indexes the pattern rows of one VinSchema. elements is
// patternElements of the same artifact. The VinSchema's TobeQCed, the WMI's
// availability and the year band are the caller's: they admit or skip the
// schema as a whole.
func newPatternIndex(a *artifact.Artifact, elements []bool, vinSchemaID int32) *patternIndex {
	t := a.Table("pattern")
	lo, hi := artifact.EqualRange(t.Int32s("vinschemaid"), 0, t.Rows(), vinSchemaID)
	keys := t.Strings("keys")
	elementIDs := t.Int32s("elementid")

	x := &patternIndex{}
	// Group by arena reference: strings are interned, so equal references
	// are equal keys.
	group := map[uint32]int{}
	var members [][]int32
	for r := lo; r < hi; r++ {
		id := elementIDs[r]
		key, _ := keys.At(r)
		if strings.IndexByte(key, '#') >= 0 && !excludedElement(id) {
			x.formula = append(x.formula, formulaRow{int32(r), compileLike(key)})
		}
		if int(id) >= len(elements) || !elements[id] {
			continue
		}
		g, seen := group[keys.Ref(r)]
		if !seen {
			g = len(members)
			group[keys.Ref(r)] = g
			members = append(members, nil)
			x.groups = append(x.groups, keyGroup{m: compileKey(key)})
		}
		members[g] = append(members[g], int32(r))
	}
	for g, rows := range members {
		x.groups[g].lo = int32(len(x.rows))
		x.rows = append(x.rows, rows...)
		x.groups[g].hi = int32(len(x.rows))
	}
	return x
}

// patternHits appends to dst the rows the pattern pass keeps for varKeys,
// ascending. err is the first key the oracle would reject (an invalid
// regular expression aborts spvindecode); dst then holds the hits so far.
func (x *patternIndex) patternHits(varKeys string, dst []int32) ([]int32, error) {
	start := len(dst)
	for i := range x.groups {
		g := &x.groups[i]
		ok, err := g.m.match(varKeys)
		if err != nil {
			return dst, err
		}
		if ok {
			dst = append(dst, x.rows[g.lo:g.hi]...)
		}
	}
	slices.Sort(dst[start:])
	return dst, nil
}

// formulaHits appends to dst the rows the formula pass keeps for varKeys,
// ascending: `formulaKeys LIKE replace(p.Keys, '*', '_') || '%'`, with none
// of the pattern pass's Decode or private filters. err is as patternHits'.
func (x *patternIndex) formulaHits(varKeys string, dst []int32) ([]int32, error) {
	if len(x.formula) == 0 {
		return dst, nil
	}
	fk := formulaKeys(varKeys)
	for i := range x.formula {
		ok, err := x.formula[i].m.match(fk)
		if err != nil {
			return dst, err
		}
		if ok {
			dst = append(dst, x.formula[i].row)
		}
	}
	return dst, nil
}
