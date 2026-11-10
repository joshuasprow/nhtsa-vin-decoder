// Package nitrovin decodes VINs the way NHTSA's vPIC does, against one month
// of its data: the Go module's entry point, and the one every surface (CLI,
// HTTP, stdio, MCP, C library) decodes through.
//
//	d, err := nitrovin.Embedded() // or nitrovin.Open("nitrovin-2026_09.artifact")
//	req, err := contract.ParseRequest([]byte(`{"vin": "1HGCM82633A004352"}`))
//	dec, err := d.Decode(req)
//	out, err := json.Marshal(dec) // a Decode of docs/api/decode.schema.json
//
// The wire types, request parsing and layouts are package contract's.
// The embedded data is the month this module's go.mod pins
// github.com/joshuasprow/nitrovin-data to; only a binary that calls Embedded
// links it, about 61 MB.
package nitrovin

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/joshuasprow/nitrovin-data/pkg/artifact"
	"github.com/joshuasprow/nitrovin-data/pkg/embedded"
	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/decoder"
)

// modulePath is this module's path, as build info records it.
const modulePath = "github.com/joshuasprow/nitrovin"

// Decoder decodes VINs against one loaded artifact. It is safe for
// concurrent use; two Decoders may hold two data months in one process.
type Decoder struct {
	info   contract.Info
	engine *decoder.Engine
	// artifact is the data, for the vocab.Month a Translator measures
	// drift against.
	artifact *artifact.Artifact
}

// Embedded loads the artifact built into the binary, nitrovin-data's
// pkg/embedded: the data every surface but the C library decodes against
// when given no --data. Info's DataSource is contract.DataSourceEmbedded.
func Embedded() (*Decoder, error) {
	return Load(embedded.Artifact(), contract.DataSourceEmbedded)
}

// Open loads the artifact at path, as --data and NITROVIN_DATA do. Info's
// DataSource is its absolute path.
func Open(path string) (*Decoder, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, err
	}
	return Load(data, abs)
}

// Load decodes against artifact bytes data, which it keeps and never
// modifies. source is Info's DataSource: contract.DataSourceEmbedded, or the
// absolute path data was read from.
func Load(data []byte, source string) (*Decoder, error) {
	a, err := artifact.Open(data)
	if err != nil {
		return nil, err
	}
	info, err := contract.NewInfo(a, source, Version())
	if err != nil {
		return nil, err
	}
	return &Decoder{info: info, engine: decoder.NewEngine(a), artifact: a}, nil
}

// Info describes the decoder's build and data, GET /v1/info.
func (d *Decoder) Info() contract.Info { return d.info }

// Decode decodes one request. When req.Now is zero it reads the wall clock
// once; the engine never reads it. The error is a *contract.Problem: an
// InvalidRequest for a request Validate refuses, an Internal for a fault in
// nitrovin. A VIN is never an error; what is wrong with it is in the
// decode's error codes.
func (d *Decoder) Decode(req contract.Request) (contract.Decode, error) {
	if req.Now.IsZero() {
		req.Now = time.Now()
	}
	return d.decode(req)
}

// DecodeBatch decodes requests in order, reading the wall clock once for
// every request whose Now is zero, as the contract fixes for one HTTP
// request. It stops at the first error, a *contract.Problem whose pointer
// names the request ("/3/vin").
func (d *Decoder) DecodeBatch(reqs []contract.Request) ([]contract.Decode, error) {
	now := time.Now()
	out := make([]contract.Decode, len(reqs))
	for i, req := range reqs {
		if req.Now.IsZero() {
			req.Now = now
		}
		dec, err := d.decode(req)
		if err != nil {
			p := *contract.AsProblem(err)
			p.Pointer = fmt.Sprintf("/%d%s", i, p.Pointer)
			return nil, &p
		}
		out[i] = dec
	}
	return out, nil
}

func (d *Decoder) decode(req contract.Request) (contract.Decode, error) {
	if err := req.Validate(); err != nil {
		return contract.Decode{}, err
	}
	req.Now = req.Now.UTC().Truncate(time.Second)
	p, err := d.engine.Project(req.VIN, req.ModelYear, req.Now)
	if err != nil {
		return contract.Decode{}, contract.AsProblem(err)
	}
	dec, err := contract.NewDecode(p, req, &d.info)
	if err != nil {
		return contract.Decode{}, contract.AsProblem(err)
	}
	return dec, nil
}

// Version is the decoder version a decode carries: this module's version
// from the build info ("v1.2.0"), or "(devel)" when the build has none.
func Version() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	v := ""
	if bi.Main.Path == modulePath {
		v = bi.Main.Version
	} else {
		for _, m := range bi.Deps {
			if m.Path == modulePath {
				if m.Replace != nil {
					m = m.Replace
				}
				v = m.Version
				break
			}
		}
	}
	if v == "" {
		return "(devel)"
	}
	return v
}
