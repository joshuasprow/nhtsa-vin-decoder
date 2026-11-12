// Package stdio is the protocol of `nitrovin stdio`: JSON lines, one
// StdioRequest of docs/api/decode.schema.json in, one StdioReply out.
//
//	{"id": 1, "vin": "1HGCM82633A004352", "model_year": 2003}
//	{"id":1,"decode":{"vin":"1HGCM82633A004352",…}}
//	{"id": 2, "op": "translate", "from": {"make": 476}, "to": ["yard"]}
//	{"id":2,"translation":{"from":{…},…}}
//	{"id": 3, "op": "fit", "from": "1D7RV1GT5AS123456", "part": {"spine": "part-types", "id": "mirror"}}
//	{"id":3,"fit":{"status":"found",…}}
//
// Reply answers one line; it is also what the C library's nitrovin_decode
// returns, so the two surfaces give the same bytes. Serve runs a session.
package stdio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

// reply is a StdioReply: the request's id, when it had one or one could be
// read from a refused line, then exactly one of decode, translation, list,
// fit and error.
type reply struct {
	ID          json.RawMessage       `json:"id,omitempty"`
	Decode      *contract.Decode      `json:"decode,omitempty"`
	Translation *contract.Translation `json:"translation,omitempty"`
	List        *contract.List        `json:"list,omitempty"`
	Fit         *contract.Fit         `json:"fit,omitempty"`
	Error       *contract.Problem     `json:"error,omitempty"`
}

// Reply answers one input line, a StdioRequest without its newline, with
// one StdioReply of compact JSON without a newline, keyed by the line's op:
// {"id":…,"decode":{…}}, {"id":…,"translation":{…}}, {"id":…,"list":{…}} or
// {"id":…,"fit":{…}},
// or {"id":…,"error":<problem>} for a line that is not JSON, a refused
// request, or a fault in nitrovin. The answer is the bytes json.Marshal
// gives for it. A request without now reads the clock once. The id is
// echoed as compact JSON. Reply never fails.
func Reply(t *nitrovin.Translator, line []byte) []byte {
	id, req, err := contract.ParseStdio(line)
	r := reply{ID: id}
	if err == nil {
		r, err = answer(t, req, id)
	}
	if err != nil {
		r.Error = contract.AsProblem(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		// An answer that will not marshal is a fault in nitrovin; the id is
		// valid JSON and a problem is strings and an int, so this marshals.
		b, _ = json.Marshal(reply{ID: id, Error: contract.AsProblem(err)})
	}
	return b
}

// answer answers one parsed request. A panic answering it (a fault in
// nitrovin, or a catalog reader failing on a damaged file) is an
// #internal error for that line alone, so a session outlives it.
func answer(t *nitrovin.Translator, req contract.StdioRequest, id json.RawMessage) (r reply, err error) {
	defer func() {
		if v := recover(); v != nil {
			r, err = reply{ID: id}, contract.NewProblem(contract.Internal, "", fmt.Sprint("panic: ", v))
		}
	}()
	r = reply{ID: id}
	switch req.Op {
	case contract.OpTranslate:
		var tr contract.Translation
		if tr, err = t.Translate(req.Translate); err == nil {
			r.Translation = &tr
		}
	case contract.OpList:
		var l contract.List
		if l, err = t.List(req.List); err == nil {
			r.List = &l
		}
	case contract.OpFit:
		var f contract.Fit
		if f, err = t.Fit(req.Fit); err == nil {
			r.Fit = &f
		}
	default:
		var dec contract.Decode
		if dec, err = t.Decoder().Decode(req.Decode); err == nil {
			r.Decode = &dec
		}
	}
	return r, err
}

// Serve runs one session: it reads lines from r until EOF and, for every
// line that is not blank, writes Reply's line plus "\n" to w in one Write,
// before reading the next line, so a client sees each reply as soon as it
// is made. Lines end at "\n"; the last may lack it. A blank line (nothing
// but spaces, tabs and a "\r") gets no reply. Serve returns nil at EOF, or
// the first error reading r or writing w.
func Serve(t *nitrovin.Translator, r io.Reader, w io.Writer) error {
	in := bufio.NewReader(r)
	var line []byte
	for {
		chunk, rerr := in.ReadSlice('\n')
		line = append(line, chunk...)
		if rerr == bufio.ErrBufferFull {
			continue // a line longer than the buffer
		}
		if rerr != nil && rerr != io.EOF {
			return rerr
		}
		line = bytes.TrimSuffix(line, []byte("\n"))
		if len(bytes.Trim(line, " \t\r")) > 0 {
			if _, err := w.Write(append(Reply(t, line), '\n')); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		line = line[:0]
	}
}
