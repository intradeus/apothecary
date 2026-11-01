package filestore_test

import (
	"testing"

	"apothecary/internal/platform/filestore"
	"apothecary/internal/platform/filestore/filetest"
)

// No ticket hook: Mem has no tickets table for a claim to miss.
func TestMemContract(t *testing.T) {
	filetest.RunContract(t, func(t *testing.T) filetest.DB { return filestore.NewMem() }, nil)
}
