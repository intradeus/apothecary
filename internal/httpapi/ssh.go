package httpapi

// The run page's SSH facts (TODO 46): how a person reaches the run's
// workspace through the engine's SSH door — the command, the VS Code link,
// the door's fingerprint — and whether anything will let them in yet.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"apothecary/internal/account/auth"
)

func (s *Server) getRunSSH(w http.ResponseWriter, r *http.Request) {
	if s.SSH == nil {
		writeJSON(w, http.StatusOK, RunSSH{Reason: "SSH access is off on this instance (set APOTHECARY_SSH_LISTEN to turn the SSH door on)"})
		return
	}
	info, err := s.Workspaces.SSHInfo(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	keys, err := s.Auth.ListSSHKeys(r.Context(), auth.UserID(r.Context()))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	host, port := s.SSH.Address()
	out := RunSSH{
		Enabled: true, Host: host, Port: port, User: info.Workspace, Path: info.Path,
		Fingerprint: s.SSH.Fingerprint(), HostKey: strings.TrimSpace(s.SSH.HostPublicKey()),
		Command:    sshCommand(host, port, info.Workspace),
		VSCodeURL:  vscodeURL(host, port, info.Workspace, info.Path),
		Extensions: info.Extensions,
		Keys:       len(keys),
	}
	writeJSON(w, http.StatusOK, out)
}

// sshCommand is the command line that opens a shell in the workspace.
func sshCommand(host string, port int, user string) string {
	if port == 22 {
		return fmt.Sprintf("ssh %s@%s", user, host)
	}
	return fmt.Sprintf("ssh -p %d %s@%s", port, user, host)
}

// vscodeURL is the link that opens the workspace in desktop VS Code with
// Remote-SSH: vscode://vscode-remote/ssh-remote+<authority><path>. The
// authority is the hex of a JSON {"hostName": "<destination>"} — the
// destination being exactly what a person would type into Remote-SSH's
// "Connect to Host" (user@host, and -p when the port is not 22). It is
// hex-encoded because a URI authority is lower-cased and takes no spaces;
// Remote-SSH decodes it and hands the destination to ssh. Without a path VS
// Code opens no folder, so the path goes when the sidecar could say it.
func vscodeURL(host string, port int, user, path string) string {
	dest := user + "@" + host
	if port != 22 {
		dest += " -p " + strconv.Itoa(port)
	}
	authority, _ := json.Marshal(map[string]string{"hostName": dest})
	u := "vscode://vscode-remote/ssh-remote+" + hex.EncodeToString(authority)
	if path != "" {
		u += (&url.URL{Path: path}).EscapedPath()
	}
	return u
}
