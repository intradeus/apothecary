package resolution

import "context"

// TrackerActor says on whose behalf a tracker write is made: a run's stage.
// The engine puts it on the context of every tracker action's calls
// (WithTrackerActor), and a tracker that records who wrote — the internal
// tracker — reads it: a status change is stamped "run:<run>/<stage>", and a
// comment is keyed by (RunID, Stage), which makes posting it twice a no-op.
// A write with no actor is a person's.
type TrackerActor struct {
	RunID string
	// Stage is the stage occurrence: "<phase>-<n>/<stage id>", so a stage a
	// watcher phase runs again writes again, while a retried attempt of the
	// same occurrence does not.
	Stage string
}

// ChangedBy is how a status change by the actor is recorded.
func (a TrackerActor) ChangedBy() string { return "run:" + a.RunID + "/" + a.Stage }

type trackerActorKey struct{}

// WithTrackerActor attaches the actor to ctx.
func WithTrackerActor(ctx context.Context, a TrackerActor) context.Context {
	return context.WithValue(ctx, trackerActorKey{}, a)
}

// TrackerActorFrom is the actor on ctx, if any.
func TrackerActorFrom(ctx context.Context) (TrackerActor, bool) {
	a, ok := ctx.Value(trackerActorKey{}).(TrackerActor)
	return a, ok
}
