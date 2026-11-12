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
// a translation the answer below them, as a page and as a fragment for the
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
		`<header>` + "\n" + `<div class="title"><a class="brand" href="/">nitrovin</a> <small class="version">v0.1.9-0.20261110120000-0123456789ab&#43;dirty</small></div>` + "\n" +
			`<nav class="site" aria-label="Pages">` + "\n" + `<ul>` + "\n" +
			`<li><a href="/" aria-current="page">Translate</a></li>` + "\n" +
			`<li><a href="/ui/fit">Fit</a></li>` + "\n" +
			`<li><button type="button" class="open-vocabularies" popovertarget="vocabularies">Vocabularies</button></li>` + "\n" +
			`<li><a href="https://github.com/joshuasprow/nitrovin/blob/main/docs/api/README.md">API docs</a></li>` + "\n" +
			`</ul>` + "\n" + `</nav>` + "\n" + `</header>`,
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
		!strings.Contains(body, `id="from-model"`) || strings.Contains(body, `id="from-vin"`) || strings.Contains(body, `class="open-vocabularies"`) {
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

	// A VIN's answer: its decode, its translation's map, the From card a
	// link to this answer, and the JSON of both in the dialog.
	status, hdr, body = response(t, h, "GET", "/?from.vin=1HGCM82633A004352", htmx)
	if status != 200 || !strings.HasPrefix(body, "<title>VIN 1HGCM82633A004352 · nitrovin</title><h1>") || !strings.HasSuffix(body, `<div id="ui-error" role="alert" hx-swap-oob="true"></div>`) {
		t.Errorf("a VIN's fragment: %d\n%s", status, body)
	}
	for _, want := range []string{
		`<h2 id="result-h">VIN 1HGCM82633A004352</h2>`,
		`<span class="chip chip-ok">Valid VIN</span>`,
		`<a id="map-n0" class="node" href="/?from.vin=1HGCM82633A004352" data-lit="map-n0"><span class="box"><span class="name mono">1HGCM82633A004352</span><span class="rel"><span class="visually-hidden">, </span>what was translated, a link to this answer<span class="visually-hidden">;</span></span> <span class="meta">VIN · not found</span></span></a>`,
		`<dialog id="result-json" popover aria-labelledby="result-json-h">`,
		`id="translation-json" data-copy="the translation&#39;s JSON">{` + "\n" + `  &#34;from&#34;: {`,
		`<a href="/v1/decode/1HGCM82633A004352" hx-boost="false">The same request on /v1</a>`,
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

// formBackend is backend with a vehicle vocabulary, yard, an extension
// set, classics, and a declared spine, parts, with its vocabulary,
// listing. It refuses every translation with refusal, else answers it
// with nothing reached.
type formBackend struct {
	backend
	refusal *contract.Problem
}

func (b formBackend) List(req contract.ListRequest) (contract.List, error) {
	if req.Of != "" {
		return contract.List{Entries: []contract.ListEntry{}}, nil
	}
	return contract.List{Entries: []contract.ListEntry{
		contract.CatalogEntry{Name: "vpic", Kind: "spine", Subject: "vehicle"},
		contract.CatalogEntry{Name: "yard", Kind: "vocabulary", Subject: "vehicle", Fields: []string{"make", "model"}, Spine: "vpic"},
		contract.CatalogEntry{Name: "classics", Kind: "extension-set", Subject: "vehicle", Spine: "vpic"},
		contract.CatalogEntry{Name: "parts", Kind: "spine", Subject: "part"},
		contract.CatalogEntry{Name: "listing", Kind: "vocabulary", Subject: "part", Fields: []string{"title"}, Spine: "parts"},
	}}, nil
}

func (b formBackend) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	if b.refusal != nil {
		return contract.Translation{}, b.refusal
	}
	from := contract.TranslationFrom{VIN: req.From.VIN, Vocabulary: req.From.Vocabulary, Entry: req.From.Entry, SpineRef: req.From.SpineRef, Status: contract.FromUnknown}
	return contract.Translation{From: from, Spine: []contract.SpineEntry{}, To: []contract.Translated{}}, nil
}

// TestPageFields: on every tab a refusal is said beside the field that
// holds the value its pointer names, the field marked invalid and
// described by it, and above the form when no field of the form holds
// it; and a from no form asks for, an extension or a declared spine's
// entry, is shown in its tab's form. The page never says "lookup".
func TestPageFields(t *testing.T) {
	const detail = "refused here"
	for _, c := range []struct {
		target, pointer, tab string
		// field is the id of the field the refusal is beside, "" for
		// a refusal above the form.
		field string
	}{
		{"/?from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram", "/from/entry/model", "entry", "entry-model"},
		{"/?from.vocabulary=yard&from.entry.make=Dodge&model_year=2010", "/model_year", "entry", "entry-model-year"},
		{"/?from.vocabulary=yard&from.entry.make=Dodge&vin=1HGCM82633A004352", "/vin", "entry", "context-vin"},
		{"/?from.vocabulary=yard&from.entry.make=Dodge", "/from/vocabulary", "entry", "from-entry-vocabulary"},
		{"/?from.vocabulary=yard&from.entry.make=Dodge", "/from/entry/colour", "entry", ""},
		{"/?from.vocabulary=yard&from.entry.make=Dodge", "/from/entry", "entry", ""},
		{"/?from.vocabulary=listing&from.entry.title=Rotor", "/from/entry/title", "entry-parts", "entry-title"},
		{"/?from.vocabulary=listing&from.entry.title=Rotor&model_year=2010", "/model_year", "entry-parts", ""},
		{"/?from.spine=parts&from.id=brake-rotor", "/from/id", "entry-parts", "from-entry-parts-id"},
		{"/?from.make=474", "/from/make", "id", "from-make"},
		{"/?from.model=1861", "/from/model", "id", "from-model"},
		{"/?from.model=1861&model_year=2003", "/model_year", "id", "vpic-model-year"},
		{"/?from.ext=ext:classics/packard", "/from/ext", "id", "from-ext"},
		{"/?from.model=1861", "/from/ext", "id", ""},
		{"/?from.model=1861", "/from", "id", ""},
	} {
		h := New(formBackend{backend: backend{info: testInfo}, refusal: contract.NewProblem(contract.InvalidRequest, c.pointer, detail)}, log.New(io.Discard, "", 0))
		status, _, body := response(t, h, "GET", c.target, nil)
		name := c.target + " refused at " + c.pointer
		if status != 400 || !strings.Contains(body, `<a id="tab-`+c.tab+`" href="/?tab=`+c.tab+`" aria-current="page">`) {
			t.Errorf("%s: %d, not on the %s tab\n%s", name, status, c.tab, body)
			continue
		}
		above := strings.Contains(body, `<p class="error" role="alert">`+detail+`</p>`)
		if c.field == "" {
			if !above || strings.Contains(body, `aria-invalid`) {
				t.Errorf("%s: not above the form alone\n%s", name, body)
			}
			continue
		}
		if above || !strings.Contains(body, `id="`+c.field+`"`) ||
			!strings.Contains(body, ` aria-invalid="true" aria-describedby="`+c.field+`-error"`) ||
			!strings.Contains(body, `<p class="error" id="`+c.field+`-error">`+detail+`</p>`) {
			t.Errorf("%s: not beside #%s\n%s", name, c.field, body)
		}
	}

	// A from no form asks for is in its tab's form: an extension in a
	// field of its own, which only such a translation shows, and a
	// declared spine's entry read-only, never sent with the form.
	h := New(formBackend{backend: backend{info: testInfo}}, log.New(io.Discard, "", 0))
	for target, want := range map[string][]string{
		"/?from.ext=ext:classics/packard": {
			`<a id="tab-id" href="/?tab=id" aria-current="page">`,
			`<input type="text" id="from-ext" name="from.ext" value="ext:classics/packard" autocomplete="off" spellcheck="false">`,
			`id="result"`,
		},
		"/?from.spine=parts&from.id=brake-rotor": {
			`<a id="tab-entry-parts" href="/?tab=entry-parts" aria-current="page">`,
			`<input type="text" id="from-entry-parts-id" value="brake-rotor" readonly autocomplete="off" spellcheck="false">`,
			`id="result"`,
		},
	} {
		status, _, body := response(t, h, "GET", target, nil)
		if status != 200 {
			t.Errorf("%s: %d\n%s", target, status, body)
		}
		for _, w := range want {
			if !strings.Contains(body, w) {
				t.Errorf("%s lacks %s\n%s", target, w, body)
			}
		}
	}
	for _, target := range []string{"/?tab=id", "/?from.model=1861", "/?tab=entry-parts", "/?from.vocabulary=listing&from.entry.title=Rotor"} {
		if _, _, body := response(t, h, "GET", target, nil); strings.Contains(body, `id="from-ext"`) || strings.Contains(body, `-id" value=`) {
			t.Errorf("%s: a field for a from it does not ask\n%s", target, body)
		}
	}

	for _, target := range []string{"/", "/?tab=id", "/?tab=entry", "/?tab=entry-parts", "/?from.vin=1HGCM82633A004352"} {
		_, _, body := response(t, h, "GET", target, nil)
		if text := strings.ToLower(body); strings.Contains(text, "lookup") || strings.Contains(text, "look up") {
			t.Errorf("%s says lookup\n%s", target, body)
		}
	}
}

// TestBrowserDecode: the browser's page, which has no decoder, decodes a
// VIN once, asking the server's GET /v1/decode/{vin} for every item with
// the decode's query alone (the names of the browser's files stay in the
// browser), lays it out by the elements the server gave it, and
// translates the same decode. No route under /ui answers JSON.
func TestBrowserDecode(t *testing.T) {
	const vin = "1D7RV1GT5AS123456"
	srv := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	if status, _, _ := response(t, srv, "GET", "/ui/decoded?vin="+vin, nil); status != 404 {
		t.Errorf("/ui/decoded on the server: %d; want 404", status)
	}

	s, d := server(t, "classics"), newDisk()
	general := contract.Value("General")
	s.info.Elements = contract.Elements{
		{ElementID: 26, Variable: "Make", Code: "Make", GroupName: general, DataType: "lookup"},
		{ElementID: 28, Variable: "Model", Code: "Model", GroupName: general, DataType: "lookup"},
		{ElementID: 29, Variable: "Model Year", Code: "ModelYear", GroupName: general, DataType: "int"},
		{ElementID: 34, Variable: "Series", Code: "Series", GroupName: general, DataType: "string"},
		{ElementID: 143, Variable: "Error Code", Code: "ErrorCode", DataType: "lookup"},
	}
	s.decodes[vin] = `{"vin":"` + vin + `","wmi":"1D7","check_digit_valid":true,"now":"2026-11-10T00:00:00Z","data_month":"2026_10","decoder_version":"v9","items":[` +
		`{"element_id":26,"value":"DODGE","attribute_id":"476"},{"element_id":28,"value":"Ram","attribute_id":"1938"},{"element_id":29,"value":"2010","attribute_id":"2010"},` +
		`{"element_id":34,"value":"DS (1500)","attribute_id":"DS (1500)"},{"element_id":143,"value":"0","attribute_id":"0"}]}`
	l := open(t, s, d)
	if _, err := l.Load(pick(t, "yard"), quiet); err != nil {
		t.Fatal(err)
	}
	s.gets = nil
	h := NewInBrowser(l, BrowserConfig{ErrorTexts: map[int]string{0: "0 - VIN decoded clean"}}, log.New(io.Discard, "", 0))
	status, _, body := response(t, h, "GET", "/?from.vin="+vin+"&to=yard&show_empty=true", nil)
	for _, want := range []string{
		`<h2 id="result-h">Dodge Ram</h2>`,
		`<section id="result" aria-labelledby="result-h" data-from="vin" data-spine="vpic">`,
		`<span class="chip chip-ok">Valid VIN</span>`,
		`<span class="name">Dodge Ram 1500</span>`,
		`&#34;Make&#34;: &#34;DODGE&#34;`,
		`&#34;now&#34;: &#34;2026-11-10T00:00:00Z&#34;`,
		`Empty values shown. <a href="/?from.vin=` + vin + `&amp;to=yard">Hide them</a>`,
		`<tr data-where="browser"><th scope="row"><a href="/ui/vocabularies/yard">yard</a></th>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the browser's page lacks %s\n%s", want, body)
		}
	}
	if status != 200 {
		t.Errorf("the browser's page: %d\n%s", status, body)
	}
	if want := []string{"/v1/decode/" + vin + "?full=true"}; !slices.Equal(s.gets, want) {
		t.Errorf("the browser asked the server %q; want %q: the VIN decoded once", s.gets, want)
	}

	// A refusal is the decode's own, beside the field it names, before
	// the server is asked.
	s.gets = nil
	status, _, body = response(t, h, "GET", "/?from.vin="+vin+"&model_year=x&to=yard", nil)
	if status != 400 || !strings.Contains(body, `<p class="error" id="model_year-error">model_year must be a 32-bit integer</p>`) || len(s.gets) != 0 {
		t.Errorf("a refusal in the browser's page: %d, asked %q\n%s", status, s.gets, body)
	}
}
