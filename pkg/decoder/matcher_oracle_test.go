package decoder

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// The answer key for keys the dump does not hold but a later month could:
// the oddities of Postgres's bracket expressions, sqlwild_to_regex's "1-A"
// rewrite, keys Postgres rejects, and LIKE keys with '%' or '\'. Each key's
// probes are keyProbes, every single character from 0x01 to 0x7E and one
// non-ASCII rune, run through the core's clauses in Postgres
// (TestMatcherPostgres checks every key the dump holds). Regenerate with
//
//	go test ./pkg/decoder -run TestMatcherOracle -update-oracle
//
// against $ORACLE_CONTAINER (default nhtsa-oracle) holding the restored dump,
// for its sqlwild_to_regex.
const matcherKey = "testdata/matcher.oracle.jsonl"

var oracleKeys = []string{
	// The "1-A" rewrite, in brackets and out.
	"[1-A-JT]", "[0-1-A]", "[1-A]1-A", "1-A[B]", "**[1-A-JT]|[1-A]",
	// Reversed ranges and other range errors.
	"[C-A]", "[Z-A]", "[9-0]", "[A-C-E]", "[A--B]", "[A--]", "[]-A]", "[\\-A]", "[A-.]",
	// Where ']' and '-' are literals.
	"[]A]", "[]]", "[]-]", "[A]]", "[A-]B]", "[-A]", "[A-]", "[--A]", "[---]", "[)--]", "[-A-]", "[A-C-]", "[A-A]",
	// Unbalanced brackets.
	"A[]B", "[A", "[A[*]", "[[*]",
	// What sqlwild_to_regex escapes or rewrites, inside brackets and out.
	"[A*]", "[*-A]", "[^A]", "[A|B]", "[A[B]", "[[]", "[A#_]", "[(-.]", "[.-A]", "[A-\\]", "[A\\]]", "[\\]",
	"[%]", "[?-A]", "[|-~]", "*[A-C]|[1-3]", "[A-C][1-3]", "[I-K]^$+?{}()|.\\*",
	// LIKE keys: '%' and '\' are LIKE's own.
	"1-A", "A%B", "%", "%A", "A\\B", "A\\", "\\%", "A\\_B", "_*_", "]A", "A]*",
	// Formula keys, one with brackets (the formula pass uses LIKE regardless).
	"#*", "**#", "[1-3]#", "*#|##",
	// Keys accepting '&' other than by a wildcard, which the matcher
	// declines (see keyMatcher): Postgres tells an input '&' from a
	// non-ASCII rune, sanitized var_keys does not.
	"[!--]", "[$-.]", "[&]", "A&", "[A]&",
}

// declinedKeys are the oracleKeys Postgres compiles and compileKey declines
// with errKeyUnsupported.
var declinedKeys = map[string]bool{"[!--]": true, "[$-.]": true, "[&]": true, "A&": true, "[A]&": true}

// matcherProbes is keyProbes plus every single character from 0x01 to 0x7E
// and a non-ASCII rune.
func matcherProbes(keys string) []string {
	probes := keyProbes(keys)
	for c := 1; c <= 0x7e; c++ {
		probes = append(probes, string(rune(c)))
	}
	probes = append(probes, "É")
	return probes
}

type matcherRow struct {
	Keys    string   `json:"keys"`
	Regex   string   `json:"regex"`
	Error   string   `json:"error,omitempty"`
	Probes  []string `json:"probes,omitempty"`
	Match   string   `json:"match,omitempty"`
	Formula string   `json:"formula,omitempty"`
}

// matcherSQL is the core's clauses (spvindecode_core.sql:96-97, 174) per
// key and probe. An invalid regular expression raises an error on the first
// probe the key is tested on, as spvindecode would; the row records it.
const matcherSQL = `
create function pg_temp.verdict(keys text, probes text[]) returns jsonb language plpgsql as $f$
declare
  rx text := vpic.sqlwild_to_regex(keys);
  bits text := '';
  fbits text := '';
  v text;
begin
  begin
    foreach v in array probes loop
      bits := bits || case when (keys not like '%[%' and v like replace(keys, '*', '_') || '%')
                             or (keys like '%[%' and v ~ rx) then '1' else '0' end;
      if strpos(keys, '#') > 0 then
        fbits := fbits || case when translate(v, '0123456789', '##########') like replace(keys, '*', '_') || '%'
                          then '1' else '0' end;
      end if;
    end loop;
  exception when others then
    return jsonb_build_object('keys', keys, 'regex', rx, 'error', sqlerrm);
  end;
  return jsonb_strip_nulls(jsonb_build_object('keys', keys, 'regex', rx, 'probes', probes,
    'match', bits, 'formula', nullif(fbits, '')));
end $f$;
select pg_temp.verdict(
         convert_from(decode(e->>'k', 'hex'), 'UTF8'),
         array(select convert_from(decode(p, 'hex'), 'UTF8')
               from json_array_elements_text(e->'p') with ordinality t(p, n) order by n))
from json_array_elements(:'input'::json) with ordinality s(e, i) order by i;
`

