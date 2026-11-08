package decoder

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestProjectionPostgres checks Project against the dump the artifact was
// built from (NITROVIN_PSQL and NITROVIN_ARTIFACT as in TestMatcherPostgres):
// for every corpus VIN, without a hint and with one (TestWrapperPostgres's
// calls), the rows the uncut vpic.spvindecode returns, compared field for
// field on all 15 columns as a multiset, every value exactly (element 144
// included, where ultravin compares a set), or an error where the procedure
// raises. The procedure's row order is checked too: groupRank never falls
// along it, nor along Project's items.
//
// It runs against the freshened oracle (ADR 0006): one transaction that
// truncates vpic.wmiyearvalidchars and rolls back, one savepoint per call.
// The subtest qc marks every third VinSchema QC'd there and in the engine,
// as TestWrapperPostgres does, so some decodes project no rows at all.
func TestProjectionPostgres(t *testing.T) {
	a, psql := dumpUnderTest(t)
	t.Cleanup(func() { psql("drop schema if exists projection_oracle cascade;\n") })
	psql("drop schema if exists projection_oracle cascade; create schema projection_oracle;\n" + projectionCall)
	corpus := loadCallInputs(t, psql, "projection_oracle")

	t.Run("hints", func(t *testing.T) {
		checkProjections(t, newEngine(a), corpus, psql, "", hintCalls("projection_oracle"))
	})
	t.Run("qc", func(t *testing.T) {
		checkProjections(t, qcEngine(a), corpus, psql, qcSetup, "select i, 1 as call, raw, null::int as year from projection_oracle.input")
	})
}

// projectionCall is vpic.spvindecode(v, year => y) with each row's place in
// its output, or one row with n = -1 and the error's message where it
// raises.
const projectionCall = `create function projection_oracle.call(v varchar, y int) returns table(
	o_n bigint, o_err text, o_groupname varchar, o_variable varchar, o_value varchar, o_pattern int,
	o_schema int, o_keys varchar, o_element int, o_attribute varchar, o_created timestamp, o_wmi int,
	o_code varchar, o_datatype varchar, o_decode varchar, o_source varchar, o_qc boolean)
language plpgsql as $$
begin
	return query select s.n, null::text, s.groupname, s.variable, s.value, s.itempatternid,
		s.itemvinschemaid, s.itemkeys, s.itemelementid, s.itemattributeid, s.itemcreatedon, s.itemwmiid,
		s.code, s.datatype, s.decode, s.itemsource, s.itemtobeqced
	from vpic.spvindecode(v => v, year => y) with ordinality as s(groupname, variable, value,
		itempatternid, itemvinschemaid, itemkeys, itemelementid, itemattributeid, itemcreatedon,
		itemwmiid, code, datatype, decode, itemsource, itemtobeqced, n);
exception when others then
	return query select -1::bigint, sqlerrm, null::varchar, null::varchar, null::varchar, null::int,
		null::int, null::varchar, null::int, null::varchar, null::timestamp, null::int, null::varchar,
		null::varchar, null::varchar, null::varchar, null::boolean;
end $$;
`

