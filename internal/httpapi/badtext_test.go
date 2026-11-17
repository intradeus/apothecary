package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"apothecary/internal/account/auth"
	"apothecary/internal/account/auth/authtest"
	"apothecary/internal/engine"
)

func authHeader(x *env) map[string]string {
	return map[string]string{"Authorization": "Bearer " + x.token}
}

// Text no column can hold is the caller's to fix, wherever it comes in: a 400
// saying so, never a 500 quoting the SQL.
func TestBadTextIsRefusedAtTheEdge(t *testing.T) {
	x := newEnv(t, engine.Config{})
	list := "/api/projects/" + testProject + "/tickets"
	for name, c := range map[string]struct{ method, path, body string }{
		"ticket body":      {"POST", list, `{"title":"t\u0000"}`},
		"a key":            {"POST", list, `{"title":"t","x\u0000":1}`},
		"nested":           {"POST", list, `{"title":"t","params":{"a":["\u0000"]}}`},
		"an optional body": {"POST", list + "/nope/runs", `{"goal":"g\u0000"}`},
		"sign-in":          {"POST", "/api/auth/login", `{"login":"lu\u0000","proof":"correct horse"}`},
		"path":             {"GET", "/api/attachments/a%00b", ""},
		"path, utf-8":      {"GET", "/api/attachments/a%ffb", ""},
		"query":            {"GET", list + "?q=a%00b", ""},
		"query key":        {"GET", list + "?a%ff=b", ""},
	} {
		t.Run(name, func(t *testing.T) {
			res, body := x.bare(t, c.method, c.path, c.body, authHeader(x))
			if res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "NUL") {
				t.Errorf("%s %s = %d %s", c.method, c.path, res.StatusCode, body)
			}
		})
	}
}

// Text that only reads like one is text: a goal can quote the escape.
func TestALiteralBackslashIsNotANUL(t *testing.T) {
	x := newEnv(t, engine.Config{})
	res, body := x.bare(t, "POST", "/api/projects/"+testProject+"/tickets", `{"title":"the \\u0000 byte"}`, authHeader(x))
	if res.StatusCode != http.StatusCreated || !strings.Contains(body, `\\u0000`) {
		t.Errorf("ticket = %d %s", res.StatusCode, body)
	}
}

// What isn't JSON stays the handler's to refuse, in its own words, and a scan
// leaves the body readable for it.
func TestTheScanLeavesTheBodyToItsHandler(t *testing.T) {
	x := newEnv(t, engine.Config{})
	list := "/api/projects/" + testProject + "/tickets"
	if res, body := x.bare(t, "POST", list, `{"title":"t"}`, authHeader(x)); res.StatusCode != http.StatusCreated {
		t.Errorf("a body the scan read = %d %s", res.StatusCode, body)
	}
	if res, body := x.bare(t, "POST", list, `{"title":`, authHeader(x)); res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid JSON") {
		t.Errorf("malformed = %d %s", res.StatusCode, body)
	}
	big := `{"title":"` + strings.Repeat("x", maxScannedBody) + `"}`
	if res, body := x.bare(t, "POST", list, big, authHeader(x)); res.StatusCode != http.StatusBadRequest || !strings.Contains(body, "invalid JSON") {
		t.Errorf("past the handler's limit = %d %.200s", res.StatusCode, body)
	}
}

// A browser says what it likes in User-Agent, and the sessions table takes only
// UTF-8. That must not cost a person with the right password their sign-in.
func TestSignInSurvivesABadUserAgent(t *testing.T) {
	x := newEnv(t, engine.Config{})
	svc := authtest.New(auth.NewMem())
	x.srv.Auth = svc
	if _, err := svc.Setup(t.Context(), auth.NewUser{Handle: "lu", Email: "l@x", Name: "L", Password: "correct horse"}); err != nil {
		t.Fatal(err)
	}
	res, body := x.bare(t, "POST", "/api/auth/login", `{"login":"lu","proof":"correct horse"}`, map[string]string{"User-Agent": "broken \xff agent"})
	if res.StatusCode != http.StatusOK {
		t.Errorf("sign-in = %d %s", res.StatusCode, body)
	}
}
