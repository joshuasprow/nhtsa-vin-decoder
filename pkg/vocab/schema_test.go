package vocab

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The file format's schema, the one docs/api publishes.
const (
	schemaPath = "../../docs/api/vocabulary.schema.json"
	schemaURL  = "https://github.com/joshuasprow/nitrovin/blob/main/docs/api/vocabulary.schema.json"
)

func lineSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(schemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schemaURL)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validLine(s *jsonschema.Schema, line string) error {
	v, err := jsonschema.UnmarshalJSON(strings.NewReader(line))
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// TestSchema: every line of the fixture, which Open reads without a fault,
// validates against vocabulary.schema.json, and lines Read refuses alone
// break it too.
func TestSchema(t *testing.T) {
	s := lineSchema(t)
	paths, err := filepath.Glob("../../testdata/vocab/*.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	drift, _ := filepath.Glob("../../testdata/vocab/drift/*.jsonl")
	fitment, _ := filepath.Glob("../../testdata/vocab/fitment/*.jsonl")
	if len(fitment) == 0 {
		t.Fatal("no testdata/vocab/fitment/*.jsonl")
	}
	for _, p := range slices.Concat(paths, drift, fitment) {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
			if err := validLine(s, string(line)); err != nil {
				t.Errorf("%s:%d does not validate: %v", p, i+1, err)
			}
		}
	}
	for _, line := range []string{
		`{"format":1,"kind":"spine","name":"vpic","version":"1"}`,
		`{"format":2,"kind":"spine","name":"parts","version":"1"}`,
		`{"format":1,"kind":"vocabulary","name":"yard","version":"1","subject":"part","fields":["make"]}`,
		`{"format":1,"kind":"vocabulary","name":"yard","version":"1","subject":"vehicle","fields":["make"],"spine":"parts"}`,
		`{"entry":{"make":"Dodge"},"mappings":[{"make":476,"model":1938}]}`,
		`{"entry":{"make":"Chevrolet"},"mappings":[{"ext":"ext:classics/corvair","qualifiers":{"34":["Monza"]}}]}`,
		`{"entry":{"make":"Dodge"},"mappings":[{"model":1938,"qualifiers":{}}]}`,
		`{"entry":{"make":"Dodge"},"mappings":[{"model":1938,"origin":"guessed"}]}`,
		`{"entry":{"make":"Dodge"},"mappings":[],"aliases":[]}`,
		`{"id":"packard","make_name":"Packard","make":467,"name":"Clipper"}`,
		`{"id":"pads","parent":"brakes"}`,
		`{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts"}`,
		`{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts","vehicles":"yard","release":""}`,
		`{"format":1,"kind":"catalog","name":"cat","version":"1","spine":"parts","vehicles":"yard","subject":"vehicle"}`,
		`{"fitment":{"type":"pads","number":"P1","side":"front"},"applications":[]}`,
		`{"fitment":{"type":"pads","number":"P1"}}`,
		`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"make":"Dodge"}}]}`,
		`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"make":476,"model":1938},"options":""}]}`,
		`{"fitment":{"type":"pads","number":"P1"},"applications":[],"to":{"type":"pads","number":"P2"}}`,
		`{"redirect":{"vehicle":{"model":1938},"from":"pads"}}`,
		`{"retired":{"type":"pads","number":"P0"}}`,
		`{"fitment":{"type":"pads","number":"P1"},"applications":[{"vehicle":{"ext":"ext:classics/corvair"},"options":"","qualifiers":{"34":["Monza"]}}]}`,
	} {
		if validLine(s, line) == nil {
			t.Errorf("%s validates; the schema should refuse it as Read does", line)
		}
	}
}
