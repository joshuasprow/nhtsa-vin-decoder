package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

func TestUnescape(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:           "plain",
		`a\\b`:            `a\b`,
		`tab\there`:       "tab\there",
		`\b\f\n\r\v`:      "\b\f\n\r\v",
		`\101\x42\x4a\7z`: "ABJ\az",
		`\xzz`:            "xzz",
		`\.`:              ".",
		`TRUCK \"BIG\"`:   `TRUCK "BIG"`,
	} {
		got, err := unescape([]byte(in))
		if err != nil || got != want {
			t.Errorf("unescape(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := unescape([]byte(`ends\`)); err == nil {
		t.Error("trailing backslash accepted")
	}
	for _, s := range []string{"a\\b\tc\nd\re\bf\fg\vh", "", `\N`} {
		if got, _ := unescape([]byte(escape(s))); got != s {
			t.Errorf("unescape(escape(%q)) = %q", s, got)
		}
	}
}

// stream is a minimal extract: every table empty except element and lookup.
func stream(rows map[string][]string) string {
	var b strings.Builder
	for _, ts := range artifact.Schema() {
		b.WriteString(marker + ts.Name + "\n")
		for _, r := range rows[ts.Name] {
			b.WriteString(r + "\n")
		}
	}
	b.WriteString(endMarker + "\n")
	return b.String()
}

var sample = map[string][]string{
	"element": {
		"26\tMake\tMake\tMake\tf\tGeneral\tlookup\tPattern\t5",
		"143\tError Code\tErrorCode\tErrorCode\t\\N\tGeneral\tstring\t\\N\t\\N",
		"5\tBody Class\tBodyClass\tBodyStyle\tt\tExterior / Body\tlookup\t\t0", // decode "", not NULL
	},
	"lookup": {
		"make\t441\tTesla",
		"make\t474\tHONDA\\tMOTOR \\\\ CO",
		"bodystyle\t64\t\\N",
	},
	"wmi": {
		"5YJ\t1998\t955\t441\t2\t\\N\t1420070400000000\t-1",
	},
}

// A stream goes into an artifact and comes back out of inspect row for row:
// NULL stays apart from "", escapes survive, rows come back sorted.
func TestBuildThenInspectGivesBackTheRows(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "x.artifact")
	var stderr bytes.Buffer
	if err := run([]string{"build", "-month", "2026_10", "-o", out}, strings.NewReader(stream(sample)), nil, &stderr); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "x.json")); err != nil {
		t.Errorf("manifest: %v", err)
	}
	for table, rows := range sample {
		var stdout bytes.Buffer
		if err := run([]string{"inspect", "-verify", out, table}, nil, &stdout, nil); err != nil {
			t.Fatal(err)
		}
		got := strings.Split(strings.TrimSuffix(stdout.String(), "\n"), "\n")[1:]
		want := slices.Clone(rows)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s:\n got %q\nwant %q", table, got, want)
		}
	}
}

func TestBuildRejectsBadStreams(t *testing.T) {
	good := stream(sample)
	bad := map[string]string{
		"truncated":       strings.TrimSuffix(good, endMarker+"\n"),
		"no last newline": strings.TrimSuffix(good, "\n"),
		"data after end":  good + "junk\n",
		"before marker":   "junk\n" + good,
		"table order":     strings.Replace(good, marker+"wmi\n", marker+"pattern\n", 1),
		"field count":     strings.Replace(good, "make\t441\tTesla", "make\t441", 1),
		"bool":            strings.Replace(good, "\tMake\tf\t", "\tMake\tyes\t", 1),
		"int":             strings.Replace(good, "\t1998\t", "\t1998.5\t", 1),
		"int32 NULL":      strings.Replace(good, "\t1998\t", "\t-2147483648\t", 1),
	}
	for name, s := range bad {
		out := filepath.Join(t.TempDir(), "x.artifact")
		err := run([]string{"build", "-month", "2026_10", "-o", out}, strings.NewReader(s), nil, &bytes.Buffer{})
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, statErr := os.Stat(out); statErr == nil {
			t.Errorf("%s: wrote an artifact anyway", name)
		}
	}
}
