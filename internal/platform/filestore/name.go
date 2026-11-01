package filestore

// Naming and MIME helpers, ported verbatim from internal/artifacts (which
// phase 2 deletes).

import (
	"crypto/rand"
	"encoding/hex"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

// SafeName reduces an uploaded filename to a single safe path component,
// preserving the extension where possible. Used for both the stored metadata
// and the in-workspace delivery path.
func SafeName(name string) string {
	name = filepath.Base(filepath.FromSlash(name))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if name == "" {
		return "file"
	}
	if len(name) > 128 {
		name = name[len(name)-128:] // keep the extension end
		for !utf8.RuneStart(name[0]) {
			name = name[1:] // the cut landed inside a character
		}
	}
	return name
}

// detectMime resolves the best content type for a stored file: a specific
// declared type wins, else content sniffing, else the extension, else
// octet-stream. The result is allowlist-checked by the caller.
func detectMime(path, filename, declared string) string {
	if d := strings.TrimSpace(strings.Split(declared, ";")[0]); d != "" && d != "application/octet-stream" {
		return d
	}
	if f, err := os.Open(path); err == nil {
		buf := make([]byte, 512)
		n, _ := f.Read(buf)
		f.Close()
		if ct := http.DetectContentType(buf[:n]); ct != "application/octet-stream" {
			return strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}
	if ext := filepath.Ext(filename); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}
	return "application/octet-stream"
}

// detectMimeBytes classifies captured content without a temp file.
func detectMimeBytes(data []byte, filename string) string {
	if ext := filepath.Ext(filename); ext != "" {
		if ct := mime.TypeByExtension(ext); ct != "" {
			return strings.TrimSpace(strings.Split(ct, ";")[0])
		}
	}
	n := min(len(data), 512)
	return strings.TrimSpace(strings.Split(http.DetectContentType(data[:n]), ";")[0])
}

// mimeAllowFunc compiles a MIME allowlist (exact types or "text/*" wildcards).
func mimeAllowFunc(allow []string) func(string) bool {
	exact := map[string]bool{}
	var prefixes []string
	for _, a := range allow {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" {
			continue
		}
		if strings.HasSuffix(a, "/*") {
			prefixes = append(prefixes, strings.TrimSuffix(a, "*"))
		} else {
			exact[a] = true
		}
	}
	return func(m string) bool {
		m = strings.ToLower(strings.TrimSpace(strings.Split(m, ";")[0]))
		if exact[m] {
			return true
		}
		for _, p := range prefixes {
			if strings.HasPrefix(m, p) {
				return true
			}
		}
		return false
	}
}

// newID mints a random 128-bit row id (rows, not blobs — blob identity is the
// sha).
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on supported platforms; fall back to a
		// time-seeded value so an id is still unique enough to not collide.
		t := time.Now().UnixNano()
		for i := range 8 {
			b[i] = byte(t >> (8 * i))
		}
	}
	return hex.EncodeToString(b[:])
}
