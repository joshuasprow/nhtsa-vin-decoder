package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/ui"
)

// demoDir is the demo set (pkg/ui/demo, #278), which `nitrovin serve
// --vocab` serves as the browser loads it.
var demoDir = abs("../../pkg/ui/demo")

// demoVINs are the demo set's VINs, in its yard and its list of requests,
// and the vPIC model, year and drive type each must decode to on the
// embedded data month: the stories the list shows rest on them (a
// four-wheel and a two-wheel drive F-150, a CR-V whose VIN gives no drive
// type, a Ram of make Ram).
var demoVINs = map[string]struct {
	model int
	year  string
	drive string
}{
	"1FTEW1EG7FFA12345": {1801, "2015", "4WD/4-Wheel Drive/4x4"},
	"1FTEW1CG3FFA12345": {1801, "2015", "4x2"},
	"1GCVKREC6EZ123456": {1850, "2014", "4WD/4-Wheel Drive/4x4"},
	"1C6RR7LT6FS512345": {13620, "2015", "4WD/4-Wheel Drive/4x4"},
	"19XFC2F52GE012345": {1863, "2016", ""},
	"2HKRW2H53KH612345": {1865, "2019", ""},
	"4T1B11HK7JU512345": {2469, "2018", "4x2"},
	"2T3W1RFV8KW012345": {2217, "2019", "4x2"},
	"2T3BFREV7HW512345": {2217, "2017", "4WD/4-Wheel Drive/4x4"},
}

// TestDemoCheck holds the demo set to the data month go.mod pins, as CI
// runs it, so a month that drops one of its vPIC ids or values fails here
// rather than shipping a broken demo: `nitrovin vocab check` finds no
// fault and no drift in it (it has no deliberate drift), it compiles, and
// each of its VINs still decodes to the vehicle its stories need.
func TestDemoCheck(t *testing.T) {
	fixtureData(t)
	if r := cli(t, nil, "", "vocab", "check", demoDir); r.exit != 0 || r.stdout != "" || r.stderr != "" {
		t.Errorf("vocab check on the demo set: exit %d, stdout %q, stderr %q; want exit 0 and nothing", r.exit, r.stdout, r.stderr)
	}
	// Its catalog compiles beside the files it names (stock never goes
	// into a compile).
	compile := []string{"vocab", "compile", "-o", filepath.Join(t.TempDir(), "demo.compiled")}
	for _, n := range []string{"demo-catalog", "demo-classics", "demo-part-codes", "demo-part-types", "demo-vehicles"} {
		compile = append(compile, filepath.Join(demoDir, n+".jsonl"))
	}
	if r := cli(t, nil, "", compile...); r.exit != 0 || r.stdout != "" {
		t.Errorf("vocab compile of the demo's catalog: exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
	for vin, want := range demoVINs {
		r := cli(t, nil, "", "decode", "--shape", "nitrovin", vin)
		var dec struct {
			Attributes map[string]struct {
				Value       *string `json:"value"`
				AttributeID *int    `json:"attribute_id"`
			} `json:"attributes"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &dec); err != nil {
			t.Fatalf("decode %s: %v (%s)", vin, err, r.stderr)
		}
		str := func(name string) string {
			if v := dec.Attributes[name].Value; v != nil {
				return *v
			}
			return ""
		}
		model := 0
		if id := dec.Attributes["Model"].AttributeID; id != nil {
			model = *id
		}
		if model != want.model || str("Model Year") != want.year || str("Drive Type") != want.drive || str("Error Code") != "0" {
			t.Errorf("%s decodes to model %d, %s, drive %q, error code %s; the demo needs model %d, %s, drive %q",
				vin, model, str("Model Year"), str("Drive Type"), str("Error Code"), want.model, want.year, want.drive)
		}
	}
}

// TestDemoHints: a server with the worker's module tells a visitor to load
// the demo set only where it can load: on a server with no catalog and no
// file of a demo name, a fit with no catalog, a link naming a demo file and
// the dialog without JavaScript say how; beside a server's catalog, or a
// file named like a demo file, which make the panel refuse the demo, none
// of them does.
func TestDemoHints(t *testing.T) {
	fixtureData(t)
	module := ui.Worker{Module: []byte("\x00asm"), Exec: []byte("// wasm_exec.js")}
	const (
		load   = "open Vocabularies and Load the demo"
		noJS   = "The demo set loads into this browser in one click, so it needs JavaScript too."
		noFit  = "/ui/fit?from.vin=1FTEW1EG7FFA12345"
		byName = "/ui/fit?from.vocabulary=demo-vehicles&from.entry.make=Ford&from.entry.model=F150&model_year=2015"
	)
	for _, c := range []struct {
		name  string
		paths []string
		hints bool
	}{
		{"nothing loaded", nil, true},
		{"a catalog on the server", []string{fitmentFixture}, false},
		{"a file of a demo name on the server", []string{filepath.Join(demoDir, "demo-part-types.jsonl")}, false},
	} {
		h := vocabServer(t, &module, c.paths...)
		page := func(target string, status int) string {
			t.Helper()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("GET", target, nil))
			if w.Code != status {
				t.Fatalf("%s, %s: HTTP %d; want %d\n%s", c.name, target, w.Code, status, w.Body)
			}
			return text(w.Body.String())
		}
		says := map[string]bool{
			"the dialog":           strings.Contains(page("/", http.StatusOK), noJS),
			"a link naming a file": strings.Contains(page(byName, http.StatusNotFound), load),
		}
		if !slices.Contains(c.paths, fitmentFixture) {
			says["a fit with no catalog"] = strings.Contains(page(noFit, http.StatusOK), load)
		}
		for where, said := range says {
			if said != c.hints {
				t.Errorf("%s: %s says how to load the demo: %v; want %v", c.name, where, said, c.hints)
			}
		}
	}
}
