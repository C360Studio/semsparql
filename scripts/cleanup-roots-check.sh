#!/usr/bin/env bash
# Fail if any *_test.go file passes context.Background() or context.TODO() to a
# Stop, Close, or Terminate call. A t.Cleanup or defer that tears down a fixture
# needs a fresh, bounded root (context.WithTimeout(context.Background(), ...))
# because t.Context() is already canceled when t.Cleanup runs, and an unbounded
# root hangs the whole binary until the 10m go-test timeout if the call blocks.
# Adapted from SemEngine 67d0ee9.
# Usage: cleanup-roots-check.sh [repo-root]
set -euo pipefail
cd "${1:-$(dirname "$0")/..}"

# One line holding both the call and the unbounded root. --untracked includes new
# test files that are not yet added, so the guard fires before the commit does.
pattern='\.(Stop|Close|Terminate)\(.*context\.(Background|TODO)\(\)'
hits=$(git grep -n --untracked -E "$pattern" -- '*_test.go' || true)

files=$(git ls-files --cached --others --exclude-standard -- '*_test.go' | wc -l | tr -d ' ')
if [ -n "$hits" ]; then
  echo "cleanup-roots: unbounded cleanup in tests (use context.WithTimeout for Stop/Close/Terminate):"
  echo "$hits" | sed 's/^/  /'
  exit 1
fi
echo "cleanup-roots: ok ($files test file(s) scanned)"
