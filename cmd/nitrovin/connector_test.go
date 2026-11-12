package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/joshuasprow/nitrovin/pkg/contract"
)

// liveFixture is testdata/vocab/stock/live: a connector's file for each of
// the synthetic shelf and yard sources, reading $NITROVIN_STOCK_DSN.
var liveFixture = abs("../../testdata/vocab/stock/live")

// connectorSecrets are the parts of the DSN TestConnectorSurfaces sets
// that no output may show.
var connectorSecrets = []string{"stockuser", "hunter2", "stockdb", "127.0.0.1"}

func showsNoSecret(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range connectorSecrets {
		if strings.Contains(text, s) {
			t.Errorf("%s shows %q: %s", what, s, text)
		}
	}
}

// liveReasons are the reasons of every live source block in an answer,
// once each, sorted.
func liveReasons(t *testing.T, answer string) []string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(answer), &v); err != nil {
		t.Fatalf("not JSON: %s", answer)
	}
	seen := map[string]bool{}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if v["kind"] == "live" {
				s := fmt.Sprintf("%s %s %s: %s", v["name"], v["connector"], v["status"], v["reason"])
				if !seen[s] {
					seen[s] = true
					out = append(out, s)
				}
			}
			for _, x := range v {
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(v)
	slices.Sort(out)
	return out
}

// TestConnectorSurfaces: a connector's file loads through --vocab and
// NITROVIN_VOCAB on every Go surface, the CLI, stdio, serve and MCP; it
// connects on its first lookup, not at load, so a database that refuses
// connections stops nothing: fit answers its lists unavailable, the rest
// of the answer standing, and Info says so. No output shows the DSN's
// user, password, host or database.
func TestConnectorSurfaces(t *testing.T) {
	fixtureData(t)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens there now
	t.Setenv("NITROVIN_STOCK_DSN", "postgres://stockuser:hunter2@"+addr+"/stockdb?sslmode=disable")
	vocabArgs := []string{"--vocab", fixture, "--vocab", fitmentFixture, "--vocab", liveFixture}
	const request = `{"from":{"fitment":{"type":"brake-rotor-front","number":"BR-2001"}},"stock":["shelf","yard"],"now":"` + now + `"}`
	want := []string{
		"shelf-annex postgres unavailable: the database is unreachable",
		"shelf-stock postgres unavailable: the database is unreachable",
		"yard-stock postgres unavailable: the database is unreachable",
	}
	check := func(what, answer string) {
		t.Helper()
		if got := liveReasons(t, answer); strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Errorf("%s: live sources\n%s\nwant\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
		}
		if !strings.Contains(answer, `"status":"found"`) {
			t.Errorf("%s: the answer did not stand: %s", what, answer)
		}
		showsNoSecret(t, what, answer)
	}

	r := cli(t, nil, "", append(vocabArgs, "info")...)
	if r.exit != 0 {
		t.Fatalf("info: exit %d, %s", r.exit, r.stderr)
	}
	var info contract.Info
	if err := json.Unmarshal([]byte(r.stdout), &info); err != nil {
		t.Fatal(err)
	}
	for _, v := range info.Vocabularies {
		if v.StockCounts != nil && (v.Connector != "postgres" || v.Status != contract.StockUnchecked || v.Relation == "") {
			t.Errorf("info before any fit: %s %+v; want connector postgres, its relation, unchecked", v.Name, *v.StockCounts)
		}
	}
	showsNoSecret(t, "info", r.stdout)

	r = cli(t, nil, request+"\n", append(vocabArgs, "fit", "-")...)
	if r.exit != 0 || r.stderr != "" {
		t.Fatalf("fit: exit %d, stderr %q", r.exit, r.stderr)
	}
	check("the CLI's fit", r.stdout)

	r = cli(t, nil, `{"id":1,"op":"fit",`+request[1:]+"\n", append(vocabArgs, "stdio")...)
	if r.exit != 0 {
		t.Fatalf("stdio: exit %d, %s", r.exit, r.stderr)
	}
	check("stdio", r.stdout)

	s := startServe(t, nil, append(vocabArgs, "--addr", "127.0.0.1:0")...)
	base := "http://127.0.0.1:" + s.port
	resp, err := client.Get(base + "/v1/fit?from.fitment.type=brake-rotor-front&from.fitment.number=BR-2001&stock=shelf&stock=yard&now=" + now)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	check("serve's GET /v1/fit", string(body))
	resp, err = client.Get(base + "/v1/info")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	for _, v := range info.Vocabularies {
		if v.StockCounts != nil && (v.Status != contract.StockUnavailable || v.Reason != "the database is unreachable") {
			t.Errorf("serve's /v1/info after a fit: %s %+v", v.Name, *v.StockCounts)
		}
	}
	showsNoSecret(t, "serve's /v1/info", string(body))
	s.stop(t)

	vocabList := strings.Join([]string{fixture, fitmentFixture, liveFixture}, string(os.PathListSeparator))
	session := mcpSession(t, "", vocabList, "2026-07-28", []mcpCall{{tool: "fit", args: request}})
	for _, w := range want {
		reason := w[strings.Index(w, ": ")+2:]
		if !strings.Contains(session.stdout, reason) || !strings.Contains(session.stdout, w[:strings.Index(w, " ")]) {
			t.Errorf("MCP's fit: no %q in %s", w, session.stdout)
		}
	}
	showsNoSecret(t, "MCP", session.stdout)

	// With the variable unset, every source says which one it reads.
	t.Setenv("NITROVIN_STOCK_DSN", "")
	r = cli(t, nil, request+"\n", append(vocabArgs, "fit", "-")...)
	unset := "environment variable NITROVIN_STOCK_DSN, which holds the DSN, is not set"
	if got := liveReasons(t, r.stdout); len(got) != 3 || !strings.HasSuffix(got[0], unset) {
		t.Errorf("fit with NITROVIN_STOCK_DSN unset: %q; want %q", got, unset)
	}
}
