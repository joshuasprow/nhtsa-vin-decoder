package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
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
	tool string
	args string // the arguments, sent as written
	// req is what args mean, for a decode; nil for a refusal.
	req *contract.Request
	// pointer is a refusal's pointer.
	pointer string
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

// session is one run of the scripted session: the bytes each side wrote.
type session struct {
	stdin, stdout string
}

// mcpSession runs the scripted session against `nitrovin mcp` with the
// SDK's client speaking protocol revision version.
func mcpSession(t *testing.T, path, version string, calls []mcpCall) session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.Command(binary, "mcp")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "NITROVIN_DATA=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "NITROVIN_DATA="+path)
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
	client := mcp.NewClient(&mcp.Implementation{Name: "nitrovin-test", Version: "v0.0.0"}, nil)
	cs, err := client.Connect(ctx, &mcp.IOTransport{
		Reader: readCloser{io.TeeReader(stdout, &out), stdout},
		Writer: writeCloser{io.MultiWriter(stdin, &in), stdin},
	}, &mcp.ClientSessionOptions{ProtocolVersion: version})
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
		if (err != nil) != (name != "decode_vin") {
			t.Errorf("call %s %s: error %v", name, c.args, err)
		}
	}
	if err := cs.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil || stderr.Len() > 0 {
		t.Fatalf("nitrovin mcp: %v, stderr %q", err, stderr.String())
	}
	return session{in.String(), out.String()}
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

// TestMCPSession runs the scripted session twice with a 2026-07-28 client
// and twice with a 2025-11-25 one. Both runs of a revision write the same
// bytes. tools/list sends mcp-tools.json's tools array byte for byte; a
// decode is the contract layer's decode as structuredContent and as the
// one text block, valid against the bundled outputSchema; a refusal is an
// isError Problem with its pointer; an unknown tool is a JSON-RPC error.
func TestMCPSession(t *testing.T) {
	path, d := artifact(t)
	calls := mcpCalls(t)
	_, wantTools, err := parseTools(mcpTools)
	if err != nil {
		t.Fatal(err)
	}
	tools, _, _ := parseTools(mcpTools)
	output := compileBundled(t, tools[0].OutputSchema)
	problemSchema := problemSchema(t)

	for _, version := range []string{"2026-07-28", "2025-11-25"} {
		t.Run(version, func(t *testing.T) {
			first := mcpSession(t, path, version, calls)
			if second := mcpSession(t, path, version, calls); second != first {
				t.Errorf("two runs differ:\n%s\n---\n%s", first.stdout, second.stdout)
			}

			responses := map[string]message{}
			for _, m := range messages(t, first.stdout) {
				if m.Method != "" {
					t.Errorf("the server sent %s", m.Method)
				}
				responses[string(m.ID)] = m
			}
			var listed bool
			k := 0
			for _, req := range messages(t, first.stdin) {
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
		})
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
func checkCall(t *testing.T, d *nitrovin.Decoder, c mcpCall, resp message, output, problem *jsonschema.Schema) {
	t.Helper()
	if c.tool != "" {
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

	if c.req != nil {
		dec, err := d.Decode(*c.req)
		if err != nil {
			t.Fatal(err)
		}
		want, err := json.Marshal(dec)
		if err != nil {
			t.Fatal(err)
		}
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
