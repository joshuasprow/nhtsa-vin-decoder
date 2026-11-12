// Package pg is the Postgres connector (ADR 0020): a stock source read
// from a table or view at request time, behind stock.Source.
//
// A connector is declared by a stock file with a header and no lines
// (vocab.StockConnector): the relation its rows are in, for a shelf the
// one-row relation of its catalog release, and the environment variable
// holding the DSN, so no file carries a secret. nitrovin dictates the
// relation's column names, never its SQL: each lookup is one fixed,
// parameterised SELECT, for every list of one answer at once, bounded by
// the page, and nothing is cached.
//
// Nothing connects at load. The first lookup connects and checks the
// relation's columns against information_schema; a missing or mistyped
// column makes the source unavailable, naming the column. A source that
// cannot connect is unavailable and tries again at most every Retry, so a
// dead database never stops nitrovin nor piles up connections. Every
// failure is a *stock.Unavailable whose reason is fixed text that names no
// DSN, host, user or database.
//
// The nitrovin binary links this package into its Go surfaces (serve,
// stdio, the CLI and MCP). Nothing built for the C library or the browser
// imports it, and package vocab never does, so neither carries a driver.
package pg

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// Retry is how long a source that could not connect, or found its
// relation wrong, answers unavailable before it tries again.
const Retry = 5 * time.Second

// Connect is stock.Connect reading each connector's DSN with getenv, as
// the nitrovin binary passes it in nitrovin.TranslatorConfig.
func Connect(getenv func(string) string) stock.Connect {
	return func(d stock.Declared) stock.Live {
		return New(d, getenv(d.File.Connector.DSNEnv))
	}
}

// Source is a connector's stock source. It is safe for concurrent use.
type Source struct {
	info stock.Info
	c    vocab.StockConnector
	cols columns
	// cfg is the pool's configuration, nil when broken says why there is
	// none: no DSN, or one that does not parse.
	cfg    *pgxpool.Config
	broken *stock.Unavailable
	now    func() time.Time

	mu sync.Mutex
	// pool is made by the first check; q is the checked relation's
	// queries, nil until a check passes and after a failure that may mean
	// the relation changed or the database went away.
	pool *pgxpool.Pool
	q    *queries
	// health is what the latest check found, tried when it failed;
	// checking is closed when the check under way ends.
	health   stock.Health
	tried    time.Time
	checking chan struct{}
	closed   bool
}

var _ stock.Live = (*Source)(nil)

