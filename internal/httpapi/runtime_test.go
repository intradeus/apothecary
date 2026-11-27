package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"apothecary/internal/platform/secrets"
	"apothecary/internal/ports/envport"
	"apothecary/internal/project/confstore"
)

// fakeOffers offers choice (when ok) and checks a workshop against it,
// recording the provisioner each check was asked about.
type fakeOffers struct {
	choice  envport.RuntimeChoice
	ok      bool
	err     error
	checked []string
	offered []string // the provisioners RuntimeChoice was asked about
}

func (f *fakeOffers) RuntimeChoice(_ context.Context, id string) (envport.RuntimeChoice, bool, error) {
	f.offered = append(f.offered, id)
	return f.choice, f.ok, f.err
}

func (f *fakeOffers) CheckRuntime(_ context.Context, id string, w confstore.WorkspaceConfig) error {
	f.checked = append(f.checked, id)
	if f.err != nil {
		return f.err
	}
	_, err := f.choice.Choose(w.Runtime, envport.RuntimeCapabilities{NestedContainers: w.Docker})
	return err
}

var sysboxOnly = envport.RuntimeChoice{Default: envport.RuntimeSysbox, Approved: []envport.Runtime{envport.RuntimeSysbox}}

// A workshop is saved only with a runtime its provisioner approves; the
// refusal names what is approved. Its own provisioner is the one asked.
func TestWorkshopRuntimeIsChecked(t *testing.T) {
	x := newConfEnv(t)
	offers := &fakeOffers{choice: sysboxOnly, ok: true}
	x.srv.Runtimes = offers
	id := x.makeProject(t, "rt", nil)
	p, err := x.conf.GetProject(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if len(offers.checked) != 1 || offers.checked[0] != p.ProvisionerID {
		t.Errorf("create checked %v, want the instance's %s", offers.checked, p.ProvisionerID)
	}
	body := projectBody{Name: p.Name, RepoAccountID: p.RepoAccountID, HarnessIDs: p.HarnessIDs,
		Repo: p.Repo, DefaultFlow: p.DefaultFlow, Workspace: p.Workspace}
	body.Workspace.Runtime = envport.RuntimePrivileged
	res, data := x.do(t, "PUT", "/api/projects/"+id, body, nil)
	if res.StatusCode != http.StatusBadRequest || !strings.Contains(string(data), "not approved") || !strings.Contains(string(data), "approved: sysbox") {
		t.Errorf("unapproved: %d %s", res.StatusCode, data)
	}
	if offers.checked[len(offers.checked)-1] != p.ProvisionerID {
		t.Errorf("update checked %v, want the workshop's own provisioner", offers.checked)
	}
	body.Workspace.Runtime = envport.RuntimeSysbox
	if res, data := x.do(t, "PUT", "/api/projects/"+id, body, nil); res.StatusCode != http.StatusOK || !strings.Contains(string(data), `"runtime":"sysbox"`) {
		t.Errorf("approved: %d %s", res.StatusCode, data)
	}
	body.Name = "rt-2"
	body.Workspace.Runtime = envport.RuntimePrivileged
	if res, data := x.do(t, "POST", "/api/projects", body, nil); res.StatusCode != http.StatusBadRequest {
		t.Errorf("create unapproved: %d %s", res.StatusCode, data)
	}
	// A workshop that is not there is a 404, not a pass.
	if res, _ := x.do(t, "PUT", "/api/projects/nope", body, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown workshop: %d", res.StatusCode)
	}
	// A provisioner that is not there either.
	offers.err = confstore.ErrNotFound
	body.ProvisionerID = "gone"
	if res, _ := x.do(t, "PUT", "/api/projects/"+id, body, nil); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown provisioner: %d", res.StatusCode)
	}
	// Nothing wired: nothing checked.
	x.srv.Runtimes = nil
	body.ProvisionerID, body.Workspace.Runtime = "", ""
	if res, data := x.do(t, "PUT", "/api/projects/"+id, body, nil); res.StatusCode != http.StatusOK {
		t.Errorf("unwired: %d %s", res.StatusCode, data)
	}
}

