package auth_test

import (
	"testing"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
)

func TestMemContract(t *testing.T) {
	authtest.RunContract(t, func(t *testing.T) auth.DB { return auth.NewMem() })
}
