package pg

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/joshuasprow/nitrovin/pkg/stock"
	"github.com/joshuasprow/nitrovin/pkg/vocab"
)

// queries are a checked relation's three lookups, each one SELECT that
// answers every list of a call: shelf by fitment keys, shelf rows with no
// number by part type, vehicle and year, and yard by vPIC model or catalog
// vehicle and years.
//
// Each takes the same first four parameters: $1 how many lists, $2 the
// locations to keep (empty: every one), $3 the page's offset and $4 its
// limit; then the lookup's own, one array per column of its keys, each
// key tagged with its list. Its result is a row per list's page row, in
// list order then id order, or one row with a null id for a list whose
// page is empty, each carrying its list's total and the release.
type queries struct {
	byFitment, byVehicle, yard string
	// nv is how many vehicle columns there are: fields' (a vocabulary's
	// fields), or vpic's three.
	nv     int
	fields []string
	vpic   bool
}

// col is a relation column as a query reads it.
func col(name string) string { return "r." + pgx.Identifier{name}.Sanitize() }

// queries are the lookups of the source's relation, whose as_of column
// has data type asOf ("" when it has none).
func (s *Source) queries(asOf string) *queries {
	q := &queries{fields: s.cols.fields, vpic: s.cols.vpic}
	vehicle := s.cols.vehicle()
	q.nv = len(vehicle)
	rel := quoted(s.c.Relation)
	release := "NULL::text"
	if s.c.ReleaseRelation != "" {
		release = "(SELECT " + pgx.Identifier{"release"}.Sanitize() + "::text FROM " + quoted(s.c.ReleaseRelation) + ")"
	}
	asOfExpr := "NULL::text"
	switch asOf {
	case "timestamp with time zone":
		asOfExpr = `to_char(` + col("as_of") + ` AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`
	case "timestamp without time zone": // read as UTC
		asOfExpr = `to_char(` + col("as_of") + `, 'YYYY-MM-DD"T"HH24:MI:SS"Z"')`
	case "date":
		asOfExpr = `to_char(` + col("as_of") + `, 'YYYY-MM-DD')`
	}

	// selected is a matched row's columns, in result order: shelf names
	// its fitment key or its part type, and vehicles its vehicle and year.
	selected := func(shelfKey, partType, vehicles, decoded bool) string {
		var b []string
		b = append(b, col("id")+"::text AS c_id", col("location")+"::text AS c_location")
		for _, c := range []string{"type", "number", "side"} {
			if shelfKey {
				b = append(b, col(c)+"::text AS c_"+c)
			} else {
				b = append(b, "NULL::text AS c_"+c)
			}
		}
		if partType {
			b = append(b, col("part_type")+"::text AS c_part_type")
		} else {
			b = append(b, "NULL::text AS c_part_type")
		}
		for i, c := range vehicle {
			if vehicles {
				b = append(b, col(c)+"::text AS c_v"+strconv.Itoa(i))
			} else {
				b = append(b, "NULL::text AS c_v"+strconv.Itoa(i))
			}
		}
		if vehicles {
			b = append(b, col("year")+"::int4 AS c_year")
		} else {
			b = append(b, "NULL::int4 AS c_year")
		}
		for _, c := range []string{"decoded_make", "decoded_model", "decoded_year"} {
			if decoded {
				b = append(b, col(c)+"::int4 AS c_"+c)
			} else {
				b = append(b, "NULL::int4 AS c_"+c)
			}
		}
		b = append(b, col("fields")+"::text AS c_fields", asOfExpr+" AS c_as_of")
		return strings.Join(b, ", ")
	}
	where := col("id") + " IS NOT NULL AND " + col("location") + " IS NOT NULL AND (cardinality($2::text[]) = 0 OR " + col("location") + " = ANY($2::text[]))"
	// vehicleIs matches a relation row's vehicle to the vehicle columns
	// of key k, c_v0… (vpic: one of make, model or ext).
	vehicleIs := func(k string) string {
		if q.vpic {
			return "(" + col(vehicle[0]) + " = " + k + ".v0 OR " + col(vehicle[1]) + " = " + k + ".v1 OR " + col(vehicle[2]) + " = " + k + ".v2)"
		}
		var b []string
		for i, c := range vehicle {
			b = append(b, col(c)+" = "+k+".v"+strconv.Itoa(i))
		}
		return strings.Join(b, " AND ")
	}
	// keys is a lookup's keys, an unnest of one array per column from
	// parameter $5: names are its columns, types their array types.
	keys := func(name string, from int, names, types []string) string {
		params := make([]string, len(names))
		for i := range names {
			params[i] = "$" + strconv.Itoa(from+i) + "::" + types[i]
		}
		return name + " AS (SELECT * FROM unnest(" + strings.Join(params, ", ") + ") AS " + name + "(" + strings.Join(names, ", ") + "))"
	}
	vnames, vtypes := make([]string, len(vehicle)), make([]string, len(vehicle))
	for i := range vehicle {
		vnames[i], vtypes[i] = "v"+strconv.Itoa(i), "text[]"
		if q.vpic && i < 2 {
			vtypes[i] = "int4[]"
		}
	}
	vcols := make([]string, len(vehicle))
	for i := range vehicle {
		vcols[i] = "p.c_v" + strconv.Itoa(i)
	}
	// page answers matched rows m: each list's total, and its page.
	page := func(with, m string) string {
		out := []string{"l.list", "coalesce(t.total, 0)", release, "p.c_id", "p.c_location", "p.c_type", "p.c_number", "p.c_side", "p.c_part_type"}
		out = append(out, vcols...)
		out = append(out, "p.c_year", "p.c_decoded_make", "p.c_decoded_model", "p.c_decoded_year", "p.c_by_model", "p.c_fields", "p.c_as_of")
		return "WITH " + with + ",\nm AS (" + m + "),\n" +
			"t AS (SELECT list, count(*) AS total FROM m GROUP BY list),\n" +
			`p AS (SELECT m.*, row_number() OVER (PARTITION BY m.list ORDER BY m.c_id COLLATE "C") AS rn FROM m)` + "\n" +
			"SELECT " + strings.Join(out, ", ") + "\n" +
			"FROM generate_series(0, $1::int4 - 1) AS l(list)\n" +
			"LEFT JOIN t ON t.list = l.list\n" +
			"LEFT JOIN p ON p.list = l.list AND p.rn > $3::int8 AND p.rn <= $3::int8 + $4::int8\n" +
			"ORDER BY l.list, p.rn"
	}

	if s.info.Holds == stock.Shelf {
		q.byFitment = page(
			keys("k", 5, []string{"list", "type", "number", "side"}, []string{"int4[]", "text[]", "text[]", "text[]"}),
			"SELECT k.list, "+selected(true, false, false, false)+", NULL::bool AS c_by_model FROM k JOIN "+rel+" r ON "+
				col("type")+" = k.type AND "+col("number")+" = k.number AND "+col("side")+" IS NOT DISTINCT FROM k.side WHERE "+where)
		q.byVehicle = page(
			keys("k", 5, append([]string{"list", "part_type", "year"}, vnames...), append([]string{"int4[]", "text[]", "int4[]"}, vtypes...)),
			"SELECT k.list, "+selected(false, true, true, false)+", NULL::bool AS c_by_model FROM k JOIN "+rel+" r ON "+
				col("number")+" IS NULL AND "+col("part_type")+" = k.part_type AND "+vehicleIs("k")+" AND "+
				col("year")+" IS NOT NULL AND (k.year IS NULL OR "+col("year")+" = k.year) WHERE "+where)
		return q
	}
	decoded := col("decoded_make") + " IS NOT NULL AND " + col("decoded_model") + " IS NOT NULL AND " + col("decoded_year") + " IS NOT NULL"
	within := func(k, year string) string {
		return "(" + k + ".first IS NULL OR " + year + " >= " + k + ".first) AND (" + k + ".last IS NULL OR " + year + " <= " + k + ".last)"
	}
	m := "SELECT km.list, " + selected(false, false, true, true) + ", true AS c_by_model FROM km JOIN " + rel + " r ON " +
		col("decoded_model") + " = km.model AND " + decoded + " AND " + within("km", col("decoded_year")) + " WHERE " + where
	with := keys("km", 5, []string{"list", "model", "first", "last"}, []string{"int4[]", "int4[]", "int4[]", "int4[]"})
	if len(vehicle) > 0 {
		with += ",\n" + keys("kv", 9, append([]string{"list", "first", "last"}, vnames...), append([]string{"int4[]", "int4[]", "int4[]"}, vtypes...))
		m += "\nUNION ALL\nSELECT kv.list, " + selected(false, false, true, true) + ", false FROM kv JOIN " + rel + " r ON " +
			vehicleIs("kv") + " AND " + col("year") + " IS NOT NULL AND " + within("kv", col("year")) +
			" WHERE " + where + " AND NOT (" + decoded + ")"
	}
	q.yard = page(with, m)
	return q
}

