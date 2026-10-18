package event

import (
	"encoding/json"
	"fmt"
	"time"
)

// registry maps event types to payload constructors, for decoding rows read
// back from the store (Postgres jsonb, SSE replay, crash recovery).
var registry = map[Type]func() Payload{
	TypeRunCreated:           func() Payload { return &RunCreated{} },
	TypeEnvRequested:         func() Payload { return &EnvRequested{} },
	TypeImageBuilt:           func() Payload { return &ImageBuilt{} },
	TypeEnvReady:             func() Payload { return &EnvReady{} },
	TypeEnvFailed:            func() Payload { return &EnvFailed{} },
	TypeStageStarted:         func() Payload { return &StageStarted{} },
	TypeStageCompleted:       func() Payload { return &StageCompleted{} },
	TypeStageFailed:          func() Payload { return &StageFailed{} },
	TypeStagePaused:          func() Payload { return &StagePaused{} },
	TypeStageResumed:         func() Payload { return &StageResumed{} },
	TypeEdgeTaken:            func() Payload { return &EdgeTaken{} },
	TypePingRaised:           func() Payload { return &PingRaised{} },
	TypePingAnswered:         func() Payload { return &PingAnswered{} },
	TypeVerdictReceived:      func() Payload { return &VerdictReceived{} },
	TypeActionStarted:        func() Payload { return &ActionStarted{} },
	TypeSnapshotCaptured:     func() Payload { return &SnapshotCaptured{} },
	TypeMRCreated:            func() Payload { return &MRCreated{} },
	TypePhaseStarted:         func() Payload { return &PhaseStarted{} },
	TypePhaseCompleted:       func() Payload { return &PhaseCompleted{} },
	TypePhaseDeclined:        func() Payload { return &PhaseDeclined{} },
	TypeReviewCommentsLoaded: func() Payload { return &ReviewCommentsLoaded{} },
	TypeReviewRepliesPosted:  func() Payload { return &ReviewRepliesPosted{} },
	TypeTargetSynced:         func() Payload { return &TargetSynced{} },
	TypeBranchPushed:         func() Payload { return &BranchPushed{} },
	TypeRunCompleted:         func() Payload { return &RunCompleted{} },
	TypeRunAbandoned:         func() Payload { return &RunAbandoned{} },
	TypeRunFailed:            func() Payload { return &RunFailed{} },
	TypeRunArchived:          func() Payload { return &RunArchived{} },
	TypeRunResumed:           func() Payload { return &RunResumed{} },
	TypeRunPaused:            func() Payload { return &RunPaused{} },
	TypeRunUnpaused:          func() Payload { return &RunUnpaused{} },
	TypeEnvDestroyed:         func() Payload { return &EnvDestroyed{} },

	TypeEnvRebuildStarted:   func() Payload { return &EnvRebuildStarted{} },
	TypeEnvRebuildCompleted: func() Payload { return &EnvRebuildCompleted{} },
	TypeEnvRebuildFailed:    func() Payload { return &EnvRebuildFailed{} },
}

// Unknown is the decode result for an event type this binary does not
// recognize — a retired type (e.g. the pre-DESIGN-FILES "artifact_published")
// or one written by a newer version. The log is append-only and outlives any
// codec, so replay must treat these as INERT HISTORY: every projection's type
// switch falls through them, and recovery/rebuild keep working instead of
// bricking the boot on the first old run. Raw passes the original payload
// through verbatim (MarshalJSON), so SSE replay shows the event untouched.
type Unknown struct {
	UnknownType Type
	Raw         json.RawMessage
}

func (u *Unknown) EventType() Type              { return u.UnknownType }
func (u *Unknown) MarshalJSON() ([]byte, error) { return u.Raw, nil }

// UnmarshalPayload decodes a payload of the given type. An unregistered type
// is NOT an error — it decodes as *Unknown (see above). A registered type
// whose payload fails to decode still errors loudly: that is corruption, not
// version skew.
func UnmarshalPayload(t Type, data []byte) (Payload, error) {
	mk, ok := registry[t]
	if !ok {
		raw := make(json.RawMessage, len(data))
		copy(raw, data)
		return &Unknown{UnknownType: t, Raw: raw}, nil
	}
	p := mk()
	if err := json.Unmarshal(data, p); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", t, err)
	}
	return p, nil
}

// UnmarshalJSON decodes a full envelope (inverse of the default marshaling).
func (e *Event) UnmarshalJSON(data []byte) error {
	var env struct {
		RunID   string          `json:"run_id"`
		Seq     int64           `json:"seq"`
		TS      time.Time       `json:"ts"`
		Type    Type            `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return err
	}
	p, err := UnmarshalPayload(env.Type, env.Payload)
	if err != nil {
		return err
	}
	e.RunID, e.Seq, e.TS, e.Type, e.Payload = env.RunID, env.Seq, env.TS, env.Type, p
	return nil
}