// checkProjections runs every call calls lists (a query of i, call, raw,
// year) on Postgres after setup, as checkDecodes does, and with e.
func checkProjections(t *testing.T, e *engine, corpus []coreInput, psql func(string) string, setup, calls string) {
	script := "drop table if exists projection_oracle.call;\ncreate table projection_oracle.call as " + calls + `;
begin;
set local datestyle = 'ISO, MDY';
truncate vpic.wmiyearvalidchars;
` + setup + `copy (select 'N', (extract(epoch from now()) * 1000000)::bigint) to stdout;
copy (select 'C', i, call, year from projection_oracle.call order by i, call) to stdout;
select 'savepoint c',
	format($$copy (select 'I', %s, %s, * from projection_oracle.call(%L, %s)) to stdout$$,
		i, call, raw, coalesce(year::text, 'null')),
	'rollback to savepoint c'
from projection_oracle.call order by i, call \gexec
rollback;
`
	type callKey struct{ i, call int }
	type oracleRow struct {
		n   int
		row string
	}
	var now int64
	hints := map[callKey]yearHint{}
	want := map[callKey][]oracleRow{}
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
			n, _ := strconv.Atoi(f[3])
			if n < 0 {
				raised[k] = f[4]
				continue
			}
			want[k] = append(want[k], oracleRow{n, strings.Join(f[5:], "\t")})
		}
	}
	if len(hints) == 0 {
		t.Fatal("Postgres listed no calls")
	}

	bad, rows, empty, misordered, byRank := 0, 0, 0, 0, map[uint8]int{}
	cdValid, cdExempt := 0, 0
	for k, h := range hints {
		raw := corpus[k.i].raw
		p, err := e.project(raw, h, now)
		msg, pgRaised := raised[k]
		switch {
		case err != nil && pgRaised:
			continue
		case err != nil:
			t.Errorf("%q hint %v: Project raised %v, Postgres did not", raw, h, err)
			bad++
			continue
		case pgRaised:
			t.Errorf("%q hint %v: Postgres raised %q, Project did not", raw, h, msg)
			bad++
			continue
		}

		w := want[k]
		slices.SortFunc(w, func(a, b oracleRow) int { return a.n - b.n })
		wantRows := make([]string, len(w))
		last := uint8(0)
		for j, r := range w {
			wantRows[j] = r.row
			g := unescapeCopy(strings.SplitN(r.row, "\t", 2)[0])
			if g == `\N` {
				g = ""
			}
			rank := groupRank(g)
			if rank < last {
				misordered++
				t.Errorf("%q hint %v: spvindecode's row %d (%q) falls from group rank %d to %d", raw, h, r.n, g, last, rank)
			}
			last = rank
			byRank[rank]++
		}
		got := make([]string, len(p.Items))
		last = 0
		for j, it := range p.Items {
			got[j] = projectedRow(it)
			if rank := groupRank(it.GroupName.V); rank < last {
				misordered++
				t.Errorf("%q hint %v: Project's item %d falls from group rank %d to %d", raw, h, j, last, rank)
			} else {
				last = rank
			}
		}
		slices.Sort(got)
		slices.Sort(wantRows)
		rows += len(wantRows)
		if len(wantRows) == 0 {
			empty++
		}
		if !slices.Equal(got, wantRows) {
			if bad++; bad <= 10 {
				t.Errorf("%q hint %v:\n%s", raw, h, rowsDiff(got, wantRows))
			}
		}

		// check_digit_valid against code 1, which C17 sets from the same
		// comparison unless a VinException exempts the VIN.
		code1 := false
		for _, it := range p.Items {
			if it.ElementID == 143 && it.Value.Valid {
				code1 = slices.Contains(strings.Split(it.Value.V, ","), "1")
			}
		}
		switch {
		case p.CheckDigitValid && (code1 || len(p.VIN) != 17):
			t.Errorf("%q: check_digit_valid with code 1 or %d characters", raw, len(p.VIN))
		case p.CheckDigitValid:
			cdValid++
		case len(p.VIN) == 17 && !code1 && len(p.Items) > 0:
			if !e.checkDigitExempt(string(newVINText(raw).echo)) {
				t.Errorf("%q: check digit invalid, no code 1 and no VinException", raw)
			}
			cdExempt++
		}
	}
	t.Logf("%d calls (%d raised on both sides), %d rows, %d with no rows: %d disagree, %d misordered",
		len(hints), len(raised), rows, empty, bad, misordered)
	t.Logf("rows by group rank: %v", byRank)
	t.Logf("check digit valid on %d decodes, invalid and VinException-exempt on %d", cdValid, cdExempt)
}

// projectedRow is a projected item as COPY renders spvindecode's columns.
func projectedRow(it contract.Item) string {
	str := func(s contract.Null[string]) string {
		if !s.Valid {
			return `\N`
		}
		return escapeCopy(s.V)
	}
	num := func(n contract.Null[int32]) string {
		if !n.Valid {
			return `\N`
		}
		return strconv.Itoa(int(n.V))
	}
	qc := `\N`
	if it.ToBeQCed.Valid {
		qc = map[bool]string{false: "f", true: "t"}[it.ToBeQCed.V]
	}
	return strings.Join([]string{
		str(it.GroupName), escapeCopy(it.Variable), str(it.Value), num(it.PatternID), num(it.VinSchemaID),
		str(it.Keys), strconv.Itoa(int(it.ElementID)), str(it.AttributeID), str(it.CreatedOn), num(it.WmiID),
		str(it.Code), str(it.DataType), str(it.Decode), str(it.Source), qc,
	}, "\t")
}

