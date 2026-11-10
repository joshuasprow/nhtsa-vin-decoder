package main

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"html/template"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	// Aliased: the package's tests name a helper artifact.
	dataartifact "github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// The decode form at GET / is for people with a browser. It sits outside
// /v1 and openapi.yaml: not part of the contract, free to change. Its query
// is GET /v1/decode/{vin}'s with the VIN as a parameter, so a decode is a
// shareable URL, and its refusals are contract.ParseQuery's, each shown
// beside the field its pointer names. Without a query it says what
// nitrovin is: examples, install, MCP, verify and links, all derived from
// the running binary's version and data month. The page is whole without
// JavaScript; /copy.js only adds copy buttons to the text it already shows.

//go:embed form.html
var formHTML string

var formTemplate = template.Must(template.New("form").Parse(formHTML))

// copyJS is GET /copy.js, the page's one script.
//
//go:embed copy.js
var copyJS []byte

// formCSP lets the page use its own inline styles and its one script from
// the server itself: no inline script or handler, no third-party resource.
const formCSP = "default-src 'self'; script-src 'self'; style-src 'unsafe-inline'"

// formFields are the query parameters the form has a field for; a refusal
// pointing elsewhere shows above the form.
var formFields = []string{"vin", "model_year", "shape", "full"}

// showEmpty is the page's own query parameter, never the decode's: "true"
// shows the vpic shape's empty values, which are hidden otherwise.
const showEmpty = "show_empty"

const (
	repoURL     = "https://github.com/joshuasprow/nitrovin"
	dataRepoURL = "https://github.com/joshuasprow/nitrovin-data"
	// dataModule is the module whose version is the embedded data's
	// nitrovin-data release.
	dataModule = "github.com/joshuasprow/nitrovin-data"
	// elementAdditionalErrorText is vPIC's AdditionalErrorText.
	elementAdditionalErrorText = 156
	// otherGroup holds the elements without a group name.
	otherGroup = "Other"
)

// formPage is what form.html renders.
type formPage struct {
	VIN, ModelYear, Shape string
	Full, ShowEmpty       bool
	// Errors holds a refusal's detail under its field's name, or under ""
	// when no field holds the offending value.
	Errors                    map[string]string
	Decode                    *formDecode
	DataMonth, DecoderVersion string
	Links                     formLinks
}

// formLinks are the page's links that depend on the running binary.
type formLinks struct {
	// Tag is the release this build is, "" for any other build.
	Tag string
	// Release is Tag's release page, or the releases index without one.
	Release string
	// DataRelease is the data month's nitrovin-data release, or a search
	// of the releases for the month when the build cannot name it.
	DataRelease string
	// Parity is docs/parity.md at Tag, or on main; ParityReport finds the
	// data month's report, the body of its refresh pull request.
	Parity, ParityReport string
}

// formDecode is a decode as the page shows it.
type formDecode struct {
	contract.Decode
	// JSON is the same request on GET /v1/decode/{vin}; NHTSA is the VIN
	// on vPIC's own decoder.
	JSON, NHTSA string
	Groups      []formGroup
	// Hidden counts the empty values not shown; EmptyToggle, set in the
	// vpic shape only, is this page with the empty values shown or hidden.
	Hidden      int
	EmptyToggle string
	Codes       []formCode
	// AdditionalErrorText is the decode's AdditionalErrorText (element 156).
	AdditionalErrorText string
	// Input and Suggested are the VIN and its corrected_vin, by character,
	// when the decode suggests one; DifferAt names the positions in which
	// they differ.
	Input, Suggested []formChar
	DifferAt         string
	Copy             formCopies
}

// formCopies are the decode's copyable texts, rendered on the server.
type formCopies struct {
	// JSON is the decode as the contract layer marshals it; URL is this
	// page's; Curl and CLI make the same request on /v1 and the command
	// line.
	JSON, URL, Curl, CLI string
}

// Clean reports a decode without errors: error code 0 alone.
func (d *formDecode) Clean() bool { return len(d.ErrorCodes) == 1 && d.ErrorCodes[0] == 0 }

// formCode is one error code and its text from the data's error-code
// table, "" when the table lacks it.
type formCode struct {
	Code int
	Text string
}

// formChar is one character of a VIN; Differs marks a position in which
// the VIN and its suggested VIN differ.
type formChar struct {
	C       string
	Differs bool
}

// formGroup is one of vPIC's element groups; Open shows it unfolded.
type formGroup struct {
	Name string
	Open bool
	Rows []formRow
}

// formRow is one element: its key in the decode's shape and its non-empty
// values, none for an empty value shown. List marks a multi-valued element
// of the nitrovin shape, shown as a list however many values it has.
type formRow struct {
	Name   string
	Values []string
	List   bool
}

