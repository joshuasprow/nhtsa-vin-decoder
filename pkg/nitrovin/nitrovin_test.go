package nitrovin

import (
	"slices"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestVehicleSpecsRepeat (#165): on 3FMTK1R40PR037734 the procedure emits
// Other Battery Info (element 1) twice, from a pattern and from Vehicle
// Specs, which never counts element 1 as held. Both layouts decode it, as
// several values.
func TestVehicleSpecsRepeat(t *testing.T) {
	d := embeddedDecoder(t)
	now := time.Date(2026, 11, 8, 12, 30, 45, 0, time.UTC)
	want := []string{
		"Lithium Ion Phosphate Traction Battery",
		"Lithium iron phosphate battery (LFP) / Standard Range Battery, Rear Primary Drive Unit",
	}
	req := contract.Request{VIN: "3FMTK1R40PR037734", Now: now, Shape: contract.ShapeNitrovin}
	dec, err := d.Decode(req)
	if err != nil {
		t.Fatalf("shape nitrovin: %v", err)
	}
	a := dec.Attributes.(contract.NitrovinAttributes)["Other Battery Info"]
	if a.ElementID != 1 || !slices.Equal(a.Values, want) {
		t.Errorf("shape nitrovin: Other Battery Info %+v, want element 1 values %q", a, want)
	}
	req.Shape = contract.ShapeVPIC
	if dec, err = d.Decode(req); err != nil {
		t.Fatalf("shape vpic: %v", err)
	}
	if got, w := dec.Attributes.(contract.VpicAttributes)["BatteryInfo"], want[0]+", "+want[1]; got != w {
		t.Errorf("shape vpic: BatteryInfo %q, want %q", got, w)
	}
}
