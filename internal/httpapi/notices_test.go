package httpapi

import (
	"context"
	"net/http"
	"testing"

	"apothecary/internal/engine"
	"apothecary/internal/notices"
)

func listedNotices(t *testing.T, x *env, headers map[string]string) []notices.Notice {
	t.Helper()
	res, body := x.do(t, "GET", "/api/notices", nil, headers)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list = %d %s", res.StatusCode, body)
	}
	return decode[[]notices.Notice](t, body)
}

func TestNoticesListOnlyTheCallers(t *testing.T) {
	x := newEnv(t, engine.Config{})
	other := x.addUser(t, "usr-other", false)
	ctx := context.Background()
	x.srv.Notices.Raise(ctx, notices.Raise{To: []string{testUser}, Kind: "k", Title: "mine", Link: "/system/channels"})
	x.srv.Notices.Raise(ctx, notices.Raise{To: []string{"usr-other"}, Kind: "k", Title: "theirs"})

	if got := listedNotices(t, x, nil); len(got) != 1 || got[0].Title != "mine" || got[0].Link != "/system/channels" || got[0].Count != 1 {
		t.Errorf("mine = %+v", got)
	}
	if got := listedNotices(t, x, other); len(got) != 1 || got[0].Title != "theirs" {
		t.Errorf("theirs = %+v", got)
	}
	// Someone else's notice is not there to dismiss.
	theirs := listedNotices(t, x, other)[0].ID
	if res, _ := x.do(t, "POST", "/api/notices/"+theirs+"/dismiss", nil, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("dismissing theirs = %d", res.StatusCode)
	}
}

func TestDismissingANoticeRemovesIt(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	x.srv.Notices.Raise(ctx, notices.Raise{To: []string{testUser}, Kind: "k", Title: "one"})
	x.srv.Notices.Raise(ctx, notices.Raise{To: []string{testUser}, Kind: "k", Title: "two"})
	list := listedNotices(t, x, nil)
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if res, body := x.do(t, "POST", "/api/notices/"+list[0].ID+"/dismiss", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("dismiss = %d %s", res.StatusCode, body)
	}
	if got := listedNotices(t, x, nil); len(got) != 1 || got[0].ID != list[1].ID {
		t.Errorf("after dismiss = %+v", got)
	}
}

func TestDismissingAllEmptiesTheList(t *testing.T) {
	x := newEnv(t, engine.Config{})
	ctx := context.Background()
	for _, title := range []string{"one", "two"} {
		x.srv.Notices.Raise(ctx, notices.Raise{To: []string{testUser}, Kind: "k", Title: title})
	}
	if res, body := x.do(t, "POST", "/api/notices/dismiss", nil, nil); res.StatusCode != http.StatusNoContent {
		t.Fatalf("dismiss all = %d %s", res.StatusCode, body)
	}
	res, body := x.do(t, "GET", "/api/notices", nil, nil)
	if res.StatusCode != http.StatusOK || string(body) != "[]\n" && string(body) != "[]" {
		t.Errorf("after dismiss all = %d %q", res.StatusCode, body)
	}
}
