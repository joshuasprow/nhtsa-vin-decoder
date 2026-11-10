package decoder

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

// TestMatcherPostgres checks the matcher and the per-schema index against
// the restored dump they were built from:
//
//   - every distinct pattern.keys in the artifact is a distinct keys in
//     vpic.pattern, and sqlwildToRegex gives its generated keys_regex;
//   - for every distinct key and every probe keyProbes derives from it, the
//     key's matcher agrees with the core's clause (spvindecode_core.sql:
//     96-97), and for keys holding '#' with the formula clause (:174);
//   - for every VinSchema and probes built from its own keys, patternHits and
//     formulaHits return the Pattern.Ids the core's two queries select,
//     filters and order included.
//
// NITROVIN_PSQL is as TestPostgres's in cmd/nitrovin-import: a shell command
// running psql on the restored dump, reading a script on stdin.
// NITROVIN_ARTIFACT is an artifact built from that database:
//
//	go run ./cmd/nitrovin-import sql | $NITROVIN_PSQL |
//	  go run ./cmd/nitrovin-import build -month 2026_10 -o "$NITROVIN_ARTIFACT"
//
// The test writes scratch tables to the schema matcher_oracle and drops it.
//
// The Postgres tests that only read the dump run in parallel, each psql call
// its own session: this one, TestCharsetPostgres and
// TestElementAttributeValuePostgres. Those that truncate
// wmiyearvalidchars or mark VinSchemas QC'd in a rolled-back transaction
// (TestCorePostgres, TestWrapperPostgres, TestProjectionPostgres) stay
// serial, so they run first and alone.
func TestMatcherPostgres(t *testing.T) {
	t.Parallel()
	a, psql := dumpUnderTest(t)
	t.Cleanup(func() { psql("drop schema if exists matcher_oracle cascade;\n") })
	psql("drop schema if exists matcher_oracle cascade; create schema matcher_oracle;\n")

	t.Run("keys", func(t *testing.T) { checkKeys(t, a, psql) })
	t.Run("index", func(t *testing.T) { checkIndex(t, a, psql) })
}

// dumpUnderTest opens $NITROVIN_ARTIFACT and returns it with a function
// running a psql script through $NITROVIN_PSQL, or skips the test when
// either is unset.
func dumpUnderTest(t *testing.T) (*artifact.Artifact, func(script string) string) {
	t.Helper()
	psqlCmd, path := os.Getenv("NITROVIN_PSQL"), os.Getenv("NITROVIN_ARTIFACT")
	if psqlCmd == "" || path == "" {
		t.Skip("NITROVIN_PSQL or NITROVIN_ARTIFACT not set: no restored dump to check against")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	a, err := artifact.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	psql := func(script string) string {
		t.Helper()
		cmd := exec.Command("sh", "-c", psqlCmd)
		cmd.Stdin = strings.NewReader("\\set ON_ERROR_STOP on\nset client_min_messages = warning;\n" + script)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("psql: %v\n%s", err, stderr.String())
		}
		return string(out)
	}
	return a, psql
}

