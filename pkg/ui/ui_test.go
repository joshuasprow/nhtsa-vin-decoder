package ui

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// The stub route's fragment, parsed into the package's templates before
// any test executes them.
var _ = template.Must(tpl.New("stub").Parse(`<p id="stub">{{.}}</p>`))

// backend is a Backend answering a fixed Info, every decode with the error
// codes alone, or err, testSpine as its spine bundle and "<name> bytes" as
// each loaded file's but broken's, which it fails to read. It counts
// SpineBundle's calls in spines, when not nil.
type backend struct {
	info   contract.Info
	err    error
	spines *atomic.Int32
}

func (b backend) Info() contract.Info { return b.info }

func (b backend) Decode(req contract.Request) (contract.Decode, error) {
	if b.err != nil {
		return contract.Decode{}, b.err
	}
	return contract.Decode{VIN: req.VIN, ErrorCodes: []int{0}, Attributes: contract.VpicAttributes{}}, nil
}

func (backend) ErrorTexts() map[int]string { return nil }

// Translate and List answer from no files: the catalog is vPIC alone, a
// VIN reaches nothing on it, and nothing else translates.
func (backend) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	if req.From.VIN != "" {
		return contract.Translation{From: contract.TranslationFrom{VIN: req.From.VIN, Status: contract.FromUnknown}, Spine: []contract.SpineEntry{}, To: []contract.Translated{}}, nil
	}
	return contract.Translation{}, contract.NewProblem(contract.InvalidRequest, "/from", "nothing to translate in the stub")
}

func (backend) List(contract.ListRequest) (contract.List, error) {
	return contract.List{Entries: []contract.ListEntry{contract.CatalogEntry{Name: "vpic", Kind: "spine", Version: "2026_10", Subject: "vehicle"}}}, nil
}

// Fit answers from no catalog, as contract.Fitter does without one.
func (backend) Fit(ctx context.Context, req contract.FitRequest) (contract.Fit, error) {
	return contract.Fitter{}.Fit(ctx, req)
}

func (b backend) SpineBundle() ([]byte, error) {
	if b.spines != nil {
		b.spines.Add(1)
	}
	return []byte(testSpine), nil
}

func (b backend) File(name string) ([]byte, error) {
	if name == "broken" {
		return nil, errors.New("/srv/broken.jsonl changed since nitrovin loaded it")
	}
	return []byte(name + " bytes"), nil
}

const testSpine = `{"format":1,"month":"2026_10"}`

// testInfo's release has two files loaded, yard and broken.
var testInfo = contract.Info{DecoderVersion: "v0.1.9-0.20261110120000-0123456789ab+dirty", DataMonth: "2026_10", ArtifactHash: "blake3:00ff",
	Vocabularies: []contract.VocabularyFile{{Name: "yard", Blake3: "ab12"}, {Name: "broken", Blake3: "cd34"}}}

// testAssets is where testInfo's release keeps its assets.
const testAssets = "/ui/r/v0.1.9-0.20261110120000-0123456789ab+dirty/2026_10/blake3:00ff"

// testAssetsAttr is testAssets in an attribute, as html/template escapes
// it and a browser reads it back.
var testAssetsAttr = strings.ReplaceAll(testAssets, "+", "&#43;")

// stubHandler is a Handler on testInfo with the stub route at /ui/stub:
// "hello" as its fragment, or err.
func stubHandler(t *testing.T, err error) *Handler {
	t.Helper()
	h := New(backend{info: testInfo}, log.New(io.Discard, "", 0))
	h.routes["/ui/stub"] = func(*http.Request) (view, error) {
		return view{title: "Stub", template: "stub", data: "hello"}, err
	}
	return h
}

