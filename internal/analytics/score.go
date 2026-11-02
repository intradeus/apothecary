package analytics

import (
	"cmp"
	"math"
	"slices"
)

// builtinScorecards are the rankings every person starts with.
var builtinScorecards = []Scorecard{
	{ID: "builtin-cheapest", Name: "Cheapest", Builtin: true, Terms: []Term{
		{Measure: MeasureRef{Field: "cost_usd", Agg: AggMedian}, Direction: DirLower, Weight: 0.8},
		{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 0.2},
	}},
	{ID: "builtin-fastest", Name: "Fastest", Builtin: true, Terms: []Term{
		{Measure: MeasureRef{Field: "wall_ms", Agg: AggMedian}, Direction: DirLower, Weight: 0.8},
		{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 0.2},
	}},
	{ID: "builtin-hands-off", Name: "Hands-off", Builtin: true, Terms: []Term{
		{Measure: MeasureRef{Field: "human_minutes_per_run"}, Direction: DirLower, Weight: 0.5},
		{Measure: MeasureRef{Field: "unwanted_pings_per_run"}, Direction: DirLower, Weight: 0.3},
		{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 0.2},
	}},
	{ID: "builtin-balanced", Name: "Balanced", Builtin: true, Terms: []Term{
		{Measure: MeasureRef{Field: "cost_usd", Agg: AggMedian}, Direction: DirLower, Weight: 0.3},
		{Measure: MeasureRef{Field: "wall_ms", Agg: AggMedian}, Direction: DirLower, Weight: 0.3},
		{Measure: MeasureRef{Field: "unwanted_pings_per_run"}, Direction: DirLower, Weight: 0.2},
		{Measure: MeasureRef{Field: "success_rate"}, Direction: DirHigher, Weight: 0.2},
	}},
}

// withTerms appends the scorecard's measures the query lacks, and returns
// the measure index each term reads.
func withTerms(q Query, sc Scorecard) (Query, []int) {
	q.Measures = slices.Clone(q.Measures)
	idx := make([]int, len(sc.Terms))
	for i, t := range sc.Terms {
		j := slices.Index(q.Measures, t.Measure)
		if j < 0 {
			j = len(q.Measures)
			q.Measures = append(q.Measures, t.Measure)
		}
		idx[i] = j
	}
	return q, idx
}

// score sets each row's Score, 0–100 in min-max mode (each term normalised
// across the rows, best = 1) and relative to the baseline row otherwise
// (each term's ratio to the baseline's, clamped to [0, 2]; the baseline
// scores 100). A nil value skips that term for that row.
func score(rows []Row, sc Scorecard, idx []int, baseline []string) error {
	norm := make([]func(x float64) float64, len(sc.Terms))
	if len(baseline) > 0 {
		b := slices.IndexFunc(rows, func(r Row) bool { return slices.Equal(r.Dims, baseline) })
		if b < 0 {
			return invalidf("baseline row not in result")
		}
		for i, t := range sc.Terms {
			v := rows[b].Values[idx[i]]
			if v == nil {
				continue
			}
			base, higher := *v, t.Direction == DirHigher
			norm[i] = func(x float64) float64 {
				num, den := x, base
				if !higher {
					num, den = base, x
				}
				if num == den {
					return 1
				}
				if den == 0 {
					return 2
				}
				return math.Max(0, math.Min(2, num/den))
			}
		}
	} else {
		for i, t := range sc.Terms {
			lo, hi, seen := math.Inf(1), math.Inf(-1), false
			for _, r := range rows {
				if v := r.Values[idx[i]]; v != nil {
					lo, hi, seen = math.Min(lo, *v), math.Max(hi, *v), true
				}
			}
			if !seen {
				continue
			}
			higher := t.Direction == DirHigher
			norm[i] = func(x float64) float64 {
				if hi == lo {
					return 1
				}
				if higher {
					return (x - lo) / (hi - lo)
				}
				return (hi - x) / (hi - lo)
			}
		}
	}
	for r := range rows {
		var sum, weights float64
		for i, t := range sc.Terms {
			v := rows[r].Values[idx[i]]
			if v == nil || norm[i] == nil {
				continue
			}
			sum += t.Weight * norm[i](*v)
			weights += t.Weight
		}
		if weights > 0 {
			rows[r].Score = new(100 * sum / weights)
		}
	}
	return nil
}

// sortByScore orders rows by score, unscored rows last.
func sortByScore(rows []Row, desc bool) {
	slices.SortStableFunc(rows, func(a, b Row) int {
		switch {
		case a.Score == nil && b.Score == nil:
			return 0
		case a.Score == nil:
			return 1
		case b.Score == nil:
			return -1
		}
		if desc {
			return cmp.Compare(*b.Score, *a.Score)
		}
		return cmp.Compare(*a.Score, *b.Score)
	})
}
