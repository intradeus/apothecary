package pgstore

import (
	"testing"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
)

func TestAuthContract(t *testing.T) {
	authtest.RunContract(t, func(t *testing.T) auth.DB { return open(t) })
}