// ShelfByFitment answers the shelf rows of each list's keys: one query.
func (s *Source) ShelfByFitment(ctx context.Context, lists [][]vocab.FitmentKey, p stock.Page) ([]stock.Result, stock.Meta, error) {
	var li []int32
	var types, numbers []string
	var sides []*string
	for i, keys := range lists {
		seen := map[vocab.FitmentKey]bool{}
		for _, k := range keys {
			if seen[k] {
				continue
			}
			seen[k] = true
			li, types, numbers = append(li, int32(i)), append(types, k.Type), append(numbers, k.Number)
			var side *string
			if k.Side != vocab.SideNone {
				side = new(k.Side.String())
			}
			sides = append(sides, side)
		}
	}
	return s.lookup(ctx, stock.Shelf, len(lists), p, func(q *queries) string { return q.byFitment }, stock.MatchedFitment, li, types, numbers, sides)
}

// ShelfByVehicle answers the shelf rows with no number of each list's part
// types, vehicles and year: one query.
func (s *Source) ShelfByVehicle(ctx context.Context, lists []stock.ShelfVehicle, p stock.Page) ([]stock.Result, stock.Meta, error) {
	var li []int32
	var types []string
	var years []*int32
	vs := s.vehicleArgs()
	for i, l := range lists {
		seen := map[string]bool{}
		for _, t := range l.PartTypes {
			for j := range l.Vehicles {
				v := &l.Vehicles[j]
				k := t + "\x00" + vocab.VehicleKey(v)
				if seen[k] || !vs.add(v) {
					continue
				}
				seen[k] = true
				li, types, years = append(li, int32(i)), append(types, t), append(years, year32(l.Year))
			}
		}
	}
	return s.lookup(ctx, stock.Shelf, len(lists), p, func(q *queries) string { return q.byVehicle }, stock.MatchedVehicle, append([]any{li, types, years}, vs.args()...)...)
}

