package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"apothecary/internal/engine"
)

// --- project secrets ------------------------------------------------------------

// ProjectSecretRequest sets one project secret. The value is write-only:
// nothing in the API ever returns it.
type ProjectSecretRequest struct {
	Value string `json:"value"`
}

func (s *Server) listProjectSecrets(w http.ResponseWriter, r *http.Request) {
	list, err := s.Conf.ListProjectSecrets(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) putProjectSecret(w http.ResponseWriter, r *http.Request) {
	var body ProjectSecretRequest
	if !readJSON(w, r, &body) {
		return
	}
	sec, err := s.Conf.SetProjectSecret(r.Context(), r.PathValue("id"), r.PathValue("name"), body.Value)
	if err != nil {
		confErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sec)
}

func (s *Server) deleteProjectSecret(w http.ResponseWriter, r *http.Request) {
	if err := s.Conf.DeleteProjectSecret(r.Context(), r.PathValue("id"), r.PathValue("name")); err != nil {
		confErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- the project browser ----------------------------------------------------------

// BrowserInfo is the project's browser as the settings page shows it: the
// toggle (saved with the project's workspace), whether this apothecary runs
// browsers at all, and the container's state.
type BrowserInfo struct {
	Enabled   bool `json:"enabled"`
	Available bool `json:"available"`
	// Container is apo-browser-<project>. State: absent | running |
	// stopped (idle; the next agent session starts it).
	Container string `json:"container,omitempty"`
	State     string `json:"state,omitempty"`
	// Image is what the container runs, Current what this build would: they
	// differ until the browser is next idle, when it is recreated.
	Image   string `json:"image,omitempty"`
	Current string `json:"current,omitempty"`
	// Sessions are the agent sessions using it now, each its own Chromium.
	Sessions int    `json:"sessions"`
	Error    string `json:"error,omitempty"`
}

func (s *Server) getProjectBrowser(w http.ResponseWriter, r *http.Request) {
	p, err := s.Conf.GetProject(r.Context(), r.PathValue("id"))
	if err != nil {
		confErr(w, err)
		return
	}
	info := BrowserInfo{Enabled: p.Workspace.Browser.Enabled}
	st, err := s.Engine.BrowserStatus(r.Context(), p.ID)
	switch {
	case errors.Is(err, engine.ErrBrowserOff):
	case err != nil:
		info.Available, info.Error = true, err.Error()
	default:
		info.Available = true
		info.Container, info.State, info.Image, info.Current, info.Sessions =
			st.Container, st.State, st.Image, st.Current, st.Sessions
	}
	writeJSON(w, http.StatusOK, info)
}

// browserMCPProtocol is the Upgrade token of the browser stream (the shim's
// `browser-mcp` speaks it).
const browserMCPProtocol = "apothecary-mcp"

// serveBrowserMCP upgrades the workspace's `shim browser-mcp` to a raw
// stream and bridges it to a Chromium in the project's browser: MCP's
// newline-delimited JSON-RPC both ways. A stream, so outside the table (and
// the spec), like the terminal. Authenticated by the run's bearer token —
// the agent's harness started the shim, in the run's workspace. Whatever is
// refused is refused before the upgrade, as plain JSON; a session that then
// fails says why on one last line the shim knows ({"apothecary_error": …}).
func (s *Server) serveBrowserMCP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.authPing(w, r, id) {
		return
	}
	if !strings.EqualFold(r.Header.Get("Upgrade"), browserMCPProtocol) {
		writeErr(w, http.StatusUpgradeRequired, "upgrade to "+browserMCPProtocol+" required")
		return
	}
	sess, err := s.Engine.OpenBrowserMCP(r.Context(), id)
	if err != nil {
		status := http.StatusConflict
		if errors.Is(err, engine.ErrBrowserOff) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		sess.Close()
		writeErr(w, http.StatusInternalServerError, "cannot hijack the connection")
		return
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		sess.Close()
		return
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("HTTP/1.1 101 Switching Protocols\r\nUpgrade: " + browserMCPProtocol + "\r\nConnection: Upgrade\r\n\r\n")); err != nil {
		sess.Close()
		return
	}
	if err := sess.Serve(r.Context(), hijacked{brw.Reader, conn}); err != nil {
		s.Log.Warn("browser session failed", "run", id, "err", err)
		line, _ := json.Marshal(map[string]string{"apothecary_error": err.Error()})
		_, _ = conn.Write(append(line, '\n'))
	}
}

// hijacked reads what the hijacked connection already buffered first.
type hijacked struct {
	r *bufio.Reader
	net.Conn
}

func (h hijacked) Read(p []byte) (int, error) { return h.r.Read(p) }
