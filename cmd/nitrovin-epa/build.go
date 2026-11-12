package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Name is the vocabulary's name.
const Name = "epa-vehicles"

// Origins of a mapping (ADR 0009).
const (
	originDerived = "derived"
	originCurated = "curated"
)

// header is the vocabulary's first line.
type header struct {
	Format    int      `json:"format"`
	Kind      string   `json:"kind"`
	Name      string   `json:"name"`
	Version   string   `json:"version"`
	DataMonth string   `json:"data_month"`
	Subject   string   `json:"subject"`
	Fields    []string `json:"fields"`
}

// line is one entry's line.
type line struct {
	Entry struct {
		Make  string `json:"make"`
		Model string `json:"model"`
	} `json:"entry"`
	Mappings []mapping `json:"mappings"`
	Note     string    `json:"note,omitempty"`
}

type mapping struct {
	Make       int32            `json:"make,omitempty"`
	Model      int32            `json:"model,omitempty"`
	First      int              `json:"first"`
	Last       int              `json:"last"`
	Qualifiers map[string][]any `json:"qualifiers,omitempty"`
	Origin     string           `json:"origin"`
	Note       string           `json:"note,omitempty"`
}

// coverage counts entries and EPA rows by outcome.
type coverage struct {
	entries, rows int
	derived       tally
	byRule        map[string]*tally
	several       tally // derived entries with more than one mapping
	curated       tally
	unmapped      tally
	noMake        tally // unmapped for want of a vPIC make
	guarded       tally // unmapped by the guard
	curatedNone   tally // unmapped by a curated line with no mappings
	stale         []string
}

type tally struct{ entries, rows int }

func (t *tally) add(e *entry) { t.entries++; t.rows += e.rows }

// build writes the vocabulary of f against month m, whose model values
// give the qualifiers, and the curated overrides cur to w, and says what
// it covered.
func build(w io.Writer, f *epaFile, m vocab.ModelMonth, cur *vocab.File) (*coverage, error) {
	mv := modelValues{m}
	curated := map[string]*vocab.Entry{}
	if cur != nil {
		if !slices.Equal(cur.Fields, []string{"make", "model"}) || cur.Subject != vocab.SubjectVehicle {
			return nil, fmt.Errorf("%s: curated overrides must be a vehicle vocabulary of fields [make model]", cur.Path)
		}
		for i := range cur.Entries {
			ce := &cur.Entries[i]
			curated[vocab.Normalize(ce.Fields[0])+"\x00"+vocab.Normalize(ce.Fields[1])] = ce
		}
	}

	// makes resolves each EPA make once: by name, and through makeTable.
	byName := map[string][]int32{}
	for id, name := range m.Makes() {
		n := vocab.Normalize(name)
		byName[n] = append(byName[n], id)
	}
	type resolvedMake struct {
		ids    []int32
		prefix string
		ix     *models
		names  string
	}
	makes := map[string]*resolvedMake{}
	makeOf := func(e *entry) *resolvedMake {
		if r, ok := makes[e.makeKey]; ok {
			return r
		}
		r := &resolvedMake{ids: slices.Clone(byName[e.makeKey])}
		if a, ok := makeTable[e.makeKey]; ok {
			r.ids = append(r.ids, a.makes...)
			r.prefix = a.prefix
		}
		slices.Sort(r.ids)
		r.ids = slices.Compact(r.ids)
		var names []string
		for _, id := range r.ids {
			n, _ := m.Make(id)
			names = append(names, n)
		}
		r.names = strings.Join(names, ", ")
		r.ix = newModels(m, r.ids, e.makeKey)
		makes[e.makeKey] = r
		return r
	}

	cov := &coverage{byRule: map[string]*tally{}}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(header{
		Format: vocab.Format, Kind: string(vocab.KindVocabulary), Name: Name,
		Version: f.date + "+" + m.Name(), DataMonth: m.Name(),
		Subject: string(vocab.SubjectVehicle), Fields: []string{"make", "model"},
	}); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, e := range f.entries {
		cov.entries++
		cov.rows += e.rows
		normModel := strings.SplitN(e.key, "\x00", 2)[1]
		var l line
		l.Entry.Make, l.Entry.Model = e.make, e.model
		l.Mappings = []mapping{}
		if ce, ok := curated[e.key]; ok {
			seen[e.key] = true
			ms, err := curatedMappings(ce, e, normModel, mv)
			if err != nil {
				return nil, fmt.Errorf("%s:%d: %w", cur.Path, ce.Line, err)
			}
			l.Mappings, l.Note = ms, ce.Note
			if len(ms) > 0 {
				cov.curated.add(e)
			} else {
				cov.unmapped.add(e)
				cov.curatedNone.add(e)
			}
		} else {
			mk := makeOf(e)
			switch {
			case len(mk.ids) == 0:
				l.Note = "vPIC has no make named " + strconv.Quote(e.make)
				cov.unmapped.add(e)
				cov.noMake.add(e)
			default:
				bases := make([]string, len(e.baseModels))
				for i, b := range e.baseModels {
					bases[i] = vocab.Normalize(b)
				}
				r := mk.ix.resolve(normModel, bases, mk.prefix)
				for _, h := range r.hits {
					mp := mapping{Model: h.model, First: e.first, Last: e.last, Origin: originDerived, Note: "rule: " + h.rule}
					mp.Qualifiers = qualifiers(mv, e, normModel, h.model)
					l.Mappings = append(l.Mappings, mp)
				}
				switch {
				case len(r.hits) > 0:
					cov.derived.add(e)
					rule := r.hits[0].rule
					if cov.byRule[rule] == nil {
						cov.byRule[rule] = &tally{}
					}
					cov.byRule[rule].add(e)
					if len(r.hits) > 1 {
						cov.several.add(e)
					}
				case r.guarded:
					l.Note = "for curation: the longest vPIC model prefix and baseModel name different models"
					cov.unmapped.add(e)
					cov.guarded.add(e)
				default:
					l.Note = "no rule matched a vPIC model under " + mk.names
					cov.unmapped.add(e)
				}
			}
		}
		if err := enc.Encode(l); err != nil {
			return nil, err
		}
	}
	if cur != nil {
		for i := range cur.Entries {
			ce := &cur.Entries[i]
			k := vocab.Normalize(ce.Fields[0]) + "\x00" + vocab.Normalize(ce.Fields[1])
			if !seen[k] {
				cov.stale = append(cov.stale, fmt.Sprintf("%s:%d: %s / %s is not in vehicles.csv", cur.Path, ce.Line, ce.Fields[0], ce.Fields[1]))
			}
		}
	}
	if _, err := w.Write(buf.Bytes()); err != nil {
		return nil, err
	}
	return cov, nil
}

