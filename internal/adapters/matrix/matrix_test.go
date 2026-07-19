package matrix

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"factory/internal/ports"
)

func TestSendPutsMessageEvent(t *testing.T) {
	var path, auth string
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.Path, r.Header.Get("Authorization")
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &body)
		if r.Method != http.MethodPut {
			w.WriteHeader(405)
			return
		}
		_, _ = w.Write([]byte(`{"event_id":"$evt1"}`))
	}))
	defer ts.Close()

	ch, err := New(Config{Homeserver: ts.URL, AccessToken: "syt_secret", RoomID: "!ops:matrix.local"})
	if err != nil {
		t.Fatal(err)
	}
	err = ch.Send(context.Background(), ports.Notification{
		Title: "plan_approval: Add login",
		Body:  "artifacts/plan.md",
		URL:   "http://localhost:8080/runs/r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/_matrix/client/v3/rooms/") || !strings.Contains(path, "/send/m.room.message/") {
		t.Errorf("path = %s", path)
	}
	if auth != "Bearer syt_secret" {
		t.Errorf("auth = %q", auth)
	}
	if body["msgtype"] != "m.text" || !strings.Contains(body["body"].(string), "plan_approval") ||
		!strings.Contains(body["formatted_body"].(string), "<strong>") {
		t.Errorf("payload = %+v", body)
	}
}

func TestSendErrorSurfaces(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN"}`))
	}))
	defer ts.Close()
	ch, _ := New(Config{Homeserver: ts.URL, AccessToken: "t", RoomID: "!r:x"})
	if err := ch.Send(context.Background(), ports.Notification{Title: "t"}); err == nil || !strings.Contains(err.Error(), "M_FORBIDDEN") {
		t.Errorf("err = %v", err)
	}
}

func TestFactoryValidation(t *testing.T) {
	if _, err := Factory(map[string]any{"homeserver": "https://m", "access_token": "t", "room_id": "!r:m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Factory(map[string]any{"homeserver": "https://m"}); err == nil {
		t.Error("missing fields must fail")
	}
}
