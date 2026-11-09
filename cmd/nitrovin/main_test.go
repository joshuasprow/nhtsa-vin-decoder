package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

// The tests run the binary `go build` makes, as a user does; only the
// terminal case runs in-process, since a test has no terminal to give it.
// The cases that decode need NITROVIN_ARTIFACT, the 2026_10 artifact; the
// usage, refusal, unloadable-data and embedded-data cases need nothing.
//
//	NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./cmd/nitrovin
//	NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./cmd/nitrovin -run TestText -update

var update = flag.Bool("update", false, "rewrite testdata/*.txt from the binary's output")

// binary is the nitrovin TestMain builds. -buildvcs=false keeps its
// version "(devel)" in a checkout.
var binary string

func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "nitrovin-cli")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binary = filepath.Join(dir, "nitrovin")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command("go", "build", "-buildvcs=false", "-o", binary, ".")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "go build:", err)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type result struct {
	exit           int
	stdout, stderr string
}

// cli runs the binary with args and stdin, in an environment without
// NITROVIN_DATA plus env.
func cli(t *testing.T, env []string, stdin string, args ...string) result {
	t.Helper()
	cmd := exec.Command(binary, args...)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NITROVIN_DATA=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	cmd.Stdin = strings.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	r := result{stdout: stdout.String(), stderr: stderr.String()}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		r.exit = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return r
}

// problem reads stderr as one problem, one line of JSON.
func problem(t *testing.T, r result) contract.Problem {
	t.Helper()
	var p contract.Problem
	if strings.Count(r.stderr, "\n") != 1 || json.Unmarshal([]byte(r.stderr), &p) != nil {
		t.Fatalf("stderr is not one problem line: %q", r.stderr)
	}
	return p
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"1HGCM82633A004352"}, // no bare-VIN shorthand
		{"--bogus", "info"},
		{"decode"},
		{"decode", "1HGCM82633A004352", "-"},
		{"decode", "--shape", "vpic,nitrovin", "1HGCM82633A004352"},
		{"decode", "-o", "yaml", "1HGCM82633A004352"},
		{"decode", "--full", "-o", "text", "1HGCM82633A004352"},
		{"decode", "--model-year", "2003.5", "1HGCM82633A004352"},
		{"decode", "--model-year", "2147483648", "1HGCM82633A004352"},
		{"decode", "--now", "yesterday", "1HGCM82633A004352"},
		{"decode", "1HGCM82633A004352", "--bogus"},
		{"info", "extra"},
	} {
		r := cli(t, nil, "", args...)
		if r.exit != 2 || r.stdout != "" || !strings.HasPrefix(r.stderr, "nitrovin: ") || !strings.Contains(r.stderr, "\nusage: nitrovin") {
			t.Errorf("nitrovin %q: exit %d, stdout %q, stderr %q; want exit 2, the mistake and the usage on stderr only", args, r.exit, r.stdout, r.stderr)
		}
	}
	for _, args := range [][]string{{"-h"}, {"decode", "-h"}, {"--help", "info"}} {
		r := cli(t, nil, "", args...)
		if r.exit != 0 || !strings.HasPrefix(r.stdout, "usage: nitrovin") || r.stderr != "" {
			t.Errorf("nitrovin %q: exit %d, stdout %q, stderr %q; want exit 0 and the usage on stdout", args, r.exit, r.stdout, r.stderr)
		}
	}
}

// TestRefusedArgument: a refused argument VIN exits 2 with nothing on
// stdout, before any data is loaded (none is here).
func TestRefusedArgument(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		detail string
	}{
		{[]string{"decode", "1HGCM82633A004352", "1HGCM82633A0043521"}, "argument 2: "},
		{[]string{"decode", "--full", "  "}, "argument 1: "},
	} {
		r := cli(t, nil, "", tc.args...)
		p := problem(t, r)
		if r.exit != 2 || r.stdout != "" || p.Type != contract.InvalidRequest || p.Pointer != "/vin" || !strings.HasPrefix(p.Detail, tc.detail) {
			t.Errorf("nitrovin %q: exit %d, stdout %q, problem %+v; want exit 2, nothing on stdout, an invalid-request at /vin naming %q", tc.args, r.exit, r.stdout, p, tc.detail)
		}
	}
}

