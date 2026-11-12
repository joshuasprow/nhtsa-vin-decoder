package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// The MCP surface: the tool definition's drift checks need nothing; the
// session needs NITROVIN_ARTIFACT, as the decode tests do.
//
//	NITROVIN_ARTIFACT=$PWD/nitrovin.artifact go test ./cmd/nitrovin -run MCP

const decodeSchemaPath = "../../docs/api/decode.schema.json"

// TestMCPToolsCopy: the embedded mcp-tools.json is docs/api/mcp-tools.json,
// byte for byte.
func TestMCPToolsCopy(t *testing.T) {
	doc, err := os.ReadFile("../../docs/api/mcp-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(doc, mcpTools) {
		t.Fatal("cmd/nitrovin/mcp-tools.json differs from docs/api/mcp-tools.json: run go generate ./cmd/nitrovin")
	}
}

// TestMCPToolsDefs is the drift check of ADR 0005: every $defs entry
// bundled in a tool's schemas equals, member for member and in order, the
// same-named definition in decode.schema.json; and each schema compiles on
// its own, every $ref resolved inside it.
func TestMCPToolsDefs(t *testing.T) {
	var source struct {
		Defs map[string]json.RawMessage `json:"$defs"`
	}
	b, err := os.ReadFile(decodeSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &source); err != nil {
		t.Fatal(err)
	}
	tools, _, err := parseTools(mcpTools)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools {
		for name, schema := range map[string]json.RawMessage{"inputSchema": tool.InputSchema, "outputSchema": tool.OutputSchema} {
			var bundle struct {
				Defs map[string]json.RawMessage `json:"$defs"`
			}
			if err := json.Unmarshal(schema, &bundle); err != nil {
				t.Fatal(err)
			}
			if len(bundle.Defs) == 0 {
				t.Errorf("%s %s bundles no $defs", tool.Name, name)
			}
			for def, got := range bundle.Defs {
				want, ok := source.Defs[def]
				if !ok {
					t.Errorf("%s %s: $defs/%s is not in decode.schema.json", tool.Name, name, def)
				} else if !bytes.Equal(compact(t, got), compact(t, want)) {
					t.Errorf("%s %s: $defs/%s differs from decode.schema.json's", tool.Name, name, def)
				}
			}
			compileBundled(t, schema)
		}
	}
}

