package pgstore

import (
	"testing"

	"apothecary/internal/assistant"
	"apothecary/internal/assistant/assistanttest"
)

func TestAssistantContract(t *testing.T) {
	assistanttest.RunContract(t, func(t *testing.T) assistant.Store { return open(t) })
}
