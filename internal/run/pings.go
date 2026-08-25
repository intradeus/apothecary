package run

import (
	"time"

	"apothecary/internal/event"
)

// PingView is the projected request/response pair (PLAN4 M15): one row per
// ping, both sides. Postgres maintains it transactionally (pgstore); tests
// and event-store-only setups derive it with ReducePings.
type PingView struct {
	PingID   string `json:"ping_id"`
	RunID    string `json:"run_id"`
	Type     string `json:"type"`
	StageID  string `json:"stage_id,omitempty"`
	Question string `json:"question"`
	Context  string `json:"context,omitempty"`
	// Files/Link mirror ping_raised's typed payload (DESIGN-FILES §4):
	// Files are attached workspace paths (first = the primary reviewed
	// document), Link a resolved URL payload. PayloadRef is the DERIVED
	// primary (Link, else Files[0]) kept for the current API shape.
	Files      []string `json:"files,omitempty"`
	Link       string   `json:"link,omitempty"`
	PayloadRef string   `json:"payload_ref,omitempty"`
	Options    []string `json:"options,omitempty"`
	Status     string   `json:"status"` // pending|answered|withdrawn (run ended unanswered)
	Answer     string   `json:"answer,omitempty"`
	Feedback   string   `json:"feedback,omitempty"`
	// AnswerAttachmentIDs are the files the human attached to the answer
	// (projected from ping_answered).
	AnswerAttachmentIDs []string   `json:"answer_attachment_ids,omitempty"`
	RaisedAt            time.Time  `json:"raised_at"`
	AnsweredAt          *time.Time `json:"answered_at,omitempty"`
}

// Primary is the ping's primary payload: the link, or the first file.
func Primary(files []string, link string) string {
	if link != "" {
		return link
	}
	if len(files) > 0 {
		return files[0]
	}
	return ""
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
				Question: p.Question, Context: p.Context,
				Files: p.Files, Link: p.Link, PayloadRef: Primary(p.Files, p.Link),
				Options: p.Options,
				Status:  "pending", RaisedAt: evs[i].TS,
			})
		case *event.PingAnswered:
			if j, ok := idx[p.PingID]; ok {
				ts := evs[i].TS
				out[j].Status = "answered"
				out[j].Answer = p.Answer
				out[j].Feedback = p.Feedback
				out[j].AnswerAttachmentIDs = p.AttachmentIDs
				out[j].AnsweredAt = &ts
			}
		case *event.RunAbandoned, *event.RunFailed, *event.RunArchived, *event.RunCompleted:
			// The run reached a terminal state with a still-open ping: its live
			// handler is gone, so the ping can never be answered. Withdraw it so
			// the inbox stops offering a request nothing will ever service (and
			// "answer" stops 404ing on a ping the durable projection still shows
			// as pending).
			for j := range out {
				if out[j].RunID == evs[i].RunID && out[j].Status == "pending" {
					out[j].Status = "withdrawn"
				}
			}
		}
	}
	return out
}