// The offer the project form reads: the default and the approved runtimes,
// each with what it gives; none offered without a choice or a provisioner.
func TestRuntimeOffer(t *testing.T) {
	x := newConfEnv(t)
	get := func() (int, string) {
		res, body := x.do(t, "GET", "/api/provisioner/runtimes", nil, nil)
		return res.StatusCode, string(body)
	}
	if code, body := get(); code != http.StatusOK || !strings.Contains(body, `"offered":false`) || !strings.Contains(body, `"approved":[]`) {
		t.Errorf("unwired: %d %s", code, body)
	}
	offers := &fakeOffers{choice: envport.RuntimeChoice{Default: envport.RuntimeSysbox,
		Approved: []envport.Runtime{envport.RuntimeSysbox, envport.RuntimePrivileged}}, ok: true}
	x.srv.Runtimes = offers
	code, body := get()
	if code != http.StatusOK || !strings.Contains(body, `"offered":true,"default":"sysbox"`) ||
		!strings.Contains(body, `{"name":"privileged","capabilities":{"nested_containers":true,"nested_kubernetes":true,"user_namespaces":true,"contained":false}}`) {
		t.Errorf("offered: %d %s", code, body)
	}
	// What the provisioner's cluster adds to a runtime is in its offer.
	offers.choice = envport.RuntimeChoice{Default: envport.RuntimeUserns, Approved: []envport.Runtime{envport.RuntimeUserns},
		Adds: map[envport.Runtime]envport.RuntimeCapabilities{envport.RuntimeUserns: {UserNamespaces: true}}}
	if code, body := get(); code != http.StatusOK ||
		!strings.Contains(body, `{"name":"userns","capabilities":{"nested_containers":true,"nested_kubernetes":true,"user_namespaces":true,"contained":true}}`) {
		t.Errorf("widened userns: %d %s", code, body)
	}
	offers.ok = false
	if code, body := get(); code != http.StatusOK || !strings.Contains(body, `"offered":false`) {
		t.Errorf("no choice: %d %s", code, body)
	}
	offers.err = errors.New("kubernetes: runtimes: unknown sandbox runtime")
	if code, body := get(); code != http.StatusOK || !strings.Contains(body, `"error":"kubernetes: runtimes`) {
		t.Errorf("unreadable: %d %s", code, body)
	}
}

