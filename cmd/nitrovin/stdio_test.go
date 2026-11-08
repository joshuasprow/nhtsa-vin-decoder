package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// TestStdioUsage: stdio takes no arguments, and with no data it answers
// nothing.
func TestStdioUsage(t *testing.T) {
	r := cli(t, nil, "", "stdio", "1HGCM82633A004352")
	if r.exit != 2 || r.stdout != "" || !strings.Contains(r.stderr, "\nusage: nitrovin stdio") {
		t.Errorf("stdio <vin>: exit %d, stdout %q, stderr %q; want exit 2 and the usage", r.exit, r.stdout, r.stderr)
	}
	r = cli(t, nil, `{"vin":"1HGCM82633A004352"}`+"\n", "stdio")
	if p := problem(t, r); r.exit != 1 || r.stdout != "" || p.Type != contract.Internal || !strings.Contains(p.Detail, "--data") {
		t.Errorf("stdio with no data: exit %d, stdout %q, problem %+v; want exit 1 and an internal problem naming --data", r.exit, r.stdout, p)
	}
}

// stdioReply compiles $defs/StdioReply of decode.schema.json.
func stdioReply(t *testing.T) *jsonschema.Schema {
	t.Helper()
	const url = "https://github.com/joshuasprow/nitrovin/blob/main/docs/api/decode.schema.json"
	f, err := os.Open("../../docs/api/decode.schema.json")
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
	if err := c.AddResource(url, doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile(url + "#/$defs/StdioReply")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestStdio runs one scripted session twice: every kind of line, with and
// without an id, now frozen, then the answer key's VINs. Each non-blank line
// gets one reply, in order: the request's id (compacted) and the contract
// layer's decode bytes, or the refusal ParseStdio gives; blank lines get
// none. Both runs are byte-identical and every reply is a StdioReply.
func TestStdio(t *testing.T) {
	path, d := artifact(t)
	vins := answerKey(t, d)
	type line struct {
		in string
		// id is the reply's id, "" for none; kind is "decode", or the
		// problem's type and pointer.
		id, kind string
		pointer  string
	}
	script := []line{
		{in: `{"vin":"1HGCM82633A004352","now":"` + now + `"}`, kind: "decode"},
		{in: `{"id":1,"vin":"5YJ3E1EA2JF000316","model_year":2019,"full":true,"shape":"nitrovin","now":"` + now + `"}`, id: "1", kind: "decode"},
		{in: ""},
		{in: " \t \r"},
		{in: `{"id":"q","vin":`, kind: string(contract.MalformedJSON)},
		{in: `not json`, kind: string(contract.MalformedJSON)},
		{in: `{"id": {"n": [1, 2]}, "vin": "1HGCM82633A0043521", "now": "` + now + `"}`, id: `{"n":[1,2]}`, kind: string(contract.InvalidRequest), pointer: "/vin"},
		{in: `{"id":null,"vin":"1HGCM82633A004352","colour":"red"}`, id: "null", kind: string(contract.InvalidRequest), pointer: "/colour"},
		{in: `{"id":"x","vin":"1HGCM82633A004352","model_year":"2003"}`, id: `"x"`, kind: string(contract.InvalidRequest), pointer: "/model_year"},
		{in: `[{"vin":"1HGCM82633A004352"}]`, kind: string(contract.InvalidRequest)},
		{in: `{"id":"crlf","vin":" 1hgcm82633a004352 ","now":"2026-11-08","shape":"vpic"}` + "\r", id: `"crlf"`, kind: "decode"},
		// The id is echoed as json.Marshal compacts it: escapes kept, HTML escaped.
		{in: `{"id":"\u00e9<&>","vin":"ZZZZZZZZZZZZZZZZZ","now":"` + now + `"}`, id: `"\u00e9\u003c\u0026\u003e"`, kind: "decode"},
	}
	for i, k := range vins {
		req := map[string]any{"id": i, "vin": k.vin, "now": now, "full": i%2 == 0}
		if k.year.Valid {
			req["model_year"] = k.year.V
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		script = append(script, line{in: string(b), id: fmt.Sprint(i), kind: "decode"})
	}

	var stdin, want strings.Builder
	for i, l := range script {
		stdin.WriteString(l.in)
		if i < len(script)-1 { // the last line has no newline
			stdin.WriteByte('\n')
		}
		if l.kind == "" {
			continue // blank: no reply
		}
		_, req, err := contract.ParseStdio([]byte(strings.TrimSuffix(l.in, "\r")))
		var body []byte
		if l.kind == "decode" {
			if err != nil {
				t.Fatalf("%s: %v", l.in, err)
			}
			dec, err := d.Decode(req)
			if err != nil {
				t.Fatal(err)
			}
			body, _ = json.Marshal(dec)
		} else {
			p := contract.AsProblem(err)
			if string(p.Type) != l.kind || p.Pointer != l.pointer {
				t.Fatalf("%s: ParseStdio gives %+v, want %s at %q", l.in, p, l.kind, l.pointer)
			}
			body, _ = json.Marshal(p)
		}
		want.WriteByte('{')
		if l.id != "" {
			want.WriteString(`"id":` + l.id + ",")
		}
		key := "error"
		if l.kind == "decode" {
			key = "decode"
		}
		want.WriteString(`"` + key + `":`)
		want.Write(body)
		want.WriteString("}\n")
	}

	env := []string{"NITROVIN_DATA=" + path}
	first := cli(t, env, stdin.String(), "stdio")
	if first.exit != 0 || first.stderr != "" || first.stdout != want.String() {
		t.Errorf("first run: exit %d, stderr %q, stdout equals the contract layer's replies: %t", first.exit, first.stderr, first.stdout == want.String())
		for i, l := range strings.SplitAfter(first.stdout, "\n") {
			if w := strings.SplitAfter(want.String(), "\n"); i < len(w) && l != w[i] {
				t.Fatalf("reply %d:\n got %.300s\nwant %.300s", i+1, l, w[i])
			}
		}
	}
	second := cli(t, env, stdin.String(), "--data", path, "stdio")
	if second != first {
		t.Errorf("second run differs: exit %d, stderr %q, stdout identical: %t", second.exit, second.stderr, second.stdout == first.stdout)
	}
	schema := stdioReply(t)
	for i, reply := range strings.Split(strings.TrimSuffix(first.stdout, "\n"), "\n") {
		v, err := jsonschema.UnmarshalJSON(strings.NewReader(reply))
		if err == nil {
			err = schema.Validate(v)
		}
		if err != nil {
			t.Errorf("reply %d is not a StdioReply: %v", i+1, err)
		}
	}
}

// TestStdioInteractive: each reply is written before the next line is
// read, so a client can wait for it with stdin still open.
func TestStdioInteractive(t *testing.T) {
	path, _ := artifact(t)
	cmd := exec.Command(binary, "--data", path, "stdio")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	out := bufio.NewReader(outPipe)
	for i, l := range []string{`{"id":1,"vin":"1HGCM82633A004352"}`, `oops`, `{"id":3,"vin":"5YJ3E1EA2JF000316"}`} {
		if _, err := fmt.Fprintln(in, l); err != nil {
			t.Fatal(err)
		}
		reply, err := out.ReadString('\n')
		if err != nil {
			t.Fatalf("reply to line %d: %v", i+1, err)
		}
		if want := []string{`{"id":1,"decode":`, `{"error":`, `{"id":3,"decode":`}[i]; !strings.HasPrefix(reply, want) {
			t.Errorf("reply to line %d: %.80s, want %s…", i+1, reply, want)
		}
	}
	in.Close()
	if rest, _ := out.ReadString('\n'); rest != "" {
		t.Errorf("after EOF: %q", rest)
	}
	if err := cmd.Wait(); err != nil {
		t.Errorf("exit after EOF: %v", err)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

// TestStdioWriteFailure: stdout failing ends the session with exit 1 and an
// internal problem on stderr.
func TestStdioWriteFailure(t *testing.T) {
	path, _ := artifact(t)
	var stderr bytes.Buffer
	code := run([]string{"--data", path, "stdio"}, &env{
		stdin:  strings.NewReader("not json\nnot json\n"),
		stdout: failWriter{},
		stderr: &stderr,
		getenv: func(string) string { return "" },
	})
	var p contract.Problem
	if err := json.Unmarshal(stderr.Bytes(), &p); code != 1 || err != nil || p.Type != contract.Internal || !strings.Contains(p.Detail, "disk full") {
		t.Errorf("exit %d, stderr %q; want exit 1 and an internal problem naming the write error", code, stderr.String())
	}
}