// TestNoData: data that will not load is an internal problem naming where
// it came from, exit 1.
func TestNoData(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.artifact")
	for _, tc := range []struct {
		env    []string
		args   []string
		detail string
	}{
		{nil, []string{"--data", missing, "info"}, "--data " + missing},
		{nil, []string{"--data", missing, "--version"}, "--data " + missing},
		{[]string{"NITROVIN_DATA=" + missing}, []string{"decode", "-"}, "NITROVIN_DATA " + missing},
	} {
		r := cli(t, tc.env, "1HGCM82633A004352\n", tc.args...)
		p := problem(t, r)
		if r.exit != 1 || r.stdout != "" || p.Type != contract.Internal || !strings.Contains(p.Detail, tc.detail) {
			t.Errorf("%q nitrovin %q: exit %d, stdout %q, problem %+v; want exit 1 and an internal problem naming %q", tc.env, tc.args, r.exit, r.stdout, p, tc.detail)
		}
	}
}

// TestEmbeddedData: with no --data and no NITROVIN_DATA, the binary decodes
// against the artifact it embeds, the month go.mod pins, and says so.
func TestEmbeddedData(t *testing.T) {
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if got := d.Info().DataSource; got != contract.DataSourceEmbedded {
		t.Fatalf("embedded data source %q, want %q", got, contract.DataSourceEmbedded)
	}
	if got := d.Info().DataMonth; got != embedded.Month {
		t.Fatalf("embedded data month %q, want %q", got, embedded.Month)
	}
	info, err := json.Marshal(d.Info())
	if err != nil {
		t.Fatal(err)
	}
	if r := cli(t, nil, "", "info"); r.exit != 0 || r.stderr != "" || r.stdout != string(info)+"\n" {
		t.Errorf("nitrovin info: exit %d, stderr %q, stdout %q, want %q", r.exit, r.stderr, r.stdout, info)
	}
	if r, w := cli(t, nil, "", "--version"), "nitrovin (devel) (data "+embedded.Month+")\n"; r.exit != 0 || r.stderr != "" || r.stdout != w {
		t.Errorf("nitrovin --version: exit %d, stderr %q, stdout %q, want %q", r.exit, r.stderr, r.stdout, w)
	}
	clock, _ := contract.ParseNow(now)
	req := contract.Request{VIN: "1HGCM82633A004352", Now: clock}
	if r, w := cli(t, nil, "", "decode", "--now", now, req.VIN), want(t, d, []contract.Request{req}); r.exit != 0 || r.stderr != "" || r.stdout != w {
		t.Errorf("nitrovin decode %s: exit %d, stderr %q, stdout %q, want %q", req.VIN, r.exit, r.stderr, r.stdout, w)
	}
}

// now is every decoding test's clock.
const now = "2026-11-08T12:30:45Z"

// artifact is $NITROVIN_ARTIFACT's path and a Decoder on it, the contract
// layer every CLI output is compared with.
func artifact(t *testing.T) (string, *nitrovin.Decoder) {
	t.Helper()
	path := os.Getenv("NITROVIN_ARTIFACT")
	if path == "" {
		t.Skip("NITROVIN_ARTIFACT not set: no artifact to decode with")
	}
	d, err := nitrovin.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, d
}

// keyed is one VIN of the oracle's answer key with its model-year hint.
type keyed struct {
	vin  string
	year contract.Null[int32]
}

