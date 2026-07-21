package event

import (
	"encoding/json"
	"fmt"
	"time"
)

// registry maps event types to payload constructors, for decoding rows read
// back from the store (Postgres jsonb, SSE replay, crash recovery).
var registry = map[Type]func() Payload{
	TypeRunCreated:      func() Payload { return &RunCreated{} },
	TypeEnvRequested:    func() Payload { return &EnvRequested{} },
	TypeImageBuilt:      func() Payload { return &ImageBuilt{} },
	TypeEnvReady:        func() Payload { return &EnvReady{} },
	TypeEnvFailed:       func() Payload { return &EnvFailed{} },
	TypeStageStarted:    func() Payload { return &StageStarted{} },
	TypeStageCompleted:  func() Payload { return &StageCompleted{} },
	TypeStageFailed:     func() Payload { return &StageFailed{} },
	TypeEdgeTaken:       func() Payload { return &EdgeTaken{} },
	TypePingRaised:      func() Payload { return &PingRaised{} },
	TypePingAnswered:    func() Payload { return &PingAnswered{} },
	TypeVerdictReceived: func() Payload { return &VerdictReceived{} },
	TypeMRCreated:       func() Payload { return &MRCreated{} },
	TypeRunCompleted:    func() Payload { return &RunCompleted{} },
	TypeRunAbandoned:    func() Payload { return &RunAbandoned{} },
	TypeRunFailed:       func() Payload { return &RunFailed{} },
	TypeRunArchived:     func() Payload { return &RunArchived{} },
	TypeRunResumed:      func() Payload { return &RunResumed{} },
	TypeRunPaused:       func() Payload { return &RunPaused{} },
	TypeRunUnpaused:     func() Payload { return &RunUnpaused{} },
	TypeEnvDestroyed:    func() Payload { return &EnvDestroyed{} },
}

// UnmarshalPayload decodes a payload of the given type.
func UnmarshalPayload(t Type, data []byte) (Payload, error) {
	mk, ok := registry[t]
	if !ok {
		return nil, fmt.Errorf("unknown event type %q", t)
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
