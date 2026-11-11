package main

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"
)

// The decode form in a browser (ADR 0013): headless Chrome or Chromium
// driven over the DevTools protocol, each run in a browser of its own with
// a fresh profile. An installed Service Worker survives JavaScript being
// turned off, so a profile that ran the pages with JavaScript would answer
// the JavaScript-off run from the worker.

// axeVersion is testdata/axe.min.js: axe.min.js of npm's axe-core at this
// version, byte for byte, held to axeSHA256 (TestAxePin). scripts/tools.sh
// outdated reports a newer release. To bump, replace the file from the
// package, then this version and the hash.
const axeVersion = "4.14.0"

// axeSHA256 is testdata/axe.min.js's sha256; npm lists the tarball's
// integrity as
// sha512-9WTZxEjsZ7b13TH8JPmbV2z8CHbl80/2hm3XPEG4JgNdQLK81IBRXmSxHfMAOkSqQeRxT/0dwNDz2GOm3zzpcQ==.
const axeSHA256 = "20c09fe157a8a34a30e241aaa1fcdade657734f08ab379ecfbeb7d45cc46e878"

//go:embed testdata/axe.min.js
var axeJS string

// TestAxePin holds the vendored axe-core to its pin.
func TestAxePin(t *testing.T) {
	sum := sha256.Sum256([]byte(axeJS))
	if got := hex.EncodeToString(sum[:]); got != axeSHA256 {
		t.Errorf("testdata/axe.min.js: sha256 %s; want %s, axe-core %s's", got, axeSHA256, axeVersion)
	}
	if !strings.HasPrefix(axeJS, "/*! axe v"+axeVersion+"\n") {
		t.Errorf("testdata/axe.min.js does not say it is axe v%s", axeVersion)
	}
}

// chromePath is the browser the runs use: $NITROVIN_CHROME, else a Chrome
// or Chromium on PATH or in /Applications; "" when there is none.
func chromePath() string {
	if p := os.Getenv("NITROVIN_CHROME"); p != "" {
		return p
	}
	for _, name := range []string{
		"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "/Applications/Chromium.app/Contents/MacOS/Chromium",
	} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

// pageRequest is a request for one of the pages, as the server got it.
type pageRequest struct {
	target string // path and query
	// htmx sent it (HX-Request), to restore history (HX-History-Restore-Request).
	htmx, restore bool
}

// requestLog serves h and records the requests for pages, assets aside.
type requestLog struct {
	h    http.Handler
	mu   sync.Mutex
	seen []pageRequest
}

// ServeHTTP records the requests for pages, / and under /ui/, assets
// aside: not the browser's own, such as /favicon.ico.
func (l *requestLog) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if p := r.URL.Path; p == "/" || strings.HasPrefix(p, "/ui/") && !strings.HasPrefix(p, "/ui/r/") {
		l.mu.Lock()
		l.seen = append(l.seen, pageRequest{r.URL.RequestURI(), r.Header.Get("HX-Request") == "true", r.Header.Get("HX-History-Restore-Request") == "true"})
		l.mu.Unlock()
	}
	l.h.ServeHTTP(w, r)
}

// take is the requests since the last take.
func (l *requestLog) take() []pageRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	seen := l.seen
	l.seen = nil
	return seen
}

// tab is one browser's one tab, at base, the form's server, with
// JavaScript or without.
type tab struct {
	t    *testing.T
	ctx  context.Context
	base string
	log  *requestLog
	js   bool
	mu   sync.Mutex
	// thrown are the exceptions thrown in the tab.
	thrown []string
}

