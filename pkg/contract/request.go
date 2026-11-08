package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Shape is the layout of a decode's attributes.
type Shape string

const (
	// ShapeVPIC is vPIC's DecodeVinValues layout, keyed by element code; the
	// default.
	ShapeVPIC Shape = "vpic"
	// ShapeNitrovin is keyed by variable name, with element ids, attribute ids
	// and values[] for multi-valued elements.
	ShapeNitrovin Shape = "nitrovin"
)

// Request limits from docs/api/README.md.
const (
	// MaxVINBytes caps a request's VIN after trimming: request hygiene, not a
	// VIN rule.
	MaxVINBytes = 64
	// MaxBatch caps the requests in one batch.
	MaxBatch = 1000
	// MaxBody caps a batch body in bytes.
	MaxBody = 1 << 20
)

// Request is one decode request, the same on every surface: an element of
// a batch, a GET's path and query, a stdio line, an MCP call's arguments, a
// C ABI call. Its zero values are the contract's defaults, except VIN, which
// is required.
type Request struct {
	// VIN as sent; the decoder trims and sanitizes it. 1 to MaxVINBytes
	// bytes after trimming spaces.
	VIN string
	// ModelYear is the model-year hint, vPIC's modelyear.
	ModelYear Null[int32]
	// Full adds the items to the decode.
	Full bool
	// Shape is the layout of the attributes; empty means ShapeVPIC.
	Shape Shape
	// Now is the frozen clock. Zero means the wall clock, read by the
	// caller once per request (once per batch, once per CLI run) and never
	// by the engine.
	Now time.Time
}

// trimVIN trims a VIN as the decoder does before decoding: spaces only,
// Postgres's trim() (spvindecode's first statement), not every whitespace.
// The decode's vin is this string sanitized, which never grows it, so a
// request within MaxVINBytes decodes to a vin within it too.
func trimVIN(vin string) string { return strings.Trim(vin, " ") }

// Validate checks r against the request rules a Go caller can break: the
// VIN's length after trimming, the shape and the range of Now. Parsed
// requests are already valid. The error is a *Problem.
func (r Request) Validate() error {
	if p := r.validate(""); p != nil {
		return p
	}
	return nil
}

func (r Request) validate(prefix string) *Problem {
	switch n := len(trimVIN(r.VIN)); {
	case n == 0:
		return NewProblem(InvalidRequest, prefix+"/vin", "vin is empty after trimming spaces")
	case n > MaxVINBytes:
		return NewProblem(InvalidRequest, prefix+"/vin", fmt.Sprintf("vin is %d bytes after trimming spaces; the limit is %d", n, MaxVINBytes))
	}
	switch r.Shape {
	case "", ShapeVPIC, ShapeNitrovin:
	default:
		return NewProblem(InvalidRequest, prefix+"/shape", fmt.Sprintf("shape must be %q or %q", ShapeVPIC, ShapeNitrovin))
	}
	if !r.Now.IsZero() {
		if y := r.Now.UTC().Year(); y < 0 || y > 9999 {
			return NewProblem(InvalidRequest, prefix+"/now", "now must fall in years 0000-9999 UTC")
		}
	}
	return nil
}

// ParseRequest parses one request object: a DecodeRequest of
// decode.schema.json. Unknown fields are refused. The error is a *Problem.
func ParseRequest(data []byte) (Request, error) {
	if !json.Valid(data) {
		return Request{}, NewProblem(MalformedJSON, "", "the request is not JSON")
	}
	r, _, p := parseObject(data, "", false)
	if p != nil {
		return Request{}, p
	}
	return r, nil
}

