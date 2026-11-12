package main

import (
	"bytes"
	"encoding/json"
	"html"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/ui"
)

// versioned is the embedded decoder reporting another decoder version, as
// a release build would.
type versioned struct {
	loaded
	version string
}

func (v versioned) Info() contract.Info {
	info := v.loaded.Info()
	info.DecoderVersion = v.version
	return info
}

func (v versioned) LoadInfo() contract.Info {
	info := v.loaded.LoadInfo()
	info.DecoderVersion = v.version
	return info
}

// formServer serves the API on the embedded data, as serve does; a version
// other than "" is the one the decoder reports.
func formServer(t *testing.T, version string) (*httptest.Server, *nitrovin.Decoder) {
	t.Helper()
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	texts, err := errorTexts(embedded.Artifact())
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d)
	if err != nil {
		t.Fatal(err)
	}
	var svc decodeService = loaded{d, tr}
	if version != "" {
		svc = versioned{loaded{d, tr}, version}
	}
	a, err := newAPI(svc, log.New(io.Discard, "", 0), io.Discard, texts, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return srv, d
}

// get fetches url, the page, and checks what it always carries: its
// headers, no CORS, and no script but the release's htmx, copy.js, map.js
// and vocab.js, nothing inline.
func get(t *testing.T, url string, status int) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	if resp.StatusCode != status {
		t.Fatalf("GET %s: status %d; want %d\n%s", url, resp.StatusCode, status, body)
	}
	pageHeaders(t, resp.Header)
	for _, s := range inlineHandler.FindAllString(body, -1) {
		t.Errorf("inline handler %s", s)
	}
	if strings.Contains(body, "<style") || strings.Contains(body, " style=") {
		t.Errorf("inline style")
	}
	scripts := scriptTag.FindAllString(body, -1)
	if len(scripts) != 4 || slices.ContainsFunc(scripts, func(s string) bool { return !releaseScript.MatchString(s) }) ||
		!strings.Contains(scripts[0], "/htmx.min.js") || !strings.Contains(scripts[1], "/copy.js") || !strings.Contains(scripts[2], "/map.js") || !strings.Contains(scripts[3], "/vocab.js") {
		t.Errorf("scripts %q; want the release's htmx, copy.js, map.js and vocab.js alone", scripts)
	}
	return body
}

var (
	inlineHandler = regexp.MustCompile(`(?i)<[^>]*\son[a-z]+\s*=[^>]*>`)
	scriptTag     = regexp.MustCompile(`(?i)<script[^>]*>`)
	releaseScript = regexp.MustCompile(`^<script src="/ui/r/[^"]+/(htmx\.min|copy|map|vocab)\.js" defer>$`)
)

func pageHeaders(t *testing.T, h http.Header) {
	t.Helper()
	for k, want := range map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Content-Security-Policy": ui.CSP,
		"X-Content-Type-Options":  "nosniff",
		"Vary":                    "HX-Request",
	} {
		if got := h.Get(k); got != want {
			t.Errorf("%s %q; want %q", k, got, want)
		}
	}
	for k := range h {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Errorf("CORS header %s", k)
		}
	}
}

// textOf is the text of the <pre> with id in body, as a browser shows it.
func textOf(t *testing.T, body, id string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)<pre[^>]* id="` + regexp.QuoteMeta(id) + `"[^>]*>(.*?)</pre>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no <pre id=%q>\n%s", id, body)
	}
	return text(m[1])
}

var tag = regexp.MustCompile(`<[^>]*>`)

// text is markup's text: tags dropped, references decoded.
func text(markup string) string { return html.UnescapeString(tag.ReplaceAllString(markup, "")) }

// groups are the page's element groups in order: each summary's name, and
// whether it is open, and its markup.
func groups(body string) (names []string, open map[string]bool, markup map[string]string) {
	open, markup = map[string]bool{}, map[string]string{}
	for _, m := range regexp.MustCompile(`(?s)<details( open)?>\s*<summary>([^<]*?) <span.*?</details>`).FindAllStringSubmatch(body, -1) {
		name := html.UnescapeString(m[2])
		names = append(names, name)
		open[name], markup[name] = m[1] != "", m[0]
	}
	return names, open, markup
}

