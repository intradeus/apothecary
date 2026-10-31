// Package filestore is the file subsystem (DESIGN-FILES): content-addressed
// blobs under one roof, with two typed stores over them — Attachments (files
// a human gave the factory; upload policy) and Snapshots (the engine-captured
// record; capture policy). The old internal/artifacts fused both into one
// scope-switched struct; here the invalid states are unrepresentable — an
// attachment has no stage fields to misuse, a snapshot's constructor demands
// a coherent attribution, and delivery code that iterates attachments cannot
// pick up snapshots.
package filestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	ErrTooLarge = errors.New("file exceeds the size limit")
	ErrMimeType = errors.New("file type is not allowed")
	ErrNotFound = errors.New("not found")
	// ErrContentGone marks a reference whose bytes are no longer on disk —
	// swept by retention, or lost with the volume. The metadata (row/event)
	// still stands; callers render "content expired" rather than 500
	// (DESIGN-FILES §3.1: records outlive bytes).
	ErrContentGone = errors.New("content is no longer stored")
	// ErrContentCorrupt marks bytes that no longer hash to their name.
	// Mismatched bytes are never served (§7 F4).
	ErrContentCorrupt = errors.New("content failed its integrity check")
)

// Blobs is the content-addressed byte store: <dir>/<sha256[:2]>/<sha256>.
// Identity IS the hash, so identical content stores once and every reference
// (attachment or snapshot row) points at it by sha. Blobs carry no semantics
// and are deleted only by Sweep — never on row deletion or a failed insert,
// because another reference may share the bytes (§3.1).
type Blobs struct {
	dir string
}

// NewBlobs roots the store at <runsDir>/blobs.
func NewBlobs(runsDir string) *Blobs {
	return &Blobs{dir: filepath.Join(runsDir, "blobs")}
}

func (b *Blobs) path(sha string) string { return filepath.Join(b.dir, sha[:2], sha) }

// validSHA guards path construction: a sha is 64 lowercase hex chars, and
// anything else must not reach filepath.Join.
func validSHA(sha string) bool {
	if len(sha) != 64 {
		return false
	}
	for _, r := range sha {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// Put stores bytes already in hand (captures) and returns their sha.
// Idempotent: existing content is left in place.
func (b *Blobs) Put(data []byte) (string, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if b.exists(sha) {
		return sha, nil
	}
	if err := os.MkdirAll(filepath.Dir(b.path(sha)), 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(b.dir, "put-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		return "", errors.Join(werr, cerr)
	}
	if err := os.Rename(tmpName, b.path(sha)); err != nil {
		os.Remove(tmpName)
		return "", err
	}
	return sha, nil
}

// PutStream stores a reader capped at max bytes (ErrTooLarge beyond it),
// hashing as it streams — the upload path, which must never buffer a whole
// file in memory. Returns the sha and the byte count.
func (b *Blobs) PutStream(r io.Reader, max int64) (sha string, size int64, err error) {
	if err := os.MkdirAll(b.dir, 0o755); err != nil {
		return "", 0, err
	}
	tmp, err := os.CreateTemp(b.dir, "put-*")
	if err != nil {
		return "", 0, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away

	h := sha256.New()
	// Read at most max+1: max+1 means the source had more than the cap.
	n, err := io.Copy(tmp, io.LimitReader(io.TeeReader(r, h), max+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	if n > max {
		return "", 0, ErrTooLarge
	}
	sha = hex.EncodeToString(h.Sum(nil))
	if b.exists(sha) {
		return sha, n, nil // dedup: identical content already stored
	}
	if err := os.MkdirAll(filepath.Dir(b.path(sha)), 0o755); err != nil {
		return "", 0, err
	}
	if err := os.Rename(tmpName, b.path(sha)); err != nil {
		return "", 0, err
	}
	return sha, n, nil
}

func (b *Blobs) exists(sha string) bool {
	_, err := os.Stat(b.path(sha))
	return err == nil
}

// Open returns a verified handle on a blob, positioned at the start. The
// whole content is hashed before the handle is handed out — bounded by the
// upload/capture caps, so affordable — and bytes that no longer match their
// name are never served (ErrContentCorrupt). A missing blob is
// ErrContentGone: the reference outlives the bytes by design.
func (b *Blobs) Open(sha string) (*os.File, error) {
	if !validSHA(sha) {
		return nil, fmt.Errorf("%w: invalid blob reference", ErrContentGone)
	}
	f, err := os.Open(b.path(sha))
	if err != nil {
		return nil, ErrContentGone
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		f.Close()
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != sha {
		f.Close()
		return nil, ErrContentCorrupt
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Bytes is Open for callers that want the content in hand (delivery).
func (b *Blobs) Bytes(sha string) ([]byte, error) {
	f, err := b.Open(sha)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// sweepGrace is how old an unreferenced blob must be before Sweep may take
// it. The write protocol is blob-first-row-second, so a fresh blob whose row
// insert is still in flight is briefly unreferenced BY DESIGN — an age guard
// (mtime-based) closes that race without any coordination. One hour dwarfs
// any insert latency and delays reclamation by at most one sweep period.
const sweepGrace = time.Hour

// Sweep removes every blob whose sha is not in live and whose mtime is older
// than the grace window — the ONE place blobs are deleted (orphaned uploads,
// retention-expired snapshot content, and the garbage of crashed writes all
// end here). Idempotent and crash-safe: a pass that dies mid-sweep leaves
// strictly less garbage for the next one.
func (b *Blobs) Sweep(live map[string]bool) (removed int, err error) {
	cutoff := time.Now().Add(-sweepGrace)
	shards, err := os.ReadDir(b.dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	old := func(e os.DirEntry) bool {
		info, err := e.Info()
		return err == nil && info.ModTime().Before(cutoff)
	}
	for _, shard := range shards {
		if !shard.IsDir() {
			// Stray temp files from crashed writes age out here too.
			if strings.HasPrefix(shard.Name(), "put-") && old(shard) {
				os.Remove(filepath.Join(b.dir, shard.Name()))
			}
			continue
		}
		entries, err := os.ReadDir(filepath.Join(b.dir, shard.Name()))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if live[e.Name()] || !old(e) {
				continue
			}
			if os.Remove(filepath.Join(b.dir, shard.Name(), e.Name())) == nil {
				removed++
			}
		}
	}
	return removed, nil
}