// response is one request to h: the status, headers and body.
func response(t *testing.T, h http.Handler, method, target string, header http.Header) (int, http.Header, string) {
	t.Helper()
	r := httptest.NewRequest(method, target, nil)
	for k, vs := range header {
		r.Header[k] = vs
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Result().Header, w.Body.String()
}

var htmx = http.Header{"Hx-Request": {"true"}}

// checkHTML checks what every page and fragment carries: HTML, the CSP,
// Vary: HX-Request and testInfo's release.
func checkHTML(t *testing.T, name string, header http.Header) {
	t.Helper()
	checkHTMLOf(t, name, header, Release(testInfo))
}

// checkHTMLOf is checkHTML of a page answered by release, "" when the
// release is not known.
func checkHTMLOf(t *testing.T, name string, header http.Header, release string) {
	t.Helper()
	for k, want := range map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Content-Security-Policy": CSP,
		"Vary":                    "HX-Request",
		"X-Content-Type-Options":  "nosniff",
		ReleaseHeader:             release,
	} {
		if got := header.Get(k); got != want {
			t.Errorf("%s: %s %q; want %q", name, k, got, want)
		}
	}
	if !strings.Contains(header.Get("Content-Security-Policy"), "script-src 'self';") {
		t.Errorf("%s: CSP %q lacks script-src 'self'", name, header.Get("Content-Security-Policy"))
	}
}

// checkPage checks body is a whole page in the layout, titled title,
// linking the release's htmx, copy.js, map.js and stylesheet, its header
// naming testInfo's version, which is no tagged release.
func checkPage(t *testing.T, name, body, title string) {
	t.Helper()
	for _, want := range []string{
		"<!doctype html>\n",
		"<title>" + title + " · nitrovin</title>",
		`<meta name="htmx-config" content="{&#34;allowEval&#34;:false,`,
		`<link rel="stylesheet" href="` + testAssetsAttr + `/ui.css">`,
		`<script src="` + testAssetsAttr + `/htmx.min.js" defer></script>`,
		`<script src="` + testAssetsAttr + `/copy.js" defer></script>`,
		`<script src="` + testAssetsAttr + `/map.js" defer></script>`,
		`<div class="title"><a class="brand" href="/">nitrovin</a> <small class="version">v0.1.9-0.20261110120000-0123456789ab&#43;dirty</small></div>`,
		`<div id="ui-error" role="alert"></div>`,
		"</html>\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s: page lacks %q:\n%s", name, want, body)
		}
	}
	if strings.Contains(body, "<script>") || strings.Contains(body, "style=") || strings.Contains(body, "<style") {
		t.Errorf("%s: inline script or style, which CSP refuses:\n%s", name, body)
	}
}

// TestStubRoute walks a route as a page and as a fragment: without
// HX-Request, or with it while htmx restores history, the fragment in the
// layout; with it, the fragment alone, clearing the error slot.
func TestStubRoute(t *testing.T) {
	h := stubHandler(t, nil)

	for name, header := range map[string]http.Header{
		"page":            nil,
		"history restore": {"Hx-Request": {"true"}, "Hx-History-Restore-Request": {"true"}},
	} {
		status, hdr, body := response(t, h, "GET", "/ui/stub", header)
		if status != 200 {
			t.Errorf("%s: %d; want 200", name, status)
		}
		checkHTML(t, name, hdr)
		checkPage(t, name, body, "Stub")
		if !strings.Contains(body, "<main id=\"ui-main\" hx-history-elt hx-history=\"false\">\n<p id=\"stub\">hello</p>\n</main>") {
			t.Errorf("%s: the fragment is not the page's main:\n%s", name, body)
		}
	}

	status, hdr, body := response(t, h, "GET", "/ui/stub", htmx)
	if status != 200 {
		t.Errorf("fragment: %d; want 200", status)
	}
	checkHTML(t, "fragment", hdr)
	if want := `<title>Stub · nitrovin</title><p id="stub">hello</p><div id="ui-error" role="alert" hx-swap-oob="true"></div>`; body != want {
		t.Errorf("fragment:\n%s\nwant\n%s", body, want)
	}
	for _, k := range []string{"HX-Retarget", "HX-Reswap"} {
		if hdr.Get(k) != "" {
			t.Errorf("fragment: %s %q, though the route names no target", k, hdr.Get(k))
		}
	}

	if status, _, _ := response(t, h, "HEAD", "/ui/stub", nil); status != 200 {
		t.Errorf("HEAD: %d; want 200", status)
	}
	status, hdr, body = response(t, h, "POST", "/ui/stub", nil)
	if status != 405 || hdr.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d, Allow %q; want 405, GET, HEAD", status, hdr.Get("Allow"))
	}
	checkHTML(t, "POST", hdr)
	checkPage(t, "POST", body, "Method Not Allowed")
}

