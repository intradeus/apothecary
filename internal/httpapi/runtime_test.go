package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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
}

func (f *fakeOffers) RuntimeChoice(context.Context, string) (envport.RuntimeChoice, bool, error) {
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
	offers.choice.NoSleep = "emptydir storage"
	if code, data := put(45); code != http.StatusBadRequest || !strings.Contains(data, "emptydir storage") {
		t.Errorf("no sleep: %d %s", code, data)
	}
	if code, data := put(envport.IdleNever); code != http.StatusOK {
		t.Errorf("never is always fine: %d %s", code, data)
	}
	res, data := x.do(t, "GET", "/api/provisioner/runtimes", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(data), `"noSleep":"emptydir storage"`) {
		t.Errorf("offer: %d %s", res.StatusCode, data)
	}
}