// checkKeys compares every distinct key's matcher with Postgres on that
// key's probes. Postgres gets each pair with the matcher's answers and
// returns the pairs where it disagrees, so the output stays small.
func checkKeys(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	// keys_regex is the dump's generated column; Postgres is its oracle.
	regex := map[string]string{}
	for _, l := range lines(psql(`copy (select distinct keys, keys_regex from vpic.pattern) to stdout;` + "\n")) {
		f := strings.Split(l, "\t")
		regex[unescapeCopy(f[0])] = unescapeCopy(f[1])
	}
	pt := a.Table("pattern")
	keys := pt.Strings("keys")
	seen := map[uint32]bool{}
	var distinct []string
	for r := range pt.Rows() {
		if !seen[keys.Ref(r)] {
			seen[keys.Ref(r)] = true
			k, _ := keys.At(r)
			distinct = append(distinct, k)
		}
	}
	slices.Sort(distinct)
	if len(distinct) != len(regex) {
		t.Fatalf("artifact has %d distinct keys, vpic.pattern %d", len(distinct), len(regex))
	}
	for _, k := range distinct {
		want, ok := regex[k]
		switch {
		case !ok:
			t.Fatalf("artifact key %q is not in vpic.pattern", k)
		case sqlwildToRegex(k) != want:
			t.Errorf("sqlwildToRegex(%q) = %q, keys_regex is %q", k, sqlwildToRegex(k), want)
		}
	}

	var in strings.Builder
	in.WriteString(`create table matcher_oracle.k (id int primary key, keys text not null);
copy matcher_oracle.k from stdin;
`)
	for i, k := range distinct {
		fmt.Fprintf(&in, "%d\t%s\n", i, escapeCopy(k))
	}
	in.WriteString("\\.\n")
	in.WriteString(`create table matcher_oracle.kp (k int not null, v text not null, c2 bool not null, formula bool);
copy matcher_oracle.kp from stdin;
`)
	pairs, matches, formulaPairs := 0, 0, 0
	for i, k := range distinct {
		m := compileKey(k)
		fm := compileLike(k)
		hasFormula := strings.Contains(k, "#")
		for _, v := range keyProbes(k) {
			sv := ampersand(v)
			got, err := m.match(sv)
			if err != nil {
				t.Fatalf("key %q: %v, which Postgres compiles", k, err)
			}
			formula := `\N`
			if hasFormula {
				ok, err := fm.match(formulaKeys(sv))
				if err != nil {
					t.Fatalf("key %q: formula: %v", k, err)
				}
				formula = strconv.FormatBool(ok)
				formulaPairs++
			}
			fmt.Fprintf(&in, "%d\t%s\t%t\t%s\n", i, escapeCopy(v), got, formula)
			pairs++
			if got {
				matches++
			}
		}
	}
	in.WriteString("\\.\n")
	// The core's clauses verbatim, against the dump's keys_regex column;
	// translate is the formula pass's ten replace calls.
	in.WriteString(`create table matcher_oracle.verdict as
select kp.k, k.keys, r.keys_regex, kp.v, kp.c2, kp.formula,
       (k.keys not like '%[%' and kp.v like replace(k.keys, '*', '_') || '%')
       or (k.keys like '%[%' and kp.v ~ r.keys_regex) as pg_c2,
       case when strpos(k.keys, '#') > 0
            then translate(kp.v, '0123456789', '##########') like replace(k.keys, '*', '_') || '%' end as pg_formula
from matcher_oracle.kp
join matcher_oracle.k on k.id = kp.k
join (select distinct keys, keys_regex from vpic.pattern) r on r.keys = k.keys;
copy (select count(*), count(*) filter (where pg_c2), count(pg_formula) from matcher_oracle.verdict) to stdout;
copy (select keys, keys_regex, v, c2, pg_c2, formula, pg_formula from matcher_oracle.verdict
      where c2 <> pg_c2 or formula is distinct from pg_formula order by k, v limit 50) to stdout;
`)
	out := lines(psql(in.String()))
	counts := strings.Split(out[0], "\t")
	if want := fmt.Sprint(pairs, matches, formulaPairs); strings.Join(counts, " ") != want {
		t.Errorf("Postgres counts pairs, matches, formula pairs as %v; the matcher %s", counts, want)
	}
	for _, l := range out[1:] {
		f := strings.Split(l, "\t")
		t.Errorf("key %q (regex %q) on %q: matcher %s, Postgres %s; formula matcher %s, Postgres %s",
			unescapeCopy(f[0]), unescapeCopy(f[1]), unescapeCopy(f[2]), f[3], f[4], f[5], f[6])
	}
	t.Logf("%d distinct keys, %d probe pairs (%d match), %d formula pairs: Postgres agrees on all but %d shown",
		len(distinct), pairs, matches, formulaPairs, len(out)-1)
}

// checkIndex compares patternHits and formulaHits with the core's two
// pattern queries, restricted to one VinSchema (the year band, WMI and
// TobeQCed joins pick schemas, not rows), for three probes per schema built
// from its first, middle and last pattern's keys, plus, for a schema with
// formula patterns, each formula key with its '#'s as digits.
func checkIndex(t *testing.T, a *artifact.Artifact, psql func(string) string) {
	pt := a.Table("pattern")
	schemaCol := pt.Int32s("vinschemaid")
	ids := pt.Int32s("id")
	keys := pt.Strings("keys")
	elements := patternElements(a)

	type query struct {
		schema int32
		v      string
	}
	var queries []query
	var want []string // "<pattern ids>;<formula ids>" per query
	var in strings.Builder
	in.WriteString(`create table matcher_oracle.q (id int primary key, schema int not null, v text not null);
copy matcher_oracle.q from stdin;
`)
	var hits, fhits []int32
	for lo := 0; lo < pt.Rows(); {
		schema := schemaCol[lo]
		_, hi := artifact.EqualRange(schemaCol, lo, pt.Rows(), schema)
		x := newPatternIndex(a, elements, schema)
		var probes []string
		for _, r := range []int{lo, (lo + hi) / 2, hi - 1} {
			k, _ := keys.At(r)
			if p := keyProbes(k); len(p) > 0 {
				probes = append(probes, p[0])
			}
		}
		for _, f := range x.formula {
			k, _ := keys.At(int(f.row))
			probes = append(probes, strings.ReplaceAll(strings.ReplaceAll(k, "#", "7"), "*", "0"))
		}
		for _, v := range probes {
			var err error
			if hits, err = x.patternHits(v, hits[:0]); err != nil {
				t.Fatalf("schema %d: %v", schema, err)
			}
			if fhits, err = x.formulaHits(v, fhits[:0]); err != nil {
				t.Fatalf("schema %d: %v", schema, err)
			}
			fmt.Fprintf(&in, "%d\t%d\t%s\n", len(queries), schema, escapeCopy(v))
			queries = append(queries, query{schema, v})
			want = append(want, idList(ids, hits)+";"+idList(ids, fhits))
		}
		lo = hi
	}
	in.WriteString("\\.\n")
	in.WriteString(`copy (
select q.id,
       coalesce((select string_agg(p.id::text, ',' order by p.id)
                 from vpic.pattern p join vpic.element e on p.elementid = e.id
                 where p.vinschemaid = q.schema
                   and ((p.keys not like '%[%' and q.v like replace(p.keys, '*', '_') || '%')
                        or (p.keys like '%[%' and q.v ~ p.keys_regex))
                   and not p.elementid in (26, 27, 29, 39)
                   and not e.decode is null
                   and coalesce(e.isprivate, false) = false), '')
       || ';' ||
       coalesce((select string_agg(p.id::text, ',' order by p.id)
                 from vpic.pattern p
                 where p.vinschemaid = q.schema
                   and strpos(p.keys, '#') > 0
                   and not p.elementid in (26, 27, 29, 39)
                   and translate(q.v, '0123456789', '##########') like replace(p.keys, '*', '_') || '%'), '')
from matcher_oracle.q order by q.id) to stdout;
`)
	got := lines(psql(in.String()))
	if len(got) != len(queries) {
		t.Fatalf("Postgres answered %d of %d queries", len(got), len(queries))
	}
	bad, nonEmpty := 0, 0
	for i, l := range got {
		f := strings.SplitN(l, "\t", 2)
		if want[i] != ";" {
			nonEmpty++
		}
		if f[1] != want[i] {
			if bad++; bad <= 20 {
				t.Errorf("schema %d, var_keys %q: index %q, Postgres %q", queries[i].schema, queries[i].v, want[i], f[1])
			}
		}
	}
	t.Logf("%d (schema, var_keys) queries, %d with hits: %d disagree", len(queries), nonEmpty, bad)
}