// TestErrors: every error has its real status, as the one error page for
// a page request and as the error fragment, for the error slot, for htmx.
func TestErrors(t *testing.T) {
	refused := stubHandler(t, contract.NewProblem(contract.InvalidRequest, "/from/vocabulary", `no vocabulary "yard" is loaded`))
	faulty := stubHandler(t, io.ErrUnexpectedEOF)
	for _, c := range []struct {
		name    string
		h       *Handler
		target  string
		status  int
		title   string
		message string
	}{
		{"unknown path", stubHandler(t, nil), "/ui/nothing", 404, "Not Found", "There is no page at /ui/nothing."},
		{"/ui itself", stubHandler(t, nil), "/ui/", 404, "Not Found", "There is no page at /ui/."},
		{"escaped path", stubHandler(t, nil), "/ui/%3Cscript%3E", 404, "Not Found", "There is no page at /ui/&lt;script&gt;."},
		{"refusal", refused, "/ui/stub", 400, "Bad Request", "no vocabulary &#34;yard&#34; is loaded"},
		{"fault", faulty, "/ui/stub", 500, "Internal Server Error", "nitrovin failed: unexpected EOF"},
	} {
		status, hdr, body := response(t, c.h, "GET", c.target, nil)
		if status != c.status {
			t.Errorf("%s: page %d; want %d", c.name, status, c.status)
		}
		checkHTML(t, c.name, hdr)
		checkPage(t, c.name, body, c.title)
		if want := "<h1>" + c.title + "</h1>\n<p>" + c.message + "</p>"; !strings.Contains(body, want) {
			t.Errorf("%s: page lacks %q:\n%s", c.name, want, body)
		}

		status, hdr, body = response(t, c.h, "GET", c.target, htmx)
		if status != c.status {
			t.Errorf("%s: fragment %d; want %d", c.name, status, c.status)
		}
		checkHTML(t, c.name+" fragment", hdr)
		if want := "<p><strong>" + c.title + ".</strong> " + c.message + "</p>"; body != want {
			t.Errorf("%s: fragment\n%s\nwant\n%s", c.name, body, want)
		}
	}
}

// panicky is a Backend whose Info panics.
type panicky struct{ backend }

func (panicky) Info() contract.Info { panic("injected fault") }

// TestPanic: a panic is the 500 error page, which links no assets and
// names no release or version when the release is what failed.
func TestPanic(t *testing.T) {
	h := New(panicky{}, log.New(io.Discard, "", 0))
	status, hdr, body := response(t, h, "GET", "/ui/nothing", nil)
	if status != 500 {
		t.Errorf("%d; want 500", status)
	}
	checkHTMLOf(t, "panic", hdr, "")
	if !strings.Contains(body, "<h1>Internal Server Error</h1>\n<p>nitrovin failed: injected fault</p>") || strings.Contains(body, "/ui/r/") ||
		!strings.Contains(body, `<div class="title"><a class="brand" href="/">nitrovin</a></div>`) {
		t.Errorf("page:\n%s", body)
	}
}