// TestElementAttributeValuePostgres holds attributeLookups to the dump's
// fElementAttributeValue, arm for arm, and resolve to the function's
// answers: for every element, attribute texts that cast from no id ('05',
// '+5', ' 5', ”, NULL, out of int4 range), and for every CASE element the
// 40 lowest and 40 highest ids of its relation. It also checks that the
// artifact's lookup union carries every row of every relation an arm reads.
func TestElementAttributeValuePostgres(t *testing.T) {
	a, psql := dumpUnderTest(t)
	e := newEngine(a)
	def := unescapeCopy(strings.TrimSuffix(psql("copy (select pg_get_functiondef('vpic.felementattributevalue'::regproc)) to stdout;\n"), "\n"))

	arm := regexp.MustCompile(`(?s)WHEN (\d+) THEN\s+select name from vpic\.(\w+) where cast\(Id as character varying\) = AttributeId into v;\s+return v;\s+`)
	arms := map[int32]string{}
	for _, m := range arm.FindAllStringSubmatch(def, -1) {
		id, _ := strconv.Atoi(m[1])
		arms[int32(id)] = strings.ToLower(m[2])
	}
	body := arm.ReplaceAllString(def, "")
	if want := regexp.MustCompile(`(?s)CASE ElementId\s+ELSE\s+return v;\s+END CASE;`); !want.MatchString(body) || strings.Count(def, "WHEN") != len(arms) {
		t.Fatalf("fElementAttributeValue has an arm of another shape than `select name … into v; return v;`, or more than an ELSE:\n%s", body)
	}
	for id, table := range arms {
		if attributeLookups[id] != table {
			t.Errorf("element %d: fElementAttributeValue reads %q, attributeLookups %q", id, table, attributeLookups[id])
		}
	}
	for id := range attributeLookups {
		if _, ok := arms[id]; !ok {
			t.Errorf("element %d: in attributeLookups, not in fElementAttributeValue", id)
		}
	}

	var sql strings.Builder
	sql.WriteString(`create temp table probe (e int, a varchar);
insert into probe select el.id, p.a from vpic.element el cross join (values (null::varchar), (''), ('0'), ('-1'), ('05'),
	('+5'), (' 5'), ('5 '), ('abc'), ('1.0'), ('999999'), ('2147483648'), ('-2147483649')) p(a);
`)
	tables := map[string]bool{}
	for id, table := range arms {
		fmt.Fprintf(&sql, `insert into probe select %d, id::varchar from (select id from vpic.%s order by id limit 40) s
	union select %[1]d, id::varchar from (select id from vpic.%[2]s order by id desc limit 40) s;
`, id, table)
		tables[table] = true
	}
	for table := range tables {
		fmt.Fprintf(&sql, "copy (select 'T', %s, count(*) from vpic.%s) to stdout;\n", pqLiteral(table), table)
	}
	sql.WriteString("copy (select 'P', e, a, vpic.felementattributevalue(e, a) from probe) to stdout;\n")

	bad, probes, hits := 0, 0, 0
	for _, l := range lines(psql(sql.String())) {
		f := strings.Split(l, "\t")
		switch f[0] {
		case "T":
			n, _ := strconv.Atoi(f[2])
			var l lookupTable
			for id, table := range attributeLookups {
				if table == f[1] {
					l = e.out.lookup[e.element.row[id]]
				}
			}
			if l.hi-l.lo != n {
				t.Errorf("relation %s: %d rows in the dump, %d in the artifact's lookup union", f[1], n, l.hi-l.lo)
			}
		case "P":
			id, _ := strconv.Atoi(f[1])
			attr := sqlText{unescapeCopy(f[2]), f[2] != `\N`}
			got, err := e.out.resolve(e.element.row[id], attr)
			if err != nil {
				t.Fatal(err)
			}
			probes++
			if got.valid && attributeLookups[int32(id)] != "" {
				hits++
			}
			if copyField(got) != f[3] {
				if bad++; bad <= 10 {
					t.Errorf("fElementAttributeValue(%d, %s) = %s, resolve %s", id, f[2], f[3], copyField(got))
				}
			}
		}
	}
	t.Logf("%d arms over %d relations; %d probes, %d resolved to a name: %d disagree", len(arms), len(tables), probes, hits, bad)
}

func pqLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
