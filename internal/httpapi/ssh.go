package httpapi

// The run page's SSH facts (TODO 46): how a person reaches the run's
// workspace through the engine's SSH door — the command, the VS Code link,
// the door's fingerprint — and whether anything will let them in yet.
//
// The door is reached through the HTTPS tunnel (`ssh <workspace>.apothecary`
// once `apothecary ssh-config` has run), unless it also listens on a TCP
// port of its own (APOTHECARY_SSH_LISTEN, the dev loop), which the facts
// then name instead.

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"apothecary/internal/account/auth"
	"apothecary/internal/platform/wstunnel"
)

func (s *Server) getRunSSH(w http.ResponseWriter, r *http.Request) {
	if s.SSH == nil {
		writeJSON(w, http.StatusOK, RunSSH{Reason: errNoDoor.Error()})
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
	out := RunSSH{
		Enabled: true, User: info.Workspace, Path: info.Path,
		Fingerprint: s.SSH.Fingerprint(), HostKey: strings.TrimSpace(s.SSH.HostPublicKey()),
		Extensions: info.Extensions,
		Keys:       len(keys),
	}
	host, port := s.SSH.Address()
	if port == 0 {
		// Through the tunnel: the host is the workspace's alias, which the
		// CLI's ssh config routes; the username is not read.
		out.Host = info.Workspace + wstunnel.SSHHostSuffix
		out.Setup = "apothecary ssh-config"
		out.Command = "ssh " + out.Host
		out.VSCodeURL = vscodeURLFor(out.Host, info.Path)
		writeJSON(w, http.StatusOK, out)
		return
	}
	if host == "" {
		// The door listens on every interface and no address was configured
		// to advertise: the host this page was reached at reaches it too.
		host = requestHostname(r)
	}
	out.Host, out.Port = host, port
	out.Command = sshCommand(host, port, info.Workspace)
	out.VSCodeURL = vscodeURL(host, port, info.Workspace, info.Path)
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
	return vscodeURLFor(dest, path)
}

// vscodeURLFor is the Remote-SSH link for a destination as typed.
func vscodeURLFor(dest, path string) string {
	authority, _ := json.Marshal(map[string]string{"hostName": dest})
	u := "vscode://vscode-remote/ssh-remote+" + hex.EncodeToString(authority)
	if path != "" {
		u += (&url.URL{Path: path}).EscapedPath()
	}
	return u
}

// requestHostname is the host a request came in on, without its port.
func requestHostname(r *http.Request) string {
	h := r.Host
	if hh, _, err := net.SplitHostPort(h); err == nil {
		h = hh
	}
	return strings.TrimSuffix(strings.TrimPrefix(h, "["), "]")
}