// A workshop's idle setting: -1, 0 or minutes; a period is refused where the
// provisioner cannot keep a stopped sandbox, saying why, and the offer
// carries that reason for the form.
func TestWorkshopIdleSettingIsChecked(t *testing.T) {
	x := newConfEnv(t)
	offers := &fakeOffers{choice: sysboxOnly, ok: true}
	x.srv.Runtimes = offers
	id := x.makeProject(t, "idle", nil)
	p, err := x.conf.GetProject(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	body := projectBody{Name: p.Name, RepoAccountID: p.RepoAccountID, HarnessIDs: p.HarnessIDs,
		Repo: p.Repo, DefaultFlow: p.DefaultFlow, Workspace: p.Workspace}
	put := func(minutes int) (int, string) {
		body.Workspace.IdleMinutes = minutes
		res, data := x.do(t, "PUT", "/api/projects/"+id, body, nil)
		return res.StatusCode, string(data)
	}
	if code, data := put(-2); code != http.StatusBadRequest || !strings.Contains(data, "idle minutes") {
		t.Errorf("-2: %d %s", code, data)
	}
	for _, m := range []int{envport.IdleNever, 0, 45} {
		if code, data := put(m); code != http.StatusOK {
			t.Errorf("%d: %d %s", m, code, data)
		}
	}
	// The update asks the workshop's own provisioner, not the instance's.
	if got := offers.offered[len(offers.offered)-1]; got != p.ProvisionerID {
		t.Errorf("asked %s, want the workshop's %s", got, p.ProvisionerID)
	}
	// A runtime whose sandboxes cannot be kept (a VM's disks go with it):
	// refused under it, whether picked or the default, fine under another.
	offers.choice = envport.RuntimeChoice{Default: envport.RuntimeSysbox,
		Approved:     []envport.Runtime{envport.RuntimeSysbox, envport.RuntimeKata},
		NoSleepUnder: map[envport.Runtime]string{envport.RuntimeKata: "a kata sandbox keeps its disks inside its VM"}}
	body.Workspace.Runtime = envport.RuntimeKata
	if code, data := put(45); code != http.StatusBadRequest || !strings.Contains(data, "inside its VM") {
		t.Errorf("kata: %d %s", code, data)
	}
	res, data := x.do(t, "GET", "/api/provisioner/runtimes", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(data), `"name":"kata","capabilities":`) ||
		!strings.Contains(string(data), `"noSleep":"a kata sandbox keeps its disks inside its VM"`) {
		t.Errorf("offer: %d %s", res.StatusCode, data)
	}
	body.Workspace.Runtime = ""
	if code, data := put(45); code != http.StatusOK {
		t.Errorf("sysbox: %d %s", code, data)
	}
	offers.choice.Default = envport.RuntimeKata
	if code, data := put(45); code != http.StatusBadRequest {
		t.Errorf("kata by default: %d %s", code, data)
	}
	offers.choice = sysboxOnly
	offers.choice.NoSleep = "emptydir storage"
	if code, data := put(45); code != http.StatusBadRequest || !strings.Contains(data, "emptydir storage") {
		t.Errorf("no sleep: %d %s", code, data)
	}
	if code, data := put(envport.IdleNever); code != http.StatusOK {
		t.Errorf("never is always fine: %d %s", code, data)
	}
	res, data = x.do(t, "GET", "/api/provisioner/runtimes", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(data), `"noSleep":"emptydir storage"`) {
		t.Errorf("offer: %d %s", res.StatusCode, data)
	}
	// A workshop that is not there is no pass.
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), "PUT", "/", nil)
	if x.srv.idleFits(rec, req, "", "nope", confstore.WorkspaceConfig{IdleMinutes: 45}) || rec.Code != http.StatusNotFound {
		t.Errorf("unknown workshop: %d", rec.Code)
	}
}

// unreadableIntegrations is a configuration store that cannot list its
// integrations.
type unreadableIntegrations struct{ confstore.DB }

func (unreadableIntegrations) ListIntegrations(confstore.Ctx, string) ([]confstore.StoredIntegration, error) {
	return nil, errors.New("db down")
}

// The provisioner a workshop's settings are checked against: the body's,
// else the workshop's own, else the instance's; none at all is nothing to
// check against, and a store that cannot say is a 500, not a pass.
func TestProvisionerFor(t *testing.T) {
	key, err := secrets.ParseKey(secrets.GenerateKey())
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), "PUT", "/", nil)
	s := &Server{Conf: confstore.New(confstore.NewMem(), key)}
	rec := httptest.NewRecorder()
	if id, ok := s.provisionerFor(rec, req, "", ""); id != "" || !ok {
		t.Errorf("none yet = %q, %v", id, ok)
	}
	if !s.runtimeFits(rec, req, "", "", confstore.WorkspaceConfig{}) || s.Runtimes != nil {
		t.Error("nothing wired passes")
	}
	s.Runtimes = &fakeOffers{choice: sysboxOnly, ok: true}
	if !s.runtimeFits(rec, req, "", "", confstore.WorkspaceConfig{}) {
		t.Error("no provisioner yet: nothing to refuse against")
	}
	if id, ok := s.provisionerFor(rec, req, "p1", ""); id != "p1" || !ok {
		t.Errorf("the body's = %q, %v", id, ok)
	}
	s.Conf = confstore.New(unreadableIntegrations{confstore.NewMem()}, key)
	if id, ok := s.provisionerFor(rec, req, "", ""); id != "" || ok || rec.Code != http.StatusInternalServerError {
		t.Errorf("unreadable = %q, %v, %d", id, ok, rec.Code)
	}
}
