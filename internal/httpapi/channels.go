package httpapi

import (
	"net/http"
	"strings"

	"apothecary/internal/account/auth"
	"apothecary/internal/platform/secrets"
	"apothecary/internal/project/channels"
)

// Notification channels: a user's Matrix/Slack/Discord destinations.

// maskChannel redacts secret-shaped config values (tokens etc.) before an
// Entry leaves the server: channel storage is a documented plaintext
// residual, but responses carry only masks, like integrations (§3).
func maskChannel(e *channels.Entry) map[string]any {
	cfg := make(map[string]any, len(e.Config))
	for k, v := range e.Config {
		lk := strings.ToLower(k)
		secret := strings.Contains(lk, "token") || strings.Contains(lk, "secret") ||
			strings.Contains(lk, "password") || strings.Contains(lk, "key") ||
			strings.Contains(lk, "webhook") // e.g. Slack webhook_url embeds its secret
		if str, ok := v.(string); ok && secret {
			cfg[k] = secrets.Mask(str)
		} else {
			cfg[k] = v
		}
	}
	return map[string]any{"id": e.ID, "kind": e.Kind, "config": cfg}
}

func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	entries := s.Registry.List(auth.UserID(r.Context()))
	out := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		out = append(out, maskChannel(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// addChannel accepts the §8 flat shape: {"kind":"matrix", "homeserver":...,
// "access_token":..., "room_id":...} — kind plus config keys.
func (s *Server) addChannel(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if !readJSON(w, r, &body) {
		return
	}
	kind, _ := body["kind"].(string)
	if kind == "" {
		writeErr(w, http.StatusBadRequest, "kind is required")
		return
	}
	delete(body, "kind")
	e, err := s.Registry.Add(auth.UserID(r.Context()), kind, body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, maskChannel(e))
}

func (s *Server) deleteChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.Registry.Delete(r.PathValue("id")); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// channelRoutes is a user's notification channels.
func (s *Server) channelRoutes() []Route {
	return []Route{
		{Method: "GET", Path: "/api/channels", ID: "listChannels", Tag: tagChannels,
			Summary: "One's notification channels (secrets masked)",
			Handler: s.listChannels, Returns: []channels.Entry{}},
		{Method: "POST", Path: "/api/channels", ID: "addChannel", Tag: tagChannels,
			Summary: "Add a notification channel",
			Handler: s.addChannel, Body: ChannelRequest{}, Returns: channels.Entry{}, Status: http.StatusCreated},
		{Method: "DELETE", Path: "/api/channels/{id}", ID: "deleteChannel", Tag: tagChannels,
			Summary: "Remove a notification channel",
			Handler: s.deleteChannel, Status: http.StatusNoContent, Scope: ScopeChannel},
	}
}
