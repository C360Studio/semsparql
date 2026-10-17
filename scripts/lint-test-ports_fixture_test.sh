#!/usr/bin/env bash
# lint-test-ports_fixture_test.sh — verifies scripts/lint-test-ports.sh
# catches the expected matrix of fixed-port shapes and exempts the
# expected non-violation forms. Run from repo root.

set -uo pipefail

PASS=0
FAIL=0

check() {
  local description=$1
  local expect=$2   # "match" or "nomatch"
  local fixture=$3

  local tmp
  tmp=$(mktemp -d)
  trap "rm -rf '$tmp'" RETURN

  cat >"$tmp/violation_test.go" <<EOF
package fixture
import "net"
$fixture
EOF

  # Run the guard in the tmp dir, capture exit code without -e tripping.
  set +e
  ( cd "$tmp" && bash "$OLDPWD/scripts/lint-test-ports.sh" >/dev/null 2>&1 )
  local rc=$?
  set -e

  if [ "$expect" = "match" ] && [ $rc -ne 1 ]; then
    echo "FAIL [$description]: expected exit 1 (match), got $rc"
    FAIL=$((FAIL + 1))
  elif [ "$expect" = "nomatch" ] && [ $rc -ne 0 ]; then
    echo "FAIL [$description]: expected exit 0 (nomatch), got $rc"
    FAIL=$((FAIL + 1))
  else
    PASS=$((PASS + 1))
  fi

  rm -rf "$tmp"
  trap - RETURN
}

export OLDPWD="$(pwd)"

# Positive matches — must FAIL the guard (exit 1).
check 'literal port :18082'            match   'func f() { net.Listen("tcp", ":18082") }'
check 'literal port with host'         match   'func f() { net.Listen("tcp", "127.0.0.1:8080") }'
check 'Sprintf bare port'              match   'import "fmt"; func f() { net.Listen("tcp", fmt.Sprintf(":%d", 9000)) }'
check 'Sprintf host-prefixed port'     match   'import "fmt"; func f() { net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", 9000)) }'
check 'Sprintf 0.0.0.0 host'           match   'import "fmt"; func f() { net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", 9000)) }'

# Negative matches — must PASS the guard (exit 0).
check 'ephemeral bare'                 nomatch 'func f() { net.Listen("tcp", ":0") }'
check 'ephemeral localhost'            nomatch 'func f() { net.Listen("tcp", "127.0.0.1:0") }'
check 'ephemeral 0.0.0.0'              nomatch 'func f() { net.Listen("tcp", "0.0.0.0:0") }'

# The old suppression marker exempts nothing (adapted in SemEngine, flake-defense D7).
check 'marked violation'               match   'import "fmt"; func f() { net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)) // gh#220:allow-fixed-port
}'

# Empty file — must PASS.
check 'empty'                          nomatch ''

echo
echo "lint-test-ports fixture test: $PASS passed, $FAIL failed"
[ $FAIL -eq 0 ]
