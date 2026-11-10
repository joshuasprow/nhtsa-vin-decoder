package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The HTTP contract test drives every route and status openapi.yaml
// declares, and validates every response body against the schema the
// served openapi.json declares for that route and status. The built binary
// answers every case but the 500s, which need a fault injected into the
// in-process API. Without NITROVIN_ARTIFACT, TestServeRefusals runs the
// cases that need no data in-process.
//
//	NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./cmd/nitrovin -run TestServe

var updateOpenAPI = flag.Bool("update-openapi", false, "rewrite openapi.json from docs/api/openapi.yaml and decode.schema.json")

// TestOpenAPIBundle: the served openapi.json is docs/api/openapi.yaml
// bundled with decode.schema.json.
func TestOpenAPIBundle(t *testing.T) {
	spec, err := os.ReadFile("../../docs/api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := os.ReadFile("../../docs/api/decode.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := bundleOpenAPI(spec, schema)
	if err != nil {
		t.Fatal(err)
	}
	if *updateOpenAPI {
		if err := os.WriteFile("openapi.json", got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	if !bytes.Equal(got, openAPI) {
		t.Fatal("openapi.json is not docs/api/openapi.yaml bundled with decode.schema.json; rewrite it: go test ./cmd/nitrovin -run TestOpenAPIBundle -update-openapi")
	}
}

// bundleOpenAPI is the OpenAPI document spec (YAML) as indented JSON in its
// own key order, with schema's (decode.schema.json's) $defs added as
// components.schemas and every reference into them rewritten to point
// there, so the document is self-contained.
func bundleOpenAPI(spec, schema []byte) ([]byte, error) {
	var doc, defs yaml.Node
	if err := yaml.Unmarshal(spec, &doc); err != nil {
		return nil, fmt.Errorf("openapi.yaml: %w", err)
	}
	if err := yaml.Unmarshal(schema, &defs); err != nil { // JSON is YAML
		return nil, fmt.Errorf("decode.schema.json: %w", err)
	}
	components := member(doc.Content[0], "components")
	schemas := member(defs.Content[0], "$defs")
	switch {
	case components == nil:
		return nil, errors.New("openapi.yaml has no components")
	case member(components, "schemas") != nil:
		return nil, errors.New("openapi.yaml has its own components.schemas")
	case schemas == nil:
		return nil, errors.New("decode.schema.json has no $defs")
	}
	components.Content = append(components.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "schemas"}, schemas)

	var compact, out bytes.Buffer
	if err := writeJSON(&compact, &doc); err != nil {
		return nil, err
	}
	if err := json.Indent(&out, compact.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// member is the value of key in the mapping n, nil when absent.
func member(n *yaml.Node, key string) *yaml.Node {
	if n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// bundledRef points ref, a $ref into decode.schema.json from openapi.yaml or
// from within the schema, at the definition's place in components.schemas.
func bundledRef(ref string) (string, error) {
	for _, prefix := range []string{"decode.schema.json#/$defs/", "#/$defs/"} {
		if rest, ok := strings.CutPrefix(ref, prefix); ok {
			return "#/components/schemas/" + rest, nil
		}
	}
	if strings.HasPrefix(ref, "#/components/") {
		return ref, nil
	}
	return "", fmt.Errorf("$ref %q points outside the bundle", ref)
}

// writeJSON writes n as compact JSON, rewriting every $ref with bundledRef.
func writeJSON(b *bytes.Buffer, n *yaml.Node) error {
	switch n.Kind {
	case yaml.DocumentNode:
		return writeJSON(b, n.Content[0])
	case yaml.AliasNode:
		return writeJSON(b, n.Alias)
	case yaml.SequenceNode:
		b.WriteByte('[')
		for i, c := range n.Content {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeJSON(b, c); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case yaml.MappingNode:
		b.WriteByte('{')
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if k.Kind != yaml.ScalarNode || k.ShortTag() == "!!merge" {
				return fmt.Errorf("line %d: a key JSON cannot hold", k.Line)
			}
			if i > 0 {
				b.WriteByte(',')
			}
			writeString(b, k.Value)
			b.WriteByte(':')
			if k.Value == "$ref" && v.Kind == yaml.ScalarNode {
				ref, err := bundledRef(v.Value)
				if err != nil {
					return fmt.Errorf("line %d: %w", v.Line, err)
				}
				writeString(b, ref)
				continue
			}
			if err := writeJSON(b, v); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case yaml.ScalarNode:
		switch n.ShortTag() {
		case "!!str":
			writeString(b, n.Value)
		case "!!null":
			b.WriteString("null")
		case "!!bool", "!!int", "!!float":
			var v any
			if err := n.Decode(&v); err != nil {
				return err
			}
			j, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("line %d: %w", n.Line, err)
			}
			b.Write(j)
		default:
			return fmt.Errorf("line %d: no JSON for a %s", n.Line, n.ShortTag())
		}
	}
	return nil
}

// writeString writes s as a JSON string, leaving <, > and & as they are.
func writeString(b *bytes.Buffer, s string) {
	e := json.NewEncoder(b)
	e.SetEscapeHTML(false)
	e.Encode(s) // a string always encodes
	b.Truncate(b.Len() - 1)
}

func TestListenAddr(t *testing.T) {
	for _, tc := range []struct {
		addr, port, want string
	}{
		{"0.0.0.0:9000", "7000", "0.0.0.0:9000"},
		{"", "7000", ":7000"},
		{"", "", "127.0.0.1:8080"},
	} {
		getenv := func(k string) string {
			if k == "PORT" {
				return tc.port
			}
			return ""
		}
		if got := listenAddr(tc.addr, getenv); got != tc.want {
			t.Errorf("--addr %q, PORT %q: %q; want %q", tc.addr, tc.port, got, tc.want)
		}
	}
}

// apiSpec is the served openapi.json, its schemas compiled from it.
type apiSpec struct {
	doc      any
	compiler *jsonschema.Compiler
}

const specURL = "https://nitrovin.test/v1/openapi.json"

func loadSpec(t *testing.T) *apiSpec {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(openAPI))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource(specURL, doc); err != nil {
		t.Fatal(err)
	}
	return &apiSpec{doc: doc, compiler: c}
}

// at is the value at JSON Pointer ptr, nil when there is none.
func (s *apiSpec) at(ptr string) map[string]any {
	v := s.doc
	for _, tok := range strings.Split(ptr, "/")[1:] {
		tok = strings.ReplaceAll(strings.ReplaceAll(tok, "~1", "/"), "~0", "~")
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[tok]
	}
	m, _ := v.(map[string]any)
	return m
}

func escapeToken(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1")
}

// operations is every "METHOD path status" the document declares.
func (s *apiSpec) operations() []string {
	var ops []string
	for path, item := range s.at("/paths") {
		for method, op := range item.(map[string]any) {
			for status := range op.(map[string]any)["responses"].(map[string]any) {
				ops = append(ops, fmt.Sprintf("%s %s %s", strings.ToUpper(method), path, status))
			}
		}
	}
	slices.Sort(ops)
	return ops
}

// allow is the Allow header a 405 on path must carry: its methods.
func (s *apiSpec) allow(path string) string {
	var methods []string
	for m := range s.at("/paths/" + escapeToken(path)) {
		methods = append(methods, strings.ToUpper(m))
	}
	slices.Sort(methods)
	return strings.Join(methods, ", ")
}

// response is the declared response of op ("GET /healthz") with status: its
// location in the document, following a $ref, and the object.
func (s *apiSpec) response(t *testing.T, op string, status int) (string, map[string]any) {
	t.Helper()
	method, path, _ := strings.Cut(op, " ")
	ptr := fmt.Sprintf("/paths/%s/%s/responses/%d", escapeToken(path), strings.ToLower(method), status)
	r := s.at(ptr)
	if ref, ok := r["$ref"].(string); ok {
		ptr = strings.TrimPrefix(ref, "#")
		r = s.at(ptr)
	}
	if r == nil {
		t.Fatalf("openapi.json declares no %d for %s", status, op)
	}
	return ptr, r
}

// apiCase is one request and the response openapi.yaml says it gets.
type apiCase struct {
	// op is the operation whose declared response this checks:
	// "GET /v1/decode/{vin}". A 404 or 405 is the path's, whatever the
	// target.
	op      string
	method  string
	target  string
	header  http.Header
	body    string
	chunked bool // send the body without a Content-Length
	status  int
	// problem and pointer are a refusal's type and pointer.
	problem contract.ProblemType
	pointer string
	// want is the whole body, when the case knows it.
	want []byte
	// check inspects the body further.
	check func(t *testing.T, body []byte)
}

func (c apiCase) name() string { return c.method + " " + c.target }

// run sends c to base and checks the response against c and the document.
func (s *apiSpec) run(t *testing.T, client *http.Client, base string, c apiCase) {
	t.Helper()
	var body io.Reader
	if c.body != "" {
		body = strings.NewReader(c.body)
		if c.chunked {
			body = io.MultiReader(body)
		}
	}
	req, err := http.NewRequest(c.method, base+c.target, body)
	if err != nil {
		t.Fatalf("%s: %v", c.name(), err)
	}
	for k, vs := range c.header {
		req.Header[k] = vs
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", c.name(), err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatalf("%s: %v", c.name(), err)
	}
	if resp.StatusCode != c.status {
		t.Errorf("%s: %d %s; want %d", c.name(), resp.StatusCode, got, c.status)
		return
	}

	ptr, declared := s.response(t, c.op, c.status)
	content, _ := declared["content"].(map[string]any)
	switch len(content) {
	case 0: // a response without a body: 202
		if len(got) != 0 {
			t.Errorf("%s: body %q; %s declares none", c.name(), got, ptr)
		}
	case 1:
		var mediaType string
		for mediaType = range content {
		}
		if ct := resp.Header.Get("Content-Type"); ct != mediaType {
			t.Errorf("%s: Content-Type %q; %s declares %q", c.name(), ct, ptr, mediaType)
		}
		sch, err := s.compiler.Compile(specURL + "#" + ptr + "/content/" + escapeToken(mediaType) + "/schema")
		if err != nil {
			t.Fatal(err)
		}
		if v, err := jsonschema.UnmarshalJSON(bytes.NewReader(got)); err != nil {
			t.Errorf("%s: body is not JSON: %v: %s", c.name(), err, got)
		} else if err := sch.Validate(v); err != nil {
			t.Errorf("%s: body breaks %s's schema: %v\n%s", c.name(), ptr, err, got)
		}
	default:
		t.Fatalf("%s: %s declares %d media types; the test expects one at most", c.name(), ptr, len(content))
	}

	if headers, _ := declared["headers"].(map[string]any); headers["Allow"] != nil {
		_, path, _ := strings.Cut(c.op, " ")
		if allow, want := resp.Header.Get("Allow"), s.allow(path); allow != want {
			t.Errorf("%s: Allow %q; want %q", c.name(), allow, want)
		}
	}
	for k := range resp.Header {
		if strings.HasPrefix(k, "Access-Control-") {
			t.Errorf("%s: CORS header %s", c.name(), k)
		}
	}
	if c.problem != "" {
		var p contract.Problem
		if err := json.Unmarshal(got, &p); err != nil {
			t.Fatalf("%s: %v", c.name(), err)
		}
		if p.Type != c.problem || p.Status != c.status || p.Title != c.problem.Title() || p.Pointer != c.pointer {
			t.Errorf("%s: problem %+v; want type %s, status %d, pointer %q", c.name(), p, c.problem, c.status, c.pointer)
		}
	}
	if c.want != nil && !bytes.Equal(got, c.want) {
		t.Errorf("%s: body\n%s\nwant\n%s", c.name(), got, c.want)
	}
	if c.check != nil {
		c.check(t, got)
	}
}

const (
	opDecodeOne      = "GET /v1/decode/{vin}"
	opDecodeBatch    = "POST /v1/decode"
	opTranslateOne   = "GET /v1/translate"
	opTranslateBatch = "POST /v1/translate"
	opCatalog        = "GET /v1/vocabularies"
	opEntries        = "GET /v1/vocabularies/{name}/entries"
	opInfo           = "GET /v1/info"
	opOpenAPI        = "GET /v1/openapi.json"
	opHealth         = "GET /healthz"
	opMCP            = "POST /mcp"
)

// batchOf is a batch body of n copies of item.
func batchOf(n int, item string) string {
	return "[" + strings.TrimSuffix(strings.Repeat(item+",", n), ",") + "]"
}

// mcpHeader is what a Streamable HTTP POST carries.
var mcpHeader = http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json, text/event-stream"}}

// refusalCases need no data: every refusal, /healthz, /v1/openapi.json
// and the MCP transport, whose tools/list reads no data.
func refusalCases() []apiCase {
	const vin = "1HGCM82633A004352"
	bad := func(target, pointer string) apiCase {
		return apiCase{op: opDecodeOne, method: "GET", target: target, status: 400, problem: contract.InvalidRequest, pointer: pointer}
	}
	post := func(body string, status int, problem contract.ProblemType, pointer string) apiCase {
		return apiCase{op: opDecodeBatch, method: "POST", target: "/v1/decode", body: body, status: status, problem: problem, pointer: pointer}
	}
	notFound := func(op, method, target string) apiCase {
		return apiCase{op: op, method: method, target: target, status: 404, problem: contract.NotFound}
	}
	notAllowed := func(op, method, target string) apiCase {
		return apiCase{op: op, method: method, target: target, status: 405, problem: contract.MethodNotAllowed}
	}
	// A body one byte over the limit, otherwise a valid batch.
	item := `{"vin":"` + vin + `"}`
	overlong := "[" + item + strings.Repeat(" ", contract.MaxBody-len(item)-1) + "]"

	translateGET := func(query, pointer string) apiCase {
		return apiCase{op: opTranslateOne, method: "GET", target: "/v1/translate" + query, status: 400, problem: contract.InvalidRequest, pointer: pointer}
	}
	translatePOST := func(body string, status int, problem contract.ProblemType, pointer string) apiCase {
		return apiCase{op: opTranslateBatch, method: "POST", target: "/v1/translate", body: body, status: status, problem: problem, pointer: pointer}
	}
	list := func(op, target, pointer string) apiCase {
		return apiCase{op: op, method: "GET", target: target, status: 400, problem: contract.InvalidRequest, pointer: pointer}
	}
	translateItem := `{"from":{"make":476}}`
	translateOverlong := "[" + translateItem + strings.Repeat(" ", contract.MaxBody-len(translateItem)-1) + "]"
	preflight := notAllowed(opDecodeBatch, "OPTIONS", "/v1/decode")
	preflight.header = http.Header{"Origin": {"https://example.com"}, "Access-Control-Request-Method": {"POST"}}
	chunked := post(overlong, 413, contract.TooLarge, "")
	chunked.chunked = true
	return []apiCase{
		{op: opHealth, method: "GET", target: "/healthz", status: 200, want: []byte(`{"status":"ok"}` + "\n")},
		{op: opOpenAPI, method: "GET", target: "/v1/openapi.json", status: 200, want: openAPI},

		bad("/v1/decode/"+vin+"1", "/vin"),
		bad("/v1/decode/%20%20", "/vin"),
		bad("/v1/decode/", "/vin"),
		bad("/v1/decode/"+vin+"?model_year=2003.5", "/model_year"),
		bad("/v1/decode/"+vin+"?model_year=2147483648", "/model_year"),
		bad("/v1/decode/"+vin+"?full=1", "/full"),
		bad("/v1/decode/"+vin+"?shape=flat", "/shape"),
		bad("/v1/decode/"+vin+"?now=yesterday", "/now"),
		bad("/v1/decode/"+vin+"?now=2026-11-01&now=2026-11-02", "/now"),
		bad("/v1/decode/"+vin+"?modelyear=2003", "/modelyear"),
		bad("/v1/decode/"+vin+"?model_year=%zz", ""),
		post(`[{"vin":`, 400, contract.MalformedJSON, ""),
		post(``, 400, contract.MalformedJSON, ""),
		post(item, 400, contract.InvalidRequest, ""),
		post(`[]`, 400, contract.InvalidRequest, ""),
		post(`[{"vin":"A"},{"vin":"B"},{"vin":"C"},{"vin":"`+vin+`1"}]`, 400, contract.InvalidRequest, "/3/vin"),
		post(`[{"vin":"`+vin+`","modelYear":2003}]`, 400, contract.InvalidRequest, "/0/modelYear"),
		post(`[{"vin":"`+vin+`","model_year":"2003"}]`, 400, contract.InvalidRequest, "/0/model_year"),
		{op: opDecodeBatch, method: "POST", target: "/v1/decode?shape=nitrovin", body: `[` + item + `]`, status: 400, problem: contract.InvalidRequest},

		post(batchOf(contract.MaxBatch+1, `{"vin":"A"}`), 413, contract.TooLarge, ""),
		post(overlong, 413, contract.TooLarge, ""),
		chunked,

		notFound(opDecodeOne, "GET", "/index.html"),
		notFound(opDecodeOne, "GET", "/v1/decode/"+vin+"/extra"),
		notFound(opDecodeOne, "GET", "/v1/info/"),
		notFound(opDecodeOne, "GET", "/v2/decode/"+vin),
		notFound(opDecodeBatch, "POST", "/v1/decode/a/b"),

		notAllowed(opDecodeOne, "POST", "/v1/decode/"+vin),
		notAllowed(opDecodeBatch, "GET", "/v1/decode"),
		preflight,
		notAllowed(opInfo, "PUT", "/v1/info"),
		notAllowed(opOpenAPI, "DELETE", "/v1/openapi.json"),
		notAllowed(opHealth, "POST", "/healthz"),

		{op: opMCP, method: "POST", target: "/mcp", header: mcpHeader, body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, status: 200,
			check: func(t *testing.T, body []byte) {
				_, want, err := parseTools(mcpTools)
				if err != nil {
					t.Fatal(err)
				}
				var res struct {
					Result struct{ Tools json.RawMessage }
				}
				if err := json.Unmarshal(body, &res); err != nil || !bytes.Equal(res.Result.Tools, want) {
					t.Errorf("POST /mcp tools/list: %s; want mcp-tools.json's tools %s", body, want)
				}
			}},
		{op: opMCP, method: "POST", target: "/mcp", header: mcpHeader, body: `{"jsonrpc":"2.0","method":"notifications/initialized"}`, status: 202},
		notAllowed(opMCP, "GET", "/mcp"),
		notAllowed(opMCP, "DELETE", "/mcp"),

		translateGET("", "/from"),
		translateGET("?to=yard", "/from"),
		translateGET("?from.vin=", "/from"),
		translateGET("?from.vin="+vin+"1", "/from"),
		translateGET("?from.vin="+vin+"&from.make=476", "/from"),
		translateGET("?from.vin="+vin+"&vin="+vin, "/vin"),
		translateGET("?from.make=476&from.model=1938", "/from"),
		translateGET("?from.make=Dodge", "/from/make"),
		translateGET("?from.make=0", "/from/make"),
		translateGET("?from.model=1938.5", "/from/model"),
		translateGET("?from.ext=classics/corvair", "/from/ext"),
		translateGET("?from.spine=parts", "/from/id"),
		translateGET("?from.vocabulary=yard", "/from/entry"),
		translateGET("?from.entry.make=Dodge", "/from/vocabulary"),
		translateGET("?from.vocabulary=yard&from.entry.make=Dodge&from.make=476", "/from/make"),
		translateGET("?from.make=476&to=yard&to=", "/to/1"),
		translateGET("?from.make=476&model_year=2010.5", "/model_year"),
		translateGET("?from.make=476&model_year=2147483648", "/model_year"),
		translateGET("?from.make=476&now=yesterday", "/now"),
		translateGET("?from.make=476&now=2026-11-01&now=2026-11-02", "/now"),
		translateGET("?from.make=476&from.mke=1", "/from/mke"),
		translateGET("?from.make=476&shape=vpic", "/shape"),
		translateGET("?from.make=%zz", ""),
		translatePOST(`[{"from":`, 400, contract.MalformedJSON, ""),
		translatePOST(translateItem, 400, contract.InvalidRequest, ""),
		translatePOST(`[]`, 400, contract.InvalidRequest, ""),
		translatePOST(`[`+translateItem+`,{"from":{"make":476},"to":"yard"}]`, 400, contract.InvalidRequest, "/1/to"),
		translatePOST(`[`+translateItem+`,{"from":"`+vin+`","vin":"`+vin+`"}]`, 400, contract.InvalidRequest, "/1/vin"),
		{op: opTranslateBatch, method: "POST", target: "/v1/translate?to=yard", body: `[` + translateItem + `]`, status: 400, problem: contract.InvalidRequest},
		translatePOST(batchOf(contract.MaxBatch+1, translateItem), 413, contract.TooLarge, ""),
		translatePOST(translateOverlong, 413, contract.TooLarge, ""),
		list(opCatalog, "/v1/vocabularies?of=yard", "/of"),
		list(opCatalog, "/v1/vocabularies?limit=5&limit=6", "/limit"),
		list(opEntries, "/v1/vocabularies/yard/entries?limit=0", "/limit"),
		list(opEntries, "/v1/vocabularies/yard/entries?limit=1001", "/limit"),
		list(opEntries, "/v1/vocabularies/yard/entries?limit=ten", "/limit"),
		list(opEntries, "/v1/vocabularies/yard/entries?after=", "/after"),
		list(opEntries, "/v1/vocabularies/yard/entries?where.make=Dodge&where.make=Ram", "/where/make"),
		list(opEntries, "/v1/vocabularies/yard/entries?page=2", "/page"),
		notFound(opEntries, "GET", "/v1/vocabularies/yard"),
		notFound(opEntries, "GET", "/v1/vocabularies//entries"),
		notFound(opEntries, "GET", "/v1/vocabularies/yard/entries/1"),
		notFound(opEntries, "GET", "/v1/vocabularies/a/b/entries"),
		notAllowed(opTranslateOne, "PUT", "/v1/translate"),
		notAllowed(opTranslateBatch, "DELETE", "/v1/translate"),
		notAllowed(opCatalog, "POST", "/v1/vocabularies"),
		notAllowed(opEntries, "POST", "/v1/vocabularies/yard/entries"),
	}
}

// wantBody is what the API answers with v: its JSON, as the Go module
// marshals it, and a newline.
func wantBody(t *testing.T, v any, err error) []byte {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// decodeCases need data; their bodies are the Go module's bytes for the
// same requests.
func decodeCases(t *testing.T, d decodeService) []apiCase {
	clock, err := contract.ParseNow(now)
	if err != nil {
		t.Fatal(err)
	}
	marshal := func(v any, err error) []byte {
		t.Helper()
		return wantBody(t, v, err)
	}
	one := func(target string, req contract.Request) apiCase {
		return apiCase{op: opDecodeOne, method: "GET", target: target, status: 200, want: marshal(d.Decode(req))}
	}
	batch := func(body string, reqs []contract.Request) apiCase {
		return apiCase{op: opDecodeBatch, method: "POST", target: "/v1/decode", body: body, status: 200, want: marshal(d.DecodeBatch(reqs))}
	}
	hinted, err := contract.ParseNow("2026-11-08T12:30:45.999+02:00")
	if err != nil {
		t.Fatal(err)
	}
	return []apiCase{
		{op: opInfo, method: "GET", target: "/v1/info", status: 200, want: marshal(d.Info(), nil)},
		one("/v1/decode/1HGCM82633A004352?now="+now, contract.Request{VIN: "1HGCM82633A004352", Now: clock}),
		one("/v1/decode/1HGCM82633A004352?model_year=2010&full=true&shape=nitrovin&now=2026-11-08T12:30:45.999%2B02:00",
			contract.Request{VIN: "1HGCM82633A004352", ModelYear: contract.Value[int32](2010), Full: true, Shape: contract.ShapeNitrovin, Now: hinted}),
		one("/v1/decode/%20%205yj3e1ea2jf000316%20?full=false&shape=vpic&now=2026-09-01",
			contract.Request{VIN: "  5yj3e1ea2jf000316 ", Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)}),
		one("/v1/decode/1HG%2FCM8?now="+now, contract.Request{VIN: "1HG/CM8", Now: clock}),
		{op: opDecodeOne, method: "GET", target: "/v1/decode/1HGCM82633A004352", status: 200, check: func(t *testing.T, body []byte) {
			var dec struct{ Now string }
			if json.Unmarshal(body, &dec) != nil || dec.Now == now {
				t.Errorf("a decode without now: now %q; want the wall clock", dec.Now)
			}
		}},
		batch(`[{"vin":"1HGCM82633A004352","now":"`+now+`"},`+
			`{"vin":"1HGCM82633A004352","model_year":2010,"shape":"nitrovin","now":"`+now+`"},`+
			`{"vin":"5YJ3E1EA2JF000316","full":true,"now":"2026-09-01"}]`,
			[]contract.Request{
				{VIN: "1HGCM82633A004352", Now: clock},
				{VIN: "1HGCM82633A004352", ModelYear: contract.Value[int32](2010), Shape: contract.ShapeNitrovin, Now: clock},
				{VIN: "5YJ3E1EA2JF000316", Full: true, Now: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},
			}),
		// The wall clock is read once for the whole batch.
		{op: opDecodeBatch, method: "POST", target: "/v1/decode", body: batchOf(contract.MaxBatch, `{"vin":"1HGCM82633A004352"}`), status: 200, check: func(t *testing.T, body []byte) {
			var decs []struct{ Now string }
			if err := json.Unmarshal(body, &decs); err != nil {
				t.Fatal(err)
			}
			if len(decs) != contract.MaxBatch {
				t.Fatalf("%d decodes; want %d", len(decs), contract.MaxBatch)
			}
			for i, dec := range decs {
				if dec.Now != decs[0].Now {
					t.Fatalf("decode %d: now %q; decode 0: %q; want one clock reading", i, dec.Now, decs[0].Now)
				}
			}
		}},
	}
}

// vocabCases need data and testdata/vocab loaded; their bodies are the Go
// module's bytes for the same requests, refusals included. Each GET
// /v1/translate asks what one item of the POST asks, and the POST answers
// exactly the GETs' answers, in order.
func vocabCases(t *testing.T, svc decodeService) []apiCase {
	const vin = "1D7RV1GT5AS123456"
	// refused is c answered with the problem err, the Go module's refusal.
	refused := func(c apiCase, err error) apiCase {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: the Go module answers; want a refusal", c.name())
		}
		p := contract.AsProblem(err)
		c.status, c.problem, c.pointer, c.want = p.Status, p.Type, p.Pointer, wantBody(t, p, nil)
		return c
	}
	var cases []apiCase
	var items, answers []string
	for _, c := range []struct{ query, item string }{
		{"from.vin=" + vin + "&to=yard", `{"from":"` + vin + `","to":["yard"]}`},
		{"from.vocabulary=yard&from.entry.make=Dodge&from.entry.model=Ram+1500&vin=" + vin + "&to=yard",
			`{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"vin":"` + vin + `","to":["yard"]}`},
		{"from.vocabulary=yard&from.entry.make=+dodge&from.entry.model=RAM%20%201500&model_year=2010",
			`{"from":{"vocabulary":"yard","entry":{"make":" dodge","model":"RAM  1500"}},"model_year":2010}`},
		{"from.make=476&to=yard&to=yard", `{"from":{"make":476},"to":["yard","yard"]}`},
		{"from.model=1938&model_year=2012&to=yard", `{"from":{"model":1938},"model_year":2012,"to":["yard"]}`},
		{"from.ext=ext:classics/corvair&to=yard", `{"from":{"ext":"ext:classics/corvair"},"to":["yard"]}`},
		{"from.spine=parts&from.id=brake-pads&to=listing", `{"from":{"spine":"parts","id":"brake-pads"},"to":["listing"]}`},
		{"from.vocabulary=listing&from.entry.category=Mystery+Box&to=listing", `{"from":{"vocabulary":"listing","entry":{"category":"Mystery Box"}},"to":["listing"]}`},
		{"from.vocabulary=yard&from.entry.make=Tucker&from.entry.model=48&to=yard", `{"from":{"vocabulary":"yard","entry":{"make":"Tucker","model":"48"}},"to":["yard"]}`},
	} {
		item := strings.TrimSuffix(c.item, "}") + `,"now":"` + now + `"}`
		req, err := contract.ParseTranslateRequest([]byte(item))
		if err != nil {
			t.Fatalf("%s: %v", item, err)
		}
		tr, err := svc.Translate(req)
		want := wantBody(t, tr, err)
		cases = append(cases, apiCase{op: opTranslateOne, method: "GET", target: "/v1/translate?" + c.query + "&now=" + now, status: 200, want: want})
		items = append(items, item)
		answers = append(answers, string(bytes.TrimSuffix(want, []byte("\n"))))
	}
	cases = append(cases, apiCase{op: opTranslateBatch, method: "POST", target: "/v1/translate", body: "[" + strings.Join(items, ",") + "]", status: 200,
		want: []byte("[" + strings.Join(answers, ",") + "]\n")})

	translate := func(query, item string) apiCase {
		t.Helper()
		req, err := contract.ParseTranslateRequest([]byte(item))
		if err != nil {
			t.Fatalf("%s: %v", item, err)
		}
		_, err = svc.Translate(req)
		return refused(apiCase{op: opTranslateOne, method: "GET", target: "/v1/translate?" + query}, err)
	}
	translateBatch := func(body string) apiCase {
		t.Helper()
		reqs, err := contract.ParseTranslateBatch([]byte(body))
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		_, err = svc.TranslateBatch(reqs)
		return refused(apiCase{op: opTranslateBatch, method: "POST", target: "/v1/translate", body: body}, err)
	}
	cases = append(cases,
		translate("from.make=476&to=listing", `{"from":{"make":476},"to":["listing"]}`),
		translate("from.make=476&to=vpic", `{"from":{"make":476},"to":["vpic"]}`),
		translate("from.vocabulary=nope&from.entry.make=Dodge", `{"from":{"vocabulary":"nope","entry":{"make":"Dodge"}}}`),
		translate("from.vocabulary=yard&from.entry.make=Dodge", `{"from":{"vocabulary":"yard","entry":{"make":"Dodge"}}}`),
		translate("from.spine=nope&from.id=1", `{"from":{"spine":"nope","id":"1"}}`),
		translateBatch(`[{"from":{"make":476}},{"from":{"make":476},"to":["listing"]}]`),
		// The wall clock is read once for the whole batch.
		apiCase{op: opTranslateBatch, method: "POST", target: "/v1/translate", body: batchOf(contract.MaxBatch, `{"from":{"make":476}}`), status: 200, check: func(t *testing.T, body []byte) {
			var trs []struct{ Now string }
			if err := json.Unmarshal(body, &trs); err != nil {
				t.Fatal(err)
			}
			if len(trs) != contract.MaxBatch {
				t.Fatalf("%d translations; want %d", len(trs), contract.MaxBatch)
			}
			for i, tr := range trs {
				if tr.Now != trs[0].Now {
					t.Fatalf("translation %d: now %q; translation 0: %q; want one clock reading", i, tr.Now, trs[0].Now)
				}
			}
		}},
	)

	list := func(op, target string, req contract.ListRequest) apiCase {
		t.Helper()
		l, err := svc.List(req)
		c := apiCase{op: op, method: "GET", target: target}
		if err != nil {
			return refused(c, err)
		}
		c.status, c.want = 200, wantBody(t, l, nil)
		return c
	}
	page := func(req contract.ListRequest) string {
		t.Helper()
		l, err := svc.List(req)
		if err != nil || l.Next == "" {
			t.Fatalf("list %+v: next %q, %v; want a next page", req, l.Next, err)
		}
		return l.Next
	}
	makes := contract.ListRequest{Of: "vpic", Where: map[string]string{"make": "476"}, Limit: 2}
	parts := contract.ListRequest{Of: "parts", Limit: 2}
	return append(cases,
		list(opCatalog, "/v1/vocabularies", contract.ListRequest{}),
		list(opCatalog, "/v1/vocabularies?limit=5", contract.ListRequest{Limit: 5}),
		list(opCatalog, "/v1/vocabularies?where.make=Dodge", contract.ListRequest{Where: map[string]string{"make": "Dodge"}}),
		list(opEntries, "/v1/vocabularies/yard/entries", contract.ListRequest{Of: "yard"}),
		list(opEntries, "/v1/vocabularies/%79ard/entries?where.make=+DODGE", contract.ListRequest{Of: "yard", Where: map[string]string{"make": " DODGE"}}),
		list(opEntries, "/v1/vocabularies/vpic/entries?limit=3", contract.ListRequest{Of: "vpic", Limit: 3}),
		list(opEntries, "/v1/vocabularies/vpic/entries?where.make=476&limit=2", makes),
		list(opEntries, "/v1/vocabularies/vpic/entries?where.make=476&limit=2&after="+url.QueryEscape(page(makes)),
			contract.ListRequest{Of: "vpic", Where: map[string]string{"make": "476"}, Limit: 2, After: page(makes)}),
		list(opEntries, "/v1/vocabularies/parts/entries?limit=2&after="+url.QueryEscape(page(parts)), contract.ListRequest{Of: "parts", Limit: 2, After: page(parts)}),
		list(opEntries, "/v1/vocabularies/classics/entries", contract.ListRequest{Of: "classics"}),
		list(opEntries, "/v1/vocabularies/listing/entries", contract.ListRequest{Of: "listing"}),
		list(opEntries, "/v1/vocabularies/nope/entries", contract.ListRequest{Of: "nope"}),
		list(opEntries, "/v1/vocabularies/yard/entries?where.trim=SLT", contract.ListRequest{Of: "yard", Where: map[string]string{"trim": "SLT"}}),
		list(opEntries, "/v1/vocabularies/yard/entries?after=zzz", contract.ListRequest{Of: "yard", After: "zzz"}),
		list(opEntries, "/v1/vocabularies/parts/entries?where.id=300", contract.ListRequest{Of: "parts", Where: map[string]string{"id": "300"}}),
	)
}

// faulty fails every call as a fault in nitrovin would: a decode error, or
// a panic.
type faulty struct{}

func (faulty) Info() contract.Info { panic("injected fault") }
func (faulty) Decode(contract.Request) (contract.Decode, error) {
	return contract.Decode{}, errors.New("injected fault")
}
func (faulty) DecodeBatch([]contract.Request) ([]contract.Decode, error) {
	return nil, errors.New("injected fault")
}
func (faulty) Translate(contract.TranslateRequest) (contract.Translation, error) {
	return contract.Translation{}, errors.New("injected fault")
}
func (faulty) TranslateBatch([]contract.TranslateRequest) ([]contract.Translation, error) {
	return nil, errors.New("injected fault")
}
func (faulty) List(contract.ListRequest) (contract.List, error) {
	return contract.List{}, errors.New("injected fault")
}
func (faulty) SpineBundle() ([]byte, error) { return nil, errors.New("injected fault") }
func (faulty) File(string) ([]byte, error)  { return nil, errors.New("injected fault") }

// faultCases are the 500s, against an API on faulty.
func faultCases() []apiCase {
	return []apiCase{
		{op: opDecodeOne, method: "GET", target: "/v1/decode/1HGCM82633A004352", status: 500, problem: contract.Internal},
		{op: opDecodeBatch, method: "POST", target: "/v1/decode", body: `[{"vin":"1HGCM82633A004352"}]`, status: 500, problem: contract.Internal},
		{op: opInfo, method: "GET", target: "/v1/info", status: 500, problem: contract.Internal},
		{op: opTranslateOne, method: "GET", target: "/v1/translate?from.make=476", status: 500, problem: contract.Internal},
		{op: opTranslateBatch, method: "POST", target: "/v1/translate", body: `[{"from":{"make":476}}]`, status: 500, problem: contract.Internal},
		{op: opCatalog, method: "GET", target: "/v1/vocabularies", status: 500, problem: contract.Internal},
		{op: opEntries, method: "GET", target: "/v1/vocabularies/yard/entries", status: 500, problem: contract.Internal},
	}
}

// faultyServer is the API on faulty, in-process, its CORS origin cors.
func faultyServer(t *testing.T, cors string) string {
	a, err := newAPI(faulty{}, log.New(io.Discard, "", 0), io.Discard, nil, cors)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	t.Cleanup(srv.Close)
	return srv.URL
}

var client = &http.Client{Timeout: time.Minute}

// TestServeRefusals: without data, the cases that need none.
func TestServeRefusals(t *testing.T) {
	s := loadSpec(t)
	base := faultyServer(t, "")
	for _, c := range append(refusalCases(), faultCases()...) {
		s.run(t, client, base, c)
	}
}

// serving is a running `nitrovin serve`.
type serving struct {
	cmd    *exec.Cmd
	port   string
	stderr bytes.Buffer // after the first line
	done   chan struct{}
}

// startServe runs the binary with args and env, and waits for the stderr
// line naming the address it serves: its first, after any drift findings
// in the files it loaded.
func startServe(t *testing.T, env []string, args ...string) *serving {
	t.Helper()
	s := &serving{cmd: exec.Command(binary, append([]string{"serve"}, args...)...), done: make(chan struct{})}
	s.cmd.Env = append(os.Environ(), env...)
	pipe, err := s.cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.cmd.Process.Kill() })
	r := bufio.NewReader(pipe)
	line, err := r.ReadString('\n')
	for err == nil && strings.HasPrefix(line, `{"file":`) {
		line, err = r.ReadString('\n')
	}
	if err != nil {
		t.Fatalf("serve: no first line: %q, %v", line, err)
	}
	go func() {
		io.Copy(&s.stderr, r)
		close(s.done)
	}()
	const prefix = "nitrovin: serving data "
	rest, ok := strings.CutPrefix(strings.TrimSuffix(line, "\n"), prefix)
	_, url, _ := strings.Cut(rest, " on http://")
	_, port, err := net.SplitHostPort(url)
	if !ok || err != nil {
		t.Fatalf("serve: first line %q; want %q<month> on http://<address>", line, prefix)
	}
	s.port = port
	return s
}

// stop interrupts the server and checks it exits 0, having said nothing
// more.
func (s *serving) stop(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	<-s.done
	if err := s.cmd.Wait(); err != nil {
		t.Errorf("serve after SIGINT: %v; want exit 0", err)
	}
	if s.stderr.Len() > 0 {
		t.Errorf("serve: stderr %q after the first line", s.stderr.String())
	}
}

// TestServeContract: the built binary, with testdata/vocab loaded, answers
// every route and status of openapi.yaml as it declares, the 500s answered
// by the API on faulty.
func TestServeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the server with SIGINT")
	}
	path, d := artifact(t)
	files, err := vocab.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	svc := translating(t, d, files...)
	s := loadSpec(t)
	srv := startServe(t, []string{"NITROVIN_DATA=" + path}, "--vocab", fixture, "--addr", "127.0.0.1:0")
	base := "http://127.0.0.1:" + srv.port

	covered := map[string]bool{}
	for _, c := range slices.Concat(refusalCases(), decodeCases(t, svc), vocabCases(t, svc)) {
		s.run(t, client, base, c)
		covered[fmt.Sprintf("%s %d", c.op, c.status)] = true
	}
	faults := faultyServer(t, "")
	for _, c := range faultCases() {
		s.run(t, client, faults, c)
		covered[fmt.Sprintf("%s %d", c.op, c.status)] = true
	}
	ops := s.operations()
	if len(ops) == 0 {
		t.Fatal("openapi.json declares no operations")
	}
	for _, op := range ops {
		if !covered[op] {
			t.Errorf("no case drives %s", op)
		}
	}
	srv.stop(t)
}

