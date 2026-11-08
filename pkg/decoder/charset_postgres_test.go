package decoder

import (
	"bytes"
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// TestCharsetPostgres checks the recomputed charset against the restored
// dump it was built from (NITROVIN_PSQL and NITROVIN_ARTIFACT as in
// TestMatcherPostgres):
//
//   - keys: for every distinct pattern.keys, validCharsInKey returns the
//     (position, character) set vpic.fValidCharsInKey does, or fails where
//     it raises;
//   - cells: for every (WMI, year) cell, every one wmiyearvalidchars holds
//     and every year from 1980 to two past NOW()'s that a band of the WMI
//     covers, charset returns the set fExtractValidCharsPerWmiYear's own
//     query yields: the distinct keys of every schema in a covering band of
//     any wmi row with the WMI, through the keys' fValidCharsInKey rows;
//   - fextract: charset agrees with vpic.fExtractValidCharsPerWmiYear
//     itself on NITROVIN_CHARSET_CELLS cells (default 1000, evenly spread;
//     "all" for every cell), run over NITROVIN_CHARSET_WORKERS sessions
//     (default 4), each call in its own transaction.
//
// It then logs how the dump's wmiyearvalidchars cache differs from the
// recompute in the cells the cache holds: the cells of the stale-cache
// class (docs/deviations/stale-cache.md), which spvindecode_errorcode reads
// from the cache and nitrovin recomputes.
//
// The test writes scratch tables to the schema charset_oracle and drops it.
func TestCharsetPostgres(t *testing.T) {
	a, psql := dumpUnderTest(t)
	t.Cleanup(func() { psql("drop schema if exists charset_oracle cascade;\n") })
	psql("drop schema if exists charset_oracle cascade; create schema charset_oracle;\n")
	e := newEngine(a)

	t.Run("keys", func(t *testing.T) { checkKeyChars(t, a, psql) })
	var cells []charsetCell
	t.Run("cells", func(t *testing.T) { cells = checkCells(t, e, psql) })
	t.Run("fextract", func(t *testing.T) { checkFExtract(t, e, cells) })
	t.Run("stale-cache", func(t *testing.T) { logStaleCache(t, psql) })
}

// canonChars renders sets as "position:characters" for each non-empty set,
// joined by ';', characters in byte order; sets[i] is position i+first.
func canonChars(sets []byteSet, first int) string {
	var b strings.Builder
	for i := range sets {
		if sets[i] == (byteSet{}) {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(';')
		}
		b.WriteString(strconv.Itoa(i+first) + ":")
		for c := range 256 {
			if sets[i].has(byte(c)) {
				b.WriteByte(byte(c))
			}
		}
	}
	return b.String()
}

// canonSQL aggregates a relation of (position p, character c) rows, given
// as SQL, the way canonChars renders a set: the empty string for none.
func canonSQL(rows string) string {
	return `(select coalesce(string_agg(p || ':' || chars, ';' order by p), '')
	from (select p, string_agg(c, '' order by c collate "C") chars
		from (select distinct p, c from (` + rows + `) r) d group by p) s)`
}

func checkKeyChars(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	keys := a.Table("pattern").Strings("keys")
	seen := map[uint32]bool{}
	var distinct []string
	for r := range keys.Len() {
		if ref := keys.Ref(r); !seen[ref] {
			seen[ref] = true
			if k, ok := keys.At(r); ok {
				distinct = append(distinct, k)
			}
		}
	}
	slices.Sort(distinct)

	var in strings.Builder
	in.WriteString(`create table charset_oracle.k (id int primary key, keys varchar);
copy charset_oracle.k from stdin;
`)
	for i, k := range distinct {
		fmt.Fprintf(&in, "%d\t%s\n", i, escapeCopy(k))
	}
	// 500 keys a statement: each call's temporary table holds its locks to
	// the end of the transaction.
	in.WriteString(`\.
create table charset_oracle.kc (k int, pos int, c text);
create table charset_oracle.kerr (k int, err text);
create function pg_temp.vk(k int, key varchar) returns void language plpgsql as $f$
begin
	insert into charset_oracle.kc select k, pos, return_chr from vpic.fvalidcharsinkey(key);
exception when others then
	insert into charset_oracle.kerr values (k, sqlerrm);
end $f$;
select format('do $$ begin perform pg_temp.vk(id, keys) from charset_oracle.k where id >= %s and id < %s; end $$', b, b + 500)
from generate_series(0, (select max(id) from charset_oracle.k), 500) b \gexec
copy (select k.id, coalesce(e.err, '!' || coalesce(x.s, ''))
	from charset_oracle.k k
	left join charset_oracle.kerr e on e.k = k.id
	left join (select k, string_agg(pos || ':' || chars, ';' order by pos) s
		from (select k, pos, string_agg(c, '' order by c collate "C") chars
			from (select distinct k, pos, c from charset_oracle.kc) d group by 1, 2) t
		group by 1) x on x.k = k.id
	order by k.id) to stdout;
`)
	got := lines(psql(in.String()))
	if len(got) != len(distinct) {
		t.Fatalf("Postgres answered %d of %d keys", len(got), len(distinct))
	}
	bad, raised := 0, 0
	for i, l := range got {
		_, want, _ := strings.Cut(l, "\t")
		k := distinct[i]
		sets, err := validCharsInKey(k)
		mine := "!" + canonChars(sets, 1)
		if err != nil {
			mine = err.Error()
		}
		if !strings.HasPrefix(want, "!") {
			raised++
		}
		if mine != want {
			if bad++; bad <= 20 {
				t.Errorf("key %q: Go %q, Postgres %q", k, mine, want)
			}
		}
	}
	t.Logf("%d distinct keys (%d raise): %d disagree", len(distinct), raised, bad)
}

type charsetCell struct {
	wmi  string
	year int
	// want is the cell as Postgres's recompute renders it (canonChars).
	want string
}

func checkCells(t *testing.T, e *engine, psql func(string) string) []charsetCell {
	out := psql(`create table charset_oracle.cell as
	select distinct wmi, year::int from vpic.wmiyearvalidchars
	union
	select distinct w.wmi, y from vpic.wmi w join vpic.wmi_vinschema b on b.wmiid = w.id
		join generate_series(1980, extract(year from now())::int + 2) y
		on y between b.yearfrom and coalesce(b.yearto, 2999);
-- fExtractValidCharsPerWmiYear's cursor (fextractvalidcharsperwmiyear.sql:8-16),
-- each distinct key's rows offset by 3, distinct.
create table charset_oracle.recompute as
	select distinct ck.wmi, ck.year, kc.pos + 3 as p, kc.c
	from (select distinct c.wmi, c.year, k.id
		from charset_oracle.cell c
		join vpic.wmi w on w.wmi = c.wmi
		join vpic.wmi_vinschema wvs on wvs.wmiid = w.id
			and c.year between wvs.yearfrom and coalesce(wvs.yearto, 2999)
		join vpic.vinschema vs on vs.id = wvs.vinschemaid
		join vpic.pattern p on p.vinschemaid = vs.id
		join charset_oracle.k k on k.keys = p.keys) ck
	join charset_oracle.kc kc on kc.k = ck.id;
create index on charset_oracle.recompute (wmi, year);
copy (select c.wmi, c.year, coalesce(x.s, '')
	from charset_oracle.cell c left join (
		select wmi, year, string_agg(p || ':' || chars, ';' order by p) s
		from (select wmi, year, p, string_agg(c, '' order by c collate "C") chars
			from charset_oracle.recompute group by 1, 2, 3) t
		group by 1, 2) x using (wmi, year)
	order by c.wmi collate "C", c.year) to stdout;
`)
	var cells []charsetCell
	bad := 0
	for _, l := range lines(out) {
		f := strings.SplitN(l, "\t", 3)
		year, _ := strconv.Atoi(f[1])
		c := charsetCell{f[0], year, f[2]}
		cells = append(cells, c)
		if mine := cellChars(e, c); mine != c.want {
			if bad++; bad <= 20 {
				t.Errorf("cell (%s, %d): Go %q, Postgres %q", c.wmi, c.year, mine, c.want)
			}
		}
	}
	if len(cells) == 0 {
		t.Fatal("Postgres listed no cells")
	}
	t.Logf("%d (WMI, year) cells: %d disagree", len(cells), bad)
	return cells
}

func cellChars(e *engine, c charsetCell) string {
	wlo, whi := e.wmi.wmi.EqualRange(0, len(e.wmi.id), c.wmi)
	sets, err := e.charset(wlo, whi, c.year)
	if err != nil {
		return err.Error()
	}
	return canonChars(sets, 0)
}

func checkFExtract(t *testing.T, e *engine, cells []charsetCell) {
	if len(cells) == 0 {
		t.Skip("no cells")
	}
	n, workers := 1000, 4
	switch s := os.Getenv("NITROVIN_CHARSET_CELLS"); s {
	case "":
	case "all":
		n = len(cells)
	default:
		var err error
		if n, err = strconv.Atoi(s); err != nil || n < 1 {
			t.Fatalf("NITROVIN_CHARSET_CELLS=%q: want a count or all", s)
		}
	}
	if s := os.Getenv("NITROVIN_CHARSET_WORKERS"); s != "" {
		var err error
		if workers, err = strconv.Atoi(s); err != nil || workers < 1 {
			t.Fatalf("NITROVIN_CHARSET_WORKERS=%q: want a count", s)
		}
	}
	n = min(n, len(cells))
	sample := make([]charsetCell, n)
	for i := range n {
		sample[i] = cells[i*len(cells)/n]
	}

	// Each worker gets every workers-th cell, one statement each.
	scripts := make([]string, workers)
	for w := range workers {
		var b strings.Builder
		b.WriteString("\\set ON_ERROR_STOP on\nset client_min_messages = warning;\n")
		b.WriteString("create temp table cell (i int, wmi varchar, year int);\ncopy cell from stdin;\n")
		for i := w; i < n; i += workers {
			fmt.Fprintf(&b, "%d\t%s\t%d\n", i, escapeCopy(sample[i].wmi), sample[i].year)
		}
		b.WriteString(`\.
select format('copy (select %s, %s) to stdout', i, format('` +
			strings.ReplaceAll(canonSQL(`select p, c::text as c from vpic.fextractvalidcharsperwmiyear(%L, %s::smallint)`), "'", "''") +
			`', wmi, year)) from cell order by i \gexec
`)
		scripts[w] = b.String()
	}
	outs := make([]string, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for w := range workers {
		wg.Go(func() { outs[w], errs[w] = runPsql(scripts[w]) })
	}
	wg.Wait()
	if err := cmp.Or(errs...); err != nil {
		t.Fatal(err)
	}
	answered, bad := 0, 0
	for _, out := range outs {
		for _, l := range lines(out) {
			i, want, _ := strings.Cut(l, "\t")
			j, _ := strconv.Atoi(i)
			c := sample[j]
			answered++
			if mine := cellChars(e, c); mine != want {
				if bad++; bad <= 20 {
					t.Errorf("fextractvalidcharsperwmiyear(%s, %d): Go %q, Postgres %q", c.wmi, c.year, mine, want)
				}
			}
		}
	}
	if answered != n {
		t.Fatalf("Postgres answered %d of %d cells", answered, n)
	}
	t.Logf("fextractvalidcharsperwmiyear on %d of %d cells: %d disagree", n, len(cells), bad)
}

// runPsql runs one script through $NITROVIN_PSQL, for callers off the test
// goroutine.
func runPsql(script string) (string, error) {
	cmd := exec.Command("sh", "-c", os.Getenv("NITROVIN_PSQL"))
	cmd.Stdin = strings.NewReader(script)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("psql: %v\n%s", err, stderr.String())
	}
	return string(out), nil
}

// logStaleCache compares wmiyearvalidchars with the recompute in every
// cell the cache holds, the summary ultravin's stale_cache_cells.json gives.
func logStaleCache(t *testing.T, psql func(string) string) {
	out := psql(`create table charset_oracle.cache as
	select distinct wmi, year::int, position::int as p, "char"::text as c from vpic.wmiyearvalidchars;
create table charset_oracle.diff as
	select coalesce(a.wmi, r.wmi) as wmi, coalesce(a.year, r.year) as year, a.p is not null as in_cache
	from charset_oracle.cache a
	full join (select r.* from charset_oracle.recompute r
		where exists (select from charset_oracle.cache a where (a.wmi, a.year) = (r.wmi, r.year))) r
	on (a.wmi, a.year, a.p, a.c) = (r.wmi, r.year, r.p, r.c)
	where a.p is null or r.p is null;
copy (select
	(select count(distinct (wmi, year)) from charset_oracle.cache),
	(select count(distinct (wmi, year)) from charset_oracle.diff),
	(select count(*) from charset_oracle.diff where in_cache),
	(select count(*) from charset_oracle.diff where not in_cache),
	(select count(*) from (select distinct wmi, year from charset_oracle.cache) a
		where not exists (select from charset_oracle.recompute r where (r.wmi, r.year) = (a.wmi, a.year))),
	(select count(*) from vpic.wmiyearvalidchars_cacheexceptions)) to stdout;
`)
	f := strings.Fields(out)
	if len(f) != 6 {
		t.Fatalf("stale-cache summary: %q", out)
	}
	t.Logf("wmiyearvalidchars: %s cells; stale %s, rows only in the cache %s, only in the recompute %s, recompute empty %s; cache exceptions %s",
		f[0], f[1], f[2], f[3], f[4], f[5])
}
