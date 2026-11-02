package secrets

import (
	"errors"
	"fmt"
	"net/url"
)

// ScrubURL takes the address out of an HTTP client's error: *url.Error quotes
// the whole request URL — a webhook address that is itself the credential, a
// redirect target's signature.
func ScrubURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
