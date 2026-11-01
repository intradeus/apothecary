package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"apothecary/internal/run/event"
)

// maxScannedBody is as far as a body is read ahead: past it the routes refuse
// it themselves, and uploads don't come through here as JSON.
const maxScannedBody = 1 << 20

// refuseBadText answers 400 for a request carrying text no column can hold: a
// NUL anywhere, or invalid UTF-8 in the address. Let through, the store refuses
// it instead, and that reaches the caller as a 500 quoting the SQL. This is the
// one check every route passes, so a new route or body shape is covered.
func refuseBadText(w http.ResponseWriter, r *http.Request) bool {
	if addressFits(r) && bodyFits(r) {
		return false
	}
	writeErr(w, http.StatusBadRequest, "text may not contain NUL characters or invalid UTF-8")
	return true
}

func addressFits(r *http.Request) bool {
	if event.CleanText(r.URL.Path) != r.URL.Path {
		return false
	}
	for k, vs := range r.URL.Query() {
		for _, v := range append(vs, k) {
			if event.CleanText(v) != v {
				return false
			}
		}
	}
	return true
}

// bodyFits scans a JSON body, keys included, leaving it for the handler to
// read. JSON's decoder has already made any invalid UTF-8 U+FFFD, so what is
// left to find is `\u0000`, which only decoding tells from a literal backslash.
func bodyFits(r *http.Request) bool {
	if r.Body == nil || strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		return true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, maxScannedBody+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(data), r.Body), r.Body}
	if err != nil || len(data) > maxScannedBody {
		return true
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return true // not JSON, or its end: the handler's to say
		}
		if s, ok := tok.(string); ok && strings.IndexByte(s, 0) >= 0 {
			return false
		}
	}
}