// ParseBatch parses the body of POST /v1/decode: an array of 1 to MaxBatch
// request objects in at most MaxBody bytes. A problem's pointer names the
// item ("/3/vin"). The error is a *Problem.
func ParseBatch(data []byte) ([]Request, error) {
	if len(data) > MaxBody {
		return nil, NewProblem(TooLarge, "", fmt.Sprintf("the body is over %d bytes", MaxBody))
	}
	if !json.Valid(data) {
		return nil, NewProblem(MalformedJSON, "", "the body is not JSON")
	}
	if firstByte(data) != '[' {
		return nil, NewProblem(InvalidRequest, "", "a batch is a JSON array of requests")
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, NewProblem(InvalidRequest, "", err.Error())
	}
	switch {
	case len(raw) == 0:
		return nil, NewProblem(InvalidRequest, "", "the batch is empty")
	case len(raw) > MaxBatch:
		return nil, NewProblem(TooLarge, "", fmt.Sprintf("the batch has %d requests; the limit is %d", len(raw), MaxBatch))
	}
	reqs := make([]Request, len(raw))
	for i, item := range raw {
		r, _, p := parseObject(item, "/"+strconv.Itoa(i), false)
		if p != nil {
			return nil, p
		}
		reqs[i] = r
	}
	return reqs, nil
}

// ParseStdio parses one line of `nitrovin stdio`, a StdioRequest: a request
// object plus an optional id, any JSON value. id is the line's id whenever
// it can be read, refused lines included, so the error line can echo it;
// nil when the line has none or is not JSON. The error is a *Problem.
func ParseStdio(line []byte) (id json.RawMessage, r Request, err error) {
	if !json.Valid(line) {
		return nil, Request{}, NewProblem(MalformedJSON, "", "the line is not JSON")
	}
	r, id, p := parseObject(line, "", true)
	if p != nil {
		return id, Request{}, p
	}
	return id, r, nil
}

// ParseQuery parses GET /v1/decode/{vin}: the path's VIN and the query's
// model_year, full, shape and now, each at most once. An unknown parameter
// is refused; a problem's pointer is "/vin" or "/<parameter>". The error is
// a *Problem.
func ParseQuery(vin string, query url.Values) (Request, error) {
	r := Request{VIN: vin}
	for _, name := range slices.Sorted(maps.Keys(query)) {
		ptr := pointerTo("", name)
		vs := query[name]
		if len(vs) != 1 {
			return Request{}, NewProblem(InvalidRequest, ptr, name+" is given more than once")
		}
		v := vs[0]
		switch name {
		case "model_year":
			y, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				return Request{}, NewProblem(InvalidRequest, ptr, "model_year must be a 32-bit integer")
			}
			r.ModelYear = Value(int32(y))
		case "full":
			switch v {
			case "true":
				r.Full = true
			case "false":
			default:
				return Request{}, NewProblem(InvalidRequest, ptr, "full must be true or false")
			}
		case "shape":
			r.Shape = Shape(v)
		case "now":
			t, err := ParseNow(v)
			if err != nil {
				return Request{}, NewProblem(InvalidRequest, ptr, err.Error())
			}
			r.Now = t
		default:
			return Request{}, NewProblem(InvalidRequest, ptr, "unknown query parameter "+strconv.Quote(name))
		}
	}
	if p := r.validate(""); p != nil {
		return Request{}, p
	}
	return r, nil
}

// requestFields are DecodeRequest's properties, in the order a refusal
// checks them.
var requestFields = []string{"vin", "model_year", "full", "shape", "now"}