// Yard answers the yard rows of each list's vPIC models and catalog
// vehicles in their years: one query. A model or vehicle named twice in a
// list is asked for once, in the union of its years, so no row matches
// twice.
func (s *Source) Yard(ctx context.Context, lists []stock.YardQuery, p stock.Page) ([]stock.Result, stock.Meta, error) {
	var mli, models, vli []int32
	var mfirst, mlast, vfirst, vlast []*int32
	vs := s.vehicleArgs()
	for i, l := range lists {
		byModel := map[int32][]span{}
		var order []int32
		for _, m := range l.Models {
			if _, ok := byModel[m.Model]; !ok {
				order = append(order, m.Model)
			}
			byModel[m.Model] = append(byModel[m.Model], spanOf(m.First, m.Last))
		}
		for _, m := range order {
			for _, sp := range merge(byModel[m]) {
				mli, models = append(mli, int32(i)), append(models, m)
				mfirst, mlast = append(mfirst, sp.first()), append(mlast, sp.last())
			}
		}
		if s.cols.vehicle() == nil {
			continue // no catalog: no vehicle columns, and no vehicle to ask for
		}
		byVehicle := map[string][]span{}
		var vorder []*vocab.Vehicle
		for j := range l.Vehicles {
			v := &l.Vehicles[j]
			k := vocab.VehicleKey(&v.Vehicle)
			if _, ok := byVehicle[k]; !ok {
				vorder = append(vorder, &v.Vehicle)
			}
			byVehicle[k] = append(byVehicle[k], spanOf(v.First, v.Last))
		}
		for _, v := range vorder {
			for _, sp := range merge(byVehicle[vocab.VehicleKey(v)]) {
				if vs.add(v) {
					vli, vfirst, vlast = append(vli, int32(i)), append(vfirst, sp.first()), append(vlast, sp.last())
				}
			}
		}
	}
	args := []any{mli, models, mfirst, mlast}
	if s.cols.vehicle() != nil {
		args = append(append(args, vli, vfirst, vlast), vs.args()...)
	}
	return s.lookup(ctx, stock.Yard, len(lists), p, func(q *queries) string { return q.yard }, "", args...)
}

