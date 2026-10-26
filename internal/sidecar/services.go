package sidecar

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ServiceState is one project service as the workspace's supervisor records
// it (the shim's serviceState: `shim service status`, GET /services).
type ServiceState struct {
	Name  string `json:"name"`
	Phase string `json:"phase"` // starting|healthy|unhealthy|exited|stopping|stopped
	// PID is the supervisor's; PGID the start script's process group.
	PID     int    `json:"pid,omitempty"`
	PGID    int    `json:"pgid,omitempty"`
	By      string `json:"by,omitempty"`    // stage|human
	Owner   string `json:"owner,omitempty"` // the phase whose stage started it
	StageID string `json:"stage_id,omitempty"`

	StartedAt   *time.Time `json:"started_at,omitempty"`
	Deadline    *time.Time `json:"deadline,omitempty"`
	HealthyAt   *time.Time `json:"healthy_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Daemonized  bool       `json:"daemonized,omitempty"`
	LastProbeAt *time.Time `json:"last_probe_at,omitempty"`
	LastProbeOK bool       `json:"last_probe_ok,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	// Launched: the start that answered this started it.
	Launched bool `json:"launched,omitempty"`
}

// Services reads every service's state the workspace has (none started:
// empty). A sidecar older than the services routes answers 404.
func (c *Client) Services(ctx context.Context) ([]ServiceState, error) {
	var out []ServiceState
	err := c.do(ctx, http.MethodGet, "/services", nil, &out)
	return out, err
}

// ServiceLog is the last n lines of a service's output.
func (c *Client) ServiceLog(ctx context.Context, name string, n int) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		base+"/services/"+url.PathEscape(name)+"/log?tail="+strconv.Itoa(n), nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if res.StatusCode >= 300 {
		return "", &Error{Status: res.StatusCode, Msg: fmt.Sprintf("service log: %s", data)}
	}
	return string(data), nil
}
