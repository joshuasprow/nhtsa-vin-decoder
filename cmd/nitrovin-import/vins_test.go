package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/decoder"
)

func TestCheckDigit(t *testing.T) {
	for _, vin := range []string{"1HGCM82633A004352", "1M8GDM9AXKP042788", "11111111111111111"} {
		if got := checkDigit([]byte(vin)); got != vin[8] {
			t.Errorf("checkDigit(%s) = %c, want %c", vin, got, vin[8])
		}
	}
}

// A key lands where the pattern pass reads it (positions 4-8, '|',
// positions 10-17), over the fills and the year, and a key no VIN of the
// WMI can satisfy builds nothing.
func TestBuildVIN(t *testing.T) {
	y2018, y1999 := yearAt(2018, false), yearAt(1999, true)
	for _, c := range []struct {
		wmi, keys string
		y         yearChoice
		want      string // position 9 as '_'; "" for no VIN
	}{
		{"1HG", "CM826|3A", y2018, "1HGCM826_3A111111"},
		{"5YJ", "3E1E[A-C]|J", y2018, "5YJ3E1EA_JA111111"},
		{"5YJ", "*[1-A]##*", y2018, "5YJA111A_JA111111"},
		{"5YJ", "", y1999, "5YJAAA1A_XA111111"},
		{"5YJ", "*", nullYear, "5YJAAAAA_0A111111"},
		{"5YJ", "ABCDE*X", y2018, "5YJABCDE_XA111111"},
		{"1F9ABC", "*****|**AB", y2018, "1F9AAAAA_JAABC111"},
		{"1F9ABC", "*****|**X", y2018, ""},
		{"5YJ", "ABCDEF", y2018, ""},
		{"5YJ", "ABCDE|123456789", y2018, ""},
	} {
		got, ok := buildVIN(c.wmi, c.keys, c.y)
		if !ok {
			if c.want != "" {
				t.Errorf("buildVIN(%s, %q, %v) built nothing, want %s", c.wmi, c.keys, c.y, c.want)
			}
			continue
		}
		if c.want == "" {
			t.Errorf("buildVIN(%s, %q, %v) = %s, want nothing", c.wmi, c.keys, c.y, got)
			continue
		}
		if masked := got[:8] + "_" + got[9:]; masked != c.want {
			t.Errorf("buildVIN(%s, %q, %v) = %s, want %s", c.wmi, c.keys, c.y, got, c.want)
		}
		if got[8] != checkDigit([]byte(got)) {
			t.Errorf("buildVIN(%s, %q, %v) = %s: check digit not stamped", c.wmi, c.keys, c.y, got)
		}
	}
}

// With an artifact (NITROVIN_ARTIFACT), on the committed parity corpus: the
// first file opens with the corpus byte for byte (what -update-parity
// freezes), the list reaches every WMI of the month, no file holds more
// than -max VINs, nothing it adds to the corpus makes the procedure raise
// (scripts/oracle.sh decode would stop there), and the same inputs give
// the same files.
func TestVinsCoversTheMonth(t *testing.T) {
	path := os.Getenv("NITROVIN_ARTIFACT")
	if path == "" {
		t.Skip("NITROVIN_ARTIFACT not set: no artifact to generate from")
	}
	const perFile = 5000
	now := time.Now().UTC().Format(time.RFC3339) // the month's WMIs are published by then
	corpora, err := filepath.Glob("../../testdata/parity/*/corpus.tsv")
	if err != nil || len(corpora) != 1 {
		t.Fatalf("committed corpus: %v %v, want one", corpora, err)
	}
	corpus := corpora[0]
	var first []string
	for i := range 2 {
		dir := t.TempDir()
		report := filepath.Join(dir, "report.json")
		var stdout, stderr bytes.Buffer
		args := []string{"vins", "-now", now, "-max", strconv.Itoa(perFile), "-o", filepath.Join(dir, "vins"), "-report", report, path, corpus}
		if err := run(args, nil, &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		files := strings.Fields(stdout.String())
		var contents []string
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			contents = append(contents, string(b))
		}
		if i == 1 {
			if strings.Join(contents, "\x00") != strings.Join(first, "\x00") {
				t.Error("a second run wrote different files")
			}
			break
		}
		first = contents
		t.Log(stderr.String())

		var rep vinsReport
		if b, err := os.ReadFile(report); err != nil {
			t.Fatal(err)
		} else if err := json.Unmarshal(b, &rep); err != nil {
			t.Fatal(err)
		}
		if len(rep.WMIs.Unreached) > 0 {
			t.Errorf("%d WMIs unreached: %v", len(rep.WMIs.Unreached), rep.WMIs.Unreached)
		}

		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		a, err := artifact.Open(b)
		if err != nil {
			t.Fatal(err)
		}
		e := decoder.NewEngine(a)
		at, _ := time.Parse(time.RFC3339, now)
		corpusText, err := os.ReadFile(corpus)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(contents[0], string(corpusText)) {
			t.Errorf("%s does not open with %s byte for byte", files[0], corpus)
		}
		for f, content := range contents {
			n, section := 0, ""
			if f == 0 {
				section = "corpus"
			}
			for _, l := range strings.Split(content, "\n") {
				if l == "" || l[0] == '#' {
					if l == "# mutations" || l == "# generated" {
						section = l[2:]
					}
					continue
				}
				n++
				if section == "corpus" {
					continue
				}
				vin, hint, _ := strings.Cut(l, "\t")
				h := contract.Null[int32]{}
				if hint != "" {
					y, err := strconv.Atoi(hint)
					if err != nil {
						t.Fatalf("%s: hint %q: %v", files[f], hint, err)
					}
					h = contract.Value(int32(y))
				}
				if _, err := e.Project(vin, h, at); err != nil {
					t.Errorf("%s: %s %q raises: %v", files[f], section, vin, err)
				}
			}
			if n > perFile {
				t.Errorf("%s holds %d VINs, more than %d", files[f], n, perFile)
			}
		}
	}
}