func compact(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// compileBundled compiles a self-contained schema: a $ref outside it fails.
func compileBundled(t *testing.T, schema []byte) *jsonschema.Schema {
	t.Helper()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	c.UseLoader(noLoader{})
	const url = "urn:nitrovin:bundled"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(url)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

type noLoader struct{}

func (noLoader) Load(url string) (any, error) { return nil, fmt.Errorf("%s: not bundled", url) }

// TestMCPCommandLine: data that will not load is an #internal problem before
// anything is served; an argument is a usage error.
func TestMCPCommandLine(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.artifact")
	r := cli(t, nil, "", "mcp", "--data", missing)
	if p := problem(t, r); r.exit != 1 || r.stdout != "" || p.Type != contract.Internal || !strings.Contains(p.Detail, "--data "+missing) {
		t.Errorf("mcp with missing data: exit %d, stdout %q, problem %+v", r.exit, r.stdout, p)
	}
	r = cli(t, nil, "", "mcp", "1HGCM82633A004352")
	if r.exit != 2 || r.stdout != "" || !strings.Contains(r.stderr, "usage: nitrovin mcp") {
		t.Errorf("mcp <vin>: exit %d, stdout %q, stderr %q", r.exit, r.stdout, r.stderr)
	}
}

// mcpCall is one tools/call of the scripted session.
type mcpCall struct {
	tool string // "" is decode_vin
	args string // the arguments, sent as written
	// req is what args mean, for a decode; nil for a refusal or another
	// tool's call.
	req *contract.Request
	// pointer is a refusal's pointer; "" for an answer.
	pointer string
}

// knownTool reports whether name is one of mcp-tools.json's tools.
func knownTool(name string) bool {
	return name == "decode_vin" || name == "translate" || name == "list"
}

// mcpCalls is the scripted session's calls after tools/list: a decode in
// each layout, with and without full, now frozen; refusals; an unknown tool.
func mcpCalls(t *testing.T) []mcpCall {
	clock, err := contract.ParseNow(now)
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	const vin = "1HGCM82633A004352"
	return []mcpCall{
		{args: `{"vin":"` + vin + `","now":"` + now + `"}`, req: &contract.Request{VIN: vin, Now: clock}},
		{args: `{"vin":"` + vin + `","full":true,"now":"` + now + `"}`, req: &contract.Request{VIN: vin, Full: true, Now: clock}},
		{args: `{"vin":"` + vin + `","shape":"nitrovin","now":"` + now + `"}`, req: &contract.Request{VIN: vin, Shape: contract.ShapeNitrovin, Now: clock}},
		{args: `{"vin":"` + vin + `","shape":"nitrovin","full":true,"model_year":2005,"now":"` + now + `"}`,
			req: &contract.Request{VIN: vin, Shape: contract.ShapeNitrovin, Full: true, ModelYear: contract.Value[int32](2005), Now: clock}},
		// A partial VIN inside spaces, a null hint, a date for now.
		{args: `{"vin":" 5UXWX7C5*BA ","shape":"vpic","model_year":null,"now":"2026-11-08"}`,
			req: &contract.Request{VIN: " 5UXWX7C5*BA ", Shape: contract.ShapeVPIC, Now: midnight}},
		// Non-ASCII decodes as '&', which the decode's JSON escapes.
		{args: `{"vin":"1HGCM82633A00435É","shape":"nitrovin","now":"` + now + `"}`,
			req: &contract.Request{VIN: "1HGCM82633A00435É", Shape: contract.ShapeNitrovin, Now: clock}},

		{args: `{"vin":""}`, pointer: "/vin"},
		{args: `{"vin":"   "}`, pointer: "/vin"},
		{args: `{"vin":"1HGCM82633A0043521"}`, pointer: "/vin"},
		{args: `{}`, pointer: "/vin"},
		{args: `{"vin":"` + vin + `","color":"red"}`, pointer: "/color"},
		{args: `{"vin":"` + vin + `","shape":"wide"}`, pointer: "/shape"},
		{args: `{"vin":"` + vin + `","now":"yesterday"}`, pointer: "/now"},
		{args: `{"vin":"` + vin + `","now":"2026-11-08T12:30:45"}`, pointer: "/now"},
		{args: `{"vin":"` + vin + `","model_year":"2003"}`, pointer: "/model_year"},
		{args: `{"vin":"` + vin + `","model_year":2003.5}`, pointer: "/model_year"},
		{args: `{"vin":"` + vin + `","model_year":2147483648}`, pointer: "/model_year"},
		{args: `{"vin":"` + vin + `","full":"yes"}`, pointer: "/full"},

		{tool: "decode_vins", args: `{"vin":"` + vin + `"}`},
	}
}

// session is one run of the scripted session: the bytes each side wrote,
// one JSON-RPC message per line.
type session struct {
	stdin, stdout string
}

// runSession runs the scripted session over transport with the SDK's
// client speaking protocol revision version: tools/list, then calls.
func runSession(t *testing.T, transport mcp.Transport, version string, calls []mcpCall) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "nitrovin-test", Version: "v0.0.0"}, nil)
	cs, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: version})
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.InitializeResult().ProtocolVersion; got != version {
		t.Errorf("negotiated protocol %s, want %s", got, version)
	}
	if _, err := cs.ListTools(ctx, nil); err != nil {
		t.Fatal(err)
	}
	for _, c := range calls {
		name := cmp.Or(c.tool, "decode_vin")
		_, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: json.RawMessage(c.args)})
		if (err != nil) != !knownTool(name) {
			t.Errorf("call %s %s: error %v", name, c.args, err)
		}
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
}

