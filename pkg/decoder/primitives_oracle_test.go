package decoder

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

// The answer key for stages W0-W3 and the stage-2 primitives: every input
// below run through the dump's own SQL functions in Postgres. Regenerate with
//
//	go test ./pkg/decoder -run TestPrimitivesOracle -update-oracle
//
// against a container holding the restored dump ($ORACLE_CONTAINER, default
// nhtsa-oracle, as scripts/oracle.sh names it) whose database was initialized
// with --lc-collate=C, ultravin's oracle contract: under the image's default
// en_US.utf8, fVinModelYear2 reads a year from 'É' at position 10. The key
// holds the year of the oracle's NOW(), which fVinModelYear2 depends on, so a
// regeneration in a later year changes it.
const primitivesKey = "testdata/primitives.oracle.jsonl"

var updateOracle = flag.Bool("update-oracle", false, "regenerate "+primitivesKey+" from the Postgres oracle")

// oracleSeeds are real-shaped VINs covering the branches the primitives
// take: a car WMI with a valid check digit, a truck WMI with no truck type
// (inconclusive year), a car WMI with a letter at position 7, and a 6-character
// WMI (VF9…795, a car) whose position 7 is a digit.
var oracleSeeds = []string{
	"1HGCM82633A004352",
	"1FTFW1ET5DFC10312",
	"5YJ3E1EA7KF317000",
	"VF9AB12C3L1795123",
}

// oracleAlphabet replaces each seed position in turn: the 33 VIN characters,
// the three letters VINs never use, the characters the SQL classes or this
// port single out ('?', ',', '*', '&', lowercase), other punctuation and
// whitespace, and non-ASCII runes, including the two Postgres upper()
// carries into ASCII (ı, ſ), one it leaves alone that case-folds to an
// ASCII letter (U+212A KELVIN SIGN), a no-break space and one outside the BMP.
const oracleAlphabet = "0123456789ABCDEFGHJKLMNPRSTUVWXYZ" + "IOQ" +
	"?,*&az!_#- \t" + "Éé日ſıK\u00a0😀"

func oracleInputs() []string {
	var in []string
	for _, seed := range oracleSeeds {
		for i := range len(seed) {
			for _, r := range oracleAlphabet {
				in = append(in, seed[:i]+string(r)+seed[i+1:])
			}
		}
	}
	// Short and long: every prefix of a seed up to 20 characters.
	for _, seed := range []string{oracleSeeds[0], oracleSeeds[3]} {
		long := seed + "987"
		for n := range len(long) + 1 {
			in = append(in, long[:n])
		}
	}
	// Commas pass the check digit's classes and weigh -1: enough of them make
	// the sum negative, every residue reached through position 17.
	for d := range 10 {
		digit := string(rune('0' + d))
		in = append(in, ",,,,,,,,,,,,,000"+digit, ",,9,,,,,,,,,,,00"+digit)
	}
	return append(in,
		" ", "   1HGCM82633A004352  ", "\t1HGCM82633A004352", "1HGCM82633A004352\n",
		"1hgcm82633a004352", "1HGCM826?3A004352", "1HGCM82633A0043521234567890",
		"ÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉÉ", "ſıſıſıſıſıſıſıſıſ", " ı1HGCM82633A004352ſ ",
	)
}

// oracleRow is one line of the key: the raw input, then what the oracle
// computes on n = upper(trim(raw)), the wrapper's vin.
type oracleRow struct {
	Raw, N, WMI, Descriptor, VarKeys string
	CheckDigit1                      string
	CheckDigit2                      [2]string // isCarMpvLT false, true
	ModelYear                        *int      // fVinModelYear2; nil is NULL
	CarLT                            bool      // the wmi row fVinModelYear2 reads
	// Scan has one digit per position of n: bit k set when the scan's
	// four-way condition flags the character with startPos 13+k.
	Scan string
}

func (r *oracleRow) UnmarshalJSON(b []byte) error {
	return json.Unmarshal(b, &[]any{&r.Raw, &r.N, &r.WMI, &r.Descriptor, &r.VarKeys,
		&r.CheckDigit1, &r.CheckDigit2[0], &r.CheckDigit2[1], &r.ModelYear, &r.CarLT, &r.Scan})
}

type oracleHeader struct {
	NowYear       int    `json:"now_year"`
	ServerVersion string `json:"server_version"`
	Collate       string `json:"collate"`
}

const oracleSQL = `
\pset tuples_only on
\pset format unaligned
select json_build_object('now_year', extract(year from now())::int,
	'server_version', current_setting('server_version'),
	'collate', (select datcollate from pg_database where datname = current_database()));
with input(i, raw) as (
	-- convert_from returns collation "C"; spvindecode's v has the database default
	select ord, convert_from(decode(h, 'hex'), 'UTF8')::varchar collate "default"
	from unnest(:'hexes'::text[]) with ordinality as t(h, ord)
), norm as (
	select i, raw, upper(trim(raw)) n from input
)
select json_build_array(raw, n, vpic.fvinwmi(n), vpic.fvindescriptor(n),
	-- spvindecode_core.sql:56-61, the one stage with no function of its own
	case when length(n) > 3 then substring(n, 4, 5) ||
		case when length(n) > 9 then '|' || substring(n, 10, 8) else '' end else '' end,
	vpic.fvincheckdigit(n), vpic.fvincheckdigit2(n, false), vpic.fvincheckdigit2(n, true),
	vpic.fvinmodelyear2(n),
	exists(select 1 from vpic.wmi w where w.wmi = vpic.fvinwmi(n)
		and (w.vehicletypeid in (2, 7) or (w.vehicletypeid = 3 and w.trucktypeid = 1))),
	coalesce((select string_agg((
		select sum(case when
			-- spvindecode_core.sql:434-437 verbatim
			j <> 9 and j < sp and chr !~ '^[0-9ABCDEFGHJKLMNPRSTUVWXYZ*]$'
			or j <> 9 and j >= sp and chr !~ '^[0-9*]$'
			or j = 9 and chr !~ '^[0-9X*]$'
			or j = 10 and chr !~ '^[1-9ABCDEFGHJKLMNPRSTVWXY]$'
		then 1 << (sp - 13) else 0 end)
		from (select substring(n, j, 1)::varchar(10) chr) c, generate_series(13, 15) sp)::text, '' order by j)
	from generate_series(1, length(n)) j), ''))
from norm order by i;
`

