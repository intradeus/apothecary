package pgstore

import (
	"testing"

	"apothecary/internal/run/event"
	"apothecary/internal/run/event/eventtest"
)

func TestEventContract(t *testing.T) {
	eventtest.RunContract(t, func(t *testing.T) event.Store { return open(t) })
}
