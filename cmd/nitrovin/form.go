package main

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// The decode form at GET / is for people with a browser. It sits outside
// /v1 and openapi.yaml: not part of the contract, free to change. Its query
// is GET /v1/decode/{vin}'s with the VIN as a parameter, so a decode is a
// shareable URL, and its refusals are contract.ParseQuery's, each shown
// beside the field its pointer names.

//go:embed form.html
var formHTML string

var formTemplate = template.Must(template.New("form").Parse(formHTML))

// formCSP lets the page use its own inline styles and nothing else: no
// script, no third-party resource.
const formCSP = "default-src 'self'; style-src 'unsafe-inline'"

// formFields are the query parameters the form has a field for; a refusal
// pointing elsewhere shows above the form.
var formFields = []string{"vin", "model_year", "shape", "full"}

// formPage is what form.html renders.
type formPage struct {
	VIN, ModelYear, Shape string
	Full                  bool
	// Errors holds a refusal's detail under its field's name, or under ""
	// when no field holds the offending value.
	Errors                    map[string]string
	Decode                    *formDecode
	DataMonth, DecoderVersion string
}

// formDecode is a decode as the page shows it.
type formDecode struct {
	contract.Decode
	// JSON is the same request on GET /v1/decode/{vin}.
	JSON string
	Rows []formRow
}

// Clean reports a decode without errors: error code 0 alone.
func (d *formDecode) Clean() bool { return len(d.ErrorCodes) == 1 && d.ErrorCodes[0] == 0 }

// formRow is one decoded element: its key in the decode's shape and its
// non-empty values. List marks a multi-valued element of the nitrovin
// shape, shown as a list however many values it has.
type formRow struct {
	Name   string
	Values []string
	List   bool
}

// formRows are the -o text table's rows in the decode's shape: one per
// element with a non-empty value, in element id order.
func formRows(dec contract.Decode, elements contract.Elements) []formRow {
	var rows []formRow
	switch attrs := dec.Attributes.(type) {
	case contract.VpicAttributes:
		for _, e := range elements {
			if v := attrs[e.Code]; v != "" {
				rows = append(rows, formRow{Name: e.Code, Values: []string{v}})
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
					rows = append(rows, formRow{Name: e.Variable, Values: []string{a.Value}})
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
				rows = append(rows, formRow{Name: e.Variable, Values: values, List: true})
			}
		}
	}
	return rows
}

// form is GET / (and HEAD /): the empty form without a query, otherwise
// the form filled in from the query and the decode, or the refusal, below.
func (a *api) form(w http.ResponseWriter, r *http.Request) {
	info := a.d.Info()
	page := formPage{Shape: string(contract.ShapeVPIC), DataMonth: info.DataMonth, DecoderVersion: info.DecoderVersion}
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
	jsonURL := "/v1/decode/" + url.PathEscape(page.VIN)
	if len(query) > 0 {
		jsonURL += "?" + query.Encode()
	}
	page.Decode = &formDecode{Decode: dec, JSON: jsonURL, Rows: formRows(dec, info.Elements)}
	a.page(w, http.StatusOK, &page)
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
