package skillstore_test

import (
	"testing"

	"apothecary/internal/skillstore"
	"apothecary/internal/skillstore/skilltest"
)

func TestMemContract(t *testing.T) {
	skilltest.RunDBContract(t, func(t *testing.T) skillstore.DB { return skillstore.NewMem() })
}
