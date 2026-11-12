package vocab

import (
	"fmt"
	"strconv"
	"strings"
)

// YearRange is a run of model years, inclusive (ADR 0022). Last 0 is open:
// still running.
type YearRange struct {
	First, Last int
}

// Has reports whether year is in r; an open range has every later year.
func (r YearRange) Has(year int) bool {
	return year >= r.First && (r.Last == 0 || year <= r.Last)
}

func (r YearRange) String() string {
	if r.Last == 0 {
		return fmt.Sprintf("%d-", r.First)
	}
	return fmt.Sprintf("%d-%d", r.First, r.Last)
}

// YearsMonth is a Month that knows each vPIC model's years (ADR 0022).
// A Month that is not one knows no model's years: they are unknown, never
// empty, so nothing is filtered or flagged by them.
type YearsMonth interface {
	Month
	// ModelYears is the model years vPIC model id's VIN schemas cover, in
	// ascending order, gaps kept; nil when unknown.
	ModelYears(id int32) []YearRange
}

// modelYears is model id's years in m, nil when m knows none.
func modelYears(m Month, id int32) []YearRange {
	if ym, ok := m.(YearsMonth); ok {
		return ym.ModelYears(id)
	}
	return nil
}

// yearsHave reports whether years has year, or are unknown (nil).
func yearsHave(years []YearRange, year int) bool {
	if years == nil {
		return true
	}
	for _, r := range years {
		if r.Has(year) {
			return true
		}
	}
	return false
}

// outside says whether a mapping's model years [first, last] fall
// "wholly" or "partly" outside years, or "" when within them, when years
// are unknown, or when the mapping is unbounded both ways. An unbounded
// end is the model's own: a mapping is measured only by the years it
// states. years are merged, so a covered mapping lies in one range.
func outside(years []YearRange, first, last *int) string {
	if years == nil || first == nil && last == nil {
		return ""
	}
	lo, hi := years[0].First, years[len(years)-1].Last // hi 0: open
	if first != nil {
		lo = *first
	}
	if last != nil {
		hi = *last
	}
	in := false
	for _, r := range years {
		if r.First <= lo && (r.Last == 0 || hi != 0 && hi <= r.Last) {
			return ""
		}
		in = in || (hi == 0 || r.First <= hi) && (r.Last == 0 || r.Last >= lo)
	}
	if in {
		return "partly"
	}
	return "wholly"
}

// yearsText is years as "1999-2006, 2009-".
func yearsText(years []YearRange) string {
	s := make([]string, len(years))
	for i, r := range years {
		s[i] = r.String()
	}
	return strings.Join(s, ", ")
}

// yearFilter is what a list of one make's vPIC models keeps for
// where.year (ADR 0022): a model whose ranges have the year, or whose
// years are unknown; every model without where.year.
func yearFilter(where map[string]string) (func(*row) bool, error) {
	v, ok := where["year"]
	if !ok {
		return func(*row) bool { return true }, nil
	}
	year, err := strconv.Atoi(v)
	if err != nil || year < 1 || year > 9999 {
		return nil, refuse("/where/year", "%q is not a model year", v)
	}
	return func(r *row) bool { return yearsHave(r.spine.Years, year) }, nil
}