// New is the source connector file d.File declares, connecting with dsn
// when first asked: d.Catalog is the catalog its vehicles are of (nil:
// none is loaded) and d.Vehicles that catalog's vehicle vocabulary (nil:
// its vehicles are vpic). It does no I/O. An empty dsn, or one that does
// not parse, makes a source that is always unavailable.
func New(d stock.Declared, dsn string) *Source {
	f, catalog, vehicles := d.File, d.Catalog, d.Vehicles
	s := &Source{
		info: stock.Info{Name: f.Name, Holds: f.Holds, Kind: stock.KindLive, Catalog: f.StockCatalog, Connector: vocab.ConnectorPostgres, Relation: f.Connector.Relation},
		c:    *f.Connector,
		now:  time.Now,
	}
	switch {
	case vehicles != nil:
		s.cols.fields = vehicles.Fields
	case catalog != nil && catalog.Vehicles == vocab.Reserved:
		s.cols.vpic = true
	}
	if dsn == "" {
		s.broken = &stock.Unavailable{Reason: fmt.Sprintf("environment variable %s, which holds the DSN, is not set", s.c.DSNEnv)}
		return s
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil { // its text may quote the DSN
		s.broken = &stock.Unavailable{Reason: fmt.Sprintf("the DSN in environment variable %s does not parse", s.c.DSNEnv)}
		return s
	}
	cfg.MaxConns = int32(s.c.MaxConnections)
	cfg.MinConns, cfg.MinIdleConns = 0, 0
	cc := cfg.ConnConfig
	cc.ConnectTimeout = s.c.ConnectTimeout
	// Every query runs in a READ ONLY transaction, its own, bounded by
	// the statement timeout: set once per connection, so a lookup is one
	// round trip.
	cc.RuntimeParams["default_transaction_read_only"] = "on"
	cc.RuntimeParams["statement_timeout"] = strconv.FormatInt(s.c.StatementTimeout.Milliseconds(), 10)
	if cc.RuntimeParams["application_name"] == "" {
		cc.RuntimeParams["application_name"] = "nitrovin"
	}
	s.cfg = cfg
	return s
}

// Info describes the source.
func (s *Source) Info() stock.Info { return s.info }

// Health is what the latest check found: unchecked before the first
// lookup, the reason it is unavailable after one that failed.
func (s *Source) Health() stock.Health {
	if s.broken != nil {
		return stock.Health{Checked: true, Down: s.broken}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.health
}

// Close closes the source's connections and stops its pool's background
// work; a lookup after it is unavailable. It is safe to call more than
// once.
func (s *Source) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed, s.q = true, nil
	s.health = stock.Health{Checked: true, Down: &stock.Unavailable{Reason: reasonClosed}}
	if s.pool != nil {
		s.pool.Close()
	}
}

// The reasons a lookup gives that name no column. None names a DSN, host,
// user or database.
const (
	reasonCancelled   = "the request ended before the database answered"
	reasonUnreachable = "the database is unreachable"
	reasonRefused     = "the database refused the connection's credentials"
	reasonNoDatabase  = "the database the DSN names does not exist"
	reasonFull        = "the database has no connection to spare"
	reasonChanged     = "the relation changed since it was checked; it is checked again on a later lookup"
	reasonSlow        = "the database did not answer in time"
	reasonClosed      = "the source is closed"
)

// ready is the checked relation's queries and the pool to run them on,
// checking the relation first when no check has passed: at most one check
// runs at once, and one that failed is not tried again for Retry.
func (s *Source) ready(ctx context.Context) (*queries, *pgxpool.Pool, error) {
	if s.broken != nil {
		return nil, nil, s.broken
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, nil, &stock.Unavailable{Reason: reasonClosed}
		}
		if s.q != nil {
			q, pool := s.q, s.pool
			s.mu.Unlock()
			return q, pool, nil
		}
		if wait := s.checking; wait != nil {
			s.mu.Unlock()
			select {
			case <-wait:
				continue
			case <-ctx.Done():
				return nil, nil, &stock.Unavailable{Reason: reasonCancelled}
			}
		}
		if s.health.Down != nil && s.now().Sub(s.tried) < Retry {
			down := s.health.Down
			s.mu.Unlock()
			return nil, nil, down
		}
		if s.pool == nil {
			pool, err := pgxpool.NewWithConfig(context.Background(), s.cfg)
			if err != nil { // only a bad configuration, which ParseConfig passed
				s.mu.Unlock()
				return nil, nil, &stock.Unavailable{Reason: reasonUnreachable}
			}
			s.pool = pool
		}
		done, pool := make(chan struct{}), s.pool
		s.checking = done
		s.mu.Unlock()

		q, down := s.check(ctx, pool)
		s.mu.Lock()
		s.checking = nil
		close(done)
		switch {
		case s.closed:
			down = &stock.Unavailable{Reason: reasonClosed}
		case down == nil:
			s.q, s.health = q, stock.Health{Checked: true}
		case ctx.Err() == nil: // a check the caller cut short says nothing of the database
			s.health, s.tried = stock.Health{Checked: true, Down: down}, s.now()
		}
		s.mu.Unlock()
		if down != nil {
			return nil, nil, down
		}
		return q, pool, nil
	}
}

// reset drops the checked queries after a failure that may mean the
// database went away or the relation changed: lookups answer down for
// Retry, then check again.
func (s *Source) reset(down *stock.Unavailable) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.q = nil
	s.health, s.tried = stock.Health{Checked: true, Down: down}, s.now()
}

