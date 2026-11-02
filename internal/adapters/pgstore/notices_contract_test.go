package pgstore

import (
	"testing"

	"apothecary/internal/notices"
	"apothecary/internal/notices/noticestest"
)

func TestNoticeContract(t *testing.T) {
	noticestest.RunContract(t, func(t *testing.T) notices.Store {
		s := open(t)
		seedUsers(t, s, mkUser(noticestest.U1, "notice-one", "n1@x"), mkUser(noticestest.U2, "notice-two", "n2@x"))
		return s
	})
}
