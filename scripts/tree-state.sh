#!/usr/bin/env bash
# Print a short fingerprint of the working tree: HEAD, the status listing, the
# tracked diff, and the content of every untracked, unignored file (the status
# listing names them but does not see an edit to one). task test:integration writes
# it to coverage/integration.tree after a passing run and cover-check.sh compares it,
# so an edit made after the run, tracked or untracked, makes cover:check refuse the
# coverage. Adapted from SemEngine 67d0ee9.
set -euo pipefail
cd "$(dirname "$0")/.."
{
  git rev-parse HEAD
  git status --porcelain
  git diff HEAD
  git ls-files --others --exclude-standard | git hash-object --stdin-paths
} | shasum | cut -c1-12