// TestVersion: every whole page's header shows the running release's
// version beside the brand, linked to its release on GitHub when it is a
// tagged release, and every response names the release (ReleaseHeader),
// assets and /sw.js included.
func TestVersion(t *testing.T) {
	for version, want := range map[string]string{
		"v0.3.4":                `<small class="version"><a href="https://github.com/joshuasprow/nitrovin/releases/tag/v0.3.4">v0.3.4</a></small>`,
		"v0.3.5-rc.1":           `<small class="version"><a href="https://github.com/joshuasprow/nitrovin/releases/tag/v0.3.5-rc.1">v0.3.5-rc.1</a></small>`,
		"(devel)":               `<small class="version">(devel)</small>`,
		testInfo.DecoderVersion: `<small class="version">v0.1.9-0.20261110120000-0123456789ab&#43;dirty</small>`,
	} {
		info := testInfo
		info.DecoderVersion = version
		h := New(backend{info: info}, log.New(io.Discard, "", 0)).WithWorker(Worker{Module: []byte("\x00asm"), Exec: []byte("// go")})
		for _, target := range []string{"/", "/ui/nothing"} {
			_, hdr, body := response(t, h, "GET", target, nil)
			if !strings.Contains(body, `<a class="brand" href="/">nitrovin</a> `+want+`</div>`) {
				t.Errorf("%s %s: the header lacks %s\n%s", version, target, want, body)
			}
			if got := hdr.Get(ReleaseHeader); got != Release(info) {
				t.Errorf("%s %s: %s %q; want %q", version, target, ReleaseHeader, got, Release(info))
			}
		}
		for _, target := range []string{"/sw.js", assetsPath(Release(info)) + "/ui.css", "/ui/r/v0.0.1/2026_10/blake3:00ff/ui.css"} {
			if _, hdr, _ := response(t, h, "HEAD", target, nil); hdr.Get(ReleaseHeader) != Release(info) {
				t.Errorf("%s HEAD %s: %s %q; want %q", version, target, ReleaseHeader, hdr.Get(ReleaseHeader), Release(info))
			}
		}
	}
}

// TestAssets: the release's assets are immutable at its URL; another
// release's, or a file it lacks, is the error page.
func TestAssets(t *testing.T) {
	h := stubHandler(t, nil)
	for name, contentType := range map[string]string{
		"copy.js":     "text/javascript; charset=utf-8",
		"htmx.min.js": "text/javascript; charset=utf-8",
		"map.js":      "text/javascript; charset=utf-8",
		"ui.css":      "text/css; charset=utf-8",
	} {
		want, err := files.ReadFile("static/" + name)
		if err != nil {
			t.Fatal(err)
		}
		status, hdr, body := response(t, h, "GET", testAssets+"/"+name, nil)
		if status != 200 || body != string(want) {
			t.Errorf("%s: %d, %d bytes; want 200, %d bytes", name, status, len(body), len(want))
		}
		for k, v := range map[string]string{
			"Content-Type":            contentType,
			"Cache-Control":           "public, max-age=31536000, immutable",
			"Content-Security-Policy": CSP,
			"Vary":                    "",
		} {
			if got := hdr.Get(k); got != v {
				t.Errorf("%s: %s %q; want %q", name, k, got, v)
			}
		}
	}
	for _, target := range []string{
		"/ui/r/v0.1.8/2026_10/blake3:00ff/htmx.min.js",
		testAssets + "/nothing.js",
		testAssets + "/",
		testAssets,
		"/ui/r/",
	} {
		status, hdr, body := response(t, h, "GET", target, nil)
		if status != 404 {
			t.Errorf("%s: %d; want 404", target, status)
		}
		checkHTML(t, target, hdr)
		checkPage(t, target, body, "Not Found")
		if hdr.Get("Cache-Control") != "" {
			t.Errorf("%s: Cache-Control %q", target, hdr.Get("Cache-Control"))
		}
	}
	if status, hdr, _ := response(t, h, "POST", testAssets+"/ui.css", nil); status != 405 || hdr.Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d, Allow %q; want 405, GET, HEAD", status, hdr.Get("Allow"))
	}
}

