// Package config holds the deployment-plane boot configuration (DESIGN2.md
// §1): env vars only. Everything user-facing lives in the DB, configured
// through the UI. (The v1 factory.yaml parser and its import-config
// command were deleted post-M11 — no v1 deployments remain.)
package config
