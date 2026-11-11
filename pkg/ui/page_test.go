package ui

import (
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// TestPage walks the page on a stub backend (the decodes themselves are
// cmd/nitrovin's TestForm): / is the controls, a tab's link its form, and
// a lookup the answer below them, as a page and as a fragment for the
// page's main, where htmx is sent with a refusal too, while a fault is
// the error page or the error slot's fragment. The vocabularies' button
// and dialog are the whole page's, never a fragment's.
func TestPage(t *testing.T) {
	h := New(backend{info: testInfo}, log.New(io.Discard, "", 0))

	status, hdr, body := response(t, h, "GET", "/", nil)
	if status != 200 {
		t.Errorf("GET /: %d; want 200", status)
	}
	checkHTML(t, "GET /", hdr)
	checkPage(t, "GET /", body, "Decode and translate")
	for _, want := range []string{
		`<a id="tab-vin" href="/" aria-current="page">VIN</a>`,
		`<a id="tab-id" href="/?tab=id">vPIC id</a>`,
		`<form method="get" action="/" hx-get="/" hx-target="#ui-main" hx-swap="innerHTML show:window:top" hx-push-url="true" aria-labelledby="tab-vin">`,
		`<a class="example" href="/?from.vin=1HGCM82633A004352"`,
		`<h2 id="install">Install</h2>`,
		`<button type="button" class="fab" popovertarget="vocabularies">Vocabularies</button>`,
		`<dialog id="vocabularies" popover aria-labelledby="browser-h">`,
		testAssetsAttr + `/vocab.js" defer></script>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET /: lacks %s\n%s", want, body)
		}
	}
	// No vocabulary is loaded: no tab asks for one's entry.
	if strings.Contains(body, `id="tab-entry`) {
		t.Errorf("GET /: an entry tab without a vocabulary:\n%s", body)
	}

	// A tab's link is the page with its form, and the fragment is the
	// main's alone.
	status, _, body = response(t, h, "GET", "/?tab=id", htmx)
	if status != 200 || !strings.Contains(body, `<a id="tab-id" href="/?tab=id" aria-current="page">vPIC id</a>`) ||
		!strings.Contains(body, `id="from-model"`) || strings.Contains(body, `id="from-vin"`) || strings.Contains(body, `class="fab"`) {
		t.Errorf("the vPIC id tab's fragment: %d\n%s", status, body)
	}

	// Old links move to the page, their query along: the decode form's
	// VIN is the page's from.
	for _, method := range []string{"GET", "HEAD"} {
		for old, want := range map[string]string{
			"/?vin=1HGCM82633A004352":                                "/?from.vin=1HGCM82633A004352",
			"/?vin=1HGCM82633A004352&shape=nitrovin&show_empty=true": "/?from.vin=1HGCM82633A004352&shape=nitrovin&show_empty=true",
			"/ui/decode?vin=1HGCM82633A004352&model_year=2003":       "/?from.vin=1HGCM82633A004352&model_year=2003",
			"/ui/decode": "/",
			"/ui/translate?from.model=1861&model_year=2003&to=yard":    "/?from.model=1861&model_year=2003&to=yard",
			"/ui/translate?from.vocabulary=yard&vin=1HGCM82633A004352": "/?from.vocabulary=yard&vin=1HGCM82633A004352",
			"/vocab":           "/",
			"/ui/vocabularies": "/",
		} {
			status, hdr, _ := response(t, h, method, old, nil)
			if status != http.StatusMovedPermanently || hdr.Get("Location") != want {
				t.Errorf("%s %s: %d, Location %q; want 301 to %s", method, old, status, hdr.Get("Location"), want)
			}
		}
	}

	// A VIN's answer: its decode, its translation's map, and the JSON of
	// both in the dialog.
	status, hdr, body = response(t, h, "GET", "/?from.vin=1HGCM82633A004352", htmx)
	if status != 200 || !strings.HasPrefix(body, "<title>VIN 1HGCM82633A004352 · nitrovin</title><h1>") || !strings.HasSuffix(body, `<div id="ui-error" role="alert" hx-swap-oob="true"></div>`) {
		t.Errorf("a VIN's fragment: %d\n%s", status, body)
	}
	for _, want := range []string{
		`<h2 id="result-h">VIN 1HGCM82633A004352</h2>`,
		`<span class="chip chip-ok">Valid VIN</span>`,
		`<span class="name mono">1HGCM82633A004352</span> <span class="meta">VIN · not found</span>`,
		`<dialog id="result-json" popover aria-labelledby="result-json-h">`,
		`id="translation-json" data-copy="the translation&#39;s JSON">{` + "\n" + `  &#34;from&#34;: {`,
		`<a href="/v1/decode/1HGCM82633A004352">The same request on /v1</a>`,
		`id="decode-json" data-copy="the decode&#39;s JSON">{` + "\n" + `  &#34;vin&#34;: &#34;1HGCM82633A004352&#34;,`,
		`id="copy-url" data-copy="this page's URL">http://example.com/?from.vin=1HGCM82633A004352</pre>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("a VIN's fragment lacks %s\n%s", want, body)
		}
	}
	checkHTML(t, "a VIN's fragment", hdr)

	// A refusal is the page with the refusal beside its field, or above
	// the form when it names none, for the page's main, not the error
	// slot.
	for _, c := range []struct {
		target, want, tab string
	}{
		{"/?from.vin=1HGCM82633A004352&model_year=2003.5", `<p class="error" id="model_year-error">model_year must be a 32-bit integer</p>`, "vin"},
		{"/?from.vin=", `<p class="error" id="vin-error">`, "vin"},
		{"/?from.model=1861", `<p class="error" role="alert">nothing to translate in the stub</p>`, "id"},
		{"/?%zz", `<p class="error" role="alert">the query string is malformed: `, "vin"},
	} {
		status, hdr, body := response(t, h, "GET", c.target, htmx)
		if status != 400 || hdr.Get("HX-Retarget") != "#ui-main" || hdr.Get("HX-Reswap") != "innerHTML" || !strings.Contains(body, c.want) ||
			!strings.Contains(body, `id="tab-`+c.tab+`" href="`) || !strings.Contains(body, `aria-current="page"`) ||
			!strings.HasSuffix(body, `hx-swap-oob="true"></div>`) || strings.Contains(body, `id="result"`) {
			t.Errorf("refusal fragment %s: %d, HX-Retarget %q, HX-Reswap %q\n%s", c.target, status, hdr.Get("HX-Retarget"), hdr.Get("HX-Reswap"), body)
		}
	}
	status, _, body = response(t, h, "GET", "/?from.vin=1HGCM82633A004352&model_year=2003.5", nil)
	if status != 400 || !strings.Contains(body, `aria-invalid="true" aria-describedby="model_year-error"`) || !strings.Contains(body, `<details class="options" open>`) {
		t.Errorf("refusal page: %d\n%s", status, body)
	}

	// A fault in the decoder is nitrovin's: the error page, or the error
	// slot's fragment.
	faulty := New(backend{info: testInfo, err: contract.NewProblem(contract.Internal, "", "injected fault")}, log.New(io.Discard, "", 0))
	status, hdr, body = response(t, faulty, "GET", "/?from.vin=1HGCM82633A004352", htmx)
	if status != 500 || hdr.Get("HX-Retarget") != "" || !strings.HasPrefix(body, "<p><strong>Internal Server Error.</strong> nitrovin failed: ") || !strings.Contains(body, "injected fault") {
		t.Errorf("fault fragment: %d, HX-Retarget %q\n%s", status, hdr.Get("HX-Retarget"), body)
	}
	status, _, body = response(t, faulty, "GET", "/?from.vin=1HGCM82633A004352", nil)
	if status != 500 || !strings.Contains(body, "<h1>Internal Server Error</h1>") {
		t.Errorf("fault page: %d\n%s", status, body)
	}
}

// TestDecoded: the server answers a VIN's decode as the page shows it, as
// JSON, and a refusal as a problem; and the browser's page, which has no
// decoder, shows the server's, asking it with the decode's query alone:
// the names of the browser's files stay in the browser.
func TestDecoded(t *testing.T) {
	const vin = "1D7RV1GT5AS123456"
	srv := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	status, hdr, body := response(t, srv, "GET", decodedPath+"?vin="+vin+"&full=true", nil)
	if status != 200 || hdr.Get("Content-Type") != "application/json" || !strings.HasPrefix(body, `{"VIN":"`+vin+`","ErrorCodes":[0],`) {
		t.Errorf("%s: %d %s\n%s", decodedPath, status, hdr.Get("Content-Type"), body)
	}
	status, hdr, body = response(t, srv, "GET", decodedPath+"?vin="+vin+"&model_year=x", nil)
	if status != 400 || hdr.Get("Content-Type") != "application/problem+json" || !strings.Contains(body, `"pointer":"/model_year"`) {
		t.Errorf("%s refused: %d %s\n%s", decodedPath, status, hdr.Get("Content-Type"), body)
	}

	s, d := server(t, "classics"), newDisk()
	s.pages = srv
	s.decodes[vin] = `{"vin":"` + vin + `","wmi":"1D7","check_digit_valid":true,"data_month":"2026_10","decoder_version":"v9","items":[` +
		`{"element_id":26,"value":"DODGE","attribute_id":"476"},{"element_id":28,"value":"Ram","attribute_id":"1938"},{"element_id":29,"value":"2010","attribute_id":"2010"},{"element_id":34,"value":"DS (1500)","attribute_id":"DS (1500)"}]}`
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	s.gets = nil
	h := NewInBrowser(l, nil, log.New(io.Discard, "", 0))
	status, _, body = response(t, h, "GET", "/?from.vin="+vin+"&to=yard&show_empty=true", nil)
	for _, want := range []string{
		`<h2 id="result-h">Dodge Ram</h2>`,
		`<span class="chip chip-ok">Valid VIN</span>`,
		`<span class="name">Dodge Ram 1500</span>`,
		`id="decode-json"`,
		`Empty values shown. <a href="/?from.vin=` + vin + `&amp;to=yard">Hide them</a>`,
		`<tr data-where="browser"><th scope="row"><a href="/ui/vocabularies/yard">yard</a></th>`,
		`<section id="browser" data-worker="/sw.js">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the browser's page lacks %s\n%s", want, body)
		}
	}
	if status != 200 {
		t.Errorf("the browser's page: %d\n%s", status, body)
	}
	if want := decodedPath + "?show_empty=true&vin=" + vin; !slices.Contains(s.gets, want) || slices.ContainsFunc(s.gets, func(p string) bool { return strings.Contains(p, "yard") }) {
		t.Errorf("the browser asked the server %q; want %s among them, and none naming yard", s.gets, want)
	}

	// The server's refusal is the page's, beside the field it names.
	status, _, body = response(t, h, "GET", "/?from.vin="+vin+"&model_year=x&to=yard", nil)
	if status != 400 || !strings.Contains(body, `<p class="error" id="model_year-error">model_year must be a 32-bit integer</p>`) {
		t.Errorf("the server's refusal in the browser's page: %d\n%s", status, body)
	}
	// The decode is the server's alone: the browser's handler has none.
	if status, _, _ := response(t, h, "GET", decodedPath+"?vin="+vin, nil); status != 404 {
		t.Errorf("%s in the browser: %d; want 404", decodedPath, status)
	}
}
