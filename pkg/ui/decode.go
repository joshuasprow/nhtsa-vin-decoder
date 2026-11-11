package ui

import (
	"cmp"
	"encoding/json"
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

// A VIN's decode on the page (page.go, ADR 0013): the vehicle in a line,
// the VIN's error codes and suggested VIN, every element by vPIC's
// groups, and the same request as JSON, curl and the command line, all
// derived from the running binary's version and data month. The decode's
// query is GET /v1/decode/{vin}'s, and its refusals contract.ParseQuery's,
// each shown beside the field its pointer names. copy.js adds copy
// buttons to the text the page already shows.
//
// The server decodes with its decoder. The browser's Service Worker,
// which draws the page itself when a lookup names a file it holds, has
// none: it asks the server's GET /v1/decode/{vin} for every item, once,
// lays the decode out as the server does (contract.NewDecode, over the
// elements the server gave it) and translates the same decode.

// decodePath was the decode form's page: old links to it move to the
// page (moved).
const decodePath = "/ui/decode"

// mainTarget is the layout's main, which the page's answer fills.
const mainTarget = "#ui-main"

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

// pageLinks are the page's links that depend on the running binary.
type pageLinks struct {
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

// decodeView is a decode as the page shows it.
type decodeView struct {
	VIN             string
	ErrorCodes      []int
	CheckDigitValid bool
	// JSON is the same request on GET /v1/decode/{vin}; NHTSA is the VIN
	// on vPIC's own decoder.
	JSON, NHTSA string
	Groups      []elementGroup
	// VPIC marks the vpic shape, whose empty values are hidden unless
	// asked for; Hidden counts those not shown.
	VPIC   bool
	Hidden int
	Codes  []errorCodeText
	// AdditionalErrorText is the decode's AdditionalErrorText (element 156).
	AdditionalErrorText string
	// Input and Suggested are the VIN and its corrected_vin, by character,
	// when the decode suggests one; DifferAt names the positions in which
	// they differ.
	Input, Suggested []vinChar
	DifferAt         string
	Copy             decodeCopies
	// Summary is the vehicle in one line: year, make, model, trim or
	// series, engine and plant.
	Summary string
	// Items are every row the decoder emitted, for a full request.
	Items []decodeItem
}

// decodeItem is one item as the page's table shows it.
type decodeItem struct {
	ElementID                        int32
	Variable                         string
	Value, AttributeID, Source, Keys contract.Null[string]
	PatternID, VinSchemaID           contract.Null[int32]
}

// vPIC's element ids the summary line reads.
const (
	elementCylinders    = 9
	elementDisplacement = 13
	elementMake         = 26
	elementModel        = 28
	elementModelYear    = 29
	elementPlantCity    = 31
	elementSeries       = 34
	elementTrim         = 38
	elementPlantState   = 77
)

// summary is dec's vehicle in one line, "" when it names none.
func summary(dec contract.Decode, elements contract.Elements) string {
	get := func(id int32) string { return strings.TrimSpace(attribute(dec, elements, id)) }
	join := func(sep string, parts ...string) string {
		return strings.Join(slices.DeleteFunc(parts, func(s string) bool { return s == "" }), sep)
	}
	trim := get(elementTrim)
	if trim == "" {
		trim = get(elementSeries)
	}
	var displacement, cylinders string
	if d := get(elementDisplacement); d != "" {
		if f, err := strconv.ParseFloat(d, 64); err == nil {
			d = strconv.FormatFloat(f, 'f', 1, 64)
		}
		displacement = d + " L"
	}
	if c := get(elementCylinders); c != "" {
		cylinders = c + " cylinders"
	}
	var plant string
	if p := join(", ", get(elementPlantCity), get(elementPlantState)); p != "" {
		plant = "built in " + p
	}
	return join(" · ", join(" ", get(elementModelYear), get(elementMake), get(elementModel), trim), join(", ", displacement, cylinders), plant)
}

// decodeCopies are the decode's copyable texts, rendered on the server.
type decodeCopies struct {
	// JSON is the decode as the contract layer marshals it; Curl and CLI
	// make the same request on /v1 and the command line.
	JSON, Curl, CLI string
}

// Clean reports a decode without errors: error code 0 alone.
func (d *decodeView) Clean() bool { return len(d.ErrorCodes) == 1 && d.ErrorCodes[0] == 0 }

// errorCodeText is one error code and its text from the data's error-code
// table, "" when the table lacks it.
type errorCodeText struct {
	Code int
	Text string
}

// vinChar is one character of a VIN; Differs marks a position in which
// the VIN and its suggested VIN differ.
type vinChar struct {
	C       string
	Differs bool
}

// elementGroup is one of vPIC's element groups; Open shows it unfolded.
type elementGroup struct {
	Name string
	Open bool
	Rows []elementRow
}

// elementRow is one element: its key in the decode's shape and its non-empty
// values, none for an empty value shown. List marks a multi-valued element
// of the nitrovin shape, shown as a list however many values it has.
type elementRow struct {
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

// elementGroups are dec's elements by group in vPIC's group order, General and
// Engine open, each group's elements in element id order: in the vpic shape
// every element, its empty values counted in hidden unless showEmpty, and in
// the nitrovin shape every decoded element with a non-empty value. A group
// with nothing to show is left out.
func elementGroups(dec contract.Decode, elements contract.Elements, showEmpty bool) (groups []elementGroup, hidden int) {
	at := map[string]int{}
	add := func(e contract.Element, row elementRow) {
		name := e.GroupName.V
		if name == "" {
			name = otherGroup
		}
		i, ok := at[name]
		if !ok {
			i, at[name] = len(groups), len(groups)
			groups = append(groups, elementGroup{Name: name, Open: name == "General" || name == "Engine"})
		}
		groups[i].Rows = append(groups[i].Rows, row)
	}
	switch attrs := dec.Attributes.(type) {
	case contract.VpicAttributes:
		for _, e := range elements {
			v := attrs[e.Code]
			switch {
			case v != "":
				add(e, elementRow{Name: e.Code, Values: []string{v}})
			case showEmpty:
				add(e, elementRow{Name: e.Code})
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
					add(e, elementRow{Name: e.Variable, Values: []string{a.Value}})
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
				add(e, elementRow{Name: e.Variable, Values: values, List: true})
			}
		}
	}
	slices.SortStableFunc(groups, func(a, b elementGroup) int {
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
func diffVINs(vin, suggested string) (in, out []vinChar, at string) {
	a, b := []rune(vin), []rune(suggested)
	var positions []string
	for i := range max(len(a), len(b)) {
		differs := i >= len(a) || i >= len(b) || unicode.ToUpper(a[i]) != unicode.ToUpper(b[i])
		if i < len(a) {
			in = append(in, vinChar{string(a[i]), differs})
		}
		if i < len(b) {
			out = append(out, vinChar{string(b[i]), differs})
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
func newFormLinks(info contract.Info) pageLinks {
	var l pageLinks
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

// links are the page's links: the running binary's, and in the browser
// the data release the server's build named (WithDataRelease).
func (h *Handler) links(info contract.Info) pageLinks {
	l := newFormLinks(info)
	if h.dataRelease != "" {
		l.DataRelease = h.dataRelease
	}
	return l
}

// decoded is the decode of vin as sent, asked with query, the decode's
// own (GET /v1/decode/{vin}'s), as the page shows it, and in the browser
// the same decode as a translation reads it (nil on the server, whose
// translator decodes itself). The error is a refusal (a
// *contract.Problem, or in the browser a *pageError when the server
// cannot be asked) or a fault.
func (h *Handler) decoded(r *http.Request, vin string, query url.Values, showEmpty bool) (*decodeView, *contract.VINDecode, error) {
	req, err := contract.ParseQuery(vin, query)
	if err != nil {
		return nil, nil, err
	}
	var dec contract.Decode
	var vd *contract.VINDecode
	if h.b != nil {
		dec, err = h.b.Decode(req)
	} else {
		dec, vd, err = h.lib.decodeVIN(vin, req)
	}
	if err != nil {
		return nil, nil, err
	}
	body, err := json.Marshal(dec) // as GET /v1/decode/{vin} writes it
	if err != nil {
		return nil, nil, err
	}
	elements := h.p.Info().Elements
	jsonURL := "/v1/decode/" + url.PathEscape(vin)
	if len(query) > 0 {
		jsonURL += "?" + query.Encode()
	}
	nhtsa := "https://vpic.nhtsa.dot.gov/decoder/VinDecoder?VIN=" + url.QueryEscape(dec.VIN)
	if req.ModelYear.Valid {
		nhtsa += "&ModelYear=" + strconv.Itoa(int(req.ModelYear.V))
	}
	d := &decodeView{
		VIN: dec.VIN, ErrorCodes: dec.ErrorCodes, CheckDigitValid: dec.CheckDigitValid,
		JSON: jsonURL, NHTSA: nhtsa,
		AdditionalErrorText: attribute(dec, elements, elementAdditionalErrorText),
		Copy: decodeCopies{
			JSON: string(body),
			Curl: "curl " + shellQuote(requestOrigin(r)+jsonURL),
			CLI:  cliCommand(vin, req, query.Get("now")),
		},
		Summary: summary(dec, elements),
	}
	d.Groups, d.Hidden = elementGroups(dec, elements, showEmpty)
	_, d.VPIC = dec.Attributes.(contract.VpicAttributes)
	for _, c := range dec.ErrorCodes {
		d.Codes = append(d.Codes, errorCodeText{c, h.errorTexts[c]})
	}
	if dec.CorrectedVIN.Valid {
		d.Input, d.Suggested, d.DifferAt = diffVINs(dec.VIN, dec.CorrectedVIN.V)
	}
	for _, it := range dec.Items {
		d.Items = append(d.Items, decodeItem{
			ElementID: it.ElementID, Variable: it.Variable, Value: it.Value, AttributeID: it.AttributeID,
			Source: it.Source, Keys: it.Keys, PatternID: it.PatternID, VinSchemaID: it.VinSchemaID,
		})
	}
	return d, vd, nil
}

// requestOrigin is the scheme and host r was sent to, as the client named
// them: https behind a proxy that says so, and in the browser's worker,
// whose requests carry their whole URL, that URL's.
func requestOrigin(r *http.Request) string {
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" || r.URL.Scheme == "https" {
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
