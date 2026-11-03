package notices

import (
	"context"
	"fmt"
	"strings"
)

// The kinds of notice apothecary raises.
const (
	KindLoginSignedOut      = "login_signed_out"
	KindLoginExpired        = "login_expired"
	KindTrackerUnreachable  = "tracker_unreachable"
	KindTrackerMapping      = "tracker_mapping"
	KindChannelFailing      = "channel_failing" // never mirrored: it is about the mirror
	KindToolsImageFailed    = "tools_image_failed"
	KindBrowserImageFailed  = "browser_image_failed"
	KindK8sImageDrift       = "k8s_image_drift"
	KindSSHDoorStopped      = "ssh_door_stopped"
	KindReaperEnvsFailed    = "reaper_envs_failed"
	KindReaperStorageFailed = "reaper_storage_failed"
)

// CPUI pages a notice links to.
const (
	linkProviders    = "/system/providers"
	linkIntegrations = "/system/integrations"
	linkChannels     = "/system/channels"
)

// LoginSignedOut: the provider refused a login's refresh for good and it was
// disabled.
func (s *Service) LoginSignedOut(ctx context.Context, userID, credentialID, name, provider, reason string) {
	s.Raise(ctx, Raise{
		To: []string{userID}, Kind: KindLoginSignedOut, Key: KindLoginSignedOut + ":" + credentialID,
		Title: fmt.Sprintf("Sign in again: %s", name),
		Body: fmt.Sprintf(`apothecary disabled the %s login "%s": %s. Runs that use it are refused until you sign in again.`,
			provider, name, reason),
		Link: linkProviders,
	})
}

// LoginExpired: a login's refresh failed and its token has run out.
func (s *Service) LoginExpired(ctx context.Context, userID, credentialID, name string, err error) {
	s.Raise(ctx, Raise{
		To: []string{userID}, Kind: KindLoginExpired, Key: KindLoginExpired + ":" + credentialID,
		Title: fmt.Sprintf(`Login "%s" has expired`, name),
		Body: fmt.Sprintf("Refreshing it failed and its token has expired, so runs that use it are refused. apothecary keeps retrying: %s",
			err),
		Link: linkProviders,
	})
}

// LoginRefreshed: a login refreshed; both of its problems are over.
func (s *Service) LoginRefreshed(ctx context.Context, credentialID string) {
	s.Clear(ctx, KindLoginSignedOut+":"+credentialID)
	s.Clear(ctx, KindLoginExpired+":"+credentialID)
}

// TrackerSync: whether the ticket poll could read an integration's tracker
// (nil err clears).
func (s *Service) TrackerSync(ctx context.Context, userID, integrationID, name string, err error) {
	key := KindTrackerUnreachable + ":" + integrationID
	if err == nil {
		s.Clear(ctx, key)
		return
	}
	s.Raise(ctx, Raise{
		To: []string{userID}, Kind: KindTrackerUnreachable, Key: key,
		Title: fmt.Sprintf("Cannot sync tickets from %s", name),
		Body:  err.Error(),
		Link:  linkIntegrations,
	})
}

// TrackerListsGrew: an integration's tracker statuses or types gained rows,
// one line each in added.
func (s *Service) TrackerListsGrew(ctx context.Context, userID, integrationID, name string, added []string) {
	if len(added) == 0 {
		return
	}
	s.Raise(ctx, Raise{
		To: []string{userID}, Kind: KindTrackerMapping,
		Title: fmt.Sprintf("%s has new tracker statuses or types to map", name),
		Body:  strings.Join(added, "\n"),
		Link:  linkIntegrations,
	})
}

// ChannelSent: how a send through a notification channel went (nil err
// clears). An unowned channel is the admins'.
func (s *Service) ChannelSent(ctx context.Context, channelID, userID, kind string, err error) {
	key := KindChannelFailing + ":" + channelID
	if err == nil {
		s.Clear(ctx, key)
		return
	}
	s.Raise(ctx, Raise{
		To: []string{userID}, Kind: KindChannelFailing, Key: key,
		Title: fmt.Sprintf("Your %s channel is failing", kind),
		Body:  fmt.Sprintf("Notifications sent to it are not arriving: %s", err),
		Link:  linkChannels,
	})
}

// ToolsImage: how the docker host's tools-bundle warm-up went (nil clears).
func (s *Service) ToolsImage(ctx context.Context, err error) {
	s.admin(ctx, KindToolsImageFailed, "tools_image", err, "The workspace tools bundle failed to build",
		"Every docker-host provision needs it and retries the build; until one succeeds, runs fail to provision: %s")
}

// BrowserImage: how pulling the project-browser image went (nil clears).
func (s *Service) BrowserImage(ctx context.Context, err error) {
	s.admin(ctx, KindBrowserImageFailed, "browser_image", err, "The project browser image could not be pulled",
		"Flows that use the browser MCP server cannot start a browser: %s")
}

// ImageDrift: whether a Kubernetes workspace's seeded part ("shim" or
// "tools") matches this engine (nil err clears).
func (s *Service) ImageDrift(ctx context.Context, userID, integrationID, name, part string, err error) {
	key := fmt.Sprintf("%s:%s:%s", KindK8sImageDrift, integrationID, part)
	if err == nil {
		s.Clear(ctx, key)
		return
	}
	s.Raise(ctx, Raise{
		To: []string{userID}, Admins: true, Kind: KindK8sImageDrift, Key: key,
		Title: fmt.Sprintf("%s: the %s image does not match this engine", name, part),
		Body: fmt.Sprintf("%s. Publish the %s image of this engine's version, or point the integration's %s_image at it.",
			err, part, part),
		Link: linkIntegrations,
	})
}

// SSHDoor: the SSH door started (nil) or stopped with err.
func (s *Service) SSHDoor(ctx context.Context, err error) {
	s.admin(ctx, KindSSHDoorStopped, "ssh_door", err, "The SSH door stopped",
		"SSH connections to workspaces are refused until apothecaryd restarts: %s")
}

// ReaperEnvs: the finished workspaces a reaper pass could not destroy, one
// line each (empty clears).
func (s *Service) ReaperEnvs(ctx context.Context, failures []string) {
	const key = "reaper_envs"
	if len(failures) == 0 {
		s.Clear(ctx, key)
		return
	}
	s.Raise(ctx, Raise{
		Admins: true, Kind: KindReaperEnvsFailed, Key: key,
		Title: fmt.Sprintf("The reaper cannot destroy %d finished workspaces", len(failures)),
		Body:  strings.Join(failures, "\n"),
	})
}

// ReaperStorage: the storage clean-up steps a reaper pass failed, one line
// each (empty clears).
func (s *Service) ReaperStorage(ctx context.Context, failures []string) {
	const key = "reaper_storage"
	if len(failures) == 0 {
		s.Clear(ctx, key)
		return
	}
	s.Raise(ctx, Raise{
		Admins: true, Kind: KindReaperStorageFailed, Key: key,
		Title: "The reaper cannot clean up storage",
		Body:  strings.Join(failures, "\n"),
	})
}

// admin raises an install-level problem to the admins, or clears it on nil.
func (s *Service) admin(ctx context.Context, kind, key string, err error, title, body string) {
	if err == nil {
		s.Clear(ctx, key)
		return
	}
	s.Raise(ctx, Raise{Admins: true, Kind: kind, Key: key, Title: title, Body: fmt.Sprintf(body, err)})
}