// mcpSession runs the scripted session against `nitrovin mcp` with the
// SDK's client speaking protocol revision version.
// path "" is the embedded data; vocab is $NITROVIN_VOCAB, "" for none.
func mcpSession(t *testing.T, path, vocab, version string, calls []mcpCall) session {
	t.Helper()
	cmd := exec.Command(binary, "mcp")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NITROVIN_DATA=") && !strings.HasPrefix(kv, "NITROVIN_VOCAB=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	if path != "" {
		cmd.Env = append(cmd.Env, "NITROVIN_DATA="+path)
	}
	if vocab != "" {
		cmd.Env = append(cmd.Env, "NITROVIN_VOCAB="+vocab)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr, in, out bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	runSession(t, &mcp.IOTransport{
		Reader: readCloser{io.TeeReader(stdout, &out), stdout},
		Writer: writeCloser{io.MultiWriter(stdin, &in), stdin},
	}, version, calls)
	if err := cmd.Wait(); err != nil || stderr.Len() > 0 {
		t.Fatalf("nitrovin mcp: %v, stderr %q", err, stderr.String())
	}
	return session{in.String(), out.String()}
}

// mcpHTTPSession runs the scripted session against serve's /mcp, the API
// on d in-process, with the SDK's Streamable HTTP client speaking protocol
// revision version. The session's bytes are every POST's body and the body
// of its response, when it has one; the client's GET for a server stream
// is refused with 405, which it takes as none.
func mcpHTTPSession(t *testing.T, d decodeService, version string, calls []mcpCall) session {
	t.Helper()
	var stderr bytes.Buffer
	a, err := newAPI(d, log.New(&stderr, "", 0), &stderr, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	defer srv.Close()
	rec := &recorder{next: http.DefaultTransport}
	runSession(t, &mcp.StreamableClientTransport{
		Endpoint:   srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: rec},
		MaxRetries: -1,
	}, version, calls)
	if stderr.Len() > 0 {
		t.Fatalf("serve /mcp: stderr %q", stderr.String())
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return session{rec.in.String(), rec.out.String()}
}

// recorder is an http.RoundTripper writing each POST's body, and its
// response's, to in and out as a line.
type recorder struct {
	next    http.RoundTripper
	mu      sync.Mutex
	in, out bytes.Buffer
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodPost {
		return r.next.RoundTrip(req)
	}
	body, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	resp, err := r.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(got))
	r.mu.Lock()
	defer r.mu.Unlock()
	line(&r.in, body)
	if len(got) > 0 {
		if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
			return nil, fmt.Errorf("POST /mcp: %d, Content-Type %q; want application/json", resp.StatusCode, ct)
		}
		line(&r.out, got)
	}
	return resp, nil
}

// line writes msg to b as one line.
func line(b *bytes.Buffer, msg []byte) {
	b.Write(bytes.TrimRight(msg, "\n"))
	b.WriteByte('\n')
}

type readCloser struct {
	io.Reader
	io.Closer
}

type writeCloser struct {
	io.Writer
	io.Closer
}

// message is one JSON-RPC message, its members kept as sent.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// messages reads newline-delimited JSON-RPC messages.
func messages(t *testing.T, stream string) []message {
	t.Helper()
	var msgs []message
	sc := bufio.NewScanner(strings.NewReader(stream))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m.JSONRPC != "2.0" {
			t.Fatalf("not a JSON-RPC 2.0 message: %q", sc.Text())
		}
		msgs = append(msgs, m)
	}
	return msgs
}

// TestMCPSession runs the scripted decode session, with no vocabulary,
// twice with a 2026-07-28 client
// and twice with a 2025-11-25 one, over stdio against `nitrovin mcp` and
// over Streamable HTTP against serve's /mcp. Both runs of a revision on a
// transport write the same bytes. tools/list sends mcp-tools.json's tools
// array byte for byte; a decode is the contract layer's decode as
// structuredContent and as the one text block, valid against the bundled
// outputSchema; a refusal is an isError Problem with its pointer; an
// unknown tool is a JSON-RPC error.
func TestMCPSession(t *testing.T) {
	path, dec := artifact(t)
	d := translating(t, dec)
	runSessions(t, d, path, "", mcpCalls(t))
}

// translating is d with no vocabulary files loaded beside it, or files.
func translating(t *testing.T, d *nitrovin.Decoder, files ...*vocab.File) loaded {
	t.Helper()
	tr, err := nitrovin.NewTranslator(d, files...)
	if err != nil {
		t.Fatal(err)
	}
	return loaded{d, tr}
}

