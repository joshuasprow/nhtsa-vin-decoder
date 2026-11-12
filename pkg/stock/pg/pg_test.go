package pg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// liveFiles are the catalog and the connector files of
// testdata/vocab/stock/live, linked, by name.
func liveFiles(t *testing.T) map[string]*vocab.File {
	t.Helper()
	files, err := vocab.Open("../../../testdata/vocab/fitment", "../../../testdata/vocab/stock/live")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*vocab.File{}
	for _, f := range files {
		byName[f.Name] = f
	}
	return byName
}

// shelfSource is testdata/vocab/stock/live's shelf-stock with dsn.
func shelfSource(t *testing.T, dsn string) *Source {
	t.Helper()
	fs := liveFiles(t)
	s := New(fs["shelf-stock"], fs["parts-catalog"], fs["catalog-models"], dsn)
	t.Cleanup(s.Close)
	return s
}

// secrets are the parts of a DSN no answer may show.
var secrets = []string{"stockuser", "hunter2", "stockdb", "db.internal", "127.0.0.1"}

// noSecrets fails when text shows any of secrets.
func noSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(text, s) {
			t.Errorf("%s shows %q: %s", what, s, text)
		}
	}
}

var oneList = [][]vocab.FitmentKey{{{Type: "mirror", Number: "MR-1001", Side: vocab.SideLeft}}}

// TestNoDSN: a connector whose environment variable is unset, or holds a
// DSN that does not parse, is unavailable from the start, its reason
// naming the variable and nothing of the DSN.
func TestNoDSN(t *testing.T) {
	for _, tc := range []struct{ name, dsn, want string }{
		{"unset", "", "environment variable NITROVIN_STOCK_DSN, which holds the DSN, is not set"},
		{"malformed", "postgres://stockuser:hunter2@db.internal:5432/stockdb?sslmode=sometimes", "the DSN in environment variable NITROVIN_STOCK_DSN does not parse"},
		{"not a DSN", "host=db.internal user=stockuser password=hunter2 dbname=stockdb port=many", "the DSN in environment variable NITROVIN_STOCK_DSN does not parse"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := shelfSource(t, tc.dsn)
			if h := s.Health(); !h.Checked || h.Down == nil || h.Down.Reason != tc.want {
				t.Errorf("Health %+v, want unavailable: %s", h, tc.want)
			}
			_, _, err := s.ShelfByFitment(context.Background(), oneList, stock.Page{Limit: 10})
			u, ok := errors.AsType[*stock.Unavailable](err)
			if !ok || u.Reason != tc.want {
				t.Fatalf("lookup: %v, want unavailable: %s", err, tc.want)
			}
			noSecrets(t, "the reason", u.Reason)
		})
	}
}

// TestUnreachable: a database that refuses connections makes the source
// unavailable, with a reason naming no host, user, password or database;
// nothing connects at load; and it tries again at most every Retry, so
// lookups in between dial nothing.
func TestUnreachable(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close() // nothing listens there now
	s := shelfSource(t, fmt.Sprintf("postgres://stockuser:hunter2@%s/stockdb?sslmode=disable", addr))
	var dials atomic.Int32
	dial := s.cfg.ConnConfig.DialFunc
	s.cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		dials.Add(1)
		return dial(ctx, network, addr)
	}
	clock := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return clock }

	if h := s.Health(); h.Checked || dials.Load() != 0 {
		t.Fatalf("before any lookup: %+v, %d dials; want unchecked, none", h, dials.Load())
	}
	lookup := func() string {
		t.Helper()
		_, _, err := s.ShelfByFitment(context.Background(), oneList, stock.Page{Limit: 10})
		u, ok := errors.AsType[*stock.Unavailable](err)
		if !ok {
			t.Fatalf("lookup: %v, want *stock.Unavailable", err)
		}
		noSecrets(t, "the reason", u.Reason)
		return u.Reason
	}
	if got := lookup(); got != reasonUnreachable || dials.Load() == 0 {
		t.Fatalf("first lookup: %q after %d dials; want %q", got, dials.Load(), reasonUnreachable)
	}
	if h := s.Health(); !h.Checked || h.Down == nil || h.Down.Reason != reasonUnreachable {
		t.Errorf("Health after it %+v", h)
	}
	n := dials.Load()
	clock = clock.Add(Retry - time.Millisecond)
	if got := lookup(); got != reasonUnreachable || dials.Load() != n {
		t.Errorf("within Retry: %q, %d more dials; want none", got, dials.Load()-n)
	}
	clock = clock.Add(time.Millisecond)
	if lookup(); dials.Load() == n {
		t.Error("after Retry: no dial; want another try")
	}
}

// TestCancelledCheck: a check cut short by its caller says nothing of the
// database, so the next lookup tries at once.
func TestCancelledCheck(t *testing.T) {
	s := shelfSource(t, "postgres://stockuser:hunter2@127.0.0.1:1/stockdb?sslmode=disable")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := s.ShelfByFitment(ctx, oneList, stock.Page{Limit: 10})
	if u, ok := errors.AsType[*stock.Unavailable](err); !ok || u.Reason != reasonCancelled {
		t.Fatalf("lookup: %v, want %q", err, reasonCancelled)
	}
	if h := s.Health(); h.Checked {
		t.Errorf("Health %+v after a cancelled check; want unchecked", h)
	}
}

