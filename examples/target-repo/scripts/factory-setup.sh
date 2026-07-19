#!/usr/bin/env bash
# Factory bootstrap step 6 (§11): dependency install / environment prep.
set -euo pipefail
go mod download
echo "setup: done"
