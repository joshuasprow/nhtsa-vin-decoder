// Command nitrovin-import builds nitrovin's data artifact from NHTSA's vPIC
// dump restored into Postgres, and inspects artifacts.
//
//	nitrovin-import sql
//	    Print the psql script that extracts every table the artifact holds,
//	    as one stream.
//	nitrovin-import build -month YYYY_MM [-o FILE] [-manifest FILE] [-in FILE]
//	    Read that stream (stdin by default) and write the artifact and its
//	    manifest: format, data month, size, blake3, rows per table.
//	nitrovin-import inspect [-verify] FILE [TABLE [COLUMN=VALUE ...]]
//	    Print an artifact's header and table directory, or the rows of one
//	    table matching every COLUMN=VALUE, in COPY text format (\N is NULL,
//	    timestamps are microseconds).
//	nitrovin-import vins [-now RFC3339] [-max N] [-o PREFIX] [-report FILE] ARTIFACT CORPUS...
//	    Write the refresh's VIN list (ADR 0008) as PREFIX-1.tsv, PREFIX-2.tsv,
//	    … of at most N VINs each (default 5000) and print their names: the
//	    CORPUS files as given, opening the first file; one one-character
//	    mutation of each corpus VIN for the error paths; then VINs synthesized
//	    from the artifact's patterns until every WMI and VinSchema the month
//	    links is reached. The coverage summary goes to stderr; -report writes
//	    it in full as JSON.
//
// With the dump restored by scripts/oracle.sh, from a checkout:
//
//	go run ./cmd/nitrovin-import sql |
//	  docker exec -i nhtsa-oracle psql -X -q -U postgres -d vpic |
//	  go run ./cmd/nitrovin-import build -month 2026_10
//
// The format is github.com/joshuasprow/nitrovin-data/pkg/artifact (ADR 0003).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nitrovin-import:", err)
		os.Exit(1)
	}
}

const usage = `usage:
  nitrovin-import sql
  nitrovin-import build -month YYYY_MM [-o FILE] [-manifest FILE] [-in FILE]
  nitrovin-import inspect [-verify] FILE [TABLE [COLUMN=VALUE ...]]
  ` + vinsUsage

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("no command\n%s", usage)
	}
	switch args[0] {
	case "sql":
		return writeSQL(stdout)
	case "build":
		return build(args[1:], stdin, stderr)
	case "inspect":
		return inspectCmd(args[1:], stdout)
	case "vins":
		return vinsCmd(args[1:], stdout, stderr)
	}
	return fmt.Errorf("unknown command %q\n%s", args[0], usage)
}

func build(args []string, stdin io.Reader, stderr io.Writer) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	month := fs.String("month", "", "data month of the dump, YYYY_MM (required)")
	out := fs.String("o", "nitrovin.artifact", "artifact to write")
	manifest := fs.String("manifest", "", "manifest to write (default: the artifact's name with .json)")
	in := fs.String("in", "", "extract stream to read (default: stdin)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *month == "" || fs.NArg() > 0 {
		return fmt.Errorf("build needs -month and no arguments\n%s", usage)
	}
	if *manifest == "" {
		*manifest = trimExt(*out) + ".json"
	}
	r := stdin
	if *in != "" {
		f, err := os.Open(*in)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}

	tables, err := readStream(r)
	if err != nil {
		return fmt.Errorf("extract: %w", err)
	}
	b, err := artifact.Encode(*month, tables)
	if err != nil {
		return err
	}
	a, err := artifact.Open(b)
	if err != nil {
		return err
	}
	if err := a.Verify(); err != nil {
		return err
	}
	m, err := json.MarshalIndent(a.Manifest(), "", "  ")
	if err != nil {
		return err
	}
	if err := writeFile(*out, b); err != nil {
		return err
	}
	if err := writeFile(*manifest, append(m, '\n')); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "%s: %d bytes, %d strings, blake3 %s\n", *out, a.Size(), a.Strings(), a.Manifest().Blake3)
	return nil
}

// writeFile replaces path atomically, so a failed build leaves the old file.
func writeFile(path string, b []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func trimExt(p string) string { return p[:len(p)-len(filepath.Ext(p))] }

// ci-matrix probe
