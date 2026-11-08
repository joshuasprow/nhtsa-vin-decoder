package decoder

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The answer key for convert: the core's conversion statement
// (conversionFunction) run in Postgres for each formula in the dump's
// conversion table plus extraFormulas, on every operand in
// conversionOperands and every Postgres keyword (pg_get_keywords, upper
// case) but LOCALTIME, whose result is the clock's (TestConvertDeclines).
// TestCorePostgres checks the operands the dump holds. Regenerate with
//
//	go test ./pkg/decoder -run TestConversionOracle -update-oracle
//
// against $ORACLE_CONTAINER (default nhtsa-oracle) holding the restored dump.
const conversionKey = "testdata/conversion.oracle.jsonl"

// extraFormulas reach what the dump's formulas do not: integer division,
// a zero divisor, an operator without spaces.
var extraFormulas = []string{"#x# / 1000", "#x# / 7", "#x# / 0", "#x# / 0.0", "#x#*3"}

// conversionOperands: the dump's shapes (integers, decimals with trailing
// zeros, TBD), literal forms Postgres reads ("5.", ".5", leading zeros),
// the int4 and int8 boundaries an integer operand crosses, results with
// more than 16 significant digits or a long scale, and identifiers.
var conversionOperands = []string{
	"0", "00", "000.000", "1", "7", "183", "4900", "0.0", "2.0", "2.00", "1.80", "0.5", ".5", "5.", "1000.",
	"007", "9.99999999", "9999.99", "0.000001", "0.00000000000000000001", "12345678901234567890.123",
	"2147483", "2147484", "2147483647", "2147483648", "9223372036854775", "9223372036854776",
	"9223372036854775807", "9223372036854775808", "99999999999999999999999999",
	"TBD", "tbd", "NULL", "null", "Null", "x_1", "_", "a$b",
}

const conversionOracleSQL = `
\pset tuples_only on
\pset format unaligned
create temp table f (id serial, formula varchar);
insert into f (formula) select formula from vpic.conversion order by id;
insert into f (formula) select unnest(:'extra'::text[]);
create temp table o (id serial, v varchar);
insert into o (v) select unnest(:'operands'::text[]);
insert into o (v) select upper(word) from pg_get_keywords() where word <> 'localtime' order by word;
select json_build_object('formulas', (select json_agg(formula order by id) from f),
	'server_version', current_setting('server_version'));
select json_build_array(o.v, (select json_agg(pg_temp.conv(f.formula, o.v) order by f.id) from f))
from o order by o.id;
`

type conversionHeader struct {
	Formulas      []string `json:"formulas"`
	ServerVersion string   `json:"server_version"`
}

func regenerateConversionKey(t *testing.T) {
	t.Helper()
	array := func(ss []string) string {
		quoted := make([]string, len(ss))
		for i, s := range ss {
			quoted[i] = `"` + s + `"`
		}
		return "'{" + strings.Join(quoted, ",") + "}'"
	}
	container := cmp.Or(os.Getenv("ORACLE_CONTAINER"), "nhtsa-oracle")
	cmd := exec.Command("docker", "exec", "-i", container,
		"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "vpic")
	cmd.Stdin = strings.NewReader(`\set extra ` + array(extraFormulas) + "\n" +
		`\set operands ` + array(conversionOperands) + "\n" +
		conversionFunction + conversionOracleSQL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle: %v\n%s", err, stderr.String())
	}
	if err := os.WriteFile(conversionKey, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestConversionOracle(t *testing.T) {
	if *updateOracle {
		regenerateConversionKey(t)
	}
	f, err := os.Open(conversionKey)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var h conversionHeader
	n := 0
	for sc.Scan() {
		if h.Formulas == nil {
			if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var operand string
		var want []*string
		if err := json.Unmarshal(sc.Bytes(), &[]any{&operand, &want}); err != nil {
			t.Fatal(err)
		}
		if len(want) != len(h.Formulas) {
			t.Fatalf("%q: %d results for %d formulas", operand, len(want), len(h.Formulas))
		}
		for i, formula := range h.Formulas {
			n++
			got, ok, err := convert(formula, operand)
			switch {
			case err != nil:
				t.Errorf("%q on %q: %v", formula, operand, err)
			case !ok && want[i] != nil:
				t.Errorf("%q on %q: NULL, Postgres %q", formula, operand, *want[i])
			case ok && (want[i] == nil || got != *want[i]):
				t.Errorf("%q on %q: %q, Postgres %v", formula, operand, got, jsonString(want[i]))
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatalf("%s holds no conversions", conversionKey)
	}
	t.Logf("%d (formula, operand) pairs", n)
}

func jsonString(s *string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestConvertDeclines checks the operands and formulas convert leaves to
// Postgres rather than guess at: SQL beyond a literal or an identifier, and
// LOCALTIME, which Postgres multiplies and divides as an interval.
func TestConvertDeclines(t *testing.T) {
	for _, c := range [][2]string{
		{"#x# * 1000", "-5"}, {"#x# * 1000", " 5"}, {"#x# * 1000", "1e3"}, {"#x# * 1000", "0x10"},
		{"#x# * 1000", "1_000"}, {"#x# * 1000", "1+1"}, {"#x# * 1000", "5--"}, {"#x# * 1000", ""},
		{"#x# * 1000", "É"}, {"#x# + 1", "5"}, {"#x# * x", "5"}, {"(#x#) * 2", "5"},
		{"#x# / 1000.", "LOCALTIME"}, {"#x# * 1000", "localtime"},
	} {
		if _, _, err := convert(c[0], c[1]); err != errConversionUnsupported {
			t.Errorf("convert(%q, %q): err %v, want errConversionUnsupported", c[0], c[1], err)
		}
	}
}
