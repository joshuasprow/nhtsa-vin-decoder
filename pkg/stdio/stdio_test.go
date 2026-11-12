package stdio

import (
	"bytes"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestReplyRecovers: a panic answering a line (here, no Translator at all)
// is that line's #internal error reply, its id echoed, and the session
// goes on to the next line.
func TestReplyRecovers(t *testing.T) {
	in := `{"id":1,"op":"fit","from":"1D7RV1GT5AS123456"}` + "\n" + `{"id":2,"vin":"1HGCM82633A004352"}` + "\n"
	var out bytes.Buffer
	if err := Serve(nil, strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("%d replies: %q; want 2", len(lines), out.String())
	}
	for i, l := range lines {
		want := `{"id":` + string(rune('1'+i)) + `,"error":{"type":"` + string(contract.Internal) + `","title":"Internal error","status":500,"detail":"panic: `
		if !strings.HasPrefix(l, want) {
			t.Errorf("reply %d: %s; want it to start %s", i+1, l, want)
		}
	}
}