// TestMCPVocabSession runs a scripted session against the embedded data
// with testdata/vocab loaded, over stdio and Streamable HTTP as
// TestMCPSession does: tools/list is still mcp-tools.json's tools array;
// translate answers a VIN and an entry, list gives the catalog and pages a
// listing end to end, each as the Translator answers in-process; a request
// the loaded files refuse is an isError Problem.
func TestMCPVocabSession(t *testing.T) {
	fixtureData(t)
	dec, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(fixture)
	if err != nil {
		t.Fatal(err)
	}
	d := translating(t, dec, files...)
	// The page after the first: the cursor the first page gives.
	first, err := d.List(contract.ListRequest{Of: "parts", Limit: 3})
	if err != nil || first.Next == "" {
		t.Fatalf("list parts: %+v, %v; want a next page", first, err)
	}
	const ram2010 = "1D7RV1GT5AS123456"
	calls := []mcpCall{
		{tool: "list", args: `{}`},
		{tool: "list", args: `{"of":"yard","where":{"make":"dodge"}}`},
		{tool: "list", args: `{"of":"parts","limit":3}`},
		{tool: "list", args: `{"of":"parts","limit":3,"after":"` + first.Next + `"}`},
		{tool: "list", args: `{"of":"vpic","where":{"model":1850,"element":15}}`},
		{tool: "translate", args: `{"from":"` + ram2010 + `","to":["yard"],"now":"` + now + `"}`},
		{tool: "translate", args: `{"from":{"vocabulary":"yard","entry":{"make":"Dodge","model":"Ram 1500"}},"vin":"` + ram2010 + `","to":["yard"],"now":"` + now + `"}`},
		{tool: "translate", args: `{"from":{"vocabulary":"listing","entry":{"category":"Brake Pads"}},"to":["listing"],"now":"` + now + `"}`},

		{tool: "translate", args: `{"from":"` + ram2010 + `","to":["listing"],"now":"` + now + `"}`, pointer: "/to/0"},
		{tool: "translate", args: `{"from":{"vocabulary":"ebay","entry":{"make":"Dodge"}},"now":"` + now + `"}`, pointer: "/from/vocabulary"},
		{tool: "translate", args: `{"now":"` + now + `"}`, pointer: "/from"},
		{tool: "list", args: `{"of":"ebay"}`, pointer: "/of"},
		{tool: "list", args: `{"of":"parts","limit":0}`, pointer: "/limit"},
		{tool: "list", args: `{"of":"vpic","where":{"element":15}}`, pointer: "/where/element"},
	}
	runSessions(t, d, "", fixture, calls)
}

// runSessions runs the scripted session twice with a 2026-07-28 client and
// twice with a 2025-11-25 one, over stdio against `nitrovin mcp` on the
// data at path with vocab loaded, and over Streamable HTTP against serve's
// /mcp on d; both runs of a revision on a transport write the same bytes.
func runSessions(t *testing.T, d loaded, path, vocab string, calls []mcpCall) {
	t.Helper()
	for _, transport := range []struct {
		name string
		run  func(t *testing.T, version string) session
	}{
		{"stdio", func(t *testing.T, version string) session { return mcpSession(t, path, vocab, version, calls) }},
		{"http", func(t *testing.T, version string) session { return mcpHTTPSession(t, d, version, calls) }},
	} {
		for _, version := range []string{"2026-07-28", "2025-11-25"} {
			t.Run(transport.name+"/"+version, func(t *testing.T) {
				first := transport.run(t, version)
				if second := transport.run(t, version); second != first {
					t.Errorf("two runs differ:\n%s\n---\n%s", first.stdout, second.stdout)
				}
				checkSession(t, d, calls, first)
			})
		}
	}
}

