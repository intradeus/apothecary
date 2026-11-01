package confstore_test

import (
	"testing"

	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
)

func TestMemContract(t *testing.T) {
	conftest.RunDBContract(t, func(t *testing.T) confstore.DB { return confstore.NewMem() })
}