// TestPublished: the spine bundle and each loaded file are immutable at
// their release's URLs, a file's naming its blake3; the bundle is asked
// for once; another release's, another blake3 and a file not loaded are
// the error page saying so, and a file that fails to read the 500 page.
func TestPublished(t *testing.T) {
	var spines atomic.Int32
	h := New(backend{info: testInfo, spines: &spines}, log.New(io.Discard, "", 0))
	yard := FilePath(testInfo, testInfo.Vocabularies[0])
	if SpinePath(testInfo) != testAssets+"/spine.json" || yard != testAssets+"/vocab/ab12/yard.jsonl" {
		t.Errorf("SpinePath %s, FilePath %s", SpinePath(testInfo), yard)
	}
	for _, c := range []struct{ target, contentType, body string }{
		{SpinePath(testInfo), "application/json", testSpine},
		{SpinePath(testInfo), "application/json", testSpine},
		{yard, "application/jsonl", "yard bytes"},
	} {
		status, hdr, body := response(t, h, "GET", c.target, nil)
		if status != 200 || body != c.body {
			t.Errorf("%s: %d, %q; want 200, %q", c.target, status, body, c.body)
		}
		for k, v := range map[string]string{
			"Content-Type":            c.contentType,
			"Cache-Control":           "public, max-age=31536000, immutable",
			"Content-Security-Policy": CSP,
			"X-Content-Type-Options":  "nosniff",
		} {
			if got := hdr.Get(k); got != v {
				t.Errorf("%s: %s %q; want %q", c.target, k, got, v)
			}
		}
	}
	if n := spines.Load(); n != 1 {
		t.Errorf("the spine bundle was asked for %d times; want once", n)
	}

	for _, c := range []struct {
		target string
		status int
		want   string
	}{
		{"/ui/r/v0.1.8/2026_10/blake3:00ff/spine.json", 404, "This release of nitrovin (" + strings.ReplaceAll(Release(testInfo), "+", "&#43;") + ") has no file /ui/r/v0.1.8/2026_10/blake3:00ff/spine.json."},
		{"/ui/r/v0.1.8/2026_10/blake3:00ff/vocab/ab12/yard.jsonl", 404, "has no file /ui/r/v0.1.8/2026_10/blake3:00ff/vocab/ab12/yard.jsonl."},
		{testAssets + "/vocab/ef56/yard.jsonl", 404, "This nitrovin&#39;s yard is another file now (blake3 ab12): reload the page."},
		{testAssets + "/vocab/ab12/classics.jsonl", 404, "This nitrovin has no file named classics loaded now: reload the page."},
		{testAssets + "/vocab/ab12/yard.json", 404, "has no file " + testAssetsAttr + "/vocab/ab12/yard.json."},
		{testAssets + "/vocab/ab12", 404, "has no file " + testAssetsAttr + "/vocab/ab12."},
		{testAssets + "/vocab/", 404, "has no file " + testAssetsAttr + "/vocab/."},
		{FilePath(testInfo, testInfo.Vocabularies[1]), 500, "nitrovin failed: /srv/broken.jsonl changed since nitrovin loaded it"},
	} {
		status, hdr, body := response(t, h, "GET", c.target, nil)
		if status != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s: %d\n%s\nwant %d with %q", c.target, status, body, c.status, c.want)
		}
		checkHTML(t, c.target, hdr)
		if hdr.Get("Cache-Control") != "" {
			t.Errorf("%s: Cache-Control %q", c.target, hdr.Get("Cache-Control"))
		}
	}
	for _, target := range []string{SpinePath(testInfo), yard} {
		if status, hdr, _ := response(t, h, "POST", target, nil); status != 405 || hdr.Get("Allow") != "GET, HEAD" {
			t.Errorf("POST %s: %d, Allow %q; want 405, GET, HEAD", target, status, hdr.Get("Allow"))
		}
	}
}

// htmxSHA256 is static/htmx.min.js's sha256: dist/htmx.min.js of npm's
// htmx.org at htmxVersion, whose tarball npm lists with integrity
// sha512-Thx/WtpeOQqSrqBCw/A1cwGJGg4UrVa3+sW0GmrM3p4gJgO89ecH4qtbnyzDDWFvBTqjnIMCgELTNt636dtamA==.
const htmxSHA256 = "d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717"

// TestHTMXPin holds the vendored htmx to its pin: the file is the one
// release's, unedited, and says it is htmxVersion.
func TestHTMXPin(t *testing.T) {
	b, err := files.ReadFile("static/htmx.min.js")
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != htmxSHA256 {
		t.Errorf("static/htmx.min.js has sha256 %x; the pin is %s", sum, htmxSHA256)
	}
	if !strings.Contains(string(b), `version:"`+htmxVersion+`"`) {
		t.Errorf("static/htmx.min.js does not say it is version %s", htmxVersion)
	}
}

// TestRelease is the Worker's cache key.
func TestRelease(t *testing.T) {
	if got, want := Release(testInfo), "v0.1.9-0.20261110120000-0123456789ab+dirty/2026_10/blake3:00ff"; got != want {
		t.Errorf("Release %q; want %q", got, want)
	}
}
