package decoder

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestWrapperPostgres checks decode against the restored dump it was built
// from (NITROVIN_PSQL and NITROVIN_ARTIFACT as in TestMatcherPostgres): for
// every corpus VIN, without a hint and with one, decode returns the
// winning pass and the items vpic.spvindecode holds just before it
// resolves values (:204), compared as a multiset on every column, or
// raises where the procedure does.
//
// The procedure is the dump's own, read with pg_get_functiondef and cut
// at the resolution: wrapperFunction. It runs against the freshened oracle
// (ADR 0006), in one transaction that truncates vpic.wmiyearvalidchars and
// rolls back. The dump has no VinSchema with TobeQCed, so the subtest qc
// marks every third one QC'd in that transaction, and in the engine, and
// decodes the corpus again without hints. The test writes scratch tables to
// the schema wrapper_oracle and drops it.
func TestWrapperPostgres(t *testing.T) {
	a, psql := dumpUnderTest(t)
	t.Cleanup(func() { psql("drop schema if exists wrapper_oracle cascade;\n") })
	psql("drop schema if exists wrapper_oracle cascade; create schema wrapper_oracle;\n")
	psql(wrapperFunction(t, psql) + wrapperCall)

	var corpus []coreInput
	for _, f := range append(coreCorpusFiles, "testdata/wrapper-vins.tsv", "testdata/ultravin-vins.tsv") {
		corpus = append(corpus, loadCorpus(t, f)...)
	}
	var in strings.Builder
	in.WriteString("create table wrapper_oracle.input (i int primary key, raw varchar, year int);\ncopy wrapper_oracle.input from stdin;\n")
	for i, c := range corpus {
		year := `\N`
		if c.year != 0 {
			year = strconv.Itoa(c.year)
		}
		fmt.Fprintf(&in, "%d\t%s\t%s\n", i, escapeCopy(c.raw), year)
	}
	in.WriteString("\\.\n")
	psql(in.String())

	t.Run("hints", func(t *testing.T) {
		checkDecodes(t, newEngine(a), corpus, psql, "", wrapperHints)
	})
	t.Run("qc", func(t *testing.T) {
		e := newEngine(a)
		qc := slices.Clone(e.vinSchema.toBeQCed)
		for r, id := range e.vinSchema.id {
			if id%3 == 0 {
				qc[r] = 1
			}
		}
		e.vinSchema.toBeQCed = qc
		checkDecodes(t, e, corpus, psql, "update vpic.vinschema set tobeqced = true where id % 3 = 0;\n",
			"select i, 1 as call, raw, null::int as year from wrapper_oracle.input")
	})
}

// wrapperHints is each input's calls: without a hint, and with the
// corpus's year or else one derived from fVinModelYear2's y, cycling
// through a year past it (pass 2), 30 years before it (omy when
// inconclusive), 1979 and three past NOW()'s (out of range: passes 3 and 4
// with code 12), and |y| itself (rmy, unless altMY replaced it).
const wrapperHints = `select i, 1 as call, raw, null::int as year from wrapper_oracle.input
union all
select i, 2, raw, coalesce(year, case i % 5
		when 0 then coalesce(abs(y) + 1, 2015)
		when 1 then coalesce(abs(y) - 30, 1990)
		when 2 then 1979
		when 3 then extract(year from now())::int + 3
		else coalesce(abs(y), 2026) end)
	from (select *, vpic.fvinmodelyear2(upper(trim(raw))) as y from wrapper_oracle.input) s`

// wrapperFunction is vpic.spvindecode as wrapper_oracle.items: the dump's
// definition up to the resolution (:204), then the winning pass as a row
// with a NULL element, then the DecodingItems left. Each cut is checked to
// match exactly once.
func wrapperFunction(t *testing.T, psql func(string) string) string {
	t.Helper()
	def := unescapeCopy(strings.TrimSuffix(psql("copy (select pg_get_functiondef('vpic.spvindecode'::regproc)) to stdout;\n"), "\n"))
	cut := func(s, from, to, with string) string {
		t.Helper()
		i := strings.Index(s, from)
		if i < 0 || strings.Count(s, from) != 1 {
			t.Fatalf("spvindecode's definition has %d of %q", strings.Count(s, from), from)
		}
		j := i + len(from)
		if to != "" {
			if j = strings.Index(s[i:], to); j < 0 {
				t.Fatalf("spvindecode's definition has no %q after %q", to, from)
			}
			j += i + len(to)
		}
		return s[:i] + with + s[j:]
	}
	def = cut(def, "CREATE OR REPLACE FUNCTION vpic.spvindecode(", "", "create function wrapper_oracle.items(")
	def = cut(def, " RETURNS TABLE(", ")\n", ` returns table(o_pass integer, o_element integer, o_attribute varchar,
	o_value varchar, o_source varchar, o_priority integer, o_keys varchar, o_pattern integer, o_schema integer,
	o_wmi integer, o_created bigint, o_qc boolean)
`)
	return cut(def, "\tupdate DecItem as t\n", "$function$", `	return query select bestPass, null::int, null::varchar, null::varchar, null::varchar,
		null::int, null::varchar, null::int, null::int, null::int, null::bigint, null::boolean;
	return query select ItemDecodingId, ItemElementId, ItemAttributeId::varchar, ItemValue::varchar,
		ItemSource::varchar, ItemPriority, ItemKeys::varchar, ItemPatternId, ItemVinSchemaId, ItemWmiId,
		(extract(epoch from ItemCreatedOn) * 1000000)::bigint, ItemTobeQCed
	from DecItem;
end;
$function$;
`)
}

