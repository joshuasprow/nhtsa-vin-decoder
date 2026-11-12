package nitrovin

import (
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// Translate answers one translate request (ADR 0012): from a VIN, a
// vocabulary entry or a spine entry, through exactly one spine, to the
// target vocabularies. A VIN, as from or as context, is decoded with the
// request's model year as its hint; its decode checks qualifiers and gives
// the year. When req.Now is zero it reads the wall clock once. Nothing to
// translate is never an error: the error is a *contract.Problem, an
// InvalidRequest for a request Validate refuses or that names a
// vocabulary, entry field or spine the loaded files lack, an Internal for
// a fault in nitrovin.
func (t *Translator) Translate(req contract.TranslateRequest) (contract.Translation, error) {
	return t.core.Translate(req)
}

// TranslateBatch translates requests in order, reading the wall clock once
// for every request whose Now is zero, as the contract fixes for one HTTP
// request. It stops at the first error, a *contract.Problem whose pointer
// names the request ("/3/to/0").
func (t *Translator) TranslateBatch(reqs []contract.TranslateRequest) ([]contract.Translation, error) {
	return t.core.TranslateBatch(reqs)
}

// List answers one list request (ADR 0012): the catalog without Of, else
// one page of vPIC's makes, a make's models or a model's values for an
// element (ADR 0015, Month's ModelValues), a vocabulary's entries with
// their mappings, a declared spine's entries or an extension set's
// extensions, or the loaded catalog's fitments (ADR 0018). The error is a
// *contract.Problem, an InvalidRequest for an Of nothing loaded has, a
// where field the listing lacks or a vPIC where it refuses, a limit out of
// range or an after it never gave.
func (t *Translator) List(req contract.ListRequest) (contract.List, error) {
	return t.core.List(req)
}

// Fit answers one fit request (ADR 0018) from the loaded catalog
// (contract.Fitter): from a vehicle, its part types with counts, with a
// part its candidates, with related the vehicles that share its
// fitments; from a fitment key, its vehicles. A VIN is decoded with the
// request's model year as its hint; its decode checks qualifiers and
// gives the year. When req.Now is zero it reads the wall clock once.
// Nothing to fit is never an error, no catalog loaded included: it is a
// status. The error is a *contract.Problem, an InvalidRequest for a
// malformed request, an Internal for a fault in nitrovin.
func (t *Translator) Fit(req contract.FitRequest) (contract.Fit, error) {
	return t.core.Fit(req)
}

// FitBatch fits requests in order, reading the wall clock once for every
// request whose Now is zero, as the contract fixes for one HTTP request.
// It stops at the first error, a *contract.Problem whose pointer names the
// request ("/3/part").
func (t *Translator) FitBatch(reqs []contract.FitRequest) ([]contract.Fit, error) {
	return t.core.FitBatch(reqs)
}

// decode is the core's decoder: the engine, answering with the Decoder's
// data month and version.
func (t *Translator) decode(vin string, modelYear contract.Null[int32], now time.Time) (contract.VINDecode, error) {
	p, err := t.d.engine.Project(vin, modelYear, now)
	if err != nil {
		return contract.VINDecode{}, err
	}
	return contract.VINDecode{Projection: p, DataMonth: t.core.DataMonth, DecoderVersion: t.core.DecoderVersion}, nil
}
