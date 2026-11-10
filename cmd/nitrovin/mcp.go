package main

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

func init() {
	register(command{
		name:     "mcp",
		synopsis: "[flags]",
		summary:  "Serve the decode_vin tool to an MCP client over stdin and stdout.",
		setup:    mcpSetup,
	})
}

// mcpTools is docs/api/mcp-tools.json, the tool definition the contract
// fixes: its tools array is exactly what tools/list returns. go:embed cannot
// reach docs/, so this directory holds a copy; TestMCPToolsCopy fails when
// the two differ.
//
//go:generate cp ../../docs/api/mcp-tools.json mcp-tools.json
//go:embed mcp-tools.json
var mcpTools []byte

// mcpTool is one entry of mcp-tools.json's tools array. Its members are
// the ones the contract defines; parseTools refuses any other, so the
// registered tool never silently lacks one.
type mcpTool struct {
	Name         string               `json:"name"`
	Title        string               `json:"title"`
	Description  string               `json:"description"`
	InputSchema  json.RawMessage      `json:"inputSchema"`
	OutputSchema json.RawMessage      `json:"outputSchema"`
	Annotations  *mcp.ToolAnnotations `json:"annotations"`
}

// parseTools reads mcp-tools.json: the tools, and their array as compact
// JSON, the bytes tools/list sends.
func parseTools(doc []byte) ([]mcpTool, json.RawMessage, error) {
	var file struct {
		Tools json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(doc, &file); err != nil {
		return nil, nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(file.Tools))
	dec.DisallowUnknownFields()
	var tools []mcpTool
	if err := dec.Decode(&tools); err != nil {
		return nil, nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, file.Tools); err != nil {
		return nil, nil, err
	}
	return tools, compact.Bytes(), nil
}

func mcpSetup(fs *flag.FlagSet) func(*env, []string) error {
	return func(e *env, args []string) error {
		if len(args) > 0 {
			return usageError("mcp takes no arguments")
		}
		d, err := e.decoder()
		if err != nil {
			return err
		}
		server, err := newMCPServer(d, e.stderr)
		if err != nil {
			return err
		}
		// The client ends the session by closing stdin; Run then returns nil.
		return server.Run(context.Background(), &mcp.IOTransport{
			Reader: io.NopCloser(e.stdin),
			Writer: nopWriteCloser{e.stdout},
		})
	}
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// newMCPServer returns the MCP server of docs/api/README.md's MCP section:
// one tool, decode_vin, decoding with d, on every protocol revision the SDK
// serves. The SDK's diagnostics go to stderr. `nitrovin mcp` serves it over
// stdio, `nitrovin serve` over Streamable HTTP at /mcp.
func newMCPServer(d decodeService, stderr io.Writer) (*mcp.Server, error) {
	tools, wire, err := parseTools(mcpTools)
	if err != nil {
		return nil, fmt.Errorf("mcp-tools.json: %w", err)
	}
	if len(tools) != 1 || tools[0].Name != "decode_vin" {
		return nil, errors.New("mcp-tools.json: want the one tool decode_vin")
	}
	t := tools[0]
	server := mcp.NewServer(&mcp.Implementation{Name: "nitrovin", Version: nitrovin.Version()}, &mcp.ServerOptions{
		Logger: slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	})
	// The low-level AddTool: the SDK neither checks the arguments against
	// the input schema nor the result against the output schema. The decode
	// layer validates the request, so a refusal is the Problem every other
	// surface gives (ADR 0005).
	server.AddTool(&mcp.Tool{
		Name:         t.Name,
		Title:        t.Title,
		Description:  t.Description,
		InputSchema:  t.InputSchema,
		OutputSchema: t.OutputSchema,
		Annotations:  t.Annotations,
	}, decodeVIN(d))
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			res, err := next(ctx, method, req)
			if l, ok := res.(*mcp.ListToolsResult); ok && err == nil && method == "tools/list" {
				return &toolsList{l, wire}, nil
			}
			return res, err
		}
	})
	return server, nil
}

// toolsList is a tools/list result whose tools are sent as mcp-tools.json
// has them. The SDK's Tool marshals its members in its own order, so the
// result it builds is the same tool but not the same bytes; this one is
// the SDK's result (paging, _meta, cache hints) with its tools member
// replaced by the file's array. Embedding the SDK's result keeps it an
// mcp.Result.
type toolsList struct {
	*mcp.ListToolsResult
	tools json.RawMessage
}

func (l *toolsList) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(l.ListToolsResult)
	if err != nil {
		return nil, err
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(b, &members); err != nil {
		return nil, err
	}
	members["tools"] = l.tools
	// Without HTML escaping, as the SDK writes every message: the
	// description's '&' stays '&'.
	var out bytes.Buffer
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(members); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(out.Bytes(), []byte("\n")), nil
}

// decodeVIN is decode_vin's handler. A decode is the result's
// structuredContent and, as the same compact JSON, its one text block.
// A refused request (or a fault in nitrovin) is an isError result whose
// one text block is the Problem, as compact JSON.
func decodeVIN(d decodeService) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.Params.Arguments
		if len(args) == 0 { // arguments are optional in MCP: none is {}
			args = json.RawMessage("{}")
		}
		b, err := decodeJSON(d, args)
		if err != nil {
			b, err := json.Marshal(contract.AsProblem(err))
			if err != nil { // a Problem is strings and an int: unreachable
				return nil, err
			}
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: string(b)}},
			StructuredContent: json.RawMessage(b),
		}, nil
	}
}

// decodeJSON decodes one request object, args, to the decode's compact JSON,
// the bytes `nitrovin decode` prints for it. The error is a *contract.Problem.
func decodeJSON(d decodeService, args []byte) ([]byte, error) {
	r, err := contract.ParseRequest(args)
	if err != nil {
		return nil, err
	}
	dec, err := d.Decode(r)
	if err != nil {
		return nil, err
	}
	return json.Marshal(dec)
}
