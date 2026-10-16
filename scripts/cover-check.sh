#!/usr/bin/env bash
# Enforce 80% statement coverage on the packages listed in targets below (or supplied
# via COVER_TARGETS for sensitivity tests), measured from the unit profile, the
# integration profile, or both merged. A package missing from its profile fails: no
# statements is not coverage. Adapted from SemEngine 67d0ee9.
# Usage: cover-check.sh [unit-profile integration-profile]
#   With no arguments it measures the unit profile itself and reads
#   coverage/integration.coverprofile, the profile of the last `task test:integration`
#   run in this worktree, which must have passed and must have run against the tree as
#   it is now.
set -euo pipefail
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

readonly threshold=80
readonly module=github.com/c360studio/semsparql

# The coverage targets, "<package directory> <profile>", profile one of unit, integration
# or merged. Runtime packages (irimap, reader, sparql, server, cmd/semsparql) add their
# lines here as tasks 1.1–4.1 land.
default_targets=(
  "irimap unit"
  "reader unit"
  "sparql unit"
  "server unit"
)
if [ -n "${COVER_TARGETS:-}" ]; then
  targets=()
  while IFS= read -r line; do
    [ -z "$line" ] || targets+=("$line")
  done <<<"$COVER_TARGETS"
else
  targets=("${default_targets[@]+"${default_targets[@]}"}")
fi

# The unit profile's packages: every target measured from it, alone or merged.
unit_packages=()
for target in "${targets[@]+"${targets[@]}"}"; do
  read -r dir kind <<<"$target"
  case "$kind" in
  unit | merged) unit_packages+=("./$dir/") ;;
  integration) ;;
  *)
    echo "cover: target $dir: unknown profile $kind" >&2
    exit 1
    ;;
  esac
done

if [ $# -eq 2 ]; then
  unit=$1 integration=$2
else
  mkdir -p coverage
  unit=coverage/unit.coverprofile
  printf 'mode: atomic\n' >"$unit"
  if [ ${#unit_packages[@]} -gt 0 ]; then
    # Its output is printed: a test that fails here must be named, not discarded.
    go test -count=1 -coverprofile="$unit" "${unit_packages[@]}"
  fi
  integration=coverage/integration.coverprofile
  # task test:integration removes coverage/integration.tree before go test and writes the
  # tree's fingerprint there only after go test passed: a profile without that file, or with
  # another tree's fingerprint, is refused, not trusted.
  now=$(scripts/tree-state.sh)
  if [ ! -f coverage/integration.tree ] || [ "$(cat coverage/integration.tree)" != "$now" ]; then
    echo "cover: no passing integration run measured this tree; run task test:integration" >&2
    exit 1
  fi
fi

# Percent of statements covered in one package, over one or more profiles. A block
# (file:range) appearing in several package runs or profiles counts once, covered if
# any run covered it.
percent() { # package profile...
  local pkg=$1
  shift
  awk -v pkg="$pkg" '
    FNR == 1 && /^mode:/ { next }
    {
      split($1, loc, ":"); file = loc[1]
      dir = file; sub(/\/[^\/]*$/, "", dir)
      if (dir != pkg) next
      stmts[$1] = $2
      if ($3 > 0) hit[$1] = 1
    }
    END {
      for (b in stmts) { total += stmts[b]; if (b in hit) covered += stmts[b] }
      if (total == 0) { print "none"; exit }
      printf "%.1f\n", 100 * covered / total
    }' "$@"
}

fail=0
check() { # package-directory label profile...
  local name=$1 label=$2 pct
  shift 2
  pct=$(percent "$module/$name" "$@")
  name=${name#internal/harness/}
  if [ "$pct" = none ]; then
    echo "cover: $name: no statements in the $label profile ($*)"
    fail=1
  elif awk -v p="$pct" -v t="$threshold" 'BEGIN { exit !(p < t) }'; then
    echo "cover: $name $pct% < $threshold% ($label profile)"
    fail=1
  else
    echo "cover: $name $pct% ($label profile)"
  fi
}

if [ ${#targets[@]} -eq 0 ]; then
  echo "cover: 0 target package(s) configured (populated as runtime packages land in tasks 1.1–4.1)"
fi

for target in "${targets[@]+"${targets[@]}"}"; do
  read -r dir kind <<<"$target"
  case "$kind" in
  unit) check "$dir" unit "$unit" ;;
  integration) check "$dir" integration "$integration" ;;
  merged) check "$dir" "merged unit and integration" "$unit" "$integration" ;;
  esac
done

if [ "$fail" -ne 0 ]; then echo "cover: FAILED (threshold $threshold%)"; exit 1; fi
echo "cover: ok (threshold $threshold%)"