// TestServeAddress: PORT when there is no --addr, and the outcome when the
// address is taken.
func TestServeAddress(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stops the server with SIGINT")
	}
	path, _ := artifact(t)
	env := []string{"NITROVIN_DATA=" + path}

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := cli(t, env, "", "serve", "--addr", held.Addr().String())
	if p := problem(t, r); r.exit != 1 || r.stdout != "" || p.Type != contract.Internal {
		t.Errorf("serve on a taken address: exit %d, stdout %q, problem %+v; want exit 1 and an internal problem", r.exit, r.stdout, p)
	}

	_, port, _ := net.SplitHostPort(held.Addr().String())
	held.Close()
	srv := startServe(t, append(env, "PORT="+port))
	if srv.port != port {
		t.Errorf("serve with PORT=%s: on port %s", port, srv.port)
	}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /healthz on PORT: %d", resp.StatusCode)
	}
	srv.stop(t)
}

// TestServeNoData: with data that will not load, serve never listens: an
// internal problem naming --data, exit 1.
func TestServeNoData(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.artifact")
	r := cli(t, nil, "", "serve", "--data", missing, "--addr", "127.0.0.1:0")
	if p := problem(t, r); r.exit != 1 || r.stdout != "" || p.Type != contract.Internal || !strings.Contains(p.Detail, "--data "+missing) {
		t.Errorf("serve with missing data: exit %d, stdout %q, problem %+v; want exit 1 and an internal problem naming --data", r.exit, r.stdout, p)
	}
	r = cli(t, nil, "", "serve", "extra")
	if r.exit != 2 || r.stdout != "" || !strings.Contains(r.stderr, "\nusage: nitrovin serve") {
		t.Errorf("serve extra: exit %d, stdout %q, stderr %q; want exit 2 and the usage", r.exit, r.stdout, r.stderr)
	}
}