// wrapperCall is wrapper_oracle.items, or one row with pass -1 and the
// error's message as value where it raises.
const wrapperCall = `create function wrapper_oracle.call(v varchar, y int) returns setof record
language plpgsql as $$
begin
	return query select * from wrapper_oracle.items(v => v, year => y);
exception when others then
	return query select -1, null::int, null::varchar, sqlerrm::varchar, null::varchar, null::int,
		null::varchar, null::int, null::int, null::int, null::bigint, null::boolean;
end $$;
`

// checkDecodes decodes every call calls (a query of i, call, raw, year)
// lists, on Postgres after setup in one transaction that truncates
// vpic.wmiyearvalidchars, and with e. Each call is a savepoint rolled back
// after it, releasing the locks of the procedure's temporary tables.
func checkDecodes(t *testing.T, e *engine, corpus []coreInput, psql func(string) string, setup, calls string) {
	script := "drop table if exists wrapper_oracle.call;\ncreate table wrapper_oracle.call as " + calls + `;
begin;
truncate vpic.wmiyearvalidchars;
` + setup + `copy (select 'N', (extract(epoch from now()) * 1000000)::bigint) to stdout;
copy (select 'C', i, call, year from wrapper_oracle.call order by i, call) to stdout;
select 'savepoint c',
	format($$copy (select 'I', %s, %s, * from wrapper_oracle.call(%L, %s) as (
		o_pass integer, o_element integer, o_attribute varchar, o_value varchar, o_source varchar,
		o_priority integer, o_keys varchar, o_pattern integer, o_schema integer, o_wmi integer,
		o_created bigint, o_qc boolean)) to stdout$$, i, call, raw, coalesce(year::text, 'null')),
	'rollback to savepoint c'
from wrapper_oracle.call order by i, call \gexec
rollback;
`
	type callKey struct{ i, call int }
	var now int64
	hints := map[callKey]yearHint{}
	want := map[callKey][]string{}
	wantPass := map[callKey]int{}
	raised := map[callKey]string{}
	for _, l := range lines(psql(script)) {
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
			y, err := strconv.Atoi(f[3])
			hints[k] = yearHint{y, err == nil} // \N is NULL
		case "I":
			pass, _ := strconv.Atoi(f[3])
			row := f[4:]
			switch {
			case pass < 0:
				raised[k] = row[3]
			case row[0] == `\N`:
				wantPass[k] = pass
			default:
				if row[3] == "Formula Pattern" {
					// The value is var_keys text, where sanitize's '&' stands
					// for each non-ASCII rune (see sanitize).
					row[2] = ampersand(row[2])
				}
				want[k] = append(want[k], strings.Join(row, "\t"))
			}
		}
	}
	if len(hints) == 0 || len(wantPass)+len(raised) != len(hints) {
		t.Fatalf("Postgres answered %d and raised on %d of %d calls", len(wantPass), len(raised), len(hints))
	}

	nowYear := time.UnixMicro(now).UTC().Year()
	bad, badPass, rows, empty := 0, 0, 0, 0
	byPass := map[int]int{}
	reached := map[string]int{}
	for k, h := range hints {
		raw := corpus[k.i].raw
		items, pass, err := e.decode(raw, h, now)
		msg, pgRaised := raised[k]
		switch {
		case err != nil && pgRaised:
			byPass[-1]++
			continue
		case err != nil:
			t.Errorf("%q hint %v: decode raised %v, Postgres did not", raw, h, err)
			bad++
			continue
		case pgRaised:
			t.Errorf("%q hint %v: Postgres raised %q, decode did not", raw, h, msg)
			bad++
			continue
		}
		byPass[pass]++
		cases := planCases(e, raw, h, nowYear)
		if slices.Contains(cases, "pass 2") && pass > 2 {
			cases = append(cases, "pass 2 ran, 3 or 4 won")
		}
		for _, c := range cases {
			reached[c]++
		}
		got := make([]string, 0, len(items))
		for j := range items {
			got = append(got, itemRow(&items[j]))
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
				t.Errorf("%q hint %v: pass %d, Postgres's %d:\n%s", raw, h, pass, wantPass[k], rowsDiff(got, w))
			}
		} else if pass != wantPass[k] {
			// Equal items from different passes: a full tie (see decode).
			badPass++
			t.Logf("%q hint %v: same items, pass %d, Postgres's %d", raw, h, pass, wantPass[k])
		}
	}
	t.Logf("%d calls, %d rows, %d with no items: %d disagree, %d tie won by another pass", len(hints), rows, empty, bad, badPass)
	t.Logf("winners by pass (-1: both raised): %v", byPass)
	t.Logf("cases reached: %v", reached)
}

// planCases names the year-plan and hint cases one decode reaches.
func planCases(e *engine, raw string, h yearHint, nowYear int) []string {
	vin := sanitize(raw)
	plan := e.planYears(vin, nowYear)
	var cases []string
	switch {
	case plan.rmy == 0:
		cases = append(cases, "rmy NULL")
	case !plan.conclusive:
		cases = append(cases, "inconclusive")
	default:
		wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), wmi(vin))
		carLT := wlo < whi && isCarMpvLT(int(e.wmi.vehicleTypeID[wlo]), int(e.wmi.truckTypeID[wlo]))
		if y, _ := modelYearRaw(vin, carLT, nowYear); y != plan.rmy {
			cases = append(cases, "altMY")
		}
	}
	switch {
	case !h.valid:
		cases = append(cases, "no hint")
	case h.year < 1980 || h.year > nowYear+2:
		cases = append(cases, "hint out of range")
	case h.year == plan.rmy || h.year == plan.omy:
		cases = append(cases, "hint is rmy or omy")
	default:
		cases = append(cases, "pass 2")
	}
	return cases
}
