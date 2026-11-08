package decoder

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The answer key for fValidCharsInKey on keys the dump does not hold but a
// later month could (TestCharsetPostgres checks every key the dump holds):
// the matcher's oddities (oracleKeys), and what only fValidCharsInRegEx
// reads, a bracket's own text: '^' negating or not, lowercase, ranges the
// "1-A" rewrite would have rescued, unclosed and empty brackets, and
// literal characters the charset keeps. Regenerate with
//
//	go test ./pkg/decoder -run TestValidCharsOracle -update-oracle
//
// against $ORACLE_CONTAINER (default nhtsa-oracle) holding the restored dump.
const validCharsKey = "testdata/validchars.oracle.jsonl"

var validCharsKeys = append(append([]string(nil), oracleKeys...),
	"[^A-C]", "[^ABC]", "[A^C]", "[^]", "[^-A]", "[^A-]", "[]", "[a-c]", "[a-cX]", "abc", "a[bc]",
	"[1-A-JT]", "[0-1-A]", "[A-Z0-9]", "[0-9A-HJ-NPR-Z]", "[I-Q]", "[0-9*|]", "[*|AB]",
	"#[#-9]", "[#A]", "**|*#[A-C]", "[A-C]]", "]A[B]", "[[A-C]", "[A[-C]", "[A-C[]",
	"[ABCDEFGHJKLMNPRSTUVWXYZ0123456789IOQ_-]", "[^0-9A-Z]", "A-B", "[A-C", "*[A-C]*[D-F]|##",
)

type validCharsRow struct {
	Keys  string `json:"keys"`
	Chars string `json:"chars"`
	Error string `json:"error,omitempty"`
}

// validCharsSQL renders each key's rows as canonChars does, or the error
// Postgres raises.
const validCharsSQL = `
create function pg_temp.vk(key varchar) returns jsonb language plpgsql as $f$
declare
	r text;
begin
	select coalesce(string_agg(pos || ':' || chars, ';' order by pos), '') into r
	from (select pos, string_agg(c, '' order by c collate "C") chars
		from (select distinct pos, return_chr::text c from vpic.fvalidcharsinkey(key)) d group by pos) s;
	return jsonb_build_object('keys', key, 'chars', r);
exception when others then
	return jsonb_build_object('keys', key, 'chars', '', 'error', sqlerrm);
end $f$;
select pg_temp.vk(convert_from(decode(h, 'hex'), 'UTF8'))
from unnest(:'hexes'::text[]) with ordinality t(h, o) order by o;
`

func regenerateValidCharsKey(t *testing.T) {
	t.Helper()
	hexes := make([]string, len(validCharsKeys))
	for i, k := range validCharsKeys {
		hexes[i] = `"` + hex.EncodeToString([]byte(k)) + `"`
	}
	container := cmp.Or(os.Getenv("ORACLE_CONTAINER"), "nhtsa-oracle")
	cmd := exec.Command("docker", "exec", "-i", container,
		"psql", "-X", "-q", "-tA", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "vpic")
	cmd.Stdin = strings.NewReader(`\set hexes '{` + strings.Join(hexes, ",") + "}'\n" + validCharsSQL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle: %v\n%s", err, stderr.String())
	}
	if err := os.WriteFile(validCharsKey, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidCharsOracle(t *testing.T) {
	if *updateOracle {
		regenerateValidCharsKey(t)
	}
	f, err := os.Open(validCharsKey)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var rows []validCharsRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var r validCharsRow
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatalf("%s:%d: %v", validCharsKey, len(rows)+1, err)
		}
		rows = append(rows, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(validCharsKeys) {
		t.Fatalf("%s has %d rows for %d keys; regenerate it with -update-oracle", validCharsKey, len(rows), len(validCharsKeys))
	}
	for i, want := range rows {
		k := validCharsKeys[i]
		if want.Keys != k {
			t.Fatalf("%s row %d is for %q, not %q; regenerate it with -update-oracle", validCharsKey, i+1, want.Keys, k)
		}
		sets, err := validCharsInKey(k)
		switch {
		case err == errKeyUnsupported:
			// Declined keys: a '&' or non-ASCII byte the charset would hold.
			if want.Error == "" && !strings.Contains(want.Chars, "&") && isASCII(k) {
				t.Errorf("key %q declined, Postgres %q", k, want.Chars)
			}
		case err != nil:
			if err.Error() != want.Error {
				t.Errorf("key %q: error %q, Postgres %q", k, err, cmp.Or(want.Error, want.Chars))
			}
		case want.Error != "":
			t.Errorf("key %q: %q, Postgres raises %q", k, canonChars(sets, 1), want.Error)
		case canonChars(sets, 1) != want.Chars:
			t.Errorf("key %q: %q, Postgres %q", k, canonChars(sets, 1), want.Chars)
		}
	}
}
