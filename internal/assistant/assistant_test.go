package assistant_test

import (
	"testing"

	"apothecary/internal/assistant"
	"apothecary/internal/assistant/assistanttest"
)

func TestMemStoreContract(t *testing.T) {
	assistanttest.RunContract(t, func(*testing.T) assistant.Store { return assistant.NewMemStore() })
}