// vpicGroups is vPIC's group order: the labels of the projection's ORDER BY
// CASE, in rank order (pkg/decoder's groupRank, spvindecode :234-263). The
// labels put spaces around '/' and the data's group names have none, so the
// engine ranks most groups alike (99); a group takes its label's place by
// name with those spaces removed and case folded (groupKey).
var vpicGroups = []string{
	"General",
	"Exterior / Body", "Exterior / Dimension", "Exterior / Truck", "Exterior / Trailer",
	"Exterior / Wheel tire", "Exterior / Motorcycle", "Exterior / Bus",
	"Interior", "Interior / Seat",
	"Mechanical / Transmission", "Mechanical / Drivetrain", "Mechanical / Brake",
	"Mechanical / Battery", "Mechanical / Battery / Charger",
	"Engine",
	"Passive Safety System", "Passive Safety System / Air Bag Location",
	"Active Safety System", "Active Safety System / Maintaining Safe Distance",
	"Active Safety System / Forward Collision Prevention", "Active Safety System / Lane and Side Assist",
	"Active Safety System / Backing Up and Parking", "Active Safety System / 911 Notification",
	"Active Safety System / Lighting Technologies",
	"Internal",
}

func groupKey(name string) string { return strings.ToLower(strings.ReplaceAll(name, " / ", "/")) }

// groupRank is a group's place on the page: vPIC's order, then any group
// the labels lack, then Other.
var groupRank = func() func(string) int {
	rank := make(map[string]int, len(vpicGroups))
	for i, g := range vpicGroups {
		rank[groupKey(g)] = i
	}
	return func(name string) int {
		if name == otherGroup {
			return len(vpicGroups) + 1
		}
		if r, ok := rank[groupKey(name)]; ok {
			return r
		}
		return len(vpicGroups)
	}
}()

