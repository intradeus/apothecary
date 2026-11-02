package notices_test

import (
	"testing"

	"apothecary/internal/notices"
	"apothecary/internal/notices/noticestest"
)

func TestMemStoreContract(t *testing.T) {
	noticestest.RunContract(t, func(*testing.T) notices.Store { return notices.NewMemStore() })
}
