package ask_test

import (
	"testing"

	"apothecary/internal/engine/ask"
	"apothecary/internal/engine/ask/asktest"
)

func TestMemContract(t *testing.T) {
	asktest.RunContract(t, func(t *testing.T) ask.Store { return ask.NewMemStore() })
}