func regenerateMatcherKey(t *testing.T) {
	t.Helper()
	type input struct {
		K string   `json:"k"`
		P []string `json:"p"`
	}
	var in []input
	for _, k := range oracleKeys {
		x := input{K: hex.EncodeToString([]byte(k))}
		for _, p := range matcherProbes(k) {
			x.P = append(x.P, hex.EncodeToString([]byte(p)))
		}
		in = append(in, x)
	}
	doc, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	container := cmp.Or(os.Getenv("ORACLE_CONTAINER"), "nhtsa-oracle")
	cmd := exec.Command("docker", "exec", "-i", container,
		"psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "vpic")
	// Hex keeps the variable free of quotes and backslashes.
	cmd.Stdin = strings.NewReader(`\set input '` + string(doc) + "'\n" + matcherSQL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle: %v\n%s", err, stderr.String())
	}
	if err := os.WriteFile(matcherKey, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMatcherOracle(t *testing.T) {
	if *updateOracle {
		regenerateMatcherKey(t)
	}
	f, err := os.Open(matcherKey)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []matcherRow
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r matcherRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("%s:%d: %v", matcherKey, len(rows)+1, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(oracleKeys) {
		t.Fatalf("%s has %d rows for %d keys; regenerate it with -update-oracle", matcherKey, len(rows), len(oracleKeys))
	}
	for i, want := range rows {
		k := oracleKeys[i]
		if want.Keys != k {
			t.Fatalf("%s row %d is for %q, not %q; regenerate it with -update-oracle", matcherKey, i+1, want.Keys, k)
		}
		if got := sqlwildToRegex(k); got != want.Regex {
			t.Errorf("sqlwildToRegex(%q) = %q, sqlwild_to_regex %q", k, got, want.Regex)
		}
		if want.Error == "" && !slices.Equal(want.Probes, matcherProbes(k)) {
			t.Fatalf("%s row %d holds other probes than %q's; regenerate it with -update-oracle", matcherKey, i+1, k)
		}
		m := compileKey(k)
		if want.Error != "" {
			// Postgres rejects the key; the matcher must too, with its
			// message unless it declines the key as unsupported.
			if _, err := m.match(""); err == nil {
				t.Errorf("key %q compiles, Postgres: %s", k, want.Error)
			} else if err != errKeyUnsupported && err.Error() != want.Error {
				t.Errorf("key %q: error %q, Postgres %q", k, err, want.Error)
			}
			continue
		}
		if declinedKeys[k] {
			if _, err := m.match(""); err != errKeyUnsupported {
				t.Errorf("key %q: error %v, want errKeyUnsupported", k, err)
			}
			continue
		}
		fm := compileLike(k)
		var match, formula strings.Builder
		for _, p := range want.Probes {
			ok, err := m.match(ampersand(p))
			if err != nil {
				t.Fatalf("key %q: %v, which Postgres compiles", k, err)
			}
			match.WriteByte(bit(ok))
			if want.Formula != "" {
				ok, err := fm.match(formulaKeys(ampersand(p)))
				if err != nil {
					t.Fatalf("key %q: formula: %v", k, err)
				}
				formula.WriteByte(bit(ok))
			}
		}
		for j := range want.Probes {
			if match.String()[j] != want.Match[j] {
				t.Errorf("key %q (regex %q) on %q: matcher %c, Postgres %c", k, want.Regex, want.Probes[j], match.String()[j], want.Match[j])
			}
			if want.Formula != "" && formula.String()[j] != want.Formula[j] {
				t.Errorf("key %q on %q: formula matcher %c, Postgres %c", k, want.Probes[j], formula.String()[j], want.Formula[j])
			}
		}
	}
}

func bit(b bool) byte {
	if b {
		return '1'
	}
	return '0'
}
