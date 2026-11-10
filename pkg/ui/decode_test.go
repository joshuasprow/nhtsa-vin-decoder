package ui

import (
	"io"
	"log"
	"net/http"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestDecodeRoutes walks the decode form's routes on a stub backend (the
// decodes themselves are cmd/nitrovin's TestForm): / is the entry page and
// redirects an old link to the form; /ui/decode is the form, as a page and
// as a fragment for the page's main, where htmx is sent with a refusal too,
// while a fault is the error page or the error slot's fragment.
func TestDecodeRoutes(t *testing.T) {
	h := New(backend{info: testInfo}, log.New(io.Discard, "", 0))

	status, hdr, body := response(t, h, "GET", "/", nil)
	if status != 200 {
		t.Errorf("GET /: %d; want 200", status)
	}
	checkHTML(t, "GET /", hdr)
	checkPage(t, "GET /", body, "Decode a VIN")
	for _, want := range []string{
		`<form method="get" action="/ui/decode" hx-get="/ui/decode" hx-target="#ui-main" hx-push-url="true">`,
		`<p hx-boost="true" hx-target="#ui-main">Try one: <a class="example" href="/ui/decode?vin=1HGCM82633A004352">`,
		`<h2 id="install">Install</h2>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /: lacks %s\n%s", want, body)
		}
	}

	// An old link to the form, /?vin=…, is the form's now: its query moves
	// along, whatever it holds.
	for _, method := range []string{"GET", "HEAD"} {
		for _, query := range []string{"vin=1HGCM82633A004352", "vin=1HGCM82633A004352&shape=nitrovin&show_empty=true", "colour=red", "%zz"} {
			status, hdr, _ := response(t, h, method, "/?"+query, nil)
			if want := "/ui/decode?" + query; status != http.StatusMovedPermanently || hdr.Get("Location") != want {
				t.Errorf("%s /?%s: %d, Location %q; want 301 to %s", method, query, status, hdr.Get("Location"), want)
			}
		}
	}

	status, hdr, body = response(t, h, "GET", "/ui/decode", nil)
	if status != 200 || !strings.Contains(body, `<input type="text" id="vin" name="vin" value=""`) || strings.Contains(body, `id="install"`) {
		t.Errorf("GET /ui/decode: %d, no empty form, or the entry page's install:\n%s", status, body)
	}
	checkPage(t, "GET /ui/decode", body, "Decode a VIN")

	status, hdr, body = response(t, h, "GET", "/ui/decode?vin=1HGCM82633A004352", htmx)
	if status != 200 || !strings.HasPrefix(body, "<title>1HGCM82633A004352 · nitrovin</title><h1>") ||
		!strings.Contains(body, "<h2>Result</h2>") || !strings.HasSuffix(body, `<div id="ui-error" role="alert" hx-swap-oob="true"></div>`) {
		t.Errorf("decode fragment: %d\n%s", status, body)
	}
	checkHTML(t, "decode fragment", hdr)

	// A refusal is the form with the refusal beside its field, for the
	// page's main, not the error slot.
	status, hdr, body = response(t, h, "GET", "/ui/decode?vin=1HGCM82633A004352&model_year=2003.5", htmx)
	if status != 400 || hdr.Get("HX-Retarget") != "#ui-main" || hdr.Get("HX-Reswap") != "innerHTML" ||
		!strings.Contains(body, `<p class="error" id="model_year-error">model_year must be a 32-bit integer</p>`) ||
		!strings.HasSuffix(body, `hx-swap-oob="true"></div>`) || strings.Contains(body, "<h2>Result") {
		t.Errorf("refusal fragment: %d, HX-Retarget %q, HX-Reswap %q\n%s", status, hdr.Get("HX-Retarget"), hdr.Get("HX-Reswap"), body)
	}
	status, _, body = response(t, h, "GET", "/ui/decode?vin=1HGCM82633A004352&model_year=2003.5", nil)
	if status != 400 || !strings.Contains(body, `aria-invalid="true" aria-describedby="model_year-error"`) {
		t.Errorf("refusal page: %d\n%s", status, body)
	}

	// A fault in the decoder is nitrovin's: the error page, or the error
	// slot's fragment.
	faulty := New(backend{info: testInfo, err: contract.NewProblem(contract.Internal, "", "injected fault")}, log.New(io.Discard, "", 0))
	status, hdr, body = response(t, faulty, "GET", "/ui/decode?vin=1HGCM82633A004352", htmx)
	if status != 500 || hdr.Get("HX-Retarget") != "" || !strings.HasPrefix(body, "<p><strong>Internal Server Error.</strong> nitrovin failed: ") || !strings.Contains(body, "injected fault") {
		t.Errorf("fault fragment: %d, HX-Retarget %q\n%s", status, hdr.Get("HX-Retarget"), body)
	}
	status, _, body = response(t, faulty, "GET", "/ui/decode?vin=1HGCM82633A004352", nil)
	if status != 500 || !strings.Contains(body, "<h1>Internal Server Error</h1>") {
		t.Errorf("fault page: %d\n%s", status, body)
	}
}