func idList(ids []int32, rows []int32) string {
	s := make([]string, len(rows))
	for i, r := range rows {
		s[i] = strconv.Itoa(int(ids[r]))
	}
	return strings.Join(s, ",")
}

func lines(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' })
}

// escapeCopy and unescapeCopy are COPY text format's escaping, for the
// characters probes and keys can hold.
var (
	copyEscaper   = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\t", `\t`, "\r", `\r`)
	copyUnescaper = strings.NewReplacer(`\\`, `\`, `\n`, "\n", `\t`, "\t", `\r`, "\r")
)

func escapeCopy(s string) string   { return copyEscaper.Replace(s) }
func unescapeCopy(s string) string { return copyUnescaper.Replace(s) }

// keyProbes derives var_keys probes from one key: the key's first accepted
// string (first: it is what checkIndex uses as a matching probe), that
// string one character short and one longer, then the first string with one
// position changed to each boundary of that position's accepted bytes
// (lo-1, lo, hi, hi+1 of each range, printable ASCII only), and, at the
// first position accepting any character, to characters with a role
// somewhere: newline, a non-ASCII rune (sanitized to '&'), '&' itself, and
// the key alphabet's punctuation. Probes are oracle input: a key's own
// reading of its accepted bytes only chooses where to look.
func keyProbes(keys string) []string {
	sets := probeSets(keys)
	if sets == nil {
		return []string{""}
	}
	base := make([]byte, len(sets))
	for i, s := range sets {
		base[i] = 'A'
		for _, c := range []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ!\"#$%&'()*+,-./:;<=>?@[\\]^_`{|}~") {
			if s.has(c) {
				base[i] = c
				break
			}
		}
	}
	b := string(base)
	probes := []string{b, b[:max(len(b)-1, 0)], b + "Z"}
	seen := map[string]bool{}
	for _, p := range probes {
		seen[p] = true
	}
	add := func(i int, c string) {
		p := b[:i] + c + b[i+1:]
		if !seen[p] {
			seen[p] = true
			probes = append(probes, p)
		}
	}
	special := false
	for i, s := range sets {
		if s == anyByte {
			if !special {
				special = true
				for _, c := range []string{"\n", "É", "&", "_", "*", "#", "|", "-", "[", "]", `\`, "%", "z", " "} {
					add(i, c)
				}
			}
			continue
		}
		for c := 32; c <= 126; c++ {
			if s.has(byte(c)) && (c == 32 || !s.has(byte(c-1))) { // a range starts
				add(i, string(rune(max(c-1, 32))))
				add(i, string(rune(c)))
			}
			if s.has(byte(c)) && (c == 126 || !s.has(byte(c+1))) { // a range ends
				add(i, string(rune(c)))
				add(i, string(rune(min(c+1, 126))))
			}
		}
	}
	return probes
}

// probeSets is the bytes each position of a key accepts, as keyProbes reads
// it: the matcher's own sets for a bracket key, the key's characters for a
// LIKE key, nil when neither applies.
func probeSets(keys string) []byteSet {
	if strings.Contains(keys, "[") {
		sets, err := regexSets(sqlwildToRegex(keys))
		if err != nil {
			return nil
		}
		return sets
	}
	if strings.ContainsAny(keys, `%\`) {
		return nil
	}
	sets := make([]byteSet, len(keys))
	for i := range len(keys) {
		if keys[i] == '*' || keys[i] == '_' {
			sets[i] = anyByte
		} else {
			sets[i].add(keys[i])
		}
	}
	return sets
}
