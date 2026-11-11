package httpapi

// The tunnels a person's machine reaches a run through, over this engine's
// own HTTPS address and nothing else (`apothecary forward`, `apothecary
// ssh-proxy`): a workspace port onto their localhost, and the SSH door.
// WebSockets, so outside the route table, like the terminal.

import (
	"errors"
	"net/http"
	"strconv"

	"apothecary/internal/account/auth"
	"apothecary/internal/platform/wstunnel"
)

// servePortTunnel carries one TCP connection to a port of the run's
// workspace. The port is reached before the upgrade, so a refusal is an
// HTTP answer the client can print.
func (s *Server) servePortTunnel(w http.ResponseWriter, r *http.Request) {
	port, err := strconv.Atoi(r.PathValue("port"))
	if err != nil || port < 1 || port > 65535 {
		writeErr(w, http.StatusBadRequest, "port must be 1-65535")
		return
	}
	if s.Ports == nil {
		writeErr(w, http.StatusServiceUnavailable, "this engine does not serve workspace ports")
		return
	}
	target, err := s.Ports.Dial(r.Context(), r.PathValue("id"), port)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	ws, err := wstunnel.Accept(w, r)
	if err != nil {
		target.Close()
		return
	}
	wstunnel.Pipe(ws, target)
}

// serveSSHTunnel hands one connection to the SSH door for the workspace the
// query names. The door decides who gets in (the key, the run's owner);
// this only says who opened the tunnel, which the door's throttle counts
// failures by.
func (s *Server) serveSSHTunnel(w http.ResponseWriter, r *http.Request) {
	if s.SSH == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoDoor.Error())
		return
	}
	workspace := r.URL.Query().Get("workspace")
	if workspace == "" {
		writeErr(w, http.StatusBadRequest, "workspace is required")
		return
	}
	ws, err := wstunnel.Accept(w, r)
	if err != nil {
		return
	}
	s.SSH.ServeConn(ws, workspace, "user:"+auth.UserID(r.Context()))
}

var errNoDoor = errors.New("SSH access is off on this instance")
