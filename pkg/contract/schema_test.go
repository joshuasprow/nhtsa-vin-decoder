package contract

import (
	"bytes"
	"os"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// The contract's schema, the one the API publishes; every wire type here is
// checked against its definition in it.
const (
	schemaPath = "../../docs/api/decode.schema.json"
	schemaURL  = "https://github.com/joshuasprow/nitrovin/blob/main/docs/api/decode.schema.json"
)

var compileSchema = sync.OnceValues(func() (*jsonschema.Compiler, error) {
	f, err := os.Open(schemaPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, err
	}
	return c, nil
})

// schemaDef compiles $defs/name of decode.schema.json.
func schemaDef(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	c, err := compileSchema()
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(schemaURL + "#/$defs/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// validate checks the JSON text b against s.
func validate(s *jsonschema.Schema, b []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return s.Validate(v)
}