// checkSession checks the responses of a run of the scripted session.
func checkSession(t *testing.T, d loaded, calls []mcpCall, s session) {
	t.Helper()
	tools, wantTools, err := parseTools(mcpTools)
	if err != nil {
		t.Fatal(err)
	}
	output := map[string]*jsonschema.Schema{}
	for _, tool := range tools {
		output[tool.Name] = compileBundled(t, tool.OutputSchema)
	}
	problemSchema := problemSchema(t)

	responses := map[string]message{}
	for _, m := range messages(t, s.stdout) {
		if m.Method != "" {
			t.Errorf("the server sent %s", m.Method)
		}
		responses[string(m.ID)] = m
	}
	var listed bool
	k := 0
	for _, req := range messages(t, s.stdin) {
		if req.ID == nil {
			continue // a notification
		}
		resp, ok := responses[string(req.ID)]
		if !ok {
			t.Fatalf("no response to %s %s", req.Method, req.ID)
		}
		switch req.Method {
		case "tools/list":
			var res struct{ Tools json.RawMessage }
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(res.Tools, wantTools) {
				t.Errorf("tools/list sent\n%s\nwant mcp-tools.json's tools\n%s", res.Tools, wantTools)
			}
			listed = true
		case "tools/call":
			checkCall(t, d, calls[k], resp, output, problemSchema)
			k++
		}
	}
	if !listed || k != len(calls) {
		t.Errorf("the session listed tools %t and made %d of %d calls", listed, k, len(calls))
	}
}

func problemSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open(decodeSchemaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	const url = "https://github.com/joshuasprow/nitrovin/blob/main/docs/api/decode.schema.json"
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(url + "#/$defs/Problem")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validates(s *jsonschema.Schema, b []byte) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
	if err != nil {
		return err
	}
	return s.Validate(v)
}

// checkCall checks the response to one tools/call.
func checkCall(t *testing.T, d loaded, c mcpCall, resp message, outputs map[string]*jsonschema.Schema, problem *jsonschema.Schema) {
	t.Helper()
	name := cmp.Or(c.tool, "decode_vin")
	if !knownTool(name) {
		var e struct{ Code int }
		if json.Unmarshal(resp.Error, &e) != nil || e.Code != -32602 || resp.Result != nil {
			t.Errorf("unknown tool %s: result %s, error %s", c.tool, resp.Result, resp.Error)
		}
		return
	}
	var res struct {
		Content []struct {
			Type, Text string
		}
		StructuredContent json.RawMessage
		IsError           bool
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil || resp.Error != nil {
		t.Fatalf("%s: result %s, error %s", c.args, resp.Result, resp.Error)
	}
	if len(res.Content) != 1 || res.Content[0].Type != "text" {
		t.Fatalf("%s: content %+v, want one text block", c.args, res.Content)
	}
	text := res.Content[0].Text

	if c.pointer == "" {
		var want []byte
		var err error
		switch name {
		case "decode_vin":
			var dec contract.Decode
			if dec, err = d.Decode(*c.req); err == nil {
				want, err = json.Marshal(dec)
			}
		case "translate":
			want, err = translateJSON(d, []byte(cmp.Or(c.args, "{}")))
		case "list":
			want, err = listJSON(d, []byte(cmp.Or(c.args, "{}")))
		}
		if err != nil {
			t.Fatalf("%s %s: %v", name, c.args, err)
		}
		output := outputs[name]
		switch {
		case res.IsError:
			t.Errorf("%s: refused: %s", c.args, text)
		case !bytes.Equal(res.StructuredContent, want):
			t.Errorf("%s: structuredContent\n%s\nwant the contract layer's\n%s", c.args, res.StructuredContent, want)
		case text != string(res.StructuredContent):
			t.Errorf("%s: the text block differs from structuredContent:\n%s", c.args, text)
		}
		if err := validates(output, res.StructuredContent); err != nil {
			t.Errorf("%s: structuredContent breaks the outputSchema: %v", c.args, err)
		}
		return
	}

	var p contract.Problem
	if err := json.Unmarshal([]byte(text), &p); err != nil {
		t.Fatalf("%s: text block %q is not a Problem", c.args, text)
	}
	compactProblem, _ := json.Marshal(p)
	switch {
	case !res.IsError || res.StructuredContent != nil:
		t.Errorf("%s: isError %t, structuredContent %s", c.args, res.IsError, res.StructuredContent)
	case p.Type != contract.InvalidRequest || p.Status != 400 || p.Pointer != c.pointer:
		t.Errorf("%s: %s, want an invalid-request at %s", c.args, text, c.pointer)
	case text != string(compactProblem):
		t.Errorf("%s: %s is not the Problem's compact JSON %s", c.args, text, compactProblem)
	}
	if err := validates(problem, []byte(text)); err != nil {
		t.Errorf("%s: not a valid Problem: %v", c.args, err)
	}
}
