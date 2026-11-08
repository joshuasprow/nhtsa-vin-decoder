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

// TestCorePostgres checks a pass against the restored dump it was built
// from (NITROVIN_PSQL and NITROVIN_ARTIFACT as in TestMatcherPostgres):
//
//   - case: Go's upper and lower agree with Postgres's on every non-ASCII
//     string the core upper- or lower-cases (make, manufacturer and vehicle
//     type names, pattern, engine model pattern and vehicle spec attribute
//     ids, engine model names);
//   - conversion: for every conversion and every distinct AttributeId its
//     FromElementId has in pattern or enginemodelpattern, convert agrees with
//     the procedure's own `select (formula)::varchar(500)`, '0' on error;
//   - passes: for every corpus VIN, called with the year fVinModelYear2
//     gives it (or the corpus's), conclusive and without error12, and again
//     with a NULL year, inconclusive and with error12, pass returns the
//     multiset of rows vpic.spvindecode_core returns, compared on every
//     column but the pass number, and its return code; errorValue agrees
//     with vpic.fErrorValue on element 143; and errorCode, on core's items,
//     returns what vpic.spvindecode_errorcode does on Postgres's.
//
// Passes run against the freshened oracle (ADR 0006): one transaction
// truncates vpic.wmiyearvalidchars and is rolled back, so the procedure
// recomputes every charset as the engine does. The test writes scratch
// tables to the schema core_oracle and drops it.
func TestCorePostgres(t *testing.T) {
	a, psql := dumpUnderTest(t)
	t.Cleanup(func() { psql("drop schema if exists core_oracle cascade;\n") })
	psql("drop schema if exists core_oracle cascade; create schema core_oracle;\n")

	t.Run("case", func(t *testing.T) { checkCase(t, a, psql) })
	t.Run("conversion", func(t *testing.T) { checkConversions(t, a, psql) })
	t.Run("passes", func(t *testing.T) { checkPasses(t, a, psql) })
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
// sample, VINs chosen for the core (non-ASCII input, the dedup's
// tie-breaks, conversions, vehicle specs, make paths) and for the error
// codes.
var coreCorpusFiles = []string{
	"../../testdata/oracle/sample-vins.tsv", "testdata/core-vins.tsv", "testdata/error-vins.tsv",
}

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

// mutatedCorpus derives error-path VINs from base, deterministically: each
// VIN with one position the correction search reads changed (codes 2-4 or
// 14 when the new character is not valid there), every third with a
// second one (code 5), and every seventh cut short; the characters cycle
// through letters, digits, I/O/Q, '_', '*', a space and a non-ASCII rune.
func mutatedCorpus(base []coreInput) []coreInput {
	positions := []int{4, 5, 6, 7, 8, 11, 12, 13, 14}
	chars := []rune("Z0I8A_9Q* É3OK")
	out := make([]coreInput, 0, len(base))
	for k, c := range base {
		vin := []rune(c.raw)
		vin[positions[k%len(positions)]-1] = chars[k%len(chars)]
		if k%3 == 0 {
			vin[positions[(k/3+4)%len(positions)]-1] = chars[(k/3+5)%len(chars)]
		}
		if k%7 == 0 {
			vin = vin[:9+k%8]
		}
		out = append(out, coreInput{strings.Trim(string(vin), " "), c.year})
	}
	return out
}

// passSQL runs every call of core_oracle.call inside one transaction that
// truncates vpic.wmiyearvalidchars, the freshened oracle, and rolls back.
// Each call is a savepoint rolled back after it, which releases the locks
// of the procedure's temporary tables (one transaction holding them all
// exhausts the lock table). Per call: the core's rows (I), its return code
// and fErrorValue of element 143 (R), and spvindecode_errorcode's OUT
// parameters (E), called after the core's items, but the Corrections and
// the defaults, fill the DecodingItems table it reads.
const passSQL = `begin;
truncate vpic.wmiyearvalidchars;
select 'savepoint c',
	format($$create temp table r as select * from vpic.spvindecode_core(3, %s, %L, '***X*|Y', %L, %L)$$,
		coalesce(year::text, 'null'), n, conclusive, error12),
	format($$copy (
	select 'I', %s, %s, coreelementid, coreattributeid, corevalue, coresource,
		corepriority, corekeys, corepatternid, corevinschemaid, corewmiid,
		(extract(epoch from corecreatedon) * 1000000)::bigint, coretobeqced
	from r) to stdout$$, i, call),
	format($$copy (
	select 'R', %s, %s, (select corereturncode from r limit 1),
		vpic.ferrorvalue((select corevalue from r where coreelementid = 143))
	) to stdout$$, i, call),
	'create temp table decodingitems (id serial primary key, decodingitem vpic."tblDecodingItem")',
	$$insert into decodingitems (decodingitem)
	select row(null, 3, corecreatedon, corepatternid, corekeys, corevinschemaid, corewmiid,
		coreelementid, coreattributeid, corevalue, coresource, corepriority, coretobeqced)::vpic."tblDecodingItem"
	from r where coresource not in ('Corrections', 'Default')$$,
	format($$copy (select 'E', %s, %s, * from vpic.spvindecode_errorcode(%L, %s)) to stdout$$,
		i, call, n, coalesce(year::text, 'null')),
	'rollback to savepoint c'
from core_oracle.call order by i, call \gexec
rollback;
`

func checkPasses(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	var corpus []coreInput
	for _, f := range coreCorpusFiles {
		corpus = append(corpus, loadCorpus(t, f)...)
	}
	generated := generatedCorpus(a, 300)
	corpus = append(corpus, generated...)
	corpus = append(corpus, mutatedCorpus(generated)...)

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
	select i, 1 as call, n, coalesce(year, abs(vpic.fvinmodelyear2(n))) as year, true as conclusive, false as error12
	from (select i, upper(trim(raw)) as n, year from core_oracle.input) s
	union all
	select i, 2, upper(trim(raw)), null, false, true from core_oracle.input;
copy (select 'N', (extract(epoch from now()) * 1000000)::bigint) to stdout;
copy (select 'C', i, call, year, conclusive, error12 from core_oracle.call order by i, call) to stdout;
`)
	in.WriteString(passSQL)
	type callKey struct{ i, call int }
	type callArgs struct {
		year              int
		conclusive, err12 bool
	}
	var now int64
	calls := map[callKey]callArgs{}
	sources := map[string]int{}
	codes := map[string]int{}
	want := map[callKey][]string{}
	wantE := map[callKey]string{}
	for _, l := range lines(psql(in.String())) {
		f := strings.Split(l, "\t")
		k := callKey{}
		if len(f) > 2 {
			k.i, _ = strconv.Atoi(f[1])
			k.call, _ = strconv.Atoi(f[2])
		}
		switch f[0] {
		case "N":
			now, _ = strconv.ParseInt(f[1], 10, 64)
		case "C":
			y, _ := strconv.Atoi(f[3]) // \N is 0, NULL
			calls[k] = callArgs{y, f[4] == "t", f[5] == "t"}
		case "I":
			row := f[3:]
			if row[3] == "Formula Pattern" {
				// The value is var_keys text, where sanitize's '&' stands
				// for each non-ASCII rune (see sanitize).
				row[2] = ampersand(row[2])
			}
			want[k] = append(want[k], strings.Join(row, "\t"))
			source := row[3]
			if strings.HasPrefix(source, "Conversion ") {
				source, _, _ = strings.Cut(source, ":")
			}
			sources[source]++
		case "R":
			want[k] = append(want[k], "R\t"+strings.Join(f[3:], "\t"))
			for _, c := range strings.Fields(f[3]) {
				codes[c]++
			}
		case "E":
			wantE[k] = strings.Join(f[3:], "\t")
		}
	}
	if len(calls) != 2*len(corpus) || len(wantE) != len(calls) {
		t.Fatalf("Postgres answered %d and %d of %d calls", len(calls), len(wantE), 2*len(corpus))
	}

	e := newEngine(a)
	bad, badE, rows := 0, 0, 0
	for k, c := range calls {
		input := corpus[k.i]
		v := newVINText(input.raw)
		items, rc, err := e.pass(v, c.year, "***X*|Y", c.conclusive, c.err12, now)
		if err != nil {
			t.Errorf("%q year %d: %v", input.raw, c.year, err)
			continue
		}
		got := make([]string, 0, len(items)+1)
		var codes143 sqlText
		for j := range items {
			got = append(got, itemRow(&items[j]))
			if items[j].elementID == 143 {
				codes143 = items[j].value
			}
		}
		got = append(got, "R\t"+rc+"\t"+strconv.Itoa(e.errorValue(codes143)))
		w := want[k]
		slices.Sort(got)
		slices.Sort(w)
		rows += len(w)
		if !slices.Equal(got, w) {
			if bad++; bad <= 10 {
				t.Errorf("%q year %d: pass:\n%s", input.raw, c.year, rowsDiff(got, w))
			}
		}

		pre, err := e.core(v.s, c.year, "***X*|Y", now)
		if err != nil {
			t.Fatal(err) // pass returned no error
		}
		wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), wmi(v.s))
		res, err := e.errorCode(v, c.year, pre, wlo, whi)
		if err != nil {
			t.Errorf("%q year %d: errorCode: %v", input.raw, c.year, err)
			continue
		}
		gotE := strings.Join([]string{
			escapeCopy(res.returnCode), escapeCopy(res.correctedVIN), escapeCopy(res.errorBytes), copyField(res.unusedPositions),
		}, "\t")
		if gotE != wantE[k] {
			if badE++; badE <= 10 {
				t.Errorf("%q year %d: errorCode %q, spvindecode_errorcode %q", input.raw, c.year, gotE, wantE[k])
			}
		}
	}
	t.Logf("%d VINs, %d calls, %d rows: %d passes and %d errorCode calls disagree", len(corpus), len(calls), rows, bad, badE)
	t.Logf("Postgres rows by source: %v", sources)
	t.Logf("Postgres passes by code: %v", codes)
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
