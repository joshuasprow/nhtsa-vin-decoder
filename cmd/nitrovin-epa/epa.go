package main

import (
	"cmp"
	"encoding/csv"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// entry is one vocabulary entry: the vehicles.csv rows whose make and model
// are equal after vocab.Normalize, which merges EPA's case and spacing
// variants (two lines for one entry would refuse the file).
type entry struct {
	key        string // normalized make, NUL, normalized model
	make       string // the most frequent spelling of the rows' make
	model      string // and of their model
	makeKey    string // normalized make
	rows       int
	first      int
	last       int
	baseModels []string       // distinct baseModel values, most rows first
	drives     map[string]int // EPA drive column value → rows
}

// epaFile is what the builder reads from vehicles.csv.
type epaFile struct {
	entries []*entry // by key
	rows    int
	// date is the newest createdOn or modifiedOn date in the file,
	// YYYY-MM-DD: the version of EPA's data a vocabulary was built from.
	date string
}

// epaDateLayout is how vehicles.csv writes createdOn and modifiedOn
// ("Tue Jan 01 00:00:00 EST 2013").
const epaDateLayout = "Mon Jan 02 15:04:05 MST 2006"

// readEPA reads vehicles.csv, naming its columns by its header.
func readEPA(r io.Reader) (*epaFile, error) {
	cr := csv.NewReader(r)
	cr.ReuseRecord = true
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("vehicles.csv: header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.TrimPrefix(h, "\ufeff")] = i
	}
	need := []string{"make", "model", "baseModel", "year", "drive", "createdOn", "modifiedOn"}
	for _, n := range need {
		if _, ok := col[n]; !ok {
			return nil, fmt.Errorf("vehicles.csv: no %q column", n)
		}
	}
	type spellings struct{ make, model map[string]int }
	type acc struct {
		e     *entry
		names spellings
		bases map[string]int
	}
	byKey := map[string]*acc{}
	f := &epaFile{}
	var newest time.Time
	for line := 2; ; line++ {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("vehicles.csv: %w", err)
		}
		rawMake, rawModel := rec[col["make"]], rec[col["model"]]
		mk, md := vocab.Normalize(rawMake), vocab.Normalize(rawModel)
		if mk == "" || md == "" {
			return nil, fmt.Errorf("vehicles.csv:%d: empty make or model", line)
		}
		year, err := strconv.Atoi(rec[col["year"]])
		if err != nil {
			return nil, fmt.Errorf("vehicles.csv:%d: year: %w", line, err)
		}
		for _, c := range []string{"createdOn", "modifiedOn"} {
			if v := rec[col[c]]; v != "" {
				t, err := time.Parse(epaDateLayout, v)
				if err != nil {
					return nil, fmt.Errorf("vehicles.csv:%d: %s: %w", line, c, err)
				}
				// The date as written, whatever its zone.
				d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
				if d.After(newest) {
					newest = d
				}
			}
		}
		key := mk + "\x00" + md
		a := byKey[key]
		if a == nil {
			a = &acc{
				e:     &entry{key: key, makeKey: mk, first: year, last: year, drives: map[string]int{}},
				names: spellings{map[string]int{}, map[string]int{}},
				bases: map[string]int{},
			}
			byKey[key] = a
		}
		e := a.e
		e.rows++
		e.first, e.last = min(e.first, year), max(e.last, year)
		a.names.make[strings.TrimSpace(rawMake)]++
		a.names.model[strings.TrimSpace(rawModel)]++
		if b := strings.TrimSpace(rec[col["baseModel"]]); b != "" {
			a.bases[b]++
		}
		if d := strings.TrimSpace(rec[col["drive"]]); d != "" {
			e.drives[d]++
		}
		f.rows++
	}
	if newest.IsZero() {
		return nil, fmt.Errorf("vehicles.csv: no createdOn or modifiedOn date")
	}
	f.date = newest.Format(time.DateOnly)
	for _, key := range slices.Sorted(maps.Keys(byKey)) {
		a := byKey[key]
		a.e.make, a.e.model = mostRows(a.names.make), mostRows(a.names.model)
		a.e.baseModels = byRows(a.bases)
		f.entries = append(f.entries, a.e)
	}
	return f, nil
}

// byRows is counts' keys, most rows first, then in byte order.
func byRows(counts map[string]int) []string {
	ks := slices.Collect(maps.Keys(counts))
	slices.SortFunc(ks, func(a, b string) int {
		return cmp.Or(cmp.Compare(counts[b], counts[a]), strings.Compare(a, b))
	})
	return ks
}

// mostRows is the key with the most rows, the first in byte order on a tie.
func mostRows(counts map[string]int) string { return byRows(counts)[0] }