// formGroups are dec's elements by group in vPIC's group order, General and
// Engine open, each group's elements in element id order: in the vpic shape
// every element, its empty values counted in hidden unless showEmpty, and in
// the nitrovin shape every decoded element with a non-empty value. A group
// with nothing to show is left out.
func formGroups(dec contract.Decode, elements contract.Elements, showEmpty bool) (groups []formGroup, hidden int) {
	at := map[string]int{}
	add := func(e contract.Element, row formRow) {
		name := e.GroupName.V
		if name == "" {
			name = otherGroup
		}
		i, ok := at[name]
		if !ok {
			i, at[name] = len(groups), len(groups)
			groups = append(groups, formGroup{Name: name, Open: name == "General" || name == "Engine"})
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}
	switch attrs := dec.Attributes.(type) {
	case contract.VpicAttributes:
		for _, e := range elements {
			v := attrs[e.Code]
			switch {
			case v != "":
				add(e, formRow{Name: e.Code, Values: []string{v}})
			case showEmpty:
				add(e, formRow{Name: e.Code})
			default:
				hidden++
			}
		}
	case contract.NitrovinAttributes:
		for _, e := range elements {
			a, ok := attrs[e.Variable]
			if !ok {
				continue
			}
			if a.Values == nil {
				if a.Value != "" {
					add(e, formRow{Name: e.Variable, Values: []string{a.Value}})
				}
				continue
			}
			var values []string
			for _, v := range a.Values {
				if v != "" {
					values = append(values, v)
				}
			}
			if values != nil {
				add(e, formRow{Name: e.Variable, Values: values, List: true})
			}
		}
	}
	slices.SortStableFunc(groups, func(a, b formGroup) int {
		return cmp.Or(cmp.Compare(groupRank(a.Name), groupRank(b.Name)), strings.Compare(a.Name, b.Name))
	})
	return groups, hidden
}

// attribute is element id's single value in dec, "" when it has none.
func attribute(dec contract.Decode, elements contract.Elements, id int32) string {
	e, ok := elements.Find(id)
	if !ok {
		return ""
	}
	switch attrs := dec.Attributes.(type) {
	case contract.VpicAttributes:
		return attrs[e.Code]
	case contract.NitrovinAttributes:
		return attrs[e.Variable].Value
	}
	return ""
}

// diffVINs splits vin and suggested into characters, marking each position
// in which they differ (case aside, as the decoder upper-cases), and names
// those positions, counted from 1.
func diffVINs(vin, suggested string) (in, out []formChar, at string) {
	a, b := []rune(vin), []rune(suggested)
	var positions []string
	for i := range max(len(a), len(b)) {
		differs := i >= len(a) || i >= len(b) || unicode.ToUpper(a[i]) != unicode.ToUpper(b[i])
		if i < len(a) {
			in = append(in, formChar{string(a[i]), differs})
		}
		if i < len(b) {
			out = append(out, formChar{string(b[i]), differs})
		}
		if differs {
			positions = append(positions, strconv.Itoa(i+1))
		}
	}
	switch len(positions) {
	case 0:
	case 1:
		at = "position " + positions[0]
	default:
		at = "positions " + strings.Join(positions, ", ")
	}
	return in, out, at
}

// errorTexts reads the error-code table of the artifact data: each code's
// text as NHTSA's ErrorText spells it ("1 - Check Digit (9th position)
// does not calculate properly"), by id.
func errorTexts(data []byte) (map[int]string, error) {
	a, err := dataartifact.Open(data)
	if err != nil {
		return nil, err
	}
	t := a.Table("errorcode")
	ids, names := t.Int32s("id"), t.Strings("name")
	texts := make(map[int]string, len(ids))
	for i, id := range ids {
		if s, ok := names.At(i); ok {
			texts[int(id)] = strings.TrimSpace(s)
		}
	}
	return texts, nil
}

// releaseTag matches a release's version, which is its tag; pseudoVersion
// matches the Go pseudo-version of any other commit.
var (
	releaseTag    = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`)
	pseudoVersion = regexp.MustCompile(`\d{14}-[0-9a-f]{12}$`)
)

// embeddedDataTag is the nitrovin-data release of the embedded data: the
// version of the data module this build links, "" when it has none.
var embeddedDataTag = sync.OnceValue(func() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, m := range bi.Deps {
		if m.Path == dataModule && m.Replace == nil {
			return m.Version
		}
	}
	return ""
})

// newFormLinks derives the page's links from the running binary: its
// version, and its data month and source.
func newFormLinks(info contract.Info) formLinks {
	var l formLinks
	if releaseTag.MatchString(info.DecoderVersion) && !pseudoVersion.MatchString(info.DecoderVersion) {
		l.Tag = info.DecoderVersion
	}
	l.Release, l.Parity = repoURL+"/releases", repoURL+"/blob/main/docs/parity.md"
	if l.Tag != "" {
		l.Release, l.Parity = repoURL+"/releases/tag/"+l.Tag, repoURL+"/blob/"+l.Tag+"/docs/parity.md"
	}
	// A data release is v<format>.<YYYYMM>.<rebuild>.
	month := strings.ReplaceAll(info.DataMonth, "_", "")
	l.DataRelease = dataRepoURL + "/releases?q=" + url.QueryEscape(month)
	if tag := embeddedDataTag(); info.DataSource == contract.DataSourceEmbedded && strings.Contains(tag, "."+month+".") {
		l.DataRelease = dataRepoURL + "/releases/tag/" + tag
	}
	l.ParityReport = repoURL + "/pulls?q=" + url.QueryEscape(`is:pr "Refresh: data month `+info.DataMonth+`"`)
	return l
}

// form is GET / (and HEAD /): the empty form without a query, otherwise
// the form filled in from the query and the decode, or the refusal, below.
func (a *api) form(w http.ResponseWriter, r *http.Request) {
	info := a.d.Info()
	page := formPage{
		Shape: string(contract.ShapeVPIC), DataMonth: info.DataMonth, DecoderVersion: info.DecoderVersion,
		Links: newFormLinks(info),
	}
	if r.URL.RawQuery == "" {
		a.page(w, http.StatusOK, &page)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		a.refusePage(w, &page, contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+err.Error()))
		return
	}
	vins := query["vin"]
	delete(query, "vin")
	page.ShowEmpty = query.Get(showEmpty) == "true"
	delete(query, showEmpty)
	// The form always sends model_year; empty is no hint.
	if ys := query["model_year"]; len(ys) == 1 && ys[0] == "" {
		delete(query, "model_year")
	}
	if len(vins) > 0 {
		page.VIN = vins[0]
	}
	page.ModelYear = query.Get("model_year")
	if shape := query.Get("shape"); shape != "" {
		page.Shape = shape
	}
	page.Full = query.Get("full") == "true"
	if len(vins) > 1 {
		a.refusePage(w, &page, contract.NewProblem(contract.InvalidRequest, "/vin", "vin is given more than once"))
		return
	}

	req, err := contract.ParseQuery(page.VIN, query)
	if err != nil {
		a.refusePage(w, &page, contract.AsProblem(err))
		return
	}
	dec, err := a.d.Decode(req)
	if err != nil {
		a.refusePage(w, &page, contract.AsProblem(err))
		return
	}
	page.Decode, err = a.decodePage(r, &page, query, req, dec, info.Elements)
	if err != nil {
		a.refusePage(w, &page, contract.AsProblem(err))
		return
	}
	a.page(w, http.StatusOK, &page)
}

// decodePage is dec as the page shows it. query is the decode's own query,
// the VIN and the page's parameters taken out.
func (a *api) decodePage(r *http.Request, page *formPage, query url.Values, req contract.Request, dec contract.Decode, elements contract.Elements) (*formDecode, error) {
	body, err := json.Marshal(dec) // as GET /v1/decode/{vin} writes it
	if err != nil {
		return nil, err
	}
	jsonURL := "/v1/decode/" + url.PathEscape(page.VIN)
	if len(query) > 0 {
		jsonURL += "?" + query.Encode()
	}
	nhtsa := "https://vpic.nhtsa.dot.gov/decoder/VinDecoder?VIN=" + url.QueryEscape(dec.VIN)
	if req.ModelYear.Valid {
		nhtsa += "&ModelYear=" + strconv.Itoa(int(req.ModelYear.V))
	}
	origin := requestOrigin(r)
	d := &formDecode{
		Decode: dec, JSON: jsonURL, NHTSA: nhtsa,
		AdditionalErrorText: attribute(dec, elements, elementAdditionalErrorText),
		Copy: formCopies{
			JSON: string(body),
			URL:  origin + r.URL.RequestURI(),
			Curl: "curl " + shellQuote(origin+jsonURL),
			CLI:  cliCommand(page.VIN, req, query.Get("now")),
		},
	}
	d.Groups, d.Hidden = formGroups(dec, elements, page.ShowEmpty)
	if _, ok := dec.Attributes.(contract.VpicAttributes); ok {
		toggle := url.Values{}
		maps.Copy(toggle, query)
		if !page.ShowEmpty {
			toggle.Set(showEmpty, "true")
		}
		d.EmptyToggle = "/?vin=" + url.QueryEscape(page.VIN)
		if len(toggle) > 0 {
			d.EmptyToggle += "&" + toggle.Encode()
		}
	}
	for _, c := range dec.ErrorCodes {
		d.Codes = append(d.Codes, formCode{c, a.errorTexts[c]})
	}
	if dec.CorrectedVIN.Valid {
		d.Input, d.Suggested, d.DifferAt = diffVINs(dec.VIN, dec.CorrectedVIN.V)
	}
	return d, nil
}

// requestOrigin is the scheme and host r was sent to, as the client named
// them: https behind a proxy that says so.
func requestOrigin(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// cliCommand is `nitrovin decode` making req, whose VIN is vin as sent and
// whose now is now as sent ("" for the clock).
func cliCommand(vin string, req contract.Request, now string) string {
	cmd := "nitrovin decode"
	if req.ModelYear.Valid {
		cmd += " --model-year " + strconv.Itoa(int(req.ModelYear.V))
	}
	if req.Shape == contract.ShapeNitrovin {
		cmd += " --shape nitrovin"
	}
	if req.Full {
		cmd += " --full"
	}
	if now != "" {
		cmd += " --now " + shellQuote(now)
	}
	switch {
	case vin == "-": // an argument "-" reads stdin
		return "echo - | " + cmd + " -"
	case strings.HasPrefix(vin, "-"):
		return cmd + " -- " + shellQuote(vin)
	}
	return cmd + " " + shellQuote(vin)
}

// shellQuote is s as one word of a POSIX shell (zsh included): itself when
// it holds only characters no shell treats specially, else single-quoted.
func shellQuote(s string) string {
	plain := s != ""
	for _, c := range s {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.ContainsRune("-_./:,+@%", c)) {
			plain = false
			break
		}
	}
	if plain {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// refusePage renders the form with p's detail beside the field p points
// at, with p's status. An internal problem is logged, as the API logs it.
func (a *api) refusePage(w http.ResponseWriter, page *formPage, p *contract.Problem) {
	if p.Status == http.StatusInternalServerError {
		a.log.Print(p)
	}
	field := strings.TrimPrefix(p.Pointer, "/")
	if !slices.Contains(formFields, field) {
		field = ""
	}
	page.Errors = map[string]string{field: p.Detail}
	a.page(w, p.Status, page)
}

// page writes the rendered form with status. The template escapes every
// value it is given: the VIN and the query are the caller's text.
func (a *api) page(w http.ResponseWriter, status int, page *formPage) {
	var b bytes.Buffer
	if err := formTemplate.Execute(&b, page); err != nil {
		a.problem(w, contract.AsProblem(err))
		return
	}
	h := w.Header()
	h.Set("Content-Security-Policy", formCSP)
	h.Set("X-Content-Type-Options", "nosniff")
	a.write(w, status, "text/html; charset=utf-8", b.Bytes())
}

// copyScript is GET /copy.js (and HEAD).
func (a *api) copyScript(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	a.write(w, http.StatusOK, "text/javascript; charset=utf-8", copyJS)
}
