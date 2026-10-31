package httpapi

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"apothecary/internal/account/auth"
	"apothecary/internal/engine"
)

func pubKeyLine(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pk, _ := ssh.NewPublicKey(pub)
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pk)))
}

// One's SSH keys: registered from a .pub line, listed with their
// fingerprint, refused when malformed or already registered, removed by
// their owner only.
func TestSSHKeysAPI(t *testing.T) {
	x := newEnv(t, engine.Config{})
	line := pubKeyLine(t)
	resp, data := x.do(t, "POST", "/api/me/ssh-keys", map[string]string{"public_key": line + " me@laptop"}, nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("add = %d: %s", resp.StatusCode, data)
	}
	var k auth.SSHKey
	_ = json.Unmarshal(data, &k)
	if k.ID == "" || k.Name != "me@laptop" || k.Type != "ssh-ed25519" || !strings.HasPrefix(k.Fingerprint, "SHA256:") || k.PublicKey != line {
		t.Errorf("key = %+v", k)
	}
	if resp, data := x.do(t, "POST", "/api/me/ssh-keys", map[string]string{"public_key": "ssh-ed25519 nope"}, nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed = %d: %s", resp.StatusCode, data)
	}
	if resp, data := x.do(t, "POST", "/api/me/ssh-keys", map[string]string{"public_key": line, "name": "again"}, nil); resp.StatusCode != http.StatusConflict {
		t.Errorf("duplicate = %d: %s", resp.StatusCode, data)
	}
	resp, data = x.do(t, "GET", "/api/me/ssh-keys", nil, nil)
	var keys []auth.SSHKey
	_ = json.Unmarshal(data, &keys)
	if resp.StatusCode != http.StatusOK || len(keys) != 1 || keys[0].ID != k.ID {
		t.Errorf("list = %d %+v", resp.StatusCode, keys)
	}
	// Another account's key is not one's to remove.
	now := time.Now().UTC()
	if err := x.users.CreateUser(context.Background(), auth.User{ID: "u-other", Handle: "other", Email: "o@x", Name: "Other", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	other, err := x.srv.Auth.AddSSHKey(context.Background(), "u-other", "theirs", pubKeyLine(t))
	if err != nil {
		t.Fatal(err)
	}
	if resp, _ := x.do(t, "DELETE", "/api/me/ssh-keys/"+other.ID, nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("foreign delete = %d", resp.StatusCode)
	}
	if resp, _ := x.do(t, "DELETE", "/api/me/ssh-keys/"+k.ID, nil, nil); resp.StatusCode != http.StatusNoContent {
		t.Errorf("delete = %d", resp.StatusCode)
	}
	if _, data := x.do(t, "GET", "/api/me/ssh-keys", nil, nil); string(data) != "[]\n" {
		t.Errorf("after delete = %s", data)
	}
}

// stubDoor is an SSHAccess for the run page's facts.
type stubDoor struct{ dropped []string }

func (stubDoor) Address() (string, int)    { return "door.example", 2222 }
func (stubDoor) Fingerprint() string       { return "SHA256:abc" }
func (stubDoor) HostPublicKey() string     { return "ssh-ed25519 AAAAhost\n" }
func (d *stubDoor) DropUser(userID string) { d.dropped = append(d.dropped, userID) }

// The run page's SSH facts: off when the instance has no door; the
// command, the link and the fingerprint when it has one.
func TestRunSSHFacts(t *testing.T) {
	x := newEnv(t, engine.Config{})
	runID := x.createTicketFlow(t, "T", "")
	// Off: the page is told so, and no error.
	resp, data := x.do(t, "GET", "/api/runs/"+runID+"/ssh", nil, nil)
	var off RunSSH
	_ = json.Unmarshal(data, &off)
	if resp.StatusCode != http.StatusOK || off.Enabled || !strings.Contains(off.Reason, "APOTHECARY_SSH_LISTEN") {
		t.Fatalf("off = %d %+v", resp.StatusCode, off)
	}
	// On: once the run has a workspace.
	x.srv.SSH = &stubDoor{}
	deadline := time.Now().Add(5 * time.Second)
	var envID string
	for envID == "" && time.Now().Before(deadline) {
		_, data := x.do(t, "GET", "/api/runs/"+runID, nil, nil)
		var detail struct {
			Run struct {
				EnvID string `json:"env_id"`
			} `json:"run"`
		}
		_ = json.Unmarshal(data, &detail)
		envID = detail.Run.EnvID
		if envID == "" {
			time.Sleep(20 * time.Millisecond)
		}
	}
	if envID == "" {
		t.Fatal("the run never got a workspace")
	}
	resp, data = x.do(t, "GET", "/api/runs/"+runID+"/ssh", nil, nil)
	var on RunSSH
	_ = json.Unmarshal(data, &on)
	if resp.StatusCode != http.StatusOK || !on.Enabled || on.Host != "door.example" || on.Port != 2222 || on.User != envID ||
		on.Fingerprint != "SHA256:abc" || on.HostKey != "ssh-ed25519 AAAAhost" || on.Command != "ssh -p 2222 "+envID+"@door.example" || on.Keys != 0 {
		t.Fatalf("on = %d %+v", resp.StatusCode, on)
	}
	if !strings.HasPrefix(on.VSCodeURL, "vscode://vscode-remote/ssh-remote+") {
		t.Errorf("vscode url = %q", on.VSCodeURL)
	}
	// A registered key is counted.
	if resp, _ := x.do(t, "POST", "/api/me/ssh-keys", map[string]string{"public_key": pubKeyLine(t)}, nil); resp.StatusCode != http.StatusCreated {
		t.Fatal("add key")
	}
	_, data = x.do(t, "GET", "/api/runs/"+runID+"/ssh", nil, nil)
	_ = json.Unmarshal(data, &on)
	if on.Keys != 1 {
		t.Errorf("keys = %d", on.Keys)
	}
}

// The VS Code link carries the ssh destination as Remote-SSH expects it.
func TestVSCodeURL(t *testing.T) {
	u := vscodeURL("door.tail.ts.net", 2222, "apo-fix-3f9a", "/workspaces/my repo")
	const prefix = "vscode://vscode-remote/ssh-remote+"
	if !strings.HasPrefix(u, prefix) || !strings.HasSuffix(u, "/workspaces/my%20repo") {
		t.Fatalf("url = %q", u)
	}
	authority := strings.TrimSuffix(strings.TrimPrefix(u, prefix), "/workspaces/my%20repo")
	raw, err := hex.DecodeString(authority)
	if err != nil {
		t.Fatal(err)
	}
	var dest struct {
		HostName string `json:"hostName"`
	}
	if err := json.Unmarshal(raw, &dest); err != nil || dest.HostName != "apo-fix-3f9a@door.tail.ts.net -p 2222" {
		t.Errorf("destination = %q, %v", dest.HostName, err)
	}
	// Port 22 needs no -p; no path opens no folder.
	raw, _ = hex.DecodeString(strings.TrimPrefix(vscodeURL("h", 22, "u", ""), prefix))
	if string(raw) != `{"hostName":"u@h"}` {
		t.Errorf("port 22 authority = %s", raw)
	}
	if sshCommand("h", 22, "u") != "ssh u@h" || sshCommand("h", 2222, "u") != "ssh -p 2222 u@h" {
		t.Error("ssh command")
	}
}