// vehicleArgs are the vehicle columns of a lookup's keys, one array per
// column, for the source's vehicle columns.
type vehicleArgs struct {
	fields []string
	vpic   bool
	text   [][]*string // a vocabulary's fields, or vpic's ext as text[2]
	ids    [2][]*int32 // vpic's make and model
}

func (s *Source) vehicleArgs() *vehicleArgs {
	return &vehicleArgs{fields: s.cols.fields, vpic: s.cols.vpic, text: make([][]*string, max(len(s.cols.fields), 3))}
}

// add adds v's columns, false when it has none of the source's: a vehicle
// that names fields its vocabulary lacks, or of the other kind.
func (a *vehicleArgs) add(v *vocab.Vehicle) bool {
	if a.vpic {
		if v.Entry != nil {
			return false
		}
		a.ids[0], a.ids[1] = append(a.ids[0], nonZero(v.Make)), append(a.ids[1], nonZero(v.Model))
		var ext *string
		if v.Ext != "" {
			ext = new(v.Ext)
		}
		a.text[2] = append(a.text[2], ext)
		return true
	}
	vals := make([]*string, len(a.fields))
	for i, f := range a.fields {
		val, ok := v.Entry[f]
		if !ok {
			return false
		}
		vals[i] = new(val)
	}
	for i := range vals {
		a.text[i] = append(a.text[i], vals[i])
	}
	return true
}

// args are the arrays, in column order.
func (a *vehicleArgs) args() []any {
	if a.vpic {
		return []any{a.ids[0], a.ids[1], a.text[2]}
	}
	out := make([]any, len(a.fields))
	for i := range a.fields {
		out[i] = a.text[i]
	}
	return out
}

func nonZero(id int32) *int32 {
	if id == 0 {
		return nil
	}
	return new(id)
}

func year32(y *int) *int32 {
	if y == nil {
		return nil
	}
	return new(int32(*y))
}

// span is a range of model years, each end inclusive, unbounded at
// math.MinInt32 or math.MaxInt32.
type span struct{ lo, hi int64 }

func spanOf(first, last *int) span {
	sp := span{math.MinInt32, math.MaxInt32}
	if first != nil {
		sp.lo = int64(*first)
	}
	if last != nil {
		sp.hi = int64(*last)
	}
	return sp
}

func (sp span) first() *int32 {
	if sp.lo == math.MinInt32 {
		return nil
	}
	return new(int32(sp.lo))
}

func (sp span) last() *int32 {
	if sp.hi == math.MaxInt32 {
		return nil
	}
	return new(int32(sp.hi))
}

// merge is spans as the fewest disjoint spans holding the same years, in
// order: overlapping and adjacent spans join.
func merge(spans []span) []span {
	slices.SortFunc(spans, func(a, b span) int { return cmp.Compare(a.lo, b.lo) })
	var out []span
	for _, sp := range spans {
		if n := len(out); n > 0 && sp.lo <= out[n-1].hi+1 {
			out[n-1].hi = max(out[n-1].hi, sp.hi)
			continue
		}
		out = append(out, sp)
	}
	return out
}

// lookup runs one of the relation's queries for n lists of what holds,
// with p's page and the lookup's own args: every list's page and total,
// the release it read, and as_of the oldest of the rows it returned.
// matched is how a shelf row matched; a yard row says its own.
func (s *Source) lookup(ctx context.Context, holds stock.Holds, n int, p stock.Page, sql func(*queries) string, matched stock.Matched, args ...any) ([]stock.Result, stock.Meta, error) {
	out := make([]stock.Result, n)
	for i := range out {
		out[i].Rows = []stock.Row{}
	}
	if s.info.Holds != holds || n == 0 {
		return out, stock.Meta{}, nil
	}
	q, pool, err := s.ready(ctx)
	if err != nil {
		return nil, stock.Meta{}, err
	}
	bctx, cancel := context.WithTimeout(ctx, s.c.ConnectTimeout+s.c.StatementTimeout)
	defer cancel()
	conn, err := pool.Acquire(bctx)
	if err != nil {
		return nil, stock.Meta{}, s.acquireFailed(ctx, pool, err)
	}
	defer conn.Release()
	location := p.Location
	if location == nil {
		location = []string{}
	}
	rows, err := conn.Query(bctx, sql(q), append([]any{int32(n), location, int64(p.Offset), int64(max(p.Limit, 0))}, args...)...)
	if err != nil {
		return nil, stock.Meta{}, s.unavailable(ctx, err)
	}
	meta, err := scan(rows, q, holds, matched, out)
	if err != nil {
		return nil, stock.Meta{}, s.unavailable(ctx, err)
	}
	return out, meta, nil
}

