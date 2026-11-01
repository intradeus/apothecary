package pgstore

import (
	"testing"

	"apothecary/internal/platform/filestore/filetest"
)

func TestFilestoreContract(t *testing.T) {
	filetest.RunContract(t,
		func(t *testing.T) filetest.DB { return open(t) },
		func(t *testing.T, db filetest.DB, id string) { mustSeedProjectAndTicket(t, db.(*Store), id) })
}