// TestServeCORS: with --cors, every response under /v1/ and on /mcp allows
// the origin, refusals included, and OPTIONS there is a preflight; the form,
// /healthz and anything else carry no CORS header. A --cors that is not an
// origin is a usage error. On the embedded data: no artifact needed.
func TestServeCORS(t *testing.T) {
	for _, origin := range []string{"https://yard.example/", "yard.example", "https://yard.example/app", "https://user@yard.example", "ftp://yard.example"} {
		r := cli(t, nil, "", "serve", "--cors", origin, "--addr", "127.0.0.1:0")
		if r.exit != 2 || r.stdout != "" || !strings.Contains(r.stderr, "\nusage: nitrovin serve") {
			t.Errorf("serve --cors %q: exit %d, stdout %q, stderr %q; want exit 2 and the usage", origin, r.exit, r.stdout, r.stderr)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("stops the server with SIGINT")
	}

	const origin = "https://yard.example:8443"
	srv := startServe(t, nil, "--cors", origin, "--addr", "127.0.0.1:0")
	base := "http://127.0.0.1:" + srv.port
	preflight := http.Header{"Origin": {origin}, "Access-Control-Request-Method": {"POST"}, "Access-Control-Request-Headers": {"content-type"}}
	for _, c := range []struct {
		method, target string
		header         http.Header
		body           string
		status         int
		// cors: the response allows origin; methods: a preflight's
		// Access-Control-Allow-Methods, "" when it is not one.
		cors    bool
		methods string
	}{
		{method: "GET", target: "/v1/info", status: 200, cors: true},
		{method: "GET", target: "/v1/decode/1HGCM82633A004352", status: 200, cors: true},
		{method: "GET", target: "/v1/decode/1HGCM82633A0043521", status: 400, cors: true},
		{method: "POST", target: "/v1/decode", header: http.Header{"Content-Type": {"application/json"}}, body: `[{"vin":"1HGCM82633A004352"}]`, status: 200, cors: true},
		{method: "GET", target: "/v1/translate?from.make=476", status: 200, cors: true},
		{method: "POST", target: "/v1/translate", header: http.Header{"Content-Type": {"application/json"}}, body: `[{"from":{"make":476}}]`, status: 200, cors: true},
		{method: "GET", target: "/v1/vocabularies", status: 200, cors: true},
		{method: "GET", target: "/v1/vocabularies/vpic/entries?limit=1", status: 200, cors: true},
		{method: "GET", target: "/v1/vocabularies/yard/entries", status: 400, cors: true},
		{method: "GET", target: "/v1/openapi.json", status: 200, cors: true},
		{method: "GET", target: "/v1/nothing", status: 404, cors: true},
		{method: "PUT", target: "/v1/info", status: 405, cors: true},
		{method: "POST", target: "/mcp", header: mcpHeader, body: `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, status: 200, cors: true},
		{method: "GET", target: "/mcp", status: 405, cors: true},
		{method: "OPTIONS", target: "/v1/decode", header: preflight, status: 204, cors: true, methods: "POST"},
		{method: "OPTIONS", target: "/v1/decode/1HGCM82633A004352", header: preflight, status: 204, cors: true, methods: "GET"},
		{method: "OPTIONS", target: "/v1/translate", header: preflight, status: 204, cors: true, methods: "GET, POST"},
		{method: "OPTIONS", target: "/v1/vocabularies/vpic/entries", header: preflight, status: 204, cors: true, methods: "GET"},
		{method: "OPTIONS", target: "/mcp", header: preflight, status: 204, cors: true, methods: "POST"},
		{method: "OPTIONS", target: "/v1/nothing", header: preflight, status: 404, cors: true},

		{method: "GET", target: "/", status: 200},
		{method: "GET", target: "/ui/decode?vin=1HGCM82633A004352", status: 200},
		{method: "GET", target: "/healthz", status: 200},
		{method: "OPTIONS", target: "/healthz", header: preflight, status: 405},
		{method: "GET", target: "/v2/info", status: 404},
		{method: "GET", target: "/mcp/", status: 404},
	} {
		name := c.method + " " + c.target
		req, err := http.NewRequest(c.method, base+c.target, strings.NewReader(c.body))
		if err != nil {
			t.Fatal(err)
		}
		for k, vs := range c.header {
			req.Header[k] = vs
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("%s: %d; want %d", name, resp.StatusCode, c.status)
		}
		want := map[string]string{}
		if c.cors {
			want["Access-Control-Allow-Origin"] = origin
		}
		if c.methods != "" {
			want["Access-Control-Allow-Methods"] = c.methods
			want["Access-Control-Allow-Headers"] = corsAllowHeaders
			want["Access-Control-Max-Age"] = "86400"
		}
		for k := range resp.Header {
			if strings.HasPrefix(k, "Access-Control-") && want[k] == "" {
				t.Errorf("%s: unexpected %s: %q", name, k, resp.Header.Get(k))
			}
		}
		for k, v := range want {
			if got := resp.Header.Get(k); got != v {
				t.Errorf("%s: %s %q; want %q", name, k, got, v)
			}
		}
	}
	srv.stop(t)

	// * allows any origin, on the same routes.
	star := faultyServer(t, "*")
	resp, err := client.Get(star + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := resp.Header.Get("Access-Control-Allow-Origin"); resp.StatusCode != 500 || got != "*" {
		t.Errorf("--cors *: GET /v1/info %d, Access-Control-Allow-Origin %q; want 500 (faulty) and *", resp.StatusCode, got)
	}
}
