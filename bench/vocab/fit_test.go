package vocab

import (
	"fmt"
	"testing"
	"time"

	"github.com/joshuasprow/nitrovin/pkg/contract"
	"github.com/joshuasprow/nitrovin/pkg/nitrovin"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// BenchmarkFit times fit on the catalog-scale fixture (ADR 0017), loaded
// once: candidates for a vehicle, a year and a part type; a vehicle's
// part-type summary; its related vehicles; and reverse, one fitment key to
// its vehicles. ADR 0018's budget, all p99 on the reference machine: at
// most 5 ms for candidates, 10 ms for part types, 50 ms for related and
// 1 ms per key for reverse. Like the load benchmark, it reports its numbers
// for comparison and holds none of them: each request kind reports p50-ns
// and p99-ns, timed per request, and items, the answers' average total. A
// request that errors or answers anything but found fails it.
//
// The requests cycle through fitBenchCases different fitments, each read
// from the generator writeCatalog uses, so every one is answered found and
// no one vehicle's or key's path stays cached.
func BenchmarkFit(b *testing.B) {
	paths := catalogPaths(b)
	d, err := nitrovin.Embedded()
	if err != nil {
		b.Fatal(err)
	}
	tr, err := loadTranslator(paths)
	if err != nil {
		b.Fatal(err)
	}
	names, err := catalogVehicles(d.Month())
	if err != nil {
		b.Fatal(err)
	}
	var candidates, partTypes, related, reverse []contract.FitRequest
	for _, i := range fitBenchFitments() {
		from := contract.FitFrom{Vocabulary: "bench-models", Entry: names[catalogVehicleOf(i, 0)]}
		year := contract.Value(int32(catalogFirstOf(i, 0)))
		part := &contract.FitPart{Spine: "bench-types", ID: catalogTypeOf(i)}
		candidates = append(candidates, contract.FitRequest{From: from, ModelYear: year, Part: part, Now: now})
		partTypes = append(partTypes, contract.FitRequest{From: from, ModelYear: year, Now: now})
		related = append(related, contract.FitRequest{From: from, ModelYear: year, Related: true, Now: now})
		key := &contract.FitmentKey{Type: catalogTypeOf(i), Number: catalogNumberOf(i), Side: catalogSideOf(i)}
		reverse = append(reverse, contract.FitRequest{From: contract.FitFrom{Fitment: key}, Now: now})
	}
	for _, k := range []struct {
		name string
		reqs []contract.FitRequest
	}{
		{"candidates", candidates},
		{"part-types", partTypes},
		{"related", related},
		{"reverse", reverse},
	} {
		b.Run(k.name, func(b *testing.B) { measureFit(b, tr, k.reqs) })
	}
}

// fitBenchCases is how many different fitments BenchmarkFit's requests
// cycle through.
const fitBenchCases = 64

// fitBenchFitments are fitBenchCases fitments spread over the catalog, each
// with a mapped vehicle as its first application's and a second
// application, so its vehicle has candidates, part types and a related
// vehicle in its first application's first year.
func fitBenchFitments() []int {
	out := make([]int, 0, fitBenchCases)
	for k := range fitBenchCases {
		i := k * (catalogFitments / fitBenchCases)
		for catalogVehicleOf(i, 0) < catalogUnmapped || applicationsPer[i%len(applicationsPer)] < 2 {
			i++
		}
		out = append(out, i)
	}
	return out
}

// measureFit runs reqs in turn under b.Loop, failing on an error or an
// answer not found, and reports p50-ns and p99-ns, timed per request, and
// items, the answers' average total.
func measureFit(b *testing.B, tr *nitrovin.Translator, reqs []contract.FitRequest) {
	b.Helper()
	h := new(histogram)
	ops, items := 0, 0
	b.ReportAllocs()
	for b.Loop() {
		n := ops % len(reqs)
		t := time.Now()
		fit, err := tr.Fit(reqs[n])
		h.record(time.Since(t))
		if err != nil {
			b.Fatalf("request %d: %v", n, err)
		}
		if fit.Status != contract.FitFound {
			b.Fatalf("request %d: status %s, want %s", n, fit.Status, contract.FitFound)
		}
		ops++
		items += fit.Total
	}
	b.ReportMetric(h.quantile(0.50), "p50-ns")
	b.ReportMetric(h.quantile(0.99), "p99-ns")
	b.ReportMetric(float64(items)/float64(ops), "items")
}

// catalogVehicles are the catalog-scale fixture's vehicle entries, by
// index, as writeCatalog names them in bench-models: the month's first
// catalogModels models, the model numbered.
func catalogVehicles(m vocab.Month) ([]map[string]string, error) {
	var out []map[string]string
	for mk, mkName := range m.Makes() {
		for _, name := range m.Models(mk) {
			out = append(out, map[string]string{"make": mkName, "model": fmt.Sprintf("%s #%d", name, len(out))})
		}
	}
	if len(out) < catalogModels {
		return nil, fmt.Errorf("data month %s has %d models; the catalog needs %d", m.Name(), len(out), catalogModels)
	}
	return out[:catalogModels], nil
}