// newTab starts a browser with a fresh profile, JavaScript off or on. With
// JavaScript on, every page records what CSP refused, and may use the
// clipboard. The back/forward cache is off, besides chromedp's defaults:
// a page it restores is no new document to the DevTools protocol, so
// queries wait on the one that left. Back and forward without JavaScript
// load the page again; with it, htmx restores the page, which the cache
// never sees.
func newTab(t *testing.T, path, base string, log *requestLog, js bool) *tab {
	t.Helper()
	opts := append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(path), chromedp.NoSandbox,
		chromedp.Flag("disable-features", "Translate,BlinkGenPropertyTrees,BackForwardCache"))
	actx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, cancel := chromedp.NewContext(actx)
	t.Cleanup(func() { cancel(); cancelAlloc() })
	tb := &tab{t: t, ctx: ctx, base: base, log: log, js: js}
	if err := chromedp.Listen(ctx, func(ev any) {
		if e, ok := ev.(*runtime.EventExceptionThrown); ok {
			tb.mu.Lock()
			tb.thrown = append(tb.thrown, e.ExceptionDetails.Text+" "+e.ExceptionDetails.URL)
			tb.mu.Unlock()
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !js {
		tb.call(func(ctx context.Context) error {
			_, err := chromedp.Call(ctx, emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: true})
			return err
		})
	} else {
		tb.call(func(ctx context.Context) error {
			_, err := chromedp.Call(ctx, page.AddScriptToEvaluateOnNewDocument, page.AddScriptToEvaluateOnNewDocumentParams{
				Source: `window.refused = []; document.addEventListener("securitypolicyviolation", e => refused.push(e.violatedDirective + " " + e.blockedURI));`,
			})
			if err != nil {
				return err
			}
			for _, name := range []string{"clipboard-read", "clipboard-write"} {
				_, err = chromedp.CallBrowser(ctx, browser.SetPermission, browser.SetPermissionParams{
					Permission: &browser.PermissionDescriptor{Name: name}, Setting: browser.PermissionSettingGranted, Origin: base,
				})
				if err != nil {
					return err
				}
			}
			return nil
		})
	}
	return tb
}

// call runs f with a minute to finish, failing the test on its error.
func (tb *tab) call(f func(ctx context.Context) error) {
	tb.t.Helper()
	ctx, cancel := context.WithTimeout(tb.ctx, time.Minute)
	defer cancel()
	if err := f(ctx); err != nil {
		tb.t.Fatal(err)
	}
}

// do runs the actions in order.
func (tb *tab) do(actions ...chromedp.Action[chromedp.Void]) {
	tb.t.Helper()
	tb.call(func(ctx context.Context) error { return chromedp.Do(ctx, actions...) })
}

// eval is the value of expression in the page.
func eval[T any](tb *tab, expression string) T {
	tb.t.Helper()
	var v T
	tb.call(func(ctx context.Context) (err error) {
		v, err = chromedp.Run(ctx, chromedp.Evaluate[T](expression, chromedp.EvalAwaitPromise))
		return err
	})
	return v
}

// at is the tab's URL, path and query, from its history: no script needed.
func (tb *tab) at() string {
	tb.t.Helper()
	var h page.GetNavigationHistoryResult
	tb.call(func(ctx context.Context) (err error) {
		h, err = chromedp.Run(ctx, chromedp.NavigationEntries())
		return err
	})
	return strings.TrimPrefix(h.Entries[h.CurrentIndex].URL, tb.base)
}

// html is the outer HTML of the first element sel matches, waiting for it.
func (tb *tab) html(sel string) string {
	tb.t.Helper()
	var s string
	tb.call(func(ctx context.Context) (err error) {
		s, err = chromedp.Run(ctx, chromedp.OuterHTML(chromedp.CSS(sel)))
		return err
	})
	return s
}

// count is how many elements sel matches now.
func (tb *tab) count(sel string) int {
	tb.t.Helper()
	var nodes []*chromedp.Node
	tb.call(func(ctx context.Context) (err error) {
		nodes, err = chromedp.Run(ctx, chromedp.Nodes(chromedp.CSSAll(sel), chromedp.AtLeast(0)))
		return err
	})
	return len(nodes)
}

// wait waits until sel matches.
func (tb *tab) wait(sel string) {
	tb.t.Helper()
	tb.do(chromedp.WaitReady(chromedp.CSS(sel)))
}

// requests checks the page requests since the last check: their targets,
// and that htmx sent them or not.
func (tb *tab) requests(step string, htmx bool, targets ...string) {
	tb.t.Helper()
	seen := tb.log.take()
	var got []string
	for _, r := range seen {
		got = append(got, r.target)
		if r.htmx != htmx {
			tb.t.Errorf("%s: %s with HX-Request %v; want %v", step, r.target, r.htmx, htmx)
		}
	}
	if !slices.Equal(got, targets) {
		tb.t.Errorf("%s: requests %q; want %q", step, got, targets)
	}
}

// restores checks the page requests since the last check: htmx's, to
// restore history, for targets.
func (tb *tab) restores(step string, targets ...string) {
	tb.t.Helper()
	seen := tb.log.take()
	var got []string
	for _, r := range seen {
		got = append(got, r.target)
		if !r.htmx || !r.restore {
			tb.t.Errorf("%s: %s with HX-Request %v, HX-History-Restore-Request %v; want both", step, r.target, r.htmx, r.restore)
		}
	}
	if !slices.Equal(got, targets) {
		tb.t.Errorf("%s: requests %q; want %q", step, got, targets)
	}
}

// violation is one axe finding: the rule, its impact, where, and axe's
// account of the first place.
type violation struct {
	ID      string   `json:"id"`
	Impact  string   `json:"impact"`
	Help    string   `json:"help"`
	Targets []string `json:"targets"`
	Summary string   `json:"summary"`
}

// audit runs axe on the page as it is now, in the light color scheme and
// in the dark, and fails on any violation: WCAG 2.2 A and AA, and axe's
// best practices. Without JavaScript, the page's scripts never ran;
// scripts are let run for the audit alone, which runs none of the page's.
func (tb *tab) audit(state string) {
	tb.t.Helper()
	if !tb.js {
		tb.scripts(true)
		defer tb.scripts(false)
	}
	if !eval[bool](tb, `typeof axe === "object"`) {
		tb.call(func(ctx context.Context) error {
			_, err := chromedp.Run(ctx, chromedp.Evaluate[chromedp.Void](axeJS))
			return err
		})
	}
	for _, scheme := range []string{"light", "dark"} {
		tb.call(func(ctx context.Context) error {
			_, err := chromedp.Call(ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{
				Features: []*emulation.MediaFeature{{Name: "prefers-color-scheme", Value: scheme}},
			})
			return err
		})
		found := eval[[]violation](tb, `axe.run(document, {resultTypes: ["violations"]}).then(r => r.violations.map(v => ({
			id: v.id, impact: v.impact, help: v.help, targets: v.nodes.map(n => n.target.join(" ")), summary: v.nodes[0].failureSummary,
		})))`)
		for _, v := range found {
			tb.t.Errorf("%s, %s: axe %s (%s): %s, at %q: %s", state, scheme, v.ID, v.Impact, v.Help, v.Targets, v.Summary)
		}
	}
	tb.call(func(ctx context.Context) error {
		_, err := chromedp.Call(ctx, emulation.SetEmulatedMedia, emulation.SetEmulatedMediaParams{})
		return err
	})
}

// history goes delta steps back (negative) or forward in the tab's history,
// without waiting for a load: a page from the back/forward cache, or one
// htmx restores, fires none.
func (tb *tab) history(delta int) {
	tb.t.Helper()
	tb.call(func(ctx context.Context) error {
		h, err := chromedp.Run(ctx, chromedp.NavigationEntries())
		if err != nil {
			return err
		}
		i := int(h.CurrentIndex) + delta
		if i < 0 || i >= len(h.Entries) {
			tb.t.Fatalf("history %+d from entry %d of %d", delta, h.CurrentIndex, len(h.Entries))
		}
		_, err = chromedp.Call(ctx, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{EntryID: h.Entries[i].ID})
		return err
	})
}

// scripts lets the page run scripts, or not.
func (tb *tab) scripts(on bool) {
	tb.t.Helper()
	tb.call(func(ctx context.Context) error {
		_, err := chromedp.Call(ctx, emulation.SetScriptExecutionDisabled, emulation.SetScriptExecutionDisabledParams{Value: !on})
		return err
	})
}

// waitText waits, up to 10 s, for the text of the element sel matches to
// be want, and fails with what it is instead.
func (tb *tab) waitText(sel, want string) {
	tb.t.Helper()
	js, err := json.Marshal([2]string{sel, want})
	if err != nil {
		tb.t.Fatal(err)
	}
	got := eval[string](tb, `new Promise(resolve => {
		const [sel, want] = `+string(js)+`, start = Date.now();
		(function poll() {
			const el = document.querySelector(sel), text = el ? el.textContent : "(no " + sel + ")";
			if (text === want || Date.now() - start > 10000) resolve(text); else setTimeout(poll, 50);
		})();
	})`)
	if got != want {
		tb.t.Errorf("%s: %q; want %q", sel, got, want)
	}
}

// browserForm runs the page's VIN form in a browser twice, each in a
// fresh profile: without JavaScript every step is a page the browser
// loads, and with it htmx makes the same steps fragments, the page never
// reloading. Both dialogs open either way. axe audits each state in both
// runs. CI (CI set) needs the browser; a machine without one skips.
func browserForm(t *testing.T, srv *httptest.Server) {
	path := chromePath()
	if path == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("no Chrome or Chromium to run the form in: set NITROVIN_CHROME")
		}
		t.Skip("no Chrome or Chromium: set NITROVIN_CHROME to run the form in a browser")
	}
	log := &requestLog{h: srv.Config.Handler}
	bsrv := httptest.NewServer(log)
	t.Cleanup(bsrv.Close)
	t.Run("js-off", func(t *testing.T) { formJSOff(t, path, bsrv.URL, log) })
	t.Run("js-on", func(t *testing.T) { formJSOn(t, path, bsrv.URL, log) })
}