// answerKey reads the VINs of pkg/contract's answer key, skipping when the
// artifact is another month's.
func answerKey(t *testing.T, d *nitrovin.Decoder) []keyed {
	t.Helper()
	var manifest struct{ Month string }
	if b, err := os.ReadFile("../../pkg/contract/testdata/oracle/manifest.json"); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if m := d.Info().DataMonth; m != manifest.Month {
		t.Skipf("the answer key is %s's; the artifact is %s", manifest.Month, m)
	}
	f, err := os.Open("../../pkg/contract/testdata/oracle/answer-key.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	var vins []keyed
	last := ""
	for _, row := range rows[1:] { // input_row, vin, year_hint, ...
		if row[0] == last {
			continue
		}
		last = row[0]
		k := keyed{vin: row[1]}
		if row[2] != "" {
			y, err := strconv.ParseInt(row[2], 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			k.year = contract.Value(int32(y))
		}
		vins = append(vins, k)
	}
	if len(vins) != 28 {
		t.Fatalf("%d VINs in the answer key, want 28", len(vins))
	}
	return vins
}

// want is what the contract layer gives for reqs: one compact JSON line
// per decode.
func want(t *testing.T, d *nitrovin.Decoder, reqs []contract.Request) string {
	t.Helper()
	var b strings.Builder
	for _, req := range reqs {
		dec, err := d.Decode(req)
		if err != nil {
			t.Fatal(err)
		}
		j, err := json.Marshal(dec)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(j)
		b.WriteByte('\n')
	}
	return b.String()
}

// TestDecodeAnswerKey: `decode -` and `decode <vin>…` over the answer key's
// VINs print, byte for byte, what the contract layer gives for the same
// requests, in input order.
func TestDecodeAnswerKey(t *testing.T) {
	path, d := artifact(t)
	vins := answerKey(t, d)
	clock, err := contract.ParseNow(now)
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"NITROVIN_DATA=" + path}
	for _, tc := range []struct {
		flags []string
		base  contract.Request // what flags mean
	}{
		{nil, contract.Request{}},
		{[]string{"--full", "--shape", "nitrovin"}, contract.Request{Full: true, Shape: contract.ShapeNitrovin}},
		{[]string{"--model-year", "2005", "--shape", "vpic"}, contract.Request{ModelYear: contract.Value[int32](2005), Shape: contract.ShapeVPIC}},
	} {
		cmd := append([]string{"decode", "--now", now}, tc.flags...)
		var stdin strings.Builder
		var stdinReqs, argReqs []contract.Request
		var args []string
		for i, k := range vins {
			req := tc.base
			req.VIN, req.Now = k.vin, clock
			argReqs = append(argReqs, req)
			args = append(args, k.vin)
			stdin.WriteString(k.vin)
			if k.year.Valid { // a line's own year beats --model-year
				req.ModelYear = k.year
				stdin.WriteString([]string{",", "\t"}[i%2] + strconv.Itoa(int(k.year.V)))
			}
			stdin.WriteString("\n")
			stdinReqs = append(stdinReqs, req)
		}

		r := cli(t, env, stdin.String(), slices.Concat(cmd, []string{"-"})...)
		if w := want(t, d, stdinReqs); r.exit != 0 || r.stderr != "" || r.stdout != w {
			t.Errorf("decode %q -: exit %d, stderr %q, stdout equals the contract layer's: %t", tc.flags, r.exit, r.stderr, r.stdout == w)
		}
		r = cli(t, env, "", slices.Concat(cmd, args)...)
		if w := want(t, d, argReqs); r.exit != 0 || r.stderr != "" || r.stdout != w {
			t.Errorf("decode %q <28 VINs>: exit %d, stderr %q, stdout equals the contract layer's: %t", tc.flags, r.exit, r.stderr, r.stdout == w)
		}
		// One VIN off a terminal: one compact line too.
		r = cli(t, env, "", slices.Concat(cmd, args[:1])...)
		if w := want(t, d, argReqs[:1]); r.exit != 0 || r.stderr != "" || r.stdout != w {
			t.Errorf("decode %q %s: exit %d, stderr %q, stdout %q, want %q", tc.flags, args[0], r.exit, r.stderr, r.stdout, w)
		}
	}
}

// TestRefusedLine: a refused stdin line stops the run with exit 1 and a
// problem naming the line; the decodes before it are complete.
func TestRefusedLine(t *testing.T) {
	path, d := artifact(t)
	clock, _ := contract.ParseNow(now)
	env := []string{"NITROVIN_DATA=" + path}
	for _, tc := range []struct {
		stdin   string
		printed []contract.Request
		detail  string
		pointer string
	}{
		{
			stdin: "1HGCM82633A004352\r\n\n5YJ3E1EA2JF000316,2019\n1HGCM82633A004352,20x3\nZZZZZZZZZZZZZZZZZ\n",
			printed: []contract.Request{
				{VIN: "1HGCM82633A004352", Now: clock},
				{VIN: "5YJ3E1EA2JF000316", ModelYear: contract.Value[int32](2019), Now: clock},
			},
			detail: "line 4: ", pointer: "/model_year",
		},
		{stdin: "\n1HGCM82633A0043521\n1HGCM82633A004352\n", detail: "line 2: ", pointer: "/vin"},
		{stdin: "1HGCM82633A004352\t\n   \n", printed: []contract.Request{{VIN: "1HGCM82633A004352", Now: clock}}, detail: "line 2: ", pointer: "/vin"},
	} {
		r := cli(t, env, tc.stdin, "decode", "--now", now, "-")
		p := problem(t, r)
		if w := want(t, d, tc.printed); r.exit != 1 || r.stdout != w || p.Type != contract.InvalidRequest || p.Pointer != tc.pointer || !strings.HasPrefix(p.Detail, tc.detail) {
			t.Errorf("decode - <<< %q: exit %d, stdout %q, problem %+v; want exit 1, stdout %q, an invalid-request at %s naming %q", tc.stdin, r.exit, r.stdout, p, w, tc.pointer, tc.detail)
		}
	}
}

// TestDataPrecedence: --data (the command's over the root's), then
// NITROVIN_DATA.
func TestDataPrecedence(t *testing.T) {
	path, d := artifact(t)
	missing := filepath.Join(t.TempDir(), "missing.artifact")
	info, err := json.Marshal(d.Info())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		env  []string
		args []string
	}{
		{[]string{"NITROVIN_DATA=" + path}, []string{"info"}},
		{[]string{"NITROVIN_DATA=" + missing}, []string{"--data", path, "info"}},
		{[]string{"NITROVIN_DATA=" + missing}, []string{"info", "--data", path}},
		{nil, []string{"--data", missing, "info", "--data", path}},
	} {
		if r := cli(t, tc.env, "", tc.args...); r.exit != 0 || r.stderr != "" || r.stdout != string(info)+"\n" {
			t.Errorf("%q nitrovin %q: exit %d, stderr %q, stdout is not the contract's info: %t", tc.env, tc.args, r.exit, r.stderr, r.stdout != string(info)+"\n")
		}
	}
	r := cli(t, nil, "", "--data", path, "--version")
	if w := fmt.Sprintf("nitrovin (devel) (data %s)\n", d.Info().DataMonth); r.exit != 0 || r.stderr != "" || r.stdout != w {
		t.Errorf("nitrovin --version: exit %d, stderr %q, stdout %q, want %q", r.exit, r.stderr, r.stdout, w)
	}
}

