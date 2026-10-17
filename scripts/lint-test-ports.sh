#!/usr/bin/env bash
# lint-test-ports.sh — substrate-flake guard adapted from SemEngine 67d0ee9.
#
# Fails (exit 1) if any *_test.go file binds a fixed TCP port via
# net.Listen. Ephemeral allocation via net.Listen("tcp", ":0") or
# "127.0.0.1:0" is the required pattern. Fixed-port literals collide
# under parallel test execution.
#
# Run from repo root:
#   scripts/lint-test-ports.sh

set -euo pipefail

# Pattern 1: literal port like ":18082" or "127.0.0.1:8080" (anything
# ending in :NNNN where NNNN is non-zero).
LITERAL_PATTERN='net\.Listen\("tcp[46]?",\s*"[^"]*:[1-9][0-9]*"'

# Pattern 2: Sprintf form, both bare (":%d") and host-prefixed
# ("127.0.0.1:%d"). The greedy [^"]* between quotes accepts any
# host segment (or none) before the :%d.
SPRINTF_PATTERN='net\.Listen\("tcp[46]?",\s*fmt\.Sprintf\("[^"]*:%d'

# No line is exempt: there is no inline marker. Exclude .worktrees and node_modules
# so a primary checkout never scans nested worktrees.
matches=$(grep -rnE "$LITERAL_PATTERN" --include='*_test.go' --exclude-dir=.worktrees --exclude-dir=node_modules . 2>/dev/null || true)
sprintf_matches=$(grep -rnE "$SPRINTF_PATTERN" --include='*_test.go' --exclude-dir=.worktrees --exclude-dir=node_modules . 2>/dev/null || true)
all=$(printf '%s\n%s\n' "$matches" "$sprintf_matches" | grep -v '^$' || true)

if [ -n "$all" ]; then
  echo 'FAIL: fixed-port net.Listen in test files (substrate-flake guard)'
  echo
  echo "$all"
  echo
  echo 'Fix: bind port 0 with net.Listen("tcp", "127.0.0.1:0") and hand the listener itself to the code'
  echo 'under test. Do not close it to reuse its port: another process can take the port in between.'
  exit 1
fi