const (
	vin = "1HGCM82633A004352"
	// submitted is the form's request for vin, every field as it starts.
	submitted = "/?from.vin=" + vin + "&model_year=&shape=vpic"
	// oldLink is an old link to the decode form, and movedLink where it
	// lands.
	oldLink   = "/?vin=5YJ3E1EA2JF000316"
	movedLink = "/?from.vin=5YJ3E1EA2JF000316"
)

// dialogs opens each of the page's dialogs from its button, audits it
// open, and closes it by its own button: popovers, which need no script.
func (tb *tab) dialogs(step string) {
	tb.t.Helper()
	for dialog, opener := range map[string]string{"#vocabularies": "button.open-vocabularies", "#result-json": "button.open-dialog"} {
		tb.do(chromedp.Click(chromedp.CSS(opener)))
		tb.wait(dialog + ":popover-open")
		tb.audit(step + ", " + dialog + " open")
		tb.do(chromedp.Click(chromedp.CSS(dialog + " .dialog-head button")))
		tb.wait(dialog + ":not(:popover-open)")
	}
}

// formJSOff: the page without JavaScript, in a fresh profile. Each step is
// a page; no copy buttons appear; back and forward are the browser's own,
// and an old link moves to the page.
func formJSOff(t *testing.T, path, base string, log *requestLog) {
	tb := newTab(t, path, base, log, false)

	tb.do(chromedp.Navigate(base + "/"))
	tb.wait("#install")
	if n := tb.count("button.copy"); n != 0 {
		t.Errorf("/: %d copy buttons; JavaScript ran", n)
	}
	tb.requests("/", false, "/")
	tb.audit("/")

	tb.do(chromedp.SetValue(chromedp.CSS("#from-vin"), vin), chromedp.Click(chromedp.CSS(`#controls form button[type=submit]`)))
	tb.wait("#copy-url")
	if got := tb.at(); got != submitted {
		t.Errorf("submit: at %s; want %s", got, submitted)
	}
	if !strings.Contains(tb.html("main"), `<th scope="row">Make</th><td>HONDA</td>`) {
		t.Errorf("submit: no Make HONDA")
	}
	tb.requests("submit", false, submitted)
	tb.audit("decode")
	tb.dialogs("decode, JS off")

	// Back is the empty page again, forward the decode.
	tb.history(-1)
	tb.wait("#install")
	if got := tb.at(); got != "/" {
		t.Errorf("back: at %s; want /", got)
	}
	tb.history(+1)
	tb.wait("#copy-url")
	if got := tb.at(); got != submitted {
		t.Errorf("forward: at %s; want %s", got, submitted)
	}
	tb.log.take() // the browser may or may not ask again: its cache's choice

	// A refusal is the form with the refusal beside its field.
	const refused = "/?from.vin=" + vin + "&model_year=2003.5"
	tb.do(chromedp.Navigate(base + refused))
	tb.wait("#model_year-error")
	tb.requests("refusal", false, refused)
	tb.audit("refusal")

	// A tab is a link to the page with its form.
	tb.do(chromedp.Click(chromedp.CSS("#tab-id")))
	tb.wait("#from-model")
	if got := tb.at(); got != "/?tab=id" {
		t.Errorf("the vPIC id tab: at %s; want /?tab=id", got)
	}
	tb.requests("the vPIC id tab", false, "/?tab=id")
	tb.audit("the vPIC id tab")

	// An old link lands on the same decode on the page.
	tb.do(chromedp.Navigate(base + oldLink))
	tb.wait("#copy-url")
	if got := tb.at(); got != movedLink {
		t.Errorf("old link: at %s; want %s", got, movedLink)
	}
	tb.requests("old link", false, oldLink, movedLink)
}