// parseObject parses one valid JSON value as a request object. prefix is
// the JSON Pointer to it. With stdio, an "id" member is allowed and
// returned; it is returned even when the request is refused.
func parseObject(data []byte, prefix string, stdio bool) (Request, json.RawMessage, *Problem) {
	if firstByte(data) != '{' {
		return Request{}, nil, NewProblem(InvalidRequest, prefix, "a request is a JSON object")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Request{}, nil, NewProblem(InvalidRequest, prefix, err.Error())
	}
	var id json.RawMessage
	if stdio {
		id = fields["id"]
		delete(fields, "id")
	}
	// Unknown fields first, in byte order, so the refusal is deterministic.
	for _, name := range slices.Sorted(maps.Keys(fields)) {
		if !slices.Contains(requestFields, name) {
			return Request{}, id, NewProblem(InvalidRequest, pointerTo(prefix, name), "unknown field "+strconv.Quote(name))
		}
	}

	var r Request
	raw, ok := fields["vin"]
	if !ok {
		return Request{}, id, NewProblem(InvalidRequest, prefix+"/vin", "vin is required")
	}
	if !jsonString(raw, &r.VIN) {
		return Request{}, id, NewProblem(InvalidRequest, prefix+"/vin", "vin must be a string")
	}
	if raw, ok := fields["model_year"]; ok && !bytes.Equal(raw, []byte("null")) {
		y, ok := jsonInt32(raw)
		if !ok {
			return Request{}, id, NewProblem(InvalidRequest, prefix+"/model_year", "model_year must be a 32-bit integer or null")
		}
		r.ModelYear = Value(y)
	}
	if raw, ok := fields["full"]; ok {
		switch string(raw) {
		case "true":
			r.Full = true
		case "false":
		default:
			return Request{}, id, NewProblem(InvalidRequest, prefix+"/full", "full must be true or false")
		}
	}
	if raw, ok := fields["shape"]; ok {
		var s string
		if !jsonString(raw, &s) || (Shape(s) != ShapeVPIC && Shape(s) != ShapeNitrovin) {
			return Request{}, id, NewProblem(InvalidRequest, prefix+"/shape", fmt.Sprintf("shape must be %q or %q", ShapeVPIC, ShapeNitrovin))
		}
		r.Shape = Shape(s)
	}
	if raw, ok := fields["now"]; ok {
		var s string
		if !jsonString(raw, &s) {
			return Request{}, id, NewProblem(InvalidRequest, prefix+"/now", "now must be a string")
		}
		t, err := ParseNow(s)
		if err != nil {
			return Request{}, id, NewProblem(InvalidRequest, prefix+"/now", err.Error())
		}
		r.Now = t
	}
	if p := r.validate(prefix); p != nil {
		return Request{}, id, p
	}
	return r, id, nil
}

// firstByte is the first non-whitespace byte of a JSON text.
func firstByte(data []byte) byte {
	data = bytes.TrimLeft(data, " \t\r\n")
	if len(data) == 0 {
		return 0
	}
	return data[0]
}

// jsonString decodes raw into s when raw is a JSON string.
func jsonString(raw json.RawMessage, s *string) bool {
	return firstByte(raw) == '"' && json.Unmarshal(raw, s) == nil
}

// jsonInt32 reads a JSON number whose value is an integer in int32's range.
// JSON Schema's "integer" is a mathematical integer, so 2003.0 and 2.003e3
// are 2003.
func jsonInt32(raw json.RawMessage) (int32, bool) {
	if c := firstByte(raw); c != '-' && (c < '0' || c > '9') {
		return 0, false
	}
	s := string(raw)
	if n, err := strconv.ParseInt(s, 10, 32); err == nil {
		return int32(n), true
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f != math.Trunc(f) || f < math.MinInt32 || f > math.MaxInt32 {
		return 0, false
	}
	return int32(f), true
}

// nowPattern is DecodeRequestFields.now's pattern in decode.schema.json.
var nowPattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}(T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?([Zz]|[+-][0-9]{2}:[0-9]{2}))?$`)

// ParseNow parses a request's now: an RFC 3339 instant, or a date
// (YYYY-MM-DD) meaning 00:00:00Z that day. Fractional seconds are
// truncated, never rounded, and the result is in UTC.
func ParseNow(s string) (time.Time, error) {
	m := nowPattern.FindStringSubmatchIndex(s)
	if m == nil {
		return time.Time{}, fmt.Errorf("now must be an RFC 3339 instant or a YYYY-MM-DD date, got %q", s)
	}
	layout := time.DateOnly
	if m[2] >= 0 {
		layout = time.RFC3339
		if m[4] >= 0 { // drop the fraction: truncation
			s = s[:m[4]] + s[m[5]:]
		}
		if s[len(s)-1] == 'z' {
			s = s[:len(s)-1] + "Z"
		}
	}
	t, err := time.Parse(layout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("now is not a valid instant: %v", err)
	}
	t = t.UTC()
	if y := t.Year(); y < 0 || y > 9999 {
		return time.Time{}, fmt.Errorf("now must fall in years 0000-9999 UTC, got %q", s)
	}
	return t, nil
}