// check connects and checks the relation's columns, and its release
// relation's, against information_schema: the queries for those columns,
// or why it cannot answer.
func (s *Source) check(ctx context.Context, pool *pgxpool.Pool) (*queries, *stock.Unavailable) {
	bctx, cancel := context.WithTimeout(ctx, s.c.ConnectTimeout+s.c.StatementTimeout)
	defer cancel()
	conn, err := pool.Acquire(bctx)
	if err != nil {
		return nil, s.unavailable(ctx, err)
	}
	defer conn.Release()
	got, err := relationColumns(bctx, conn, s.c.Relation)
	if err != nil {
		return nil, s.unavailable(ctx, err)
	}
	asOf, down := checkColumns(s.c.Relation, got, s.cols.want(s.info.Holds))
	if down != nil {
		return nil, down
	}
	if s.c.ReleaseRelation != "" {
		got, err := relationColumns(bctx, conn, s.c.ReleaseRelation)
		if err != nil {
			return nil, s.unavailable(ctx, err)
		}
		if _, down := checkColumns(s.c.ReleaseRelation, got, []column{{"release", classText, false}}); down != nil {
			return nil, down
		}
	}
	return s.queries(asOf), nil
}

// relationColumns are relation's columns that the connection's user can
// read, by name, each its information_schema data_type; empty when there
// is no such relation.
func relationColumns(ctx context.Context, conn *pgxpool.Conn, relation string) (map[string]string, error) {
	rows, err := conn.Query(ctx, `SELECT c.column_name::text, c.data_type::text
FROM information_schema.columns c
JOIN pg_catalog.pg_class k ON k.oid = to_regclass($1)
JOIN pg_catalog.pg_namespace n ON n.oid = k.relnamespace
WHERE c.table_schema = n.nspname AND c.table_name = k.relname`, quoted(relation))
	if err != nil {
		return nil, err
	}
	got := map[string]string{}
	var name, typ string
	_, err = pgx.ForEachRow(rows, []any{&name, &typ}, func() error {
		got[name] = typ
		return nil
	})
	return got, err
}

// A column class is the data types a column may have.
type class int

const (
	classText class = iota
	classInteger
	classJSON
	classTime
)

var classTypes = map[class][]string{
	classText:    {"text", "character varying", "character"},
	classInteger: {"smallint", "integer", "bigint"},
	classJSON:    {"jsonb", "json"},
	classTime:    {"timestamp with time zone", "timestamp without time zone", "date"},
}

var classNames = map[class]string{
	classText:    "text (text, character varying or character)",
	classInteger: "an integer (smallint, integer or bigint)",
	classJSON:    "jsonb or json",
	classTime:    "a time (timestamp with or without time zone, or date)",
}

// column is a column a relation must, or may, have.
type column struct {
	name     string
	class    class
	optional bool
}

// columns are the vehicle columns a source's relation has: one per field
// of the catalog's vehicle vocabulary (vehicle_<field>), or, for a vpic
// catalog, vehicle_make, vehicle_model and vehicle_ext; none when no
// catalog is loaded.
type columns struct {
	fields []string
	vpic   bool
}

// vehicle are the vehicle columns' names.
func (c columns) vehicle() []string {
	if c.vpic {
		return []string{"vehicle_make", "vehicle_model", "vehicle_ext"}
	}
	out := make([]string, len(c.fields))
	for i, f := range c.fields {
		out[i] = "vehicle_" + f
	}
	return out
}

// want are the columns a relation holding holds must have (ADR 0020).
func (c columns) want(holds stock.Holds) []column {
	out := []column{{"id", classText, false}, {"location", classText, false}}
	if holds == stock.Shelf {
		out = append(out, column{"type", classText, false}, column{"number", classText, false}, column{"side", classText, false}, column{"part_type", classText, false})
	} else {
		out = append(out, column{"decoded_make", classInteger, false}, column{"decoded_model", classInteger, false}, column{"decoded_year", classInteger, false})
	}
	for i, name := range c.vehicle() {
		cl := classText
		if c.vpic && i < 2 {
			cl = classInteger
		}
		out = append(out, column{name, cl, false})
	}
	return append(out, column{"year", classInteger, false}, column{"fields", classJSON, false}, column{"as_of", classTime, true})
}