// TestForm drives the page against the embedded data: the controls at /,
// a VIN's decode in each shape, error codes, refusals beside their field,
// the caller's text escaped, the headers on every page, and old links to
// the decode form moved to the page. Then a browser runs it twice
// (browser_test.go), without JavaScript and with it.
func TestForm(t *testing.T) {
	srv, d := formServer(t, "")
	info := d.Info()

	for _, c := range []struct {
		target string
		status int
		// has are substrings of the body; hasNot must be absent.
		has, hasNot []string
	}{
		{"/", 200,
			[]string{
				`<form method="get" action="/" hx-get="/" hx-target="#ui-main" hx-swap="innerHTML show:window:top" hx-push-url="true" aria-labelledby="tab-vin">`,
				`value="vpic" checked`, "data " + info.DataMonth, "decoder " + info.DecoderVersion,
				`href="/?from.vin=1HGCM82633A004352"`, `href="/?from.vin=5YJ3E1EA2JF000316"`,
				`href="https://vpic.nhtsa.dot.gov/"`, `href="https://vpic.nhtsa.dot.gov/api/"`, `href="https://vpic.nhtsa.dot.gov/decoder/"`,
				// A (devel) build is no release: the releases index, docs on main.
				`href="https://github.com/joshuasprow/nitrovin/releases"`, `/blob/main/docs/parity.md"`,
				// The embedded data's own release, and its month's report.
				`href="https://github.com/joshuasprow/nitrovin-data/releases/tag/v1.` + strings.ReplaceAll(info.DataMonth, "_", "") + ".",
				`href="https://github.com/joshuasprow/nitrovin/pulls?q=is%3Apr&#43;%22Refresh%3A&#43;data&#43;month&#43;` + info.DataMonth + `%22"`,
				"TAG=vX.Y.Z\n", "This build, " + info.DecoderVersion + ", is not a release",
			},
			[]string{`id="result"`, `class="error"`, "/releases/tag/v0", "<mark>"}},
		{"/?from.vin=1HGCM82633A004352&model_year=&shape=vpic", 200,
			[]string{
				`<th scope="row">Make</th><td>HONDA</td>`, `<a href="/v1/decode/1HGCM82633A004352?shape=vpic"`, `<span class="chip chip-ok">Valid VIN</span>`,
				`<h2 id="result-h">2003 Honda Accord</h2>`,
				`<p class="summary">2003 HONDA Accord EX-V6 · 3.0 L, 6 cylinders · built in MARYSVILLE, OHIO</p>`,
				`value="1HGCM82633A004352"`, `<span class="name mono">1HGCM82633A004352</span><span class="rel"><span class="visually-hidden">, </span>what was translated, a link to this answer<span class="visually-hidden">;</span></span> <span class="meta">VIN, model year 2003</span>`,
				`href="https://vpic.nhtsa.dot.gov/decoder/VinDecoder?VIN=1HGCM82633A004352"`,
			},
			[]string{"<h3>Items", "Suggested VIN", `id="install"`}},
		{"/?from.vin=5YJ3E1EA2JF000316&model_year=2018&shape=nitrovin&full=true", 200,
			[]string{
				`<th scope="row">Manufacturer Name</th><td>TESLA, INC.</td>`,
				`<th scope="row">Note</th><td><ul><li>Curb Weight (lbs): 3549-4072</li></ul></td>`,
				`href="/v1/decode/5YJ3E1EA2JF000316?full=true&amp;model_year=2018&amp;shape=nitrovin"`,
				`href="https://vpic.nhtsa.dot.gov/decoder/VinDecoder?VIN=5YJ3E1EA2JF000316&amp;ModelYear=2018"`,
				`value="nitrovin" checked`, `value="true" checked`, "<h3>Items",
			},
			// The nitrovin shape has no empty values to hide.
			[]string{"empty values hidden", "empty values shown"}},
		// A wrong check digit is error code 1, spelled out: a decode, not a
		// refusal.
		{"/?from.vin=1HGCM82633A004353", 200,
			[]string{
				`codes-flagged">1</span>`, "check digit invalid", `<th scope="row">Make</th><td>HONDA</td>`,
				"<li>1 - Check Digit (9th position) does not calculate properly</li>",
			},
			[]string{`class="error"`}},
		// Code 14's positions are the decode's AdditionalErrorText.
		{"/?from.vin=JH2PC40066M200001", 200,
			[]string{
				"<li>0 - VIN decoded clean. Check Digit (9th position) is correct</li>",
				"<li>14 - Unable to provide information for some of the characters in the VIN, based on the manufacturer submission.</li>",
				"<p>Unused position(s): 5,6,7,8.</p>",
			},
			nil},
		// A suggested VIN, its one differing position marked on both.
		{"/?from.vin=1FBNF0AA21A111111", 200,
			[]string{
				"<li>3 - VIN corrected, error in one position (assuming Check Digit is correct)</li>",
				`<th scope="row">VIN</th><td class="mono">1FBNF0<mark>A</mark>A21A111111</td>`,
				`<th scope="row">Suggested VIN</th><td><span class="mono">1FBNF0<mark>1</mark>A21A111111</span> <span class="hint">differs at position 7</span>`,
			},
			nil},
		// '!' marks each position the procedure could not correct.
		{"/?from.vin=1GTW7NFH5PA077131", 200,
			[]string{`1GTW7NF<mark>H</mark>5P<mark>A</mark>077131`, `1GTW7NF<mark>!</mark>5P<mark>!</mark>077131`, "differs at positions 8, 11"},
			nil},
		{"/?from.vin=1HGCM82633A0043521&model_year=2003", 400,
			[]string{`aria-invalid="true" aria-describedby="vin-error"`, `<p class="error" id="vin-error">`, `value="2003"`},
			[]string{`id="result"`, `id="model_year-error"`}},
		{"/?from.vin=1HGCM82633A004352&model_year=2003.5", 400,
			[]string{`<p class="error" id="model_year-error">model_year must be a 32-bit integer</p>`},
			[]string{`id="result"`}},
		{"/?from.vin=1HGCM82633A004352&colour=red", 400,
			[]string{`<p class="error" role="alert">unknown query parameter &#34;colour&#34;</p>`},
			[]string{`id="result"`}},
		// The caller's text is escaped wherever the page echoes it,
		// non-ASCII kept; the decode's VIN is sanitized (non-ASCII → &).
		{"/?from.vin=%3Cb%3E%C3%89%22x%3C%2Fb%3E", 200,
			[]string{
				`value="&lt;b&gt;É&#34;x&lt;/b&gt;"`, `href="/v1/decode/%3Cb%3E%C3%89%22x%3C%2Fb%3E"`,
				`<td class="mono"><mark>&lt;</mark>B<mark>&gt;</mark><mark>&amp;</mark><mark>&#34;</mark>X<mark>&lt;</mark><mark>/</mark><mark>B</mark><mark>&gt;</mark></td>`,
				`nitrovin decode &#39;&lt;b&gt;É&#34;x&lt;/b&gt;&#39;</pre>`,
			},
			[]string{"<b>", "<B>"}},
	} {
		t.Run(c.target, func(t *testing.T) {
			body := get(t, srv.URL+c.target, c.status)
			for _, s := range c.has {
				if !strings.Contains(body, s) {
					t.Errorf("body lacks %s\n%s", s, body)
				}
			}
			for _, s := range c.hasNot {
				if strings.Contains(body, s) {
					t.Errorf("body has %s", s)
				}
			}
		})
	}

	// HEAD is the GET's headers without the body.
	resp, err := client.Head(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	pageHeaders(t, resp.Header)
	if resp.StatusCode != 200 || resp.ContentLength <= 0 {
		t.Errorf("HEAD /: %d, Content-Length %d; want 200 and the GET's length", resp.StatusCode, resp.ContentLength)
	}

	// Every other method is the pages' 405.
	resp, err = client.Post(srv.URL+"/", "application/x-www-form-urlencoded", strings.NewReader("vin=1HGCM82633A004352"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD" || resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("POST /: %d, Allow %q, %s; want 405, GET, HEAD, the error page", resp.StatusCode, resp.Header.Get("Allow"), resp.Header.Get("Content-Type"))
	}

	// An old link to the form, /?vin=… or /ui/decode?vin=…, moves to the
	// page, its query along and the VIN the page's from, and lands on the
	// same decode.
	const old = "/?vin=1HGCM82633A004352&shape=nitrovin"
	noFollow := &http.Client{Timeout: client.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, link := range []string{old, "/ui/decode?vin=1HGCM82633A004352&shape=nitrovin"} {
		resp, err = noFollow.Get(srv.URL + link)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != "/?from.vin=1HGCM82633A004352&shape=nitrovin" {
			t.Errorf("GET %s: %d, Location %q; want 301 to / with the query", link, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if body := get(t, srv.URL+old, 200); !strings.Contains(body, `<th scope="row">Make</th><td>HONDA</td>`) || !strings.Contains(body, `value="nitrovin" checked`) {
		t.Errorf("GET %s, redirected: not the decode\n%s", old, body)
	}

	t.Run("browser", func(t *testing.T) { browserForm(t, srv) })
}

// TestFormGroups: a decode's elements sit in vPIC's groups, in vPIC's group
// order with Other last, General and Engine open; the vpic shape hides its
// empty values until asked, and a group with nothing to show is left out.
func TestFormGroups(t *testing.T) {
	srv, d := formServer(t, "")
	const vin = "/?from.vin=1HGCM82633A004352"

	body := get(t, srv.URL+vin, 200)
	names, open, markup := groups(body)
	if want := []string{
		"General", "Exterior/Body", "Exterior/Dimension", "Exterior/Truck", "Exterior/Trailer", "Exterior/Motorcycle", "Exterior/Bus",
		"Mechanical/Transmission", "Engine", "Passive Safety System", "Passive Safety System/Air Bag Location",
		"Other",
	}; !slices.Equal(names, want) {
		t.Errorf("groups %q;\nwant %q", names, want)
	}
	for name, isOpen := range open {
		if isOpen != (name == "General" || name == "Engine") {
			t.Errorf("group %s open %v", name, isOpen)
		}
	}
	for group, row := range map[string]string{
		"General": `<th scope="row">Make</th><td>HONDA</td>`,
		"Engine":  `<th scope="row">DisplacementL</th><td>2.998832712</td>`,
		"Other":   `<th scope="row">ErrorCode</th><td>0</td>`,
	} {
		if !strings.Contains(markup[group], row) {
			t.Errorf("group %s lacks %s\n%s", group, row, markup[group])
		}
	}
	hidden := regexp.MustCompile(`(\d+) empty values hidden\. <a href="([^"]+)">Show them</a>`).FindStringSubmatch(body)
	if hidden == nil || hidden[2] != "/?from.vin=1HGCM82633A004352&amp;show_empty=true" {
		t.Fatalf("no link to show the empty values: %q\n%s", hidden, body)
	}
	if strings.Contains(body, `<th scope="row">BusLength</th>`) || slices.Contains(names, "Interior") {
		t.Errorf("empty BusLength, or Interior with no value, shown by default")
	}

	// Shown, every element has a row and Interior appears; the link hides
	// them again.
	body = get(t, srv.URL+html.UnescapeString(hidden[2]), 200)
	names, _, markup = groups(body)
	rows := strings.Count(body, `<tr><th scope="row">`)
	if shown := strings.Count(body, `<span class="null">empty</span>`); rows != len(d.Info().Elements) || strconv.Itoa(shown) != hidden[1] {
		t.Errorf("%d rows, %d of them empty; want one per element, %s empty", rows, shown, hidden[1])
	}
	if !strings.Contains(markup["Exterior/Bus"], `<th scope="row">BusLength</th><td><span class="null">empty</span></td>`) {
		t.Errorf("Exterior/Bus lacks the empty BusLength\n%s", markup["Exterior/Bus"])
	}
	if i := slices.Index(names, "Interior"); i < 0 || names[i-1] != "Exterior/Bus" {
		t.Errorf("Interior missing or out of place: %q", names)
	}
	if !strings.Contains(body, `Empty values shown. <a href="/?from.vin=1HGCM82633A004352">Hide them</a>`) || !strings.Contains(body, `name="show_empty" value="true" checked`) {
		t.Errorf("no link to hide the empty values, or the box unchecked\n%s", body)
	}

	// The nitrovin shape groups alike, by variable name.
	body = get(t, srv.URL+vin+"&shape=nitrovin", 200)
	_, _, markup = groups(body)
	if !strings.Contains(markup["General"], `<th scope="row">Make</th><td>HONDA</td>`) || !strings.Contains(markup["Engine"], `<th scope="row">Displacement (L)</th><td>2.998832712</td>`) {
		t.Errorf("nitrovin shape: Make not under General or Displacement (L) not under Engine\n%s", body)
	}
}

// TestFormCopies: the decode's copyable texts are the decode's JSON as
// /v1 answers, indented, this page's URL, and a curl and a nitrovin
// decode that make the same request.
func TestFormCopies(t *testing.T) {
	srv, _ := formServer(t, "")
	const (
		target = "/?from.vin=1HGCM82633A004352&model_year=2003&shape=nitrovin&full=true&now=2026-11-09T12:00:00Z"
		v1     = "/v1/decode/1HGCM82633A004352?full=true&model_year=2003&now=2026-11-09T12%3A00%3A00Z&shape=nitrovin"
		cmd    = "nitrovin decode --model-year 2003 --shape nitrovin --full --now 2026-11-09T12:00:00Z 1HGCM82633A004352"
	)
	body := get(t, srv.URL+target, 200)
	resp, err := client.Get(srv.URL + v1)
	if err != nil {
		t.Fatal(err)
	}
	api, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(textOf(t, body, "decode-json"))); err != nil {
		t.Fatal(err)
	}
	for id, got := range map[string]string{
		"decode-json": compact.String(),
		"copy-url":    textOf(t, body, "copy-url"),
		"copy-curl":   textOf(t, body, "copy-curl"),
		"copy-cli":    textOf(t, body, "copy-cli"),
	} {
		want := map[string]string{
			"decode-json": strings.TrimSuffix(string(api), "\n"),
			"copy-url":    srv.URL + target,
			"copy-curl":   "curl '" + srv.URL + v1 + "'",
			"copy-cli":    cmd,
		}[id]
		if got != want {
			t.Errorf("%s:\n%s\nwant\n%s", id, got, want)
		}
	}
	// The command line gives the same decode.
	if r := cli(t, nil, "", strings.Fields(cmd)[1:]...); r.exit != 0 || r.stdout != string(api) {
		t.Errorf("%s: exit %d\n%s%s\nwant\n%s", cmd, r.exit, r.stdout, r.stderr, api)
	}

	// Each is one shell word wherever the shell would split or expand it.
	for vin, want := range map[string][2]string{
		"1HGCM8263%2AA004352": {"curl " + srv.URL + "/v1/decode/1HGCM8263%2AA004352", "nitrovin decode '1HGCM8263*A004352'"},
		"-1HGCM82633A00435":   {"curl " + srv.URL + "/v1/decode/-1HGCM82633A00435", "nitrovin decode -- -1HGCM82633A00435"},
		"-":                   {"curl " + srv.URL + "/v1/decode/-", "echo - | nitrovin decode -"},
		"it%27s":              {"curl " + srv.URL + "/v1/decode/it%27s", `nitrovin decode 'it'\''s'`},
	} {
		body := get(t, srv.URL+"/?from.vin="+vin, 200)
		if got := [2]string{textOf(t, body, "copy-curl"), textOf(t, body, "copy-cli")}; got != want {
			t.Errorf("vin %s: %q; want %q", vin, got, want)
		}
	}
}

// TestFormREADME: the empty page's install commands, MCP configurations
// and verify commands are the README's, and a release build links to its
// own release.
func TestFormREADME(t *testing.T) {
	b, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	readme := string(b)
	const version = "v9.8.7"
	srv, _ := formServer(t, version)
	body := get(t, srv.URL+"/", 200)
	for _, s := range []string{
		`href="https://github.com/joshuasprow/nitrovin/releases/tag/` + version + `"`,
		`href="https://github.com/joshuasprow/nitrovin/blob/` + version + `/docs/parity.md"`,
	} {
		if !strings.Contains(body, s) {
			t.Errorf("body lacks %s", s)
		}
	}

	// Install: each channel's commands are the README's code spans.
	install := section(t, readme, "## Install")
	page := body[strings.Index(body, `id="install"`):strings.Index(body, `id="mcp"`)]
	var channels []string
	for _, row := range regexp.MustCompile(`(?s)<tr><th scope="row">([^<]+)</th><td>(.*?)</td>`).FindAllStringSubmatch(page, -1) {
		channel := html.UnescapeString(row[1])
		channels = append(channels, channel)
		var got []string
		for _, code := range regexp.MustCompile(`(?s)<(?:pre|code)[^>]*>(.*?)</(?:pre|code)>`).FindAllStringSubmatch(row[2], -1) {
			got = append(got, strings.Split(text(code[1]), "\n")...)
		}
		var want []string
		for line := range strings.SplitSeq(install, "\n") {
			if cells := strings.Split(line, " | "); len(cells) == 3 && cells[0] == "| "+channel {
				for _, span := range regexp.MustCompile("`([^`]+)`").FindAllStringSubmatch(cells[1], -1) {
					want = append(want, span[1])
				}
			}
		}
		if want == nil || !slices.Equal(got, want) {
			t.Errorf("%s: %q; README %q", channel, got, want)
		}
	}
	if want := []string{"Homebrew", "Scoop", "winget", "Docker", "Go 1.27+"}; !slices.Equal(channels, want) {
		t.Errorf("channels %q; want %q", channels, want)
	}

	// MCP: the README's snippet, and with Docker its command and args.
	mcp := section(t, readme, "### MCP")
	local := fence(t, mcp, "json")
	if got := textOf(t, body, "mcp-local"); got != local {
		t.Errorf("mcp-local:\n%s\nREADME:\n%s", got, local)
	}
	docker := regexp.MustCompile("With Docker instead, `([^`]+)` and\\s+`([^`]+)`").FindStringSubmatch(mcp)
	if docker == nil {
		t.Fatal("README: no Docker command and args")
	}
	lines := strings.Split(local, "\n")
	for i, line := range lines {
		indent := line[:len(line)-len(strings.TrimLeft(line, " "))]
		switch {
		case strings.HasPrefix(line, indent+`"command": `):
			lines[i] = indent + docker[1] + ","
		case strings.HasPrefix(line, indent+`"args": `):
			lines[i] = indent + docker[2]
		}
	}
	if got, want := textOf(t, body, "mcp-docker"), strings.Join(lines, "\n"); got != want {
		t.Errorf("mcp-docker:\n%s\nwant:\n%s", got, want)
	}

	// Verify: the README's commands with this release's tag.
	verify := fence(t, section(t, readme, "## Verify a release"), "sh")
	first, rest, _ := strings.Cut(verify, "\n")
	if !strings.HasPrefix(first, "TAG=") {
		t.Fatalf("README's verify commands start %q, not TAG=", first)
	}
	if got, want := textOf(t, body, "verify-commands"), "TAG="+version+"\n"+rest; got != want {
		t.Errorf("verify-commands:\n%s\nwant:\n%s", got, want)
	}

	// Neither a pseudo-version nor a modified build is a release.
	for _, v := range []string{"v0.1.5-0.20261109120000-0123456789ab", "v0.1.4+dirty"} {
		srv, _ := formServer(t, v)
		if body := get(t, srv.URL+"/", 200); strings.Contains(body, "/releases/tag/v0") || !strings.Contains(body, "TAG=vX.Y.Z\n") {
			t.Errorf("%s: links a release or tags the verify commands", v)
		}
	}
}

// section is md from heading's line to the next heading of its level or
// higher.
func section(t *testing.T, md, heading string) string {
	t.Helper()
	i := strings.Index(md, "\n"+heading+"\n")
	if i < 0 {
		t.Fatalf("README: no %q", heading)
	}
	s := md[i+1+len(heading):]
	level := strings.Repeat("#", strings.Index(heading, " "))
	for j := 0; j < len(s); {
		k := strings.Index(s[j:], "\n#")
		if k < 0 {
			break
		}
		j += k + 1
		if h := s[j:]; strings.HasPrefix(h, level+" ") || !strings.HasPrefix(h, level) {
			return s[:j]
		}
	}
	return s
}

// fence is the first fenced block of lang in md, without its fences.
func fence(t *testing.T, md, lang string) string {
	t.Helper()
	_, s, ok := strings.Cut(md, "```"+lang+"\n")
	if !ok {
		t.Fatalf("README: no %s block", lang)
	}
	s, _, ok = strings.Cut(s, "\n```")
	if !ok {
		t.Fatalf("README: unclosed %s block", lang)
	}
	return s
}

// costlyInfo is loaded whose Info, which measures model-qualifier drift on
// its first call (ADR 0015), must not be reached.
type costlyInfo struct{ loaded }

func (costlyInfo) Info() contract.Info { panic("Info read: model-qualifier drift measured") }

// TestPagesReadLoadInfo: requests that show no drift (the Service Worker,
// assets, an unknown page) read the info load measured, never Info, so no
// page but the one listing drift builds the model values index.
func TestPagesReadLoadInfo(t *testing.T) {
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := nitrovin.NewTranslator(d)
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAPI(costlyInfo{loaded{d, tr}}, log.New(io.Discard, "", 0), io.Discard, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	for _, path := range []string{"/sw.js", "/ui/no-such-page"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusInternalServerError {
			t.Errorf("%s: 500, Info was read", path)
		}
	}
}
