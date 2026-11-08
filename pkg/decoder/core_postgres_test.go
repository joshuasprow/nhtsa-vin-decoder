package decoder

import (
	"bufio"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// TestCorePostgres checks the core against the restored dump it was built
// from (NITROVIN_PSQL and NITROVIN_ARTIFACT as in TestMatcherPostgres):
//
//   - case: Go's upper and lower agree with Postgres's on every non-ASCII
//     string the core upper- or lower-cases (make, manufacturer and vehicle
//     type names, pattern, engine model pattern and vehicle spec attribute
//     ids, engine model names);
//   - conversion: for every conversion and every distinct AttributeId its
//     FromElementId has in pattern or enginemodelpattern, convert agrees with
//     the procedure's own `select (formula)::varchar(500)`, '0' on error;
//   - items: for every corpus VIN, called with the year fVinModelYear2 gives
//     it (or the corpus's) and again with a NULL year, core returns the
//     multiset of rows vpic.spvindecode_core returns, Corrections rows
//     excepted, compared on every column but the pass number.
//
// The test writes scratch tables to the schema core_oracle and drops it.
func TestCorePostgres(t *testing.T) {
	a, psql := dumpUnderTest(t)
	t.Cleanup(func() { psql("drop schema if exists core_oracle cascade;\n") })
	psql("drop schema if exists core_oracle cascade; create schema core_oracle;\n")

	t.Run("case", func(t *testing.T) { checkCase(t, a, psql) })
	t.Run("conversion", func(t *testing.T) { checkConversions(t, a, psql) })
	t.Run("items", func(t *testing.T) { checkItems(t, a, psql) })
}

func checkCase(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	type probe struct{ s, fn string }
	seen := map[probe]bool{}
	var probes []probe
	add := func(col artifact.Strings, fn string) {
		for r := range col.Len() {
			s, ok := col.At(r)
			if p := (probe{s, fn}); ok && !isASCII(s) && !seen[p] {
				seen[p] = true
				probes = append(probes, p)
			}
		}
	}
	add(a.Table("lookup").Strings("name"), "upper")
	add(a.Table("pattern").Strings("attributeid"), "lower")
	add(a.Table("enginemodelpattern").Strings("attributeid"), "lower")
	add(a.Table("vehiclespecpattern").Strings("attributeid"), "lower")
	add(a.Table("enginemodel").Strings("name"), "lower")

	var in strings.Builder
	in.WriteString("create table core_oracle.c (id int primary key, fn text, s varchar);\ncopy core_oracle.c from stdin;\n")
	for i, p := range probes {
		fmt.Fprintf(&in, "%d\t%s\t%s\n", i, p.fn, escapeCopy(p.s))
	}
	in.WriteString("\\.\ncopy (select id, case fn when 'upper' then upper(s) else lower(s) end from core_oracle.c order by id) to stdout;\n")
	got := lines(psql(in.String()))
	if len(got) != len(probes) {
		t.Fatalf("Postgres answered %d of %d strings", len(got), len(probes))
	}
	bad := 0
	for i, l := range got {
		_, want, _ := strings.Cut(l, "\t")
		want = unescapeCopy(want)
		mine := strings.ToLower(probes[i].s)
		if probes[i].fn == "upper" {
			mine = strings.ToUpper(probes[i].s)
		}
		if mine != want {
			if bad++; bad <= 20 {
				t.Errorf("%s(%q): Go %q, Postgres %q", probes[i].fn, probes[i].s, mine, want)
			}
		}
	}
	t.Logf("%d non-ASCII strings: %d disagree", len(probes), bad)
}

func checkConversions(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	cv := a.Table("conversion")
	from, ids, formulas := cv.Int32s("fromelementid"), cv.Int32s("id"), cv.Strings("formula")
	operands := map[int32][]string{}
	for _, table := range []string{"pattern", "enginemodelpattern"} {
		pt := a.Table(table)
		elements, attrs := pt.Int32s("elementid"), pt.Strings("attributeid")
		for r, el := range elements {
			if !slices.Contains(from, el) {
				continue
			}
			if v, _ := attrs.At(r); !slices.Contains(operands[el], v) {
				operands[el] = append(operands[el], v)
			}
		}
	}
	type probe struct {
		conv           int32
		formula, value string
	}
	var probes []probe
	var in strings.Builder
	in.WriteString(conversionFunction + "create table core_oracle.v (id int primary key, formula varchar, value varchar);\ncopy core_oracle.v from stdin;\n")
	for r := range cv.Rows() {
		f, _ := formulas.At(r)
		for _, v := range operands[from[r]] {
			fmt.Fprintf(&in, "%d\t%s\t%s\n", len(probes), escapeCopy(f), escapeCopy(v))
			probes = append(probes, probe{ids[r], f, v})
		}
	}
	in.WriteString("\\.\ncopy (select id, pg_temp.conv(formula, value) from core_oracle.v order by id) to stdout;\n")
	got := lines(psql(in.String()))
	if len(got) != len(probes) {
		t.Fatalf("Postgres answered %d of %d conversions", len(got), len(probes))
	}
	bad := 0
	for i, l := range got {
		_, want, _ := strings.Cut(l, "\t")
		p := probes[i]
		r, ok, err := convert(p.formula, p.value)
		mine := copyField(sqlText{r, ok})
		if err != nil {
			mine = err.Error()
		}
		if mine != want {
			if bad++; bad <= 20 {
				t.Errorf("conversion %d %q on %q: Go %q, Postgres %q", p.conv, p.formula, p.value, mine, want)
			}
		}
	}
	t.Logf("%d (conversion, operand) pairs: %d disagree", len(probes), bad)
}

// conversionFunction is the core's conversion statement (spvindecode_core.sql:
// 263-280) as a function of the formula and the operand.
const conversionFunction = `create function pg_temp.conv(formula varchar, value varchar) returns varchar
language plpgsql as $$
declare
	sql varchar;
	result varchar(500) = '';
begin
	formula = replace(formula, '#x#', value);
	sql = 'select (' || formula || ')::varchar(500)';
	begin
		execute sql into result;
	exception
		when others then
			result = '0';
	end;
	return result;
end $$;
`

// coreInput is one corpus VIN: raw input, and the year to call the core with
// first, 0 for fVinModelYear2's (absolute) year.
type coreInput struct {
	raw  string
	year int
}

// coreCorpusFiles are the committed parts of the corpus: the oracle's smoke
// sample, and VINs chosen for this stage (non-ASCII input, the dedup's
// tie-breaks, conversions, vehicle specs, make paths).
var coreCorpusFiles = []string{"../../testdata/oracle/sample-vins.tsv", "testdata/core-vins.tsv"}

func loadCorpus(t *testing.T, path string) []coreInput {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var in []coreInput
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		vin, year, hasYear := strings.Cut(l, "\t")
		c := coreInput{raw: vin}
		if hasYear {
			if c.year, err = strconv.Atoi(year); err != nil {
				t.Fatalf("%s: %q: %v", path, l, err)
			}
		}
		in = append(in, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return in
}

// generatedCorpus derives n VINs from the artifact, deterministically: for
// wmi_vinschema rows at an even stride, the band's WMI and the first string
// one of its schema's pattern keys accepts, called with the band's first
// year.
func generatedCorpus(a *artifact.Artifact, n int) []coreInput {
	wt := a.Table("wmi")
	wmiOf := map[int32]string{}
	wmis := wt.Strings("wmi")
	for r, id := range wt.Int32s("id") {
		wmiOf[id], _ = wmis.At(r)
	}
	wvs := a.Table("wmi_vinschema")
	wvsWmi, wvsSchema, wvsFrom := wvs.Int32s("wmiid"), wvs.Int32s("vinschemaid"), wvs.Int32s("yearfrom")
	pt := a.Table("pattern")
	schemaCol, keys := pt.Int32s("vinschemaid"), pt.Strings("keys")
	var out []coreInput
	for i := range n {
		b := i * wvs.Rows() / n
		lo, hi := artifact.EqualRange(schemaCol, 0, pt.Rows(), wvsSchema[b])
		if lo == hi {
			continue
		}
		k, _ := keys.At(lo + (i*7919)%(hi-lo))
		vk := []byte("00000|00000000")
		if p := keyProbes(k); len(p[0]) > 0 {
			copy(vk, p[0])
		}
		vk[5] = '|'
		w := wmiOf[wvsWmi[b]]
		vin := []byte(w[:3] + string(vk[:5]) + "0" + string(vk[6:]))
		if len(w) == 6 {
			copy(vin[11:14], w[3:])
		}
		out = append(out, coreInput{string(vin), int(wvsFrom[b])})
	}
	return out
}

func checkItems(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	var corpus []coreInput
	for _, f := range coreCorpusFiles {
		corpus = append(corpus, loadCorpus(t, f)...)
	}
	corpus = append(corpus, generatedCorpus(a, 300)...)

	var in strings.Builder
	in.WriteString(`create table core_oracle.input (i int primary key, raw varchar, year int);
copy core_oracle.input from stdin;
`)
	for i, c := range corpus {
		year := `\N`
		if c.year != 0 {
			year = strconv.Itoa(c.year)
		}
		fmt.Fprintf(&in, "%d\t%s\t%s\n", i, escapeCopy(c.raw), year)
	}
	in.WriteString(`\.
create table core_oracle.call as
	select i, 1 as call, n, coalesce(year, abs(vpic.fvinmodelyear2(n))) as year
	from (select i, upper(trim(raw)) as n, year from core_oracle.input) s
	union all
	select i, 2, upper(trim(raw)), null from core_oracle.input;
copy (select 'N', (extract(epoch from now()) * 1000000)::bigint) to stdout;
copy (select 'C', i, call, year from core_oracle.call order by i, call) to stdout;
-- One statement, so one transaction, per call: the procedure's temporary
-- tables would exhaust the lock table in one.
select format($$copy (
	select 'I', %s, %s, coreelementid, coreattributeid, corevalue, coresource,
		corepriority, corekeys, corepatternid, corevinschemaid, corewmiid,
		(extract(epoch from corecreatedon) * 1000000)::bigint, coretobeqced
	from vpic.spvindecode_core(3, %s, %L, '***X*|Y', true, false)
	where coresource is distinct from 'Corrections'
) to stdout$$, i, call, coalesce(year::text, 'null'), n)
from core_oracle.call order by i, call \gexec
`)
	type callKey struct{ i, call int }
	var now int64
	years := map[callKey]int{}
	sources := map[string]int{}
	want := map[callKey][]string{}
	for _, l := range lines(psql(in.String())) {
		f := strings.Split(l, "\t")
		switch f[0] {
		case "N":
			now, _ = strconv.ParseInt(f[1], 10, 64)
		case "C":
			i, _ := strconv.Atoi(f[1])
			call, _ := strconv.Atoi(f[2])
			y, _ := strconv.Atoi(f[3]) // \N is 0, NULL
			years[callKey{i, call}] = y
		case "I":
			i, _ := strconv.Atoi(f[1])
			call, _ := strconv.Atoi(f[2])
			row := f[3:]
			if row[3] == "Formula Pattern" {
				// The value is var_keys text, where sanitize's '&' stands
				// for each non-ASCII rune (see sanitize).
				row[2] = ampersand(row[2])
			}
			k := callKey{i, call}
			want[k] = append(want[k], strings.Join(row, "\t"))
			source := row[3]
			if strings.HasPrefix(source, "Conversion ") {
				source, _, _ = strings.Cut(source, ":")
			}
			sources[source]++
		}
	}
	if len(years) != 2*len(corpus) {
		t.Fatalf("Postgres answered %d of %d calls", len(years), 2*len(corpus))
	}

	e := newEngine(a)
	bad, rows, empty := 0, 0, 0
	for k, year := range years {
		c := corpus[k.i]
		items, err := e.core(sanitize(c.raw), year, "***X*|Y", now)
		if err != nil {
			t.Errorf("%q year %d: %v", c.raw, year, err)
			continue
		}
		got := make([]string, len(items))
		for j := range items {
			got[j] = itemRow(&items[j])
		}
		w := want[k]
		slices.Sort(got)
		slices.Sort(w)
		rows += len(w)
		if len(w) == 0 {
			empty++
		}
		if !slices.Equal(got, w) {
			if bad++; bad <= 10 {
				t.Errorf("%q year %d:\n%s", c.raw, year, rowsDiff(got, w))
			}
		}
	}
	t.Logf("%d VINs, %d calls (%d with no items), %d rows: %d calls disagree", len(corpus), len(years), empty, rows, bad)
	t.Logf("Postgres rows by source: %v", sources)
}

// itemRow is an item as checkItems reads Postgres's rows: element,
// attribute, value, source, priority, keys, pattern, schema, wmi, created
// (µs), tobeqced, in COPY text format.
func itemRow(it *item) string {
	num := func(v int32) string {
		if v == artifact.NullInt32 {
			return `\N`
		}
		return strconv.Itoa(int(v))
	}
	created := `\N`
	if it.createdOn != artifact.NullInt64 {
		created = strconv.FormatInt(it.createdOn, 10)
	}
	qc := `\N`
	switch it.toBeQCed {
	case 0:
		qc = "f"
	case 1:
		qc = "t"
	}
	return strings.Join([]string{
		num(it.elementID), copyField(it.attributeID), copyField(it.value), escapeCopy(it.source),
		num(it.priority), copyField(it.keys), num(it.patternID), num(it.vinSchemaID), num(it.wmiID),
		created, qc,
	}, "\t")
}

func copyField(s sqlText) string {
	if !s.valid {
		return `\N`
	}
	return escapeCopy(s.s)
}

// rowsDiff lists the rows only one side has.
func rowsDiff(got, want []string) string {
	var b strings.Builder
	for _, g := range got {
		if !slices.Contains(want, g) {
			fmt.Fprintf(&b, "  core only:     %s\n", g)
		}
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			fmt.Fprintf(&b, "  Postgres only: %s\n", w)
		}
	}
	if b.Len() == 0 {
		fmt.Fprintf(&b, "  same rows, different multiplicity: core %d, Postgres %d\n", len(got), len(want))
	}
	return b.String()
}
