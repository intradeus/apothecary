// Package checkout holds the git steps a run's checkout is set up with: the
// branch step, which puts the checkout on the run's branch, and the
// credential-store line for its repository host.
//
// Two places need them. The provisioners (the Docker and Kubernetes hosts)
// run the branch step right after the clone and before the devcontainer is
// built from the checkout, so the build reads the files the run works on: a
// run fixing the devcontainer builds with its fix, a run on a merge request
// builds with the merge request's. Bootstrap runs the same step again inside
// the workspace, where it re-enters the branch the provisioner created (and
// creates it for the extra checkouts, which only bootstrap clones). One
// implementation, so the two cannot drift apart.
package checkout

import (
	"fmt"
	"net/url"

	"apothecary/internal/platform/shell"
)

// BranchScript enters a checkout's branch; run inside the checkout. The
// branch is re-entered when it exists — locally (a restart, a rebuild) or
// on the remote (a rerun of a pushed branch, a merge request's own branch:
// `git switch` takes the remote's up when exactly one remote holds it) —
// else created: from the merge request's fetched head when the run was
// created on one (fetch is the ref its repository host keeps for it, the
// only way to a fork's head), else forked from origin/<source>.
func BranchScript(branch, source, fetch string) string {
	b := shell.Quote(branch)
	if fetch != "" {
		return fmt.Sprintf("git switch %s 2>/dev/null || { git fetch origin %s && git switch -c %s FETCH_HEAD; }",
			b, shell.Quote(fetch), b)
	}
	return fmt.Sprintf("git switch %s 2>/dev/null || git switch -c %s %s", b, b, shell.Quote("origin/"+source))
}

// CredentialLine builds the git credential-store entry for the repository
// host of repoURL: `scheme://user:pass@host`, both parts URL-escaped as the
// store format wants. An empty user is GitLab's token user, "oauth2".
func CredentialLine(repoURL, user, pass string) (string, error) {
	u, err := url.Parse(repoURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot parse the repository URL")
	}
	if user == "" {
		user = "oauth2"
	}
	return fmt.Sprintf("%s://%s:%s@%s", u.Scheme, url.QueryEscape(user), url.QueryEscape(pass), u.Host), nil
}
