#!/usr/bin/env bash
# Run every gate cheapest first, report wall time per step, then require that
# verification left tracked files unchanged. Keeps going after a failure so one
# run reports every broken gate. Adapted from SemEngine 67d0ee9.
set -uo pipefail
cd "$(dirname "$0")/.."

# Order is the contract: cheapest first. The Go gates (fmt:check tidy:check build
# vet lint vuln test:unit test:integration cover:check test:repeat) join here with
# task 0.1 of issue #1, test:repeat last.
steps=(spec:check docs:check)

# Tracked-file state before the run. Comparing before/after (not just "is dirty")
# keeps verify usable on a working tree with edits in progress; on a clean
# checkout, as in CI, it is identical to requiring a clean tree.
tracked_state() { git status --porcelain --untracked-files=no; git diff HEAD | shasum; }
before=$(tracked_state)

failed=()
timings=()
for s in "${steps[@]}"; do
  echo "==> task $s"
  start=$(date +%s)
  if ! task "$s"; then failed+=("$s"); fi
  timings+=("$(printf '%-20s %3ds' "$s" $(($(date +%s) - start)))")
done

echo
echo "step timings:"
printf '  %s\n' "${timings[@]}"

if [ "$(tracked_state)" != "$before" ]; then
  echo "verify: tracked files changed during verification:"
  git status --porcelain --untracked-files=no
  failed+=("tree-unchanged")
fi
untracked=$(git status --porcelain | grep '^??' || true)
[ -z "$untracked" ] || { echo "untracked files (reported, allowed):"; echo "$untracked" | sed 's/^/  /'; }

if [ ${#failed[@]} -ne 0 ]; then
  echo "verify: FAILED: ${failed[*]}"
  exit 1
fi
echo "verify: ok"
