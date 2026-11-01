package pgstore

import (
	"testing"

	"apothecary/internal/engine/ask"
	"apothecary/internal/engine/ask/asktest"
)

func TestAskContract(t *testing.T) {
	asktest.RunContract(t, func(t *testing.T) ask.Store { return open(t) })
}