// TestText compares `-o text` with testdata/text.txt: a VIN with a
// multi-valued element (Note) and one decoding to error codes.
func TestText(t *testing.T) {
	path, d := artifact(t)
	answerKey(t, d) // the transcript is 2026_10's
	r := cli(t, []string{"NITROVIN_DATA=" + path}, "", "decode", "-o", "text", "--shape", "vpic", "--now", now, "2T1BURHE8JC123456", "5UXWX7C5XBA123456")
	if r.exit != 0 || r.stderr != "" {
		t.Fatalf("exit %d, stderr %q", r.exit, r.stderr)
	}
	const golden = "testdata/text.txt"
	if *update {
		if err := os.WriteFile(golden, []byte(r.stdout), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if r.stdout != string(w) {
		t.Errorf("-o text differs from %s (-update rewrites it):\n%s", golden, r.stdout)
	}
}

// TestTerminal: on a terminal one decode, and info, are pretty-printed;
// several decodes are still JSON lines.
func TestTerminal(t *testing.T) {
	path, d := artifact(t)
	clock, _ := contract.ParseNow(now)
	runOn := func(args ...string) string {
		var stdout, stderr bytes.Buffer
		e := &env{stdin: strings.NewReader(""), stdout: &stdout, stderr: &stderr, terminal: true,
			getenv: func(k string) string { return map[string]string{"NITROVIN_DATA": path}[k] }}
		if code := run(args, e); code != 0 {
			t.Fatalf("nitrovin %q: exit %d, stderr %q", args, code, stderr.String())
		}
		return stdout.String()
	}
	pretty := func(v any) string {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		return string(b) + "\n"
	}
	dec, err := d.Decode(contract.Request{VIN: "1HGCM82633A004352", Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	if got, w := runOn("decode", "--now", now, "1HGCM82633A004352"), pretty(dec); got != w {
		t.Errorf("one decode on a terminal:\n%s\nwant\n%s", got, w)
	}
	if got, w := runOn("info"), pretty(d.Info()); got != w {
		t.Errorf("info on a terminal is not the pretty-printed info")
	}
	reqs := []contract.Request{{VIN: "1HGCM82633A004352", Now: clock}, {VIN: "5YJ3E1EA2JF000316", Now: clock}}
	if got, w := runOn("decode", "--now", now, "1HGCM82633A004352", "5YJ3E1EA2JF000316"), want(t, d, reqs); got != w {
		t.Errorf("two decodes on a terminal:\n%s\nwant\n%s", got, w)
	}
}
