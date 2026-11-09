package main

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

// TestForm drives the decode form at / against the embedded data: the empty
// form, a decode in each shape, error codes, refusals beside their field,
// the caller's text escaped, and the headers on every page.
func TestForm(t *testing.T) {
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(&api{d: d, log: log.New(io.Discard, "", 0)})
	t.Cleanup(srv.Close)

	for _, c := range []struct {
		method, target string
		status         int
		// has are substrings of the body; hasNot must be absent.
		has, hasNot []string
	}{
		{"GET", "/", 200,
			[]string{`<form method="get" action="/">`, `value="vpic" checked`, "data " + d.Info().DataMonth, "decoder " + d.Info().DecoderVersion, `/docs/parity.md"`},
			[]string{"<h2>Result", `class="error"`}},
		{"GET", "/?vin=1HGCM82633A004352&model_year=&shape=vpic", 200,
			[]string{`<th scope="row">Make</th><td>HONDA</td>`, `<a href="/v1/decode/1HGCM82633A004352?shape=vpic"`, `codes-clean">0</span>`, `value="1HGCM82633A004352"`},
			[]string{"<h2>Items"}},
		{"GET", "/?vin=5YJ3E1EA2JF000316&model_year=2018&shape=nitrovin&full=true", 200,
			[]string{
				`<th scope="row">Manufacturer Name</th><td>TESLA, INC.</td>`,
				`<th scope="row">Note</th><td><ul><li>Curb Weight (lbs): 3549-4072</li></ul></td>`,
				`href="/v1/decode/5YJ3E1EA2JF000316?full=true&amp;model_year=2018&amp;shape=nitrovin"`,
				`value="nitrovin" checked`, `value="true" checked`, "<h2>Items",
			},
			nil},
		// A wrong check digit is error code 1: a decode, not a refusal.
		{"GET", "/?vin=1HGCM82633A004353", 200,
			[]string{`codes-flagged">1</span>`, "check digit invalid", `<th scope="row">Make</th><td>HONDA</td>`},
			[]string{`class="error"`}},
		{"GET", "/?vin=1HGCM82633A0043521&model_year=2003", 400,
			[]string{`aria-invalid="true" aria-describedby="vin-error"`, `<p class="error" id="vin-error">`, `value="2003"`},
			[]string{"<h2>Result", `id="model_year-error"`}},
		{"GET", "/?vin=1HGCM82633A004352&model_year=2003.5", 400,
			[]string{`<p class="error" id="model_year-error">model_year must be a 32-bit integer</p>`},
			[]string{"<h2>Result"}},
		{"GET", "/?vin=1HGCM82633A004352&colour=red", 400,
			[]string{`<p class="error" role="alert">unknown query parameter &#34;colour&#34;</p>`},
			[]string{"<h2>Result"}},
		// The caller's text is escaped wherever the page echoes it,
		// non-ASCII kept; the decode's VIN is sanitized (non-ASCII → &).
		{"GET", "/?vin=%3Cb%3E%C3%89%22x%3C%2Fb%3E", 200,
			[]string{`value="&lt;b&gt;É&#34;x&lt;/b&gt;"`, `<td class="mono">&lt;B&gt;&amp;&#34;X&lt;/B&gt;</td>`, `href="/v1/decode/%3Cb%3E%C3%89%22x%3C%2Fb%3E"`},
			[]string{"<b>", "<B>"}},
		{"HEAD", "/", 200, nil, nil},
	} {
		t.Run(c.method+" "+c.target, func(t *testing.T) {
			req, err := http.NewRequest(c.method, srv.URL+c.target, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			body := string(b)
			if resp.StatusCode != c.status {
				t.Fatalf("status %d; want %d\n%s", resp.StatusCode, c.status, body)
			}
			for k, want := range map[string]string{
				"Content-Type":            "text/html; charset=utf-8",
				"Content-Security-Policy": "default-src 'self'; style-src 'unsafe-inline'",
				"X-Content-Type-Options":  "nosniff",
			} {
				if got := resp.Header.Get(k); got != want {
					t.Errorf("%s %q; want %q", k, got, want)
				}
			}
			for k := range resp.Header {
				if strings.HasPrefix(k, "Access-Control-") {
					t.Errorf("CORS header %s", k)
				}
			}
			if c.method == "HEAD" && (body != "" || resp.ContentLength <= 0) {
				t.Errorf("HEAD: body %q, Content-Length %d; want none and the GET's length", body, resp.ContentLength)
			}
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

	// Every other method is the API's 405.
	resp, err := client.Post(srv.URL+"/", "application/x-www-form-urlencoded", strings.NewReader("vin=1HGCM82633A004352"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 405 || resp.Header.Get("Allow") != "GET, HEAD" || resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("POST /: %d, Allow %q, %s; want 405, GET, HEAD, a problem", resp.StatusCode, resp.Header.Get("Allow"), resp.Header.Get("Content-Type"))
	}
}
