package event_test

import (
	"testing"

	"apothecary/internal/run/event"
	"apothecary/internal/run/event/eventtest"
)

func TestMemContract(t *testing.T) {
	eventtest.RunContract(t, func(t *testing.T) event.Store { return event.NewMemStore() })
}
