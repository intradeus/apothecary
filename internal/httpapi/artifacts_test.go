package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/filestore"
)

// newFilesFor wires real filestore stores (over the in-package Mem) into a
// test server + engine — upload/claim side and capture/delivery side.
func newFilesFor(t *testing.T, x *env, maxBytes int64, allow []string) (*filestore.Attachments, *filestore.Snapshots, *filestore.Mem) {
	t.Helper()
	mem := filestore.NewMem()
	blobs := filestore.NewBlobs(t.TempDir())
	att := filestore.NewAttachments(mem, blobs, maxBytes, allow)
	snaps := filestore.NewSnapshots(mem, blobs, maxBytes)
	x.srv.Attachments = att
	x.srv.Snapshots = snaps
	x.e.SetFiles(att, snaps)
	return att, snaps, mem
}

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
	newFilesFor(t, x, 32, []string{"text/*"})

	upload := func(name, ctype string, data []byte) (*http.Response, []byte) {
		body, ct := multipartBody(name, ctype, data)
		req, _ := http.NewRequest("POST", x.ts.URL+"/api/attachments", body)
		req.Header.Set("Content-Type", ct)
		res, err := http.DefaultClient.Do(x.authed(req))
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
	var out struct {
		Attachments []map[string]any `json:"attachments"`
	}
	_ = json.Unmarshal(data, &out)
	if len(out.Attachments) != 1 || out.Attachments[0]["filename"] != "notes.txt" {
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

	// The stored bytes stream back from the byte route, verified.
	id := out.Attachments[0]["id"].(string)
	getReq, _ := http.NewRequest("GET", x.ts.URL+"/api/attachments/"+id, nil)
	got, err := http.DefaultClient.Do(x.authed(getReq))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(got.Body)
	got.Body.Close()
	if got.StatusCode != http.StatusOK || string(body) != "hello" {
		t.Errorf("byte route = %d %q", got.StatusCode, body)
	}
}
