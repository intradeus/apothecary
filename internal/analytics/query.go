package analytics

import (
	"errors"
	"fmt"
	"time"
)

var (
	// ErrInvalid is a query, dashboard or scorecard the catalog does not
	// allow; the error's text says which token.
	ErrInvalid  = errors.New("invalid")
	ErrNotFound = errors.New("not found")
	// ErrReadOnly is a write to a built-in dashboard or scorecard.
	ErrReadOnly = errors.New("built-ins are read-only")
	// ErrTimeout is a query the database gave up on (the 5s statement
	// timeout): the caller narrows it.
	ErrTimeout = errors.New("query timed out")
)

// invalidError is-matches ErrInvalid without polluting the message.
type invalidError struct{ msg string }

func (e invalidError) Error() string         { return e.msg }
func (invalidError) Is(target error) bool    { return target == ErrInvalid }
func invalidf(format string, a ...any) error { return invalidError{fmt.Sprintf(format, a...)} }

// MeasureRef names a measure: a plain one with its agg, a derived one with
// none.
type MeasureRef struct {
	Field string `json:"field"`
	Agg   Agg    `json:"agg,omitempty"`
}

// Filter ops.
const (
	OpEq    = "eq"
	OpNeq   = "neq"
	OpIn    = "in"
	OpNotIn = "not_in"
)

// Filter restricts a dimension: eq|neq take one value, in|not_in one or
// more. Values compare as text (a bool dimension is "true"/"false").
type Filter struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// Buckets.
const (
	BucketDay   = "day"
	BucketWeek  = "week"
	BucketMonth = "month"
)

// TimeRange restricts the grain's time field to [From, To); Bucket, when
// set, groups by that period first.
type TimeRange struct {
	From   *time.Time `json:"from,omitempty"`
	To     *time.Time `json:"to,omitempty"`
	Bucket string     `json:"bucket,omitempty"`
}

// OrderBy sorts the rows: by "n" (the row count), "score", "m<i>" (the i-th
// measure, from 0), "bucket", or a group-by dimension's id.
type OrderBy struct {
	By   string `json:"by"`
	Desc bool   `json:"desc,omitempty"`
}

// ScoreSpec ranks the rows by a scorecard: min-max across the rows, or
// relative to the Baseline row (its dimension values) when one is named.
type ScoreSpec struct {
	Scorecard string   `json:"scorecard"`
	Baseline  []string `json:"baseline,omitempty"`
}

// Query is one whitelisted question over a grain.
type Query struct {
	Grain    Grain        `json:"grain"`
	Measures []MeasureRef `json:"measures"`
	GroupBy  []string     `json:"group_by,omitempty"`
	Filters  []Filter     `json:"filters,omitempty"`
	Time     TimeRange    `json:"time"`
	Order    *OrderBy     `json:"order,omitempty"`
	// Limit caps the rows (default and maximum 500).
	Limit int        `json:"limit,omitempty"`
	Score *ScoreSpec `json:"score,omitempty"`
}

// Row is one group: its dimension values (the bucket first when
// bucketed), one value per measure (nil when the group has none), its row
// count, whether that count is too small to trust, and its score.
type Row struct {
	Dims   []string   `json:"dims"`
	Values []*float64 `json:"values"`
	N      int64      `json:"n"`
	Small  bool       `json:"small"`
	Score  *float64   `json:"score,omitempty"`
}

// Result is a query's answer. Labels maps a dimension id (project_id,
// flow_hash) to display names for its values. Truncated says rows were cut
// at the limit.
type Result struct {
	Dims      []Field                      `json:"dims"`
	Measures  []Field                      `json:"measures"`
	Rows      []Row                        `json:"rows"`
	Labels    map[string]map[string]string `json:"labels"`
	Truncated bool                         `json:"truncated"`
}

// Value is one distinct dimension value, labelled.
type Value struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// DrillRun is one run behind a group.
type DrillRun struct {
	RunID     string    `json:"run_id"`
	Title     string    `json:"title"`
	FlowName  string    `json:"flow_name"`
	FlowHash  string    `json:"flow_hash"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	WallMS    int64     `json:"wall_ms"`
	CostUSD   float64   `json:"cost_usd"`
}

// Panel vizzes.
var vizzes = []string{"number", "table", "bar", "line", "scatter", "heatmap", "box"}

// Panel widths, in twelfths.
var widths = []int{3, 4, 6, 8, 12}

// Panel is one query on a dashboard and how it draws. The dashboard
// supplies the time range when it is viewed, so a saved query has none.
type Panel struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Query Query  `json:"query"`
	Viz   string `json:"viz"`
	Width int    `json:"width"`
}

// Dashboard is a person's (or a built-in) set of panels. Filters are the
// dimensions its filter bar offers.
type Dashboard struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Builtin   bool      `json:"builtin"`
	Filters   []string  `json:"filters"`
	Panels    []Panel   `json:"panels"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DashboardInput is what a person writes of a dashboard.
type DashboardInput struct {
	Name    string   `json:"name"`
	Filters []string `json:"filters"`
	Panels  []Panel  `json:"panels"`
}

// Term directions.
const (
	DirLower  = "lower"
	DirHigher = "higher"
)

// Term is one weighted measure of a scorecard: lower or higher is better.
type Term struct {
	Measure   MeasureRef `json:"measure"`
	Direction string     `json:"direction"`
	Weight    float64    `json:"weight"`
}

// Scorecard is a weighted blend of measures a query's rows are ranked by.
type Scorecard struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Name      string    `json:"name"`
	Builtin   bool      `json:"builtin"`
	Terms     []Term    `json:"terms"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ScorecardInput is what a person writes of a scorecard.
type ScorecardInput struct {
	Name  string `json:"name"`
	Terms []Term `json:"terms"`
}