func regeneratePrimitivesKey(t *testing.T) {
	t.Helper()
	hexes := make([]string, 0, 4000)
	for _, in := range oracleInputs() {
		hexes = append(hexes, `"`+hex.EncodeToString([]byte(in))+`"`)
	}
	container := cmp.Or(os.Getenv("ORACLE_CONTAINER"), "nhtsa-oracle")
	cmd := exec.Command("docker", "exec", "-i", container,
		"psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "vpic")
	// On stdin: one argument can't hold the inputs (Linux caps it at 128 KiB).
	cmd.Stdin = strings.NewReader(`\set hexes '{` + strings.Join(hexes, ",") + "}'\n" + oracleSQL)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("oracle: %v\n%s", err, stderr.String())
	}
	if err := os.WriteFile(primitivesKey, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadPrimitivesKey(t *testing.T) (oracleHeader, []oracleRow) {
	t.Helper()
	f, err := os.Open(primitivesKey)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var h oracleHeader
	var rows []oracleRow
	for sc.Scan() {
		var err error
		if h.NowYear == 0 {
			err = json.Unmarshal(sc.Bytes(), &h)
		} else {
			var r oracleRow
			err = json.Unmarshal(sc.Bytes(), &r)
			rows = append(rows, r)
		}
		if err != nil {
			t.Fatalf("%s:%d: %v", primitivesKey, len(rows)+1, err)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return h, rows
}

// ampersand is what sanitize does to the oracle's upper(trim(raw)): every
// rune still outside ASCII becomes '&'.
func ampersand(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= utf8.RuneSelf {
			return '&'
		}
		return r
	}, s)
}

func TestPrimitivesOracle(t *testing.T) {
	if *updateOracle {
		regeneratePrimitivesKey(t)
	}
	h, rows := loadPrimitivesKey(t)
	if h.Collate != "C" {
		t.Fatalf("%s was built under collation %q; the oracle's is C (ultravin docs/ACCEPTANCE.md), and fVinModelYear2's BETWEEN differs under a locale-aware one",
			primitivesKey, h.Collate)
	}
	inputs := oracleInputs()
	if len(rows) != len(inputs) {
		t.Fatalf("%s has %d rows for %d inputs; regenerate it with -update-oracle", primitivesKey, len(rows), len(inputs))
	}
	for i, want := range rows {
		if want.Raw != inputs[i] {
			t.Fatalf("%s row %d is %q, input is %q; regenerate it with -update-oracle", primitivesKey, i+1, want.Raw, inputs[i])
		}
		v := newVINText(want.Raw)
		vin := v.s
		if echo := string(v.echo); echo != want.N {
			t.Errorf("row %d %q: echo %q, upper(trim()) %q", i+1, want.Raw, echo, want.N)
		}
		var scan strings.Builder
		for j := range len(vin) {
			bits := 0
			for k, sp := range []int{13, 14, 15} {
				if invalidAt(j+1, vin[j], sp) {
					bits |= 1 << k
				}
			}
			scan.WriteString(strconv.Itoa(bits))
		}
		got := oracleRow{
			Raw:         want.Raw,
			N:           vin,
			WMI:         wmi(vin),
			Descriptor:  descriptor(v.echo),
			VarKeys:     varKeys(vin),
			CheckDigit1: digitString(checkDigit1(vin)),
			CheckDigit2: [2]string{digitString(checkDigit2(vin, false)), digitString(checkDigit2(vin, true))},
			CarLT:       want.CarLT,
			Scan:        scan.String(),
		}
		if y, ok := modelYearRaw(vin, want.CarLT, h.NowYear); ok {
			got.ModelYear = &y
		}
		// sanitize's '&' stands for each rune left outside ASCII; the echo
		// and the descriptor keep it. Every computed value must match as is.
		want.N, want.WMI, want.VarKeys = ampersand(want.N), ampersand(want.WMI), ampersand(want.VarKeys)
		if diff := rowDiff(got, want); diff != "" {
			t.Errorf("row %d %q: %s", i+1, want.Raw, diff)
		}
	}
}

func digitString(c byte) string {
	if c == 0 {
		return ""
	}
	return string(c)
}

func rowDiff(got, want oracleRow) string {
	var d []string
	field := func(name string, g, w any) {
		if fmt.Sprint(g) != fmt.Sprint(w) {
			d = append(d, fmt.Sprintf("%s got %v want %v", name, g, w))
		}
	}
	field("n", strconv.Quote(got.N), strconv.Quote(want.N))
	field("wmi", got.WMI, want.WMI)
	field("descriptor", got.Descriptor, want.Descriptor)
	field("var_keys", got.VarKeys, want.VarKeys)
	field("cd1", got.CheckDigit1, want.CheckDigit1)
	field("cd2", got.CheckDigit2, want.CheckDigit2)
	field("my", yearString(got.ModelYear), yearString(want.ModelYear))
	field("scan", got.Scan, want.Scan)
	return strings.Join(d, "; ")
}

func yearString(y *int) string {
	if y == nil {
		return "NULL"
	}
	return strconv.Itoa(*y)
}
