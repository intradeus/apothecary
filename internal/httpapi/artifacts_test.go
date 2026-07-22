package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"
	"time"

	"apothecary/internal/artifacts"
	"apothecary/internal/engine"
)

// memArtifactDB is a minimal artifacts.DB for the upload handler test.
type memArtifactDB struct{ rows map[string]artifacts.Meta }

func (d *memArtifactDB) InsertArtifact(_ context.Context, m artifacts.Meta) error {
	d.rows[m.ID] = m
	return nil
}
func (d *memArtifactDB) AssociateArtifacts(context.Context, []string, string, string, string) error {
	return nil
}
func (d *memArtifactDB) ArtifactsForRun(context.Context, string) ([]artifacts.Meta, error) {
	return nil, nil
}
func (d *memArtifactDB) ArtifactsForPing(context.Context, string, string) ([]artifacts.Meta, error) {
	return nil, nil
}
func (d *memArtifactDB) GetArtifact(_ context.Context, id string) (artifacts.Meta, error) {
	if m, ok := d.rows[id]; ok {
		return m, nil
	}
	return artifacts.Meta{}, artifacts.ErrNotFound
}
func (d *memArtifactDB) DeleteArtifactsForRun(context.Context, string) ([]string, error) {
	return nil, nil
}
func (d *memArtifactDB) OrphanArtifactIDsBefore(context.Context, time.Time) ([]string, error) {
	return nil, nil
}
func (d *memArtifactDB) DeleteArtifacts(context.Context, []string) error { return nil }

func multipartBody(name, ctype string, data []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="files"; filename="`+name+`"`)
	h.Set("Content-Type", ctype)
	part, _ := mw.CreatePart(h)
	_, _ = part.Write(data)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestPostArtifacts(t *testing.T) {
	x := newEnv(t, engine.Config{})
	x.srv.Artifacts = artifacts.New(&memArtifactDB{rows: map[string]artifacts.Meta{}}, t.TempDir(), 32, []string{"text/*"})

	upload := func(name, ctype string, data []byte) (*http.Response, []byte) {
		body, ct := multipartBody(name, ctype, data)
		res, err := http.Post(x.ts.URL+"/api/artifacts", ct, body)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, out
	}

	// Happy path: an allowed text file → 201 + metadata.
	res, data := upload("notes.txt", "text/plain", []byte("hello"))
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d: %s", res.StatusCode, data)
	}
	var metas []map[string]any
	_ = json.Unmarshal(data, &metas)
	if len(metas) != 1 || metas[0]["filename"] != "notes.txt" {
		t.Errorf("response = %s", data)
	}

	// Disallowed type → 415.
	if res, _ = upload("x.bin", "application/x-thing", []byte("data")); res.StatusCode != http.StatusUnsupportedMediaType {
		t.Errorf("bad-mime status = %d", res.StatusCode)
	}
	// Over the 32-byte cap → 413.
	if res, _ = upload("big.txt", "text/plain", bytes.Repeat([]byte("x"), 40)); res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized status = %d", res.StatusCode)
	}
}