// TestOtherKind: a lookup of the kind a source does not hold answers every
// list empty, with no connection, as a file does.
func TestOtherKind(t *testing.T) {
	s := shelfSource(t, "")
	rs, meta, err := s.Yard(context.Background(), []stock.YardQuery{{Models: []stock.ModelYears{{Model: 1938}}}, {}}, stock.Page{Limit: 10})
	if err != nil || len(rs) != 2 || rs[0].Rows == nil || rs[0].Total != 0 || meta != (stock.Meta{}) {
		t.Errorf("Yard of a shelf: %+v, %+v, %v; want two empty lists", rs, meta, err)
	}
}

// TestReasons: a database's error becomes a fixed reason, whatever its
// message holds, and only the statement timeout's names a number.
func TestReasons(t *testing.T) {
	s := shelfSource(t, "postgres://stockuser:hunter2@db.internal/stockdb")
	ctx := context.Background()
	for _, tc := range []struct {
		err  error
		want string
	}{
		{&pgconn.PgError{Code: "28P01", Message: `password authentication failed for user "stockuser"`}, reasonRefused},
		{&pgconn.PgError{Code: "3D000", Message: `database "stockdb" does not exist`}, reasonNoDatabase},
		{&pgconn.PgError{Code: "53300", Message: "too many connections"}, reasonFull},
		{&pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, reasonUnreachable},
		{&pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, "the query ran past its statement timeout, 1000 ms"},
		{&pgconn.PgError{Code: "42703", Message: `column "vehicle_model" does not exist`}, reasonChanged},
		{&pgconn.PgError{Code: "21000", Message: "more than one row returned by a subquery"}, "release relation stock.shelf_stock_release holds more than one row"},
		{&pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "db.internal"`}, "the query failed with SQLSTATE 22P02"},
		{fmt.Errorf("dial tcp db.internal:5432: %w", context.DeadlineExceeded), reasonSlow},
		{errors.New("read tcp 10.0.0.5:5432: connection reset by stockuser"), reasonUnreachable},
	} {
		got := s.unavailable(ctx, tc.err)
		if got.Reason != tc.want {
			t.Errorf("%v: %q, want %q", tc.err, got.Reason, tc.want)
		}
		noSecrets(t, "the reason", got.Reason)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := s.unavailable(cancelled, &pgconn.PgError{Code: "57014"}); got.Reason != reasonCancelled {
		t.Errorf("a query the caller cancelled: %q, want %q", got.Reason, reasonCancelled)
	}
}

// TestMerge: a model or vehicle asked for in several year ranges is asked
// for once per disjoint run of years, so no row matches twice.
func TestMerge(t *testing.T) {
	y := func(n int) *int { return &n }
	text := func(spans []span) string {
		var out []string
		for _, sp := range spans {
			f, l := "…", "…"
			if p := sp.first(); p != nil {
				f = fmt.Sprint(*p)
			}
			if p := sp.last(); p != nil {
				l = fmt.Sprint(*p)
			}
			out = append(out, f+"-"+l)
		}
		return strings.Join(out, " ")
	}
	for _, tc := range []struct {
		in   []span
		want string
	}{
		{[]span{spanOf(y(2009), y(2012)), spanOf(y(2009), y(2012))}, "2009-2012"},
		{[]span{spanOf(y(2013), nil), spanOf(y(2009), y(2012))}, "2009-…"},
		{[]span{spanOf(y(2009), y(2010)), spanOf(y(2014), y(2016))}, "2009-2010 2014-2016"},
		{[]span{spanOf(y(2014), y(2016)), spanOf(nil, y(2010)), spanOf(y(2011), y(2011))}, "…-2011 2014-2016"},
		{[]span{spanOf(nil, nil), spanOf(y(2010), y(2010))}, "…-…"},
	} {
		if got := text(merge(tc.in)); got != tc.want {
			t.Errorf("merge: %s, want %s", got, tc.want)
		}
	}
}

// TestNoDriverElsewhere: only the nitrovin binary links the driver. The
// C library, the browser's WebAssembly and the packages they build on
// carry none (ADR 0020).
func TestNoDriverElsewhere(t *testing.T) {
	for _, tc := range []struct {
		env  []string
		pkgs []string
	}{
		{nil, []string{"../../../cmd/libnitrovin", "../../vocab", "../../nitrovin", "../../stdio", "../../contract", "../../stock"}},
		{[]string{"GOOS=js", "GOARCH=wasm"}, []string{"../../../cmd/nitrovin-sw", "../../vocab", "../../ui/..."}},
	} {
		cmd := osexec.Command("go", append([]string{"list", "-deps"}, tc.pkgs...)...)
		cmd.Env = append(os.Environ(), tc.env...)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list -deps %v: %v", tc.pkgs, err)
		}
		for _, p := range strings.Fields(string(out)) {
			if strings.HasPrefix(p, "github.com/jackc/") || strings.HasSuffix(p, "/pkg/stock/pg") {
				t.Errorf("%v %v links %s", tc.env, tc.pkgs, p)
			}
		}
	}
}