// formJSOn: the page with JavaScript, in a fresh profile. htmx asks for
// each step as a fragment (HX-Request), swaps it into the page's main and
// pushes its URL, and the page never reloads; copy buttons come with each
// swap; back and forward ask the server for each state, never a snapshot
// (pkg/ui, ui.html); nothing is refused by CSP and nothing throws.
func formJSOn(t *testing.T, path, base string, log *requestLog) {
	tb := newTab(t, path, base, log, true)

	tb.do(chromedp.Navigate(base + "/"))
	tb.wait("#install")
	tb.wait("#mcp-local + button.copy")
	tb.requests("/", false, "/")
	tb.audit("/")
	// A page that reloads loses this.
	eval[bool](tb, `window.stayed = true`)

	tb.do(chromedp.SetValue(chromedp.CSS("#from-vin"), vin), chromedp.Click(chromedp.CSS(`#controls form button[type=submit]`)))
	tb.wait("#copy-url + button.copy")
	if got := tb.at(); got != submitted {
		t.Errorf("submit: at %s; want %s", got, submitted)
	}
	if !strings.Contains(tb.html("main"), `<th scope="row">Make</th><td>HONDA</td>`) {
		t.Errorf("submit: no Make HONDA")
	}
	if title := eval[string](tb, `document.title`); title != "2003 Honda Accord · nitrovin" {
		t.Errorf("submit: title %q", title)
	}
	tb.requests("submit", true, submitted)
	tb.audit("decode")
	tb.dialogs("decode")

	// A copy button copies its text and says so: the decode's JSON, in
	// its dialog.
	tb.do(chromedp.Click(chromedp.CSS("button.open-dialog")))
	tb.wait("#result-json:popover-open")
	tb.do(chromedp.Click(chromedp.CSS(`#decode-json + button.copy`)))
	tb.waitText("#copy-status", "Copied the decode's JSON")
	if got, want := eval[string](tb, `navigator.clipboard.readText()`), eval[string](tb, `document.getElementById("decode-json").textContent`); got != want {
		t.Errorf("copy: clipboard %q; want the decode's JSON", got)
	}
	tb.do(chromedp.Click(chromedp.CSS("#result-json .dialog-head button")))
	tb.wait("#result-json:not(:popover-open)")

	// A refusal is the form with the refusal beside its field, in the
	// page's main, and the error slot stays empty.
	tb.do(chromedp.SetValue(chromedp.CSS("#model_year"), "2003.5"), chromedp.Click(chromedp.CSS(`#controls form button[type=submit]`)))
	tb.wait("#model_year-error")
	const refused = "/?from.vin=" + vin + "&model_year=2003.5&shape=vpic"
	tb.requests("refusal", true, refused)
	if slot := eval[string](tb, `document.getElementById("ui-error").innerHTML`); slot != "" {
		t.Errorf("refusal: error slot %q", slot)
	}
	tb.audit("refusal")

	// Back and forward ask the server for each state, the whole page,
	// which htmx takes the main out of: live copy buttons, one each, and
	// one main and one Vocabularies button.
	eval[bool](tb, `history.back(), true`)
	tb.wait("#copy-url + button.copy")
	if got := tb.at(); got != submitted {
		t.Errorf("back: at %s; want %s", got, submitted)
	}
	eval[bool](tb, `history.back(), true`)
	tb.wait("#install")
	tb.wait("#mcp-local + button.copy")
	if got := tb.at(); got != "/" {
		t.Errorf("back: at %s; want /", got)
	}
	eval[bool](tb, `history.forward(), true`)
	tb.wait("#copy-url + button.copy")
	if got := tb.at(); got != submitted {
		t.Errorf("forward: at %s; want %s", got, submitted)
	}
	if copies, buttons := tb.count("[data-copy]"), tb.count("button.copy"); copies != buttons {
		t.Errorf("forward: %d copy buttons for %d texts", buttons, copies)
	}
	eval[bool](tb, `document.querySelectorAll("details.dev").forEach(d => d.open = true), true`)
	tb.do(chromedp.Click(chromedp.CSS(`#copy-cli + button.copy`)))
	tb.waitText("#copy-status", "Copied the nitrovin decode command")
	eval[bool](tb, `history.back(), true`)
	tb.wait("#install")
	if got := tb.at(); got != "/" {
		t.Errorf("back again: at %s; want /", got)
	}
	if mains, buttons := tb.count("main"), tb.count("button.open-vocabularies"); mains != 1 || buttons != 1 {
		t.Errorf("back again: %d mains, %d Vocabularies buttons", mains, buttons)
	}
	tb.restores("back and forward", submitted, "/", submitted, "/")
	if n := eval[int](tb, `sessionStorage.getItem("htmx-history-cache") === null ? 0 : JSON.parse(sessionStorage.getItem("htmx-history-cache")).length`); n != 0 {
		t.Errorf("back and forward: htmx kept %d snapshots", n)
	}
	tb.audit("restored /")

	// The examples and the tabs are links htmx follows the same way.
	tb.do(chromedp.Click(chromedp.CSS(`a.example[href="` + movedLink + `"]`)))
	tb.wait("#copy-url + button.copy")
	if got := tb.at(); got != movedLink {
		t.Errorf("example: at %s", got)
	}
	tb.requests("example", true, movedLink)
	tb.do(chromedp.Click(chromedp.CSS("#tab-id")))
	tb.wait("#from-model")
	if got := tb.at(); got != "/?tab=id" {
		t.Errorf("the vPIC id tab: at %s; want /?tab=id", got)
	}
	tb.requests("the vPIC id tab", true, "/?tab=id")
	tb.audit("the vPIC id tab")

	if !eval[bool](tb, `window.stayed === true`) {
		t.Errorf("the page reloaded")
	}
	if refused := eval[[]string](tb, `refused`); len(refused) > 0 {
		t.Errorf("CSP refused %q", refused)
	}
	tb.mu.Lock()
	if len(tb.thrown) > 0 {
		t.Errorf("thrown: %q", tb.thrown)
	}
	tb.mu.Unlock()

	// An old link lands on the same decode on the page.
	tb.do(chromedp.Navigate(base + oldLink))
	tb.wait("#copy-url + button.copy")
	if got := tb.at(); got != movedLink {
		t.Errorf("old link: at %s; want %s", got, movedLink)
	}
	tb.requests("old link", false, oldLink, movedLink)
}