// checkColumns checks relation's columns got against want: the first
// missing or mistyped column makes it unavailable, by name. asOf is the
// data type of its as_of column, "" when it has none.
func checkColumns(relation string, got map[string]string, want []column) (asOf string, down *stock.Unavailable) {
	if len(got) == 0 {
		return "", &stock.Unavailable{Reason: fmt.Sprintf("relation %s does not exist, or the connection's user cannot read it", relation)}
	}
	for _, c := range want {
		typ, ok := got[c.name]
		switch {
		case !ok && c.optional:
			continue
		case !ok:
			return "", &stock.Unavailable{Reason: fmt.Sprintf("column %s is missing from %s", c.name, relation)}
		case !slices.Contains(classTypes[c.class], typ):
			return "", &stock.Unavailable{Reason: fmt.Sprintf("column %s of %s is %s, not %s", c.name, relation, typ, classNames[c.class])}
		}
		if c.name == "as_of" {
			asOf = typ
		}
	}
	return asOf, nil
}

// quoted is relation, "name" or "schema.name", as an SQL identifier.
func quoted(relation string) string {
	return pgx.Identifier(strings.Split(relation, ".")).Sanitize()
}

// unavailable is why a lookup or check failed with err, ctx being the
// caller's context, as a reason fit for every caller: a deadline that is
// not the caller's is the source's own bound. A failure that may mean the
// database went away or the relation changed resets the source.
func (s *Source) unavailable(ctx context.Context, err error) *stock.Unavailable {
	if u, ok := errors.AsType[*stock.Unavailable](err); ok {
		return u
	}
	if pe, ok := errors.AsType[*pgconn.PgError](err); ok {
		switch c := pe.Code; {
		case c == "57014": // query_canceled: the statement timeout, or a cancelled context
			if ctx.Err() == nil {
				return &stock.Unavailable{Reason: fmt.Sprintf("the query ran past its statement timeout, %d ms", s.c.StatementTimeout.Milliseconds())}
			}
		case c == "21000": // cardinality_violation: the release subquery
			return &stock.Unavailable{Reason: fmt.Sprintf("release relation %s holds more than one row", s.c.ReleaseRelation)}
		case c == "42P01", c == "42703", c == "42501", c == "42804": // the relation changed, or its grants did
			return s.down(reasonChanged)
		case strings.HasPrefix(c, "28"):
			return s.down(reasonRefused)
		case c == "3D000":
			return s.down(reasonNoDatabase)
		case c == "53300":
			return s.down(reasonFull)
		case strings.HasPrefix(c, "08"), strings.HasPrefix(c, "57P"):
			return s.down(reasonUnreachable)
		default:
			return &stock.Unavailable{Reason: "the query failed with SQLSTATE " + c}
		}
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return &stock.Unavailable{Reason: reasonCancelled}
	}
	// A connection that could not be made, refused or timed out: the
	// database is gone, whether at the first check or after it, so the
	// source answers down at once for Retry rather than dialling again on
	// every lookup.
	if _, ok := errors.AsType[*pgconn.ConnectError](err); ok {
		return s.down(reasonUnreachable)
	}
	if errors.Is(err, context.DeadlineExceeded) || pgconn.Timeout(err) {
		return &stock.Unavailable{Reason: reasonSlow}
	}
	return s.down(reasonUnreachable)
}

// down is reason, resetting the source when it has connected: what a
// failed connection says of the database lasts Retry.
func (s *Source) down(reason string) *stock.Unavailable {
	u := &stock.Unavailable{Reason: reason}
	s.mu.Lock()
	checked := s.q != nil
	s.mu.Unlock()
	if checked {
		s.reset(u)
	}
	return u
}
