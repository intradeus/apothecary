package pgstore

import (
	"testing"

	"apothecary/internal/project/confstore"
	"apothecary/internal/project/confstore/conftest"
)

func TestConfContract(t *testing.T) {
	conftest.RunDBContract(t, func(t *testing.T) confstore.DB { return openConf(t) })
}
