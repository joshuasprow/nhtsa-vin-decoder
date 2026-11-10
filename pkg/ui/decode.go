package ui

import (
	"cmp"
	"encoding/json"
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

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// The decode form (ADR 0013). GET / is the entry page: what nitrovin is,
// the form, examples, install, MCP, verify and links, all derived from the
// running binary's version and data month. The form asks GET /ui/decode,
// whose query is GET /v1/decode/{vin}'s with the VIN as a parameter, so a
// decode is a shareable URL, and whose refusals are contract.ParseQuery's,
// each shown beside the field its pointer names. The pages are whole
// without JavaScript. With it, htmx swaps the answer into the page's main
// and pushes its URL, and copy.js adds copy buttons to the text the page
// already shows. Old links to the form, /?vin=…, redirect to /ui/decode.

// decodePath is the decode form's page.
const decodePath = "/ui/decode"

// mainTarget is the layout's main, which the form's answer fills.
const mainTarget = "#ui-main"

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

// formPage is what the home and decode templates render.
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

// newFormPage is the empty form on info.
func newFormPage(info contract.Info) *formPage {
	return &formPage{
		Shape: string(contract.ShapeVPIC), DataMonth: info.DataMonth, DecoderVersion: info.DecoderVersion,
		Links: newFormLinks(info),
	}
}

// formTitle is the form's title before it has a decode.
const formTitle = "Decode a VIN"

// home is GET /: the entry page. A query is an old link to the form,
// which lives at /ui/decode now: redirected there, query and all.
func (h *Handler) home(r *http.Request) (view, error) {
	if r.URL.RawQuery != "" {
		return view{location: decodePath + "?" + r.URL.RawQuery, status: http.StatusMovedPermanently}, nil
	}
	return view{title: formTitle, template: "home", data: newFormPage(h.b.Info())}, nil
}

// decode is GET /ui/decode: the empty form without a query, otherwise the
// form filled in from the query and the decode below it, or the refusal
// beside the field it names. A refusal is the form still, with its status,
// so htmx sends it to the page's main and not to the error slot.
func (h *Handler) decode(r *http.Request) (view, error) {
	info := h.b.Info()
	page := newFormPage(info)
	v := view{title: formTitle, template: "decode", data: page, target: mainTarget}
	if r.URL.RawQuery == "" {
		return v, nil
	}
	refuse := func(err error) (view, error) {
		p := contract.AsProblem(err)
		if p.Type == contract.Internal {
			return view{}, err // a fault in nitrovin: the error page, logged
		}
		field := strings.TrimPrefix(p.Pointer, "/")
		if !slices.Contains(formFields, field) {
			field = ""
		}
		page.Errors = map[string]string{field: p.Detail}
		v.status = p.Status
		return v, nil
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return refuse(contract.NewProblem(contract.InvalidRequest, "", "the query string is malformed: "+err.Error()))
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
		return refuse(contract.NewProblem(contract.InvalidRequest, "/vin", "vin is given more than once"))
	}

	req, err := contract.ParseQuery(page.VIN, query)
	if err != nil {
		return refuse(err)
	}
	dec, err := h.b.Decode(req)
	if err != nil {
		return refuse(err)
	}
	page.Decode, err = h.decodePage(r, page, query, req, dec, info.Elements)
	if err != nil {
		return view{}, err
	}
	v.title = dec.VIN
	return v, nil
}

// decodePage is dec as the page shows it. query is the decode's own query,
// the VIN and the page's parameters taken out.
func (h *Handler) decodePage(r *http.Request, page *formPage, query url.Values, req contract.Request, dec contract.Decode, elements contract.Elements) (*formDecode, error) {
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
		d.EmptyToggle = decodePath + "?vin=" + url.QueryEscape(page.VIN)
		if len(toggle) > 0 {
			d.EmptyToggle += "&" + toggle.Encode()
		}
	}
	for _, c := range dec.ErrorCodes {
		d.Codes = append(d.Codes, formCode{c, h.b.ErrorText(c)})
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