// acquireFailed is why a lookup found no connection: every one busy until
// its bound, or none to be made.
func (s *Source) acquireFailed(ctx context.Context, pool *pgxpool.Pool, err error) *stock.Unavailable {
	if ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
		if st := pool.Stat(); st.AcquiredConns() >= st.MaxConns() {
			return &stock.Unavailable{Reason: fmt.Sprintf("all %d of its connections were busy until the lookup's deadline", st.MaxConns())}
		}
	}
	return s.unavailable(ctx, err)
}

// scan reads a lookup's result into out, one Result per list.
func scan(rows pgx.Rows, q *queries, holds stock.Holds, matched stock.Matched, out []stock.Result) (stock.Meta, error) {
	var (
		list                        int32
		total                       int64
		release, id, location       *string
		typ, number, side, partType *string
		year, dmake, dmodel, dyear  *int32
		byModel                     *bool
		fields, asOf                *string
		vehicle                     = make([]*string, q.nv)
		meta                        stock.Meta
	)
	dest := []any{&list, &total, &release, &id, &location, &typ, &number, &side, &partType}
	for i := range vehicle {
		dest = append(dest, &vehicle[i])
	}
	dest = append(dest, &year, &dmake, &dmodel, &dyear, &byModel, &fields, &asOf)
	_, err := pgx.ForEachRow(rows, dest, func() error {
		if list < 0 || int(list) >= len(out) {
			return fmt.Errorf("pg: a result row of list %d of %d", list, len(out))
		}
		r := &out[list]
		r.Total = int(total)
		if release != nil {
			meta.Release = *release
		}
		if id == nil {
			return nil
		}
		row := stock.Row{ID: *id, Location: *location, Matched: matched}
		if typ != nil && number != nil {
			k := vocab.FitmentKey{Type: *typ, Number: *number}
			if side != nil {
				k.Side, _ = vocab.ParseSide(*side) // the key asked for it, so it parses
			}
			row.Fitment = &k
		}
		if partType != nil {
			row.PartType = *partType
		}
		row.Vehicle = vehicleOf(q, vehicle)
		if year != nil {
			row.Year = new(int(*year))
		}
		if holds == stock.Yard {
			row.Matched = stock.MatchedCatalogVehicle
			if byModel != nil && *byModel {
				row.Matched = stock.MatchedVIN
			}
			if dmake != nil && dmodel != nil && dyear != nil {
				row.Decoded = &vocab.StockDecoded{Make: *dmake, Model: *dmodel, Year: int(*dyear)}
			}
		}
		// fields are a JSON object, as a file's are; null, or any other
		// value, which the column type allows, is served as {}.
		row.Fields = json.RawMessage("{}")
		if fields != nil && strings.HasPrefix(*fields, "{") {
			row.Fields = json.RawMessage(*fields)
		}
		if asOf != nil {
			row.AsOf = *asOf
			if meta.AsOf == "" || row.AsOf < meta.AsOf {
				meta.AsOf = row.AsOf
			}
		}
		r.Rows = append(r.Rows, row)
		return nil
	})
	return meta, err
}

// vehicleOf is a row's catalog vehicle from its vehicle columns, nil when
// it names none: a vocabulary's every field, or one of vpic's make, model
// or ext.
func vehicleOf(q *queries, cols []*string) *vocab.Vehicle {
	if q.nv == 0 {
		return nil
	}
	if q.vpic {
		var v vocab.Vehicle
		switch {
		case cols[0] != nil:
			id, err := strconv.ParseInt(*cols[0], 10, 32)
			if err != nil {
				return nil
			}
			v.Make = int32(id)
		case cols[1] != nil:
			id, err := strconv.ParseInt(*cols[1], 10, 32)
			if err != nil {
				return nil
			}
			v.Model = int32(id)
		case cols[2] != nil:
			v.Ext = *cols[2]
		default:
			return nil
		}
		return &v
	}
	entry := make(map[string]string, len(q.fields))
	for i, f := range q.fields {
		if cols[i] == nil {
			return nil
		}
		entry[f] = *cols[i]
	}
	return &vocab.Vehicle{Entry: entry}
}
