package run

import (
	"time"

	"apothecary/internal/event"
)

// PingView is the projected request/response pair (PLAN4 M15): one row per
// ping, both sides. Postgres maintains it transactionally (pgstore); tests
// and event-store-only setups derive it with ReducePings.
type PingView struct {
	PingID     string     `json:"ping_id"`
	RunID      string     `json:"run_id"`
	Type       string     `json:"type"`
	StageID    string     `json:"stage_id,omitempty"`
	Question   string     `json:"question"`
	Context    string     `json:"context,omitempty"`
	PayloadRef string     `json:"payload_ref,omitempty"`
	Options    []string   `json:"options,omitempty"`
	Status     string     `json:"status"` // pending|answered
	Answer     string     `json:"answer,omitempty"`
	Feedback   string     `json:"feedback,omitempty"`
	RaisedAt   time.Time  `json:"raised_at"`
	AnsweredAt *time.Time `json:"answered_at,omitempty"`
}

// ReducePings folds one run's events into its ping views, in raise order.
func ReducePings(evs []event.Event) []PingView {
	var out []PingView
	idx := map[string]int{}
	for i := range evs {
		switch p := evs[i].Payload.(type) {
		case *event.PingRaised:
			idx[p.PingID] = len(out)
			out = append(out, PingView{
				PingID: p.PingID, RunID: evs[i].RunID, Type: p.Type, StageID: p.StageID,
				Question: p.Question, Context: p.Context, PayloadRef: p.PayloadRef,
				Options: p.Options,
				Status:  "pending", RaisedAt: evs[i].TS,
			})
		case *event.PingAnswered:
			if j, ok := idx[p.PingID]; ok {
				ts := evs[i].TS
				out[j].Status = "answered"
				out[j].Answer = p.Answer
				out[j].Feedback = p.Feedback
				out[j].AnsweredAt = &ts
			}
		}
	}
	return out
}
