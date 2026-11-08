package contract

import (
	"errors"
	"strings"
)

// ProblemType is the type URI of a refusal, one of the problem types in
// docs/api/README.md: that file's URL with the type's anchor as fragment.
type ProblemType string

const problemBase = "https://github.com/joshuasprow/nitrovin/blob/main/docs/api/README.md#"

// The problem types of API v1. Every surface uses these URIs and statuses:
// HTTP as the response, stdio as an error line, MCP as an isError result.
const (
	// MalformedJSON: the body or line is not JSON.
	MalformedJSON ProblemType = problemBase + "malformed-json"
	// InvalidRequest: valid JSON that breaks the request schema.
	InvalidRequest ProblemType = problemBase + "invalid-request"
	// TooLarge: more than MaxBatch items, or a body over MaxBody bytes.
	TooLarge ProblemType = problemBase + "too-large"
	// NotFound: no such route.
	NotFound ProblemType = problemBase + "not-found"
	// MethodNotAllowed: the route exists, the method does not.
	MethodNotAllowed ProblemType = problemBase + "method-not-allowed"
	// Internal: a fault in nitrovin.
	Internal ProblemType = problemBase + "internal"
)

// Status is the HTTP status of the problem type, carried by every surface.
func (t ProblemType) Status() int {
	switch t {
	case MalformedJSON, InvalidRequest:
		return 400
	case TooLarge:
		return 413
	case NotFound:
		return 404
	case MethodNotAllowed:
		return 405
	default:
		return 500
	}
}

// Title is the problem type's short, fixed summary.
func (t ProblemType) Title() string {
	switch t {
	case MalformedJSON:
		return "Malformed JSON"
	case InvalidRequest:
		return "Invalid request"
	case TooLarge:
		return "Request too large"
	case NotFound:
		return "Not found"
	case MethodNotAllowed:
		return "Method not allowed"
	default:
		return "Internal error"
	}
}

// Problem is an RFC 9457 problem details object: every refusal on every
// surface. It is also the error this package's parsers return.
type Problem struct {
	Type   ProblemType `json:"type"`
	Title  string      `json:"title"`
	Status int         `json:"status"`
	Detail string      `json:"detail,omitempty"`
	// Pointer is a JSON Pointer into the request at the offending value
	// ("/vin", "/3/now"), empty when the problem is the whole request.
	Pointer string `json:"pointer,omitempty"`
}

// NewProblem returns a problem of type t, with t's title and status.
func NewProblem(t ProblemType, pointer, detail string) *Problem {
	return &Problem{Type: t, Title: t.Title(), Status: t.Status(), Detail: detail, Pointer: pointer}
}

func (p *Problem) Error() string {
	var b strings.Builder
	b.WriteString(p.Title)
	if p.Pointer != "" {
		b.WriteString(" at ")
		b.WriteString(p.Pointer)
	}
	if p.Detail != "" {
		b.WriteString(": ")
		b.WriteString(p.Detail)
	}
	return b.String()
}

// AsProblem returns err as a Problem: the Problem it wraps, or an Internal
// problem carrying its text. A decode fails only for a fault in nitrovin
// (the decoder raising where Postgres raises included); a VIN is never one.
func AsProblem(err error) *Problem {
	if p, ok := errors.AsType[*Problem](err); ok {
		return p
	}
	return NewProblem(Internal, "", err.Error())
}

// pointerTo appends the reference token name to the JSON Pointer prefix,
// escaped per RFC 6901.
func pointerTo(prefix, name string) string {
	name = strings.ReplaceAll(name, "~", "~0")
	name = strings.ReplaceAll(name, "/", "~1")
	return prefix + "/" + name
}
