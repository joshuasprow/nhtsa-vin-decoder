package contract_test

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

// TestEngineAnswerKey is the decode contract end to end on the engine's
// output (NITROVIN_ARTIFACT, the 2026_10 artifact): every VIN of the
// oracle's answer key, decoded through pkg/nitrovin with its hint, has the
// oracle's rows as its items, compared as a multiset; validates against
// decode.schema.json in both layouts, with and without items; and decodes
// to the same bytes on a second Decoder loaded from the same file.
//
// now is fixed after the answer key's run. It matters to items only through
// WMI publication dates and the model-year limit, and 2026_10 has neither
// between the two.
func TestEngineAnswerKey(t *testing.T) {
	path := os.Getenv("NITROVIN_ARTIFACT")
	if path == "" {
		t.Skip("NITROVIN_ARTIFACT not set: no artifact to decode with")
	}
	d1, err := nitrovin.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := nitrovin.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct{ Month string }
	if b, err := os.ReadFile("testdata/oracle/manifest.json"); err != nil {
		t.Fatal(err)
	} else if err := json.Unmarshal(b, &manifest); err != nil {
		t.Fatal(err)
	}
	if m := d1.Info().DataMonth; m != manifest.Month {
		t.Skipf("the answer key is %s's; the artifact is %s", manifest.Month, m)
	}
	now := time.Date(2026, 11, 8, 12, 30, 45, 0, time.UTC)
	schema := contract.SchemaDef(t, "Decode")
	vins := contract.LoadAnswerKey(t)
	if len(vins) != 28 {
		t.Fatalf("%d VINs in the answer key, want 28", len(vins))
	}
	rows := 0
	for i, v := range vins {
		for _, shape := range []contract.Shape{contract.ShapeVPIC, contract.ShapeNitrovin} {
			for _, full := range []bool{false, true} {
				req := contract.Request{VIN: v.VIN, ModelYear: v.Hint, Full: full, Shape: shape, Now: now}
				dec, err := d1.Decode(req)
				if err != nil {
					t.Fatalf("row %d (%q) %s full %v: %v", i+1, v.VIN, shape, full, err)
				}
				b, err := json.Marshal(dec)
				if err != nil {
					t.Fatal(err)
				}
				if err := contract.ValidateJSON(schema, b); err != nil {
					t.Errorf("row %d (%q) %s full %v does not validate: %v", i+1, v.VIN, shape, full, err)
				}
				again, err := d2.Decode(req)
				if err != nil {
					t.Fatal(err)
				}
				if b2, _ := json.Marshal(again); !bytes.Equal(b, b2) {
					t.Errorf("row %d (%q) %s full %v: a second decoder gives other bytes", i+1, v.VIN, shape, full)
				}
				if !full || shape != contract.ShapeNitrovin {
					continue
				}
				want := slices.Clone(v.Items)
				contract.SortItems(want)
				rows += len(want)
				if !slices.Equal(dec.Items, want) {
					t.Errorf("row %d (%q): items differ from the oracle's\n%s", i+1, v.VIN, itemsDiff(dec.Items, want))
				}
			}
		}
	}
	t.Logf("%d VINs, %d oracle rows", len(vins), rows)
}

// itemsDiff lists the items only one side has, in item order.
func itemsDiff(got, want []contract.Item) string {
	var b bytes.Buffer
	i, j := 0, 0
	for i < len(got) || j < len(want) {
		var c int
		switch {
		case i == len(got):
			c = 1
		case j == len(want):
			c = -1
		default:
			c = contract.CompareItems(got[i], want[j])
		}
		switch {
		case c < 0:
			line, _ := json.Marshal(got[i])
			b.WriteString("  engine " + string(line) + "\n")
			i++
		case c > 0:
			line, _ := json.Marshal(want[j])
			b.WriteString("  oracle " + string(line) + "\n")
			j++
		default:
			i, j = i+1, j+1
		}
	}
	return b.String()
}
