// Package stdio is the protocol of `nitrovin stdio`: JSON lines, one
// StdioRequest of docs/api/decode.schema.json in, one StdioReply out.
//
//	{"id": 1, "vin": "1HGCM82633A004352", "model_year": 2003}
//	{"id":1,"decode":{"vin":"1HGCM82633A004352",…}}
//
// Reply answers one line; it is also what the C library's nitrovin_decode
// returns, so the two surfaces give the same bytes. Serve runs a session.
package stdio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
)

// reply is a StdioReply: the request's id, when it had one or one could be
// read from a refused line, then exactly one of decode and error.
type reply struct {
	ID     json.RawMessage   `json:"id,omitempty"`
	Decode *contract.Decode  `json:"decode,omitempty"`
	Error  *contract.Problem `json:"error,omitempty"`
}

// Reply answers one input line, a StdioRequest without its newline, with
// one StdioReply of compact JSON without a newline: {"id":…,"decode":{…}},
// or {"id":…,"error":<problem>} for a line that is not JSON, a refused
// request, or a fault in nitrovin. The decode is the bytes json.Marshal
// gives for it. A request without now reads the clock once. The id is
// echoed as compact JSON. Reply never fails.
func Reply(d *nitrovin.Decoder, line []byte) []byte {
	id, req, err := contract.ParseStdio(line)
	r := reply{ID: id}
	if err == nil {
		var dec contract.Decode
		if dec, err = d.Decode(req); err == nil {
			r.Decode = &dec
		}
	}
	if err != nil {
		r.Error = contract.AsProblem(err)
	}
	b, err := json.Marshal(r)
	if err != nil {
		// A decode that will not marshal is a fault in nitrovin; the id is
		// valid JSON and a problem is strings and an int, so this marshals.
		b, _ = json.Marshal(reply{ID: id, Error: contract.AsProblem(err)})
	}
	return b
}

// Serve runs one session: it reads lines from r until EOF and, for every
// line that is not blank, writes Reply's line plus "\n" to w in one Write,
// before reading the next line, so a client sees each reply as soon as it
// is made. Lines end at "\n"; the last may lack it. A blank line (nothing
// but spaces, tabs and a "\r") gets no reply. Serve returns nil at EOF, or
// the first error reading r or writing w.
func Serve(d *nitrovin.Decoder, r io.Reader, w io.Writer) error {
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
			if _, err := w.Write(append(Reply(d, line), '\n')); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		line = line[:0]
	}
}
