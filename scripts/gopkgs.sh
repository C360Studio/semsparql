#!/usr/bin/env bash
# Run a package-scoped Go check, reporting the module's package count first.
# An empty module is stated explicitly ("0 package(s) found ... nothing to run")
# instead of letting go vet/test/govulncheck fail on an unmatched ./... pattern
# or silently passing. Usage: gopkgs.sh <command> [args...]
set -euo pipefail
# stderr is kept: a broken go.mod must say so, not exit 1 in silence.
n=$(go list ./... | wc -l | tr -d ' ')
echo "go: $n package(s) found in module $(go list -m)"
if [ "$n" -eq 0 ]; then
  echo "go: nothing to run for: $*"
  exit 0
fi
exec "$@"
