package analytics

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Query limits: a panel is a chart, not an export.
const (
	maxMeasures = 8
	maxGroupBy  = 3
	maxFilters  = 10
	maxRows     = 500
	// drillRows / valueRows cap the drill list and a filter's choices.
	drillRows = 200
	valueRows = 200
)

// Compiled is a query as SQL. The SELECT list is Dims text columns (the
// bucket first when bucketed), then Measures float8 columns, then count(*);
// Fields describes the Dims and Measures columns in order. Limit is the
// rows asked for: the SQL fetches one more, so a full page is known to be
// truncated.
type Compiled struct {
	SQL      string
	Args     []any
	Dims     int
	Measures int
	Fields   []Field
	Limit    int
}

// builder accumulates the bound arguments; $1 is always the user.
type builder struct {
	args []any
}

func (b *builder) bind(v any) string {
	b.args = append(b.args, v)
	return "$" + strconv.Itoa(len(b.args))
}

// Compile checks q against the catalog and builds its SQL, with userID bound
// as $1 — the only way analytics SQL is ever built.
func Compile(q Query, userID string) (Compiled, error) {
	g, ok := grainOf(q.Grain)
	if !ok {
		return Compiled{}, invalidf("unknown grain %q", q.Grain)
	}
	if len(q.Measures) > maxMeasures {
		return Compiled{}, invalidf("at most %d measures, got %d", maxMeasures, len(q.Measures))
	}
	if len(q.GroupBy) > maxGroupBy {
		return Compiled{}, invalidf("at most %d group-by dimensions, got %d", maxGroupBy, len(q.GroupBy))
	}
	if q.Limit < 0 {
		return Compiled{}, invalidf("limit %d is negative", q.Limit)
	}
	b := &builder{args: []any{userID}}
	var cols, fs []Field
	var sel []string

	switch q.Time.Bucket {
	case "":
	case BucketDay, BucketWeek, BucketMonth:
		sel = append(sel, "date_trunc('"+q.Time.Bucket+"', "+g.timeExpr+")::text")
		cols = append(cols, Field{ID: "bucket", Label: strings.ToUpper(q.Time.Bucket[:1]) + q.Time.Bucket[1:], Type: TypeTime})
	default:
		return Compiled{}, invalidf("unknown bucket %q", q.Time.Bucket)
	}
	for _, id := range q.GroupBy {
		d, ok := g.dim(id)
		if !ok {
			return Compiled{}, invalidf("unknown dimension %q on grain %s", id, g.id)
		}
		if slices.ContainsFunc(cols, func(f Field) bool { return f.ID == id }) {
			return Compiled{}, invalidf("dimension %q grouped twice", id)
		}
		sel = append(sel, "("+d.expr+")::text")
		cols = append(cols, d.Field)
	}
	dims := len(sel)
	for _, m := range q.Measures {
		sql, f, err := measureSQL(g, m)
		if err != nil {
			return Compiled{}, err
		}
		sel = append(sel, "("+sql+")::float8")
		fs = append(fs, f)
	}
	sel = append(sel, "count(*)")

	conds, err := where(g, b, q.Filters, q.Time)
	if err != nil {
		return Compiled{}, err
	}
	order, err := orderBy(q, cols, dims, len(q.Measures))
	if err != nil {
		return Compiled{}, err
	}
	limit := q.Limit
	if limit == 0 || limit > maxRows {
		limit = maxRows
	}

	var sb strings.Builder
	sb.WriteString("SELECT " + strings.Join(sel, ", ") + " FROM " + g.from)
	if len(conds) > 0 {
		sb.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	if dims > 0 {
		ords := make([]string, dims)
		for i := range ords {
			ords[i] = strconv.Itoa(i + 1)
		}
		sb.WriteString(" GROUP BY " + strings.Join(ords, ", "))
	}
	if order != "" {
		sb.WriteString(" ORDER BY " + order)
	}
	sb.WriteString(" LIMIT " + strconv.Itoa(limit+1))
	return Compiled{
		SQL: sb.String(), Args: b.args, Dims: dims, Measures: len(q.Measures),
		Fields: append(cols, fs...), Limit: limit,
	}, nil
}

// measureSQL is a measure's aggregate and how its column reads.
func measureSQL(g *grain, m MeasureRef) (string, Field, error) {
	if d, ok := find(g.derived, m.Field); ok {
		if m.Agg != "" {
			return "", Field{}, invalidf("%s is derived and takes no agg (got %q)", m.Field, m.Agg)
		}
		return d.expr, d.Field, nil
	}
	p, ok := find(g.measures, m.Field)
	if !ok {
		return "", Field{}, invalidf("unknown measure %q on grain %s", m.Field, g.id)
	}
	if m.Agg == "" {
		return "", Field{}, invalidf("measure %s needs an agg", m.Field)
	}
	if !slices.Contains(p.Aggs, m.Agg) {
		return "", Field{}, invalidf("unknown agg %q", m.Agg)
	}
	f := p.Field
	f.Label += " (" + string(m.Agg) + ")"
	f.Aggs = []Agg{m.Agg}
	return aggSQL(m.Agg, p.expr), f, nil
}

// where is the grain's own predicate, the filters and the time range.
func where(g *grain, b *builder, filters []Filter, t TimeRange) ([]string, error) {
	if len(filters) > maxFilters {
		return nil, invalidf("at most %d filters, got %d", maxFilters, len(filters))
	}
	var conds []string
	if g.where != "" {
		conds = append(conds, g.where)
	}
	for _, f := range filters {
		d, ok := g.dim(f.Field)
		if !ok {
			return nil, invalidf("unknown filter dimension %q on grain %s", f.Field, g.id)
		}
		col := "(" + d.expr + ")::text"
		switch f.Op {
		case OpEq, OpNeq:
			if len(f.Values) != 1 {
				return nil, invalidf("filter %s %s takes one value, got %d", f.Field, f.Op, len(f.Values))
			}
			op := " = "
			if f.Op == OpNeq {
				op = " <> "
			}
			conds = append(conds, col+op+b.bind(f.Values[0]))
		case OpIn, OpNotIn:
			if len(f.Values) == 0 {
				return nil, invalidf("filter %s %s needs a value", f.Field, f.Op)
			}
			if f.Op == OpIn {
				conds = append(conds, col+" = ANY("+b.bind(f.Values)+"::text[])")
			} else {
				conds = append(conds, col+" <> ALL("+b.bind(f.Values)+"::text[])")
			}
		default:
			return nil, invalidf("unknown filter op %q", f.Op)
		}
	}
	if t.From != nil {
		conds = append(conds, g.timeExpr+" >= "+b.bind(*t.From))
	}
	if t.To != nil {
		conds = append(conds, g.timeExpr+" < "+b.bind(*t.To))
	}
	return conds, nil
}

// orderBy is the ORDER BY clause by column ordinal. A score order is done
// after scoring; the SQL then keeps the default order.
func orderBy(q Query, cols []Field, dims, measures int) (string, error) {
	countCol := dims + measures + 1
	def := ""
	switch {
	case q.Time.Bucket != "":
		def = "1"
	case dims > 0:
		def = strconv.Itoa(countCol) + " DESC"
	}
	if q.Order == nil {
		return def, nil
	}
	dir := " ASC NULLS LAST"
	if q.Order.Desc {
		dir = " DESC NULLS LAST"
	}
	by := q.Order.By
	switch {
	case by == "n":
		return strconv.Itoa(countCol) + dir, nil
	case by == "score":
		if q.Score == nil {
			return "", invalidf("order by score needs a scorecard")
		}
		return def, nil
	case strings.HasPrefix(by, "m") && len(by) > 1 && by[1] >= '0' && by[1] <= '9':
		i, err := strconv.Atoi(by[1:])
		if err != nil || i >= measures {
			return "", invalidf("unknown order %q", by)
		}
		return strconv.Itoa(dims+i+1) + dir, nil
	}
	if i := slices.IndexFunc(cols, func(f Field) bool { return f.ID == by }); i >= 0 {
		return strconv.Itoa(i+1) + dir, nil
	}
	return "", invalidf("unknown order %q", by)
}

// compileValues is a dimension's distinct values over the time range.
func compileValues(g Grain, fieldID string, t TimeRange, userID string) (Compiled, error) {
	gr, ok := grainOf(g)
	if !ok {
		return Compiled{}, invalidf("unknown grain %q", g)
	}
	d, ok := gr.dim(fieldID)
	if !ok {
		return Compiled{}, invalidf("unknown dimension %q on grain %s", fieldID, g)
	}
	b := &builder{args: []any{userID}}
	conds, err := where(gr, b, nil, TimeRange{From: t.From, To: t.To})
	if err != nil {
		return Compiled{}, err
	}
	conds = append(conds, "("+d.expr+") IS NOT NULL")
	sql := fmt.Sprintf("SELECT DISTINCT (%s)::text FROM %s WHERE %s ORDER BY 1 LIMIT %d",
		d.expr, gr.from, strings.Join(conds, " AND "), valueRows)
	return Compiled{SQL: sql, Args: b.args, Dims: 1, Fields: []Field{d.Field}, Limit: valueRows}, nil
}

// drillCols are a DrillRun's columns, in its field order.
const drillCols = `f.run_id, coalesce(r.ticket_title, ''), f.flow_name, f.flow_hash, f.state, f.created_at, f.wall_ms, f.cost_usd`

// compileDrill is the runs behind a group: every run a matching row of the
// grain belongs to, newest first.
func compileDrill(g Grain, filters []Filter, t TimeRange, userID string) (Compiled, error) {
	gr, ok := grainOf(g)
	if !ok {
		return Compiled{}, invalidf("unknown grain %q", g)
	}
	if gr.runID == "" {
		return Compiled{}, invalidf("grain %s has no runs to drill into", g)
	}
	b := &builder{args: []any{userID}}
	conds, err := where(gr, b, filters, TimeRange{From: t.From, To: t.To})
	if err != nil {
		return Compiled{}, err
	}
	sql := fmt.Sprintf(`SELECT %s FROM run_facts f LEFT JOIN runs r ON r.run_id = f.run_id
WHERE f.user_id = $1 AND f.run_id IN (SELECT DISTINCT %s FROM %s WHERE %s)
ORDER BY f.created_at DESC LIMIT %d`,
		drillCols, gr.runID, gr.from, strings.Join(conds, " AND "), drillRows)
	return Compiled{SQL: sql, Args: b.args, Limit: drillRows}, nil
}