// qualifiers are a model mapping's derived qualifiers: Drive Type when
// the name carries a drive token, Series when it names a tonnage.
func qualifiers(mv modelValues, e *entry, normModel string, model int32) map[string][]any {
	q := map[string][]any{}
	if vs := mv.driveQualifier(e, normModel, model); len(vs) > 0 {
		for _, v := range vs {
			q[strconv.Itoa(elementDriveType)] = append(q[strconv.Itoa(elementDriveType)], v)
		}
	}
	if vs := mv.seriesQualifier(e, normModel, model); len(vs) > 0 {
		for _, v := range vs {
			q[strconv.Itoa(elementSeries)] = append(q[strconv.Itoa(elementSeries)], v)
		}
	}
	if len(q) == 0 {
		return nil
	}
	return q
}

// curatedMappings are a curated entry's mappings, which replace the
// derived ones: origin curated; years, when the line gives none, the EPA
// rows'; qualifiers, when the line gives none, derived as for a derived
// mapping to the same model.
func curatedMappings(ce *vocab.Entry, e *entry, normModel string, mv modelValues) ([]mapping, error) {
	out := []mapping{}
	for _, cm := range ce.Mappings {
		if cm.Make == 0 && cm.Model == 0 {
			return nil, fmt.Errorf("a curated mapping names a vPIC make or model, never an extension")
		}
		mp := mapping{Make: cm.Make, Model: cm.Model, First: e.first, Last: e.last, Origin: originCurated, Note: cm.Note}
		if cm.First != nil {
			mp.First = *cm.First
		}
		if cm.Last != nil {
			mp.Last = *cm.Last
		}
		if len(cm.Qualifiers) > 0 {
			mp.Qualifiers = map[string][]any{}
			for _, q := range cm.Qualifiers {
				k := strconv.Itoa(int(q.Element))
				for _, v := range q.Values {
					if v.IsText {
						mp.Qualifiers[k] = append(mp.Qualifiers[k], v.Text)
					} else {
						mp.Qualifiers[k] = append(mp.Qualifiers[k], v.Attribute)
					}
				}
			}
		} else if cm.Model != 0 {
			mp.Qualifiers = qualifiers(mv, e, normModel, cm.Model)
		}
		out = append(out, mp)
	}
	return out, nil
}

// report writes the coverage summary.
func (c *coverage) report(w io.Writer) {
	pct := func(n, of int) string { return fmt.Sprintf("%.1f%%", 100*float64(n)/float64(max(of, 1))) }
	row := func(label string, t tally) {
		fmt.Fprintf(w, "  %-26s %5d entries %6s  %6d rows %6s\n", label, t.entries, pct(t.entries, c.entries), t.rows, pct(t.rows, c.rows))
	}
	fmt.Fprintf(w, "%d entries, %d rows\n", c.entries, c.rows)
	row("derived", c.derived)
	for _, r := range []string{ruleExact, ruleStripped, rulePrefix, ruleBase, ruleFolded, ruleMakeName, ruleSlash} {
		if t := c.byRule[r]; t != nil {
			row("  "+r, *t)
		}
	}
	row("  to several models", c.several)
	row("curated", c.curated)
	row("unmapped", c.unmapped)
	row("  no vPIC make", c.noMake)
	row("  guard (for curation)", c.guarded)
	row("  by curation", c.curatedNone)
	if len(c.stale) > 0 {
		fmt.Fprintf(w, "%d curated entries are not in vehicles.csv, such as:\n", len(c.stale))
		for _, s := range c.stale[:min(len(c.stale), 3)] {
			fmt.Fprintln(w, "  "+s)
		}
	}
}
