// Command nitrovin-epa builds the epa-vehicles vocabulary: EPA's
// fuel-economy names (vehicles.csv, fueleconomy.gov) mapped onto one data
// month's vPIC makes and models.
//
//	nitrovin-epa [-data ARTIFACT] [-curated FILE] [-o FILE] VEHICLES.CSV
//
// VEHICLES.CSV is vehicles.csv, or the vehicles.csv.zip EPA publishes at
// https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip. The data month
// is -data's artifact, else the one embedded in this build. The vocabulary
// (docs/api/vocabulary.schema.json, format 1) goes to -o, else stdout, and
// a coverage summary to stderr.
//
// An entry is an EPA (make, model) after vocab.Normalize, so EPA's case and
// spacing variants are one entry spelled as most of its rows spell it.
// Each mapping holds for the years of the entry's rows. Mappings are
// derived by the rules in match.go (origin derived, the rule in its note),
// with Drive Type and Series qualifiers where the name implies them,
// narrowed to the values the model's VIN schemas emit (qualify.go). An
// entry of the curated overrides replaces the derived mappings (origin
// curated). Entries nothing maps are listed unmapped, with a note.
//
// The curated overrides are a vehicle vocabulary of fields [make, model]
// (curated.jsonl beside this file, built in; -curated names another).
// A mapping there with no years takes the entry's, and one with no
// qualifiers takes the derived ones for its model; an entry with no
// mappings stays unmapped, its note saying why. Same inputs, same bytes:
// the version is the newest createdOn or modifiedOn date in vehicles.csv
// and the data month ("2026-11-03+2026_10").
//
// From a checkout:
//
//	curl -sSLO https://www.fueleconomy.gov/feg/epadata/vehicles.csv.zip
//	go run ./cmd/nitrovin-epa -o epa-vehicles.jsonl vehicles.csv.zip
//	go run ./cmd/nitrovin vocab check epa-vehicles.jsonl
package main

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

//go:embed curated.jsonl
var curatedJSONL []byte

// curatedName is the built-in overrides' name in messages.
const curatedName = "cmd/nitrovin-epa/curated.jsonl"

const usage = "usage: nitrovin-epa [-data ARTIFACT] [-curated FILE] [-o FILE] VEHICLES.CSV"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "nitrovin-epa:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("nitrovin-epa", flag.ContinueOnError)
	fs.SetOutput(stderr)
	data := fs.String("data", "", "artifact of the data month (default: the embedded one)")
	curatedPath := fs.String("curated", "", "curated overrides (default: the built-in "+curatedName+")")
	out := fs.String("o", "", "vocabulary to write (default: stdout)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("%s", usage)
	}

	f, err := openEPA(fs.Arg(0))
	if err != nil {
		return err
	}
	raw := embedded.Artifact()
	source := contract.DataSourceEmbedded
	if *data != "" {
		if source, err = filepath.Abs(*data); err != nil {
			return err
		}
		if raw, err = os.ReadFile(source); err != nil {
			return err
		}
	}
	d, err := nitrovin.Load(raw, source)
	if err != nil {
		return err
	}
	cur, err := readCurated(*curatedPath)
	if err != nil {
		return err
	}

	var buf bytes.Buffer
	cov, err := build(&buf, f, d.Month().(vocab.ModelMonth), cur)
	if err != nil {
		return err
	}
	if *out == "" {
		_, err = stdout.Write(buf.Bytes())
	} else {
		err = os.WriteFile(*out, buf.Bytes(), 0o644)
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "%s %s+%s: ", Name, f.date, d.Month().Name())
	cov.report(stderr)
	return nil
}

// openEPA reads vehicles.csv, or the vehicles.csv in a zip.
func openEPA(path string) (*epaFile, error) {
	if !strings.HasSuffix(strings.ToLower(path), ".zip") {
		r, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readEPA(r)
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer z.Close()
	for _, zf := range z.File {
		if filepath.Base(zf.Name) != "vehicles.csv" {
			continue
		}
		r, err := zf.Open()
		if err != nil {
			return nil, err
		}
		defer r.Close()
		return readEPA(r)
	}
	return nil, fmt.Errorf("%s: no vehicles.csv in the zip", path)
}

// readCurated reads the curated overrides: path, or the built-in file.
func readCurated(path string) (*vocab.File, error) {
	if path == "" {
		return vocab.Read(curatedName, bytes.NewReader(curatedJSONL))
	}
	r, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return vocab.Read(path, r)
}
