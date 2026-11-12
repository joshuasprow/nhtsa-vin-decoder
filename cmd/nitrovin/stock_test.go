package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// downSource is a live stock source that is down: every lookup fails with
// err.
type downSource struct {
	name  string
	holds stock.Holds
	err   error
}

func (s downSource) Info() stock.Info {
	return stock.Info{Name: s.name, Holds: s.holds, Kind: stock.KindLive}
}
func (s downSource) ShelfByFitment(context.Context, [][]vocab.FitmentKey, stock.Page) ([]stock.Result, stock.Meta, error) {
	return nil, stock.Meta{}, s.err
}
func (s downSource) ShelfByVehicle(context.Context, []stock.ShelfVehicle, stock.Page) ([]stock.Result, stock.Meta, error) {
	return nil, stock.Meta{}, s.err
}
func (s downSource) Yard(context.Context, []stock.YardQuery, stock.Page) ([]stock.Result, stock.Meta, error) {
	return nil, stock.Meta{}, s.err
}

// stockService answers fits from f, everything else from the embedded
// service.
type stockService struct {
	decodeService
	f contract.Fitter
}

func (s stockService) Fit(ctx context.Context, r contract.FitRequest) (contract.Fit, error) {
	return s.f.Fit(ctx, r)
}
func (s stockService) FitBatch(ctx context.Context, rs []contract.FitRequest) ([]contract.Fit, error) {
	return s.f.FitBatch(ctx, rs)
}

// TestServeStockUnavailable: over HTTP, a stock source that is down
// answers unavailable in its block, with its own reason or, for any other
// error, a fixed one that never shows the error's text; the file sources'
// rows and the rest of the answer stand.
func TestServeStockUnavailable(t *testing.T) {
	fixtureData(t)
	d, err := nitrovin.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files, err := vocab.Open(fixture, fitmentFixture, stockFixture)
	if err != nil {
		t.Fatal(err)
	}
	set, err := vocab.NewSet(d.Month(), files)
	if err != nil {
		t.Fatal(err)
	}
	info := d.Info()
	f := contract.Fitter{Translator: contract.Translator{Set: set, DataMonth: info.DataMonth, DecoderVersion: info.DecoderVersion}, Catalog: set.Catalog()}
	for _, s := range set.Stock() {
		f.Stock = append(f.Stock, stock.NewFile(s, nil))
	}
	f.Stock = append(f.Stock,
		downSource{"live-shelf", stock.Shelf, &stock.Unavailable{Reason: "the database is not answering"}},
		downSource{"live-yard", stock.Yard, errors.New("dial tcp 10.0.0.5:5432: user=stock")})
	a, err := newAPI(stockService{translating(t, d, files...), f}, log.New(io.Discard, "", 0), io.Discard, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a)
	defer srv.Close()

	resp, err := client.Get(srv.URL + "/v1/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-2001&stock=shelf&stock=yard&now=" + now)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// The parts of the answer this test reads.
	type list struct {
		Rows    []json.RawMessage
		Total   int
		Sources []contract.StockSource
	}
	var got struct {
		Status   contract.FitStatus
		Vehicles []json.RawMessage
		Shelf    *list
		Yard     *list
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &got) != nil || got.Shelf == nil || got.Yard == nil {
		t.Fatalf("GET /v1/fit: %d %s", resp.StatusCode, body)
	}
	last := func(l *list) contract.StockSource { return l.Sources[len(l.Sources)-1] }
	if s := last(got.Shelf); s.Name != "live-shelf" || s.Kind != "live" || s.Status != "unavailable" || s.Reason != "the database is not answering" {
		t.Errorf("the live shelf's block %+v", s)
	}
	if s := last(got.Yard); s.Name != "live-yard" || s.Status != "unavailable" || s.Reason != contract.StockFailed {
		t.Errorf("the live yard's block %+v", s)
	}
	if strings.Contains(string(body), "10.0.0.5") || got.Shelf.Total == 0 || got.Status != contract.FitFound || len(got.Vehicles) == 0 {
		t.Errorf("the answer: %s", body)
	}
}
