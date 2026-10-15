#!/usr/bin/env bash
# Report tool versions and compare each against its single pin. Starts no workloads.
# Adapted from SemEngine 67d0ee9. The go and Docker checks join with task 0.1 of issue #1.
set -uo pipefail
cd "$(dirname "$0")/.."

fail=0
ok()   { printf '  ok    %-13s %s\n' "$1" "$2"; }
bad()  { printf '  FAIL  %-13s %s\n' "$1" "$2"; fail=1; }

pkg_pin() { node -p "require('./package.json').devDependencies['$1']" 2>/dev/null; }

echo "SemSparql doctor"

# task: .task-version is the pin (CI installs it from the same file).
if command -v task >/dev/null; then
  want=$(tr -d '[:space:]' < .task-version)
  have=$(task --version | grep -Eo '[0-9]+\.[0-9]+\.[0-9]+' | head -n1)
  if [ "$have" = "$want" ]; then ok task "$have"; else bad task "$have != .task-version $want"; fi
else bad task "not found"; fi

# node: .nvmrc is the pin, compared by major version.
if command -v node >/dev/null; then
  want=$(tr -d '[:space:]v' < .nvmrc)
  have=$(node --version | sed 's/^v//')
  if [ "${have%%.*}" = "${want%%.*}" ]; then ok node "$have (.nvmrc: $want)"; else bad node "$have != .nvmrc $want"; fi
else bad node "not found"; fi

if command -v npm >/dev/null; then ok npm "$(npm --version)"; else bad npm "not found"; fi

# python3: no pin; scripts/openspec-queue.sh parses JSON and timestamps with it. task spec:check runs that
# script with --check, so task verify needs python3 too.
if command -v python3 >/dev/null; then ok python3 "$(python3 --version 2>&1 | sed 's/^Python //')"
else bad python3 "not found (task spec:check, task verify and task spec:queue need it)"; fi

# gh and jq: task merge:check reads GitHub with them.
for t in gh jq; do
  if command -v "$t" >/dev/null; then ok "$t" "$("$t" --version 2>&1 | head -n1)"
  else bad "$t" "not found (task merge:check needs it)"; fi
done

# npm-installed tools resolve from node_modules, pinned in package.json.
check_npm_tool() { # name bin
  local want have
  want=$(pkg_pin "$1")
  if [ ! -x "node_modules/.bin/$2" ]; then bad "$2" "not installed: run npm ci"; return; fi
  have=$(node -p "require('./node_modules/$1/package.json').version")
  if [ "$have" = "$want" ]; then ok "$2" "$have"; else bad "$2" "$have != package.json $want"; fi
}
check_npm_tool @fission-ai/openspec openspec
check_npm_tool markdownlint-cli2 markdownlint-cli2

if [ "$fail" -ne 0 ]; then echo "doctor: FAILED"; exit 1; fi
echo "doctor: ok"
