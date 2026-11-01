package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/ports/notifyport"
)

const secret = "syt_s3cr3tvalue"

func TestSendEscapesAndAddressesTheRoom(t *testing.T) {
	var path, auth string
	var body map[string]string
	var txns []string
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		path, auth = r.URL.EscapedPath(), r.Header.Get("Authorization")
		_ = json.NewDecoder(r.Body).Decode(&body)
		txns = append(txns, path[strings.LastIndex(path, "/")+1:])
	}))
	defer ts.Close()
	ch, err := New(Config{Homeserver: ts.URL + "/", AccessToken: secret, RoomID: "!room:example.org"})
	if err != nil {
		t.Fatal(err)
	}
	n := notifyport.Notification{Title: "a <b>", Body: "x & y", URL: `http://x/?q="1"`}
	if err := ch.Send(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(path, "/_matrix/client/v3/rooms/%21room:example.org/send/m.room.message/") {
		t.Errorf("path = %q", path)
	}
	if auth != "Bearer "+secret {
		t.Errorf("auth = %q", auth)
	}
	if body["body"] != "a <b>\nx & y\n"+n.URL {
		t.Errorf("plain body = %q", body["body"])
	}
	want := `<strong>a &lt;b&gt;</strong><br/>x &amp; y<br/><a href="http://x/?q=&#34;1&#34;">open in CPUI</a>`
	if body["formatted_body"] != want {
		t.Errorf("formatted = %q", body["formatted_body"])
	}

	// A second send is a second event: the transaction id must differ, or the
	// homeserver would drop it as a retry.
	if err := ch.Send(context.Background(), notifyport.Notification{Title: "only"}); err != nil {
		t.Fatal(err)
	}
	if body["body"] != "only" || body["formatted_body"] != "<strong>only</strong>" {
		t.Errorf("title only = %v", body)
	}
	if len(txns) != 2 || txns[0] == txns[1] {
		t.Errorf("transaction ids = %v", txns)
	}
}

func TestSendFailuresKeepTheTokenOut(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	url := gone.URL
	gone.Close()
	ch, _ := New(Config{Homeserver: url, AccessToken: secret, RoomID: "!r:x"})
	err := ch.Send(context.Background(), notifyport.Notification{Title: "t"})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("dead server = %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"errcode":"M_FORBIDDEN"}`))
	}))
	defer ts.Close()
	ch, _ = New(Config{Homeserver: ts.URL, AccessToken: secret, RoomID: "!r:x"})
	err = ch.Send(context.Background(), notifyport.Notification{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "M_FORBIDDEN") || strings.Contains(err.Error(), secret) {
		t.Errorf("403 = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ch.Send(ctx, notifyport.Notification{Title: "t"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled = %v", err)
	}
	bad, _ := New(Config{Homeserver: "http://bad host", AccessToken: secret, RoomID: "!r:x"})
	if err := bad.Send(context.Background(), notifyport.Notification{Title: "t"}); err == nil {
		t.Error("an unparsable homeserver is an error")
	}
}
