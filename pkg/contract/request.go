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
	"unicode/utf8"
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
	// MaxVINChars caps a request's VIN after trimming: spvindecode stores
	// the trimmed VIN in a varchar(17) and raises on a longer one, so a
	// longer VIN has no answer to give.
	MaxVINChars = 17
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
	// VIN as sent; the decoder trims and sanitizes it. 1 to MaxVINChars
	// characters after trimming spaces.
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
// request within MaxVINChars decodes to a vin within it too.
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
	if p := checkVIN(r.VIN, prefix+"/vin", "vin"); p != nil {
		return p
	}
	switch r.Shape {
	case "", ShapeVPIC, ShapeNitrovin:
	default:
		return NewProblem(InvalidRequest, prefix+"/shape", fmt.Sprintf("shape must be %q or %q", ShapeVPIC, ShapeNitrovin))
	}
	return checkNow(r.Now, prefix+"/now")
}

// checkVIN refuses, at ptr, a VIN (the request's field name) that is empty
// or over MaxVINChars after trimming.
func checkVIN(vin, ptr, name string) *Problem {
	switch n := utf8.RuneCountInString(trimVIN(vin)); {
	case n == 0:
		return NewProblem(InvalidRequest, ptr, name+" is empty after trimming spaces")
	case n > MaxVINChars:
		return NewProblem(InvalidRequest, ptr, fmt.Sprintf("%s is %d characters after trimming spaces; the limit is %d", name, n, MaxVINChars))
	}
	return nil
}

// checkNow refuses, at ptr, a clock outside years 0000-9999 UTC.
func checkNow(now time.Time, ptr string) *Problem {
	if !now.IsZero() {
		if y := now.UTC().Year(); y < 0 || y > 9999 {
			return NewProblem(InvalidRequest, ptr, "now must fall in years 0000-9999 UTC")
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
	r, p := parseObject(data, "")
	if p != nil {
		return Request{}, p
	}
	return r, nil
}

// ParseBatch parses the body of POST /v1/decode: an array of 1 to MaxBatch
// request objects in at most MaxBody bytes. A problem's pointer names the
// item ("/3/vin"). The error is a *Problem.
func ParseBatch(data []byte) ([]Request, error) {
	raw, p := parseBatch(data)
	if p != nil {
		return nil, p
	}
	reqs := make([]Request, len(raw))
	for i, item := range raw {
		r, p := parseObject(item, "/"+strconv.Itoa(i))
		if p != nil {
			return nil, p
		}
		reqs[i] = r
	}
	return reqs, nil
}

// parseBatch reads a batch body, POST /v1/decode's or POST /v1/translate's,
// into its items: a JSON array of 1 to MaxBatch values in at most MaxBody
// bytes.
func parseBatch(data []byte) ([]json.RawMessage, *Problem) {
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
	return raw, nil
}

// Op is what a StdioRequest asks for (ADR 0012).
type Op string

// The ops of a StdioRequest.
const (
	OpDecode    Op = "decode"
	OpTranslate Op = "translate"
	OpList      Op = "list"
)

// StdioRequest is one line of `nitrovin stdio`, and the request_json of
// nitrovin_decode: its op and that op's request, the other two zero.
type StdioRequest struct {
	Op        Op
	Decode    Request
	Translate TranslateRequest
	List      ListRequest
}

// ParseStdio parses one line of `nitrovin stdio`, a StdioRequest: an
// optional op, OpDecode when absent, and an optional id, any JSON value,
// beside that op's request object. id is the line's id whenever it can be
// read, refused lines included, so the error line can echo it; nil when the
// line has none or is not JSON. The error is a *Problem.
func ParseStdio(line []byte) (id json.RawMessage, r StdioRequest, err error) {
	if !json.Valid(line) {
		return nil, StdioRequest{}, NewProblem(MalformedJSON, "", "the line is not JSON")
	}
	fields, p := objectFields(line, "", "a request", nil)
	if p != nil {
		return nil, StdioRequest{}, p
	}
	id = fields["id"]
	delete(fields, "id")
	r.Op = OpDecode
	if raw, ok := fields["op"]; ok {
		delete(fields, "op")
		var s string
		if !jsonString(raw, &s) || (Op(s) != OpDecode && Op(s) != OpTranslate && Op(s) != OpList) {
			return id, StdioRequest{}, NewProblem(InvalidRequest, "/op", fmt.Sprintf("op must be %q, %q or %q", OpDecode, OpTranslate, OpList))
		}
		r.Op = Op(s)
	}
	switch r.Op {
	case OpTranslate:
		if p = unknownField(fields, "", translateFields); p == nil {
			r.Translate, p = readTranslate(fields, "")
		}
	case OpList:
		if p = unknownField(fields, "", listFields); p == nil {
			r.List, p = readList(fields)
		}
	default:
		if p = unknownField(fields, "", requestFields); p == nil {
			r.Decode, p = readRequest(fields, "")
		}
	}
	if p != nil {
		return id, StdioRequest{}, p
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
// the JSON Pointer to it.
func parseObject(data []byte, prefix string) (Request, *Problem) {
	fields, p := objectFields(data, prefix, "a request", requestFields)
	if p != nil {
		return Request{}, p
	}
	return readRequest(fields, prefix)
}

// readRequest reads a request object's fields, unknown ones already
// refused; prefix is the JSON Pointer to the object.
func readRequest(fields map[string]json.RawMessage, prefix string) (Request, *Problem) {
	var r Request
	raw, ok := fields["vin"]
	if !ok {
		return Request{}, NewProblem(InvalidRequest, prefix+"/vin", "vin is required")
	}
	if !jsonString(raw, &r.VIN) {
		return Request{}, NewProblem(InvalidRequest, prefix+"/vin", "vin must be a string")
	}
	if raw, ok := fields["model_year"]; ok && !bytes.Equal(raw, []byte("null")) {
		y, ok := jsonInt32(raw)
		if !ok {
			return Request{}, NewProblem(InvalidRequest, prefix+"/model_year", "model_year must be a 32-bit integer or null")
		}
		r.ModelYear = Value(y)
	}
	if raw, ok := fields["full"]; ok {
		switch string(raw) {
		case "true":
			r.Full = true
		case "false":
		default:
			return Request{}, NewProblem(InvalidRequest, prefix+"/full", "full must be true or false")
		}
	}
	if raw, ok := fields["shape"]; ok {
		var s string
		if !jsonString(raw, &s) || (Shape(s) != ShapeVPIC && Shape(s) != ShapeNitrovin) {
			return Request{}, NewProblem(InvalidRequest, prefix+"/shape", fmt.Sprintf("shape must be %q or %q", ShapeVPIC, ShapeNitrovin))
		}
		r.Shape = Shape(s)
	}
	if raw, ok := fields["now"]; ok {
		var s string
		if !jsonString(raw, &s) {
			return Request{}, NewProblem(InvalidRequest, prefix+"/now", "now must be a string")
		}
		t, err := ParseNow(s)
		if err != nil {
			return Request{}, NewProblem(InvalidRequest, prefix+"/now", err.Error())
		}
		r.Now = t
	}
	if p := r.validate(prefix); p != nil {
		return Request{}, p
	}
	return r, nil
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
