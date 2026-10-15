#!/usr/bin/env bash
# Merge check, adopted unchanged in behaviour from semengine
# (scripts/merge-check.sh at 67d0ee9, where semengine's openspec changes
# flake-defense and review-gate-check and its merge-gate spec define it).
# Reads GitHub when it runs, so a re-run sees the state of that moment.
#
#   1. The rules in force on main must require the check `Required` with the
#      strict (up-to-date) setting on, from a ruleset whose enforcement is active.
#   2. On a pull-request run: while an issue labelled class:flake is open, fail
#      unless this pull request's closing references include every open one.
#      There is no waiver: no comment, label or variable exempts a pull request.
#   3. On a pull-request run, before rule 2 (semengine's
#      review-gate-check): a code pull request that is
#      not a draft fails unless its description has one implemented-by: line
#      naming claude, codex, or gemini (not read for a bot's) and one reviewed-by:
#      line naming another agent (any of the three for a bot's, or when more than
#      one is named); a draft's findings are printed and fail nothing.
#
# Usage: merge-check.sh <pr-number>
#   The kind of run comes from GITHUB_EVENT_NAME only when GITHUB_ACTIONS is
#   true (pull_request: a pull-request run; push: a push run, which checks rule 1
#   only). Outside Actions GITHUB_EVENT_NAME is not read and every run is a
#   pull-request run. Run it locally as `task merge:check -- <n>` immediately
#   before merging, in a shell where GITHUB_ACTIONS is not set.
#
# A read that fails, or an answer that is not the shape asked for, exits 2
# naming the read; it is never taken as "no known flake". Needs gh and jq.
set -uo pipefail

readonly label=class:flake
readonly limit=100
readonly check=Required

die() { echo "merge-check: $*" >&2; exit 1; }

if [ "${GITHUB_ACTIONS:-}" = true ]; then
  case "${GITHUB_EVENT_NAME:-}" in
    pull_request) kind=pull-request how="GitHub Actions, pull_request event" ;;
    push) kind=push how="GitHub Actions, push event" ;;
    "") echo "merge-check: inside GitHub Actions with GITHUB_EVENT_NAME unset"; die "the event is missing" ;;
    *)
      echo "merge-check: inside GitHub Actions on event ${GITHUB_EVENT_NAME}"
      die "event ${GITHUB_EVENT_NAME} has no rule here; add one before the workflow triggers on it" ;;
  esac
else
  kind=pull-request how="outside GitHub Actions; GITHUB_EVENT_NAME not read"
fi

pr=${1:-}
if [ "$kind" = pull-request ]; then
  echo "merge-check: pull-request run for #${pr:-?} (${how})"
  [ -n "$pr" ] || die "a pull request number is required: merge-check.sh <pr-number>"
  case "$pr" in *[!0-9]* | 0*) die "pull request number '$pr' is not a positive integer" ;; esac
else
  echo "merge-check: push run (${how}); the known-flake check does not apply, the up-to-date rule is checked"
  echo "merge-check: the review check does not apply to a push run; no pull request is read"
fi

# read NAME SHAPE CMD...: run a gh read and keep its stdout in $answer when it
# succeeded and jq accepts SHAPE; otherwise exit 2 naming the read.
answer=
errfile=$(mktemp)
trap 'rm -f "$errfile"' EXIT
read_gh() {
  local name=$1 shape=$2
  shift 2
  if ! answer=$("$@" </dev/null 2>"$errfile"); then
    echo "merge-check: unavailable: the read of ${name} failed ($*): $(cat "$errfile")" >&2
    exit 2
  fi
  if ! printf '%s' "$answer" | jq -e "$shape" >/dev/null 2>&1; then
    echo "merge-check: unavailable: the read of ${name} returned something other than what was asked for ($*): ${answer}" >&2
    exit 2
  fi
}

failed=0

# 1. Up-to-date rule. GitHub answers which rules apply to main; only the fields
# that carry the decision are read (design D9).
read_gh "the rules in force on main" 'type == "array" and all(.[]; type == "object")' \
  gh api 'repos/{owner}/{repo}/rules/branches/main'
rules=$answer
found=$(printf '%s' "$rules" | jq -c --arg c "$check" '[.[]
  | select(.type == "required_status_checks")
  | select([.parameters.required_status_checks[]?.context] | index($c))
  | {ruleset_id, strict: .parameters.strict_required_status_checks_policy}]')
if [ "$(printf '%s' "$found" | jq 'length')" -eq 0 ]; then
  echo "merge-check: no rule in force on main requires ${check}; rules found: $(printf '%s' "$rules" | jq -c '[.[] | {type, ruleset_id}]')"
  failed=1
else
  ok=0
  while IFS=$'\t' read -r id strict; do
    case "$id" in '' | *[!0-9]*) die "a rule requiring ${check} names ruleset_id '${id}', not a number" ;; esac
    read_gh "ruleset ${id}" 'type == "object" and (.enforcement | type == "string")' \
      gh api "repos/{owner}/{repo}/rulesets/${id}"
    enforcement=$(printf '%s' "$answer" | jq -r '.enforcement')
    echo "merge-check: rule type=required_status_checks context=${check} strict_required_status_checks_policy=${strict} ruleset_id=${id} enforcement=${enforcement}"
    if [ "$strict" = true ] && [ "$enforcement" = active ]; then ok=1; fi
  done < <(printf '%s' "$found" | jq -r '.[] | [(.ruleset_id | tostring), (.strict | tostring)] | @tsv')
  if [ "$ok" -ne 1 ]; then
    echo "merge-check: FAIL: no active ruleset requires ${check} with strict_required_status_checks_policy=true; a head behind main could merge"
    failed=1
  fi
fi

if [ "$kind" = push ]; then
  [ "$failed" -eq 0 ] || { echo "merge-check: FAILED"; exit 1; }
  echo "merge-check: ok"
  exit 0
fi

# Review check (spec merge-gate, "Cross-agent review check"). The pull request,
# then every page of its changed files: gh prints the pages joined into one list
# or as lists one after the other, and both are read the same way.
read_gh "pull request #${pr} for the review check" \
  'type == "object" and (.draft | type) == "boolean" and (.changed_files | type) == "number" and (.body == null or (.body | type) == "string") and (.user.type | type) == "string"' \
  gh api "repos/{owner}/{repo}/pulls/${pr}"
pull=$answer
read_gh "the files of pull request #${pr}" 'type == "array"' \
  gh api --paginate "repos/{owner}/{repo}/pulls/${pr}/files?per_page=100"
# The shape check above sees only the last page; every page is checked here.
if ! files=$(printf '%s' "$answer" | jq -c -s 'if all(.[]; type == "array" and all(.[]; type == "object" and (.filename | type) == "string" and (.previous_filename == null or (.previous_filename | type) == "string"))) then add // [] else error("not pages of files") end'); then
  echo "merge-check: unavailable: the read of the files of pull request #${pr} returned something other than what was asked for: ${answer}" >&2
  exit 2
fi
reported=$(printf '%s' "$pull" | jq '.changed_files')
draft=$(printf '%s' "$pull" | jq '.draft')
entries=$(printf '%s' "$files" | jq 'length')
# A failed jq leaves an empty number, which a test would not compare.
case "$entries:$reported:$draft" in
  [0-9]*:[0-9]*:true | [0-9]*:[0-9]*:false) ;;
  *) echo "merge-check: unavailable: jq failed reading the file count or draft flag of pull request #${pr}" >&2; exit 2 ;;
esac
# Compared in jq: a shell test on a count that is not an integer errors, and an
# error would read as "the counts match".
if ! printf '%s' "$pull" | jq -e --argjson n "$entries" '.changed_files == $n' >/dev/null; then
  echo "merge-check: unavailable: the file list of pull request #${pr} is incomplete: it has ${entries} of ${reported} files" >&2
  exit 2
fi
# A document name ends in .md or starts with openspec/, and is not under
# .claude/agents/ or .agents/contracts/; every other name is a code name. A
# renamed file's previous name counts too. A failed jq leaves an empty list,
# which would read as documents only. The names stay a JSON list: printed one
# per line, a name that is empty or only line feeds would vanish.
if ! code=$(printf '%s' "$files" | jq -c '[.[] | .filename, (.previous_filename // empty)]
  | map(select(((endswith(".md") or startswith("openspec/")) and (startswith(".claude/agents/") | not) and (startswith(".agents/contracts/") | not)) | not))
  | unique'); then
  echo "merge-check: unavailable: jq failed sorting the changed files of pull request #${pr} into code names" >&2
  exit 2
fi
if [ "$code" = "[]" ]; then
  echo "merge-check: pull request #${pr} is documents only (${reported} changed files); the review check passes"
else
  echo "merge-check: pull request #${pr} is a code pull request; its code files ($(printf '%s' "$code" | jq length), up to ten shown):"
  printf '%s' "$code" | jq -r '.[:10][] | "merge-check: code file: \(.)"'
  # The two lines: each starts in the first column; a carriage return before the
  # line feed is dropped. An agent name is claude, codex, or gemini in any letter
  # case, with no letter or digit directly before or after it. Nothing else is read.
  if ! lines=$(printf '%s' "$pull" | jq -r '
    def agents: [scan("(?i)(?:^|[^\\p{L}\\p{N}])(claude|codex|gemini)(?=[^\\p{L}\\p{N}]|$)") | .[0] | ascii_downcase] | unique | join(" ");
    [(.body // "") | split("\n")[] | rtrimstr("\r")] as $l
    | ($l | map(select(startswith("implemented-by:")))) as $i
    | ($l | map(select(startswith("reviewed-by:")))) as $r
    | [.user.type, ($i | length), ($i[0] // "" | ltrimstr("implemented-by:") | agents),
       ($r | length), ($r[0] // "" | ltrimstr("reviewed-by:") | agents)] | map(tostring) | join("|")'); then
    echo "merge-check: unavailable: jq failed reading the description of pull request #${pr}" >&2
    exit 2
  fi
  IFS='|' read -r author icount iagents rcount ragents <<<"$lines"
  case "$icount:$rcount" in
    *[!0-9:]* | :* | *:) echo "merge-check: unavailable: the description of pull request #${pr} was read as '${lines}'" >&2; exit 2 ;;
  esac

  # The single implementing agent when implemented-by: names one; empty when any
  # agent may review: a bot's pull request, more than one agent named, or no valid line.
  impl_single=
  others=
  wantline="reviewed-by: claude, reviewed-by: codex, or reviewed-by: gemini"
  if [ "$author" != Bot ] && [ "$icount" -eq 1 ]; then
    case "$iagents" in
      claude) impl_single=claude others="codex or gemini" wantline="reviewed-by: codex (or reviewed-by: gemini)" ;;
      codex)  impl_single=codex  others="claude or gemini" wantline="reviewed-by: claude (or reviewed-by: gemini)" ;;
      gemini) impl_single=gemini others="claude or codex"  wantline="reviewed-by: claude (or reviewed-by: codex)" ;;
    esac
  fi
  implline="implemented-by: claude (<model or persona>), implemented-by: codex (<model or persona>), or implemented-by: gemini (<model or persona>)"
  findings=0
  finding() { echo "merge-check: review finding: $1; the line to write: $2"; findings=$((findings + 1)); }
  if [ "$author" != Bot ]; then
    if [ "$icount" -eq 0 ]; then
      finding "no line starts implemented-by:, and the implemented-by: line must name claude, codex, or gemini, the agent that wrote the commits" "$implline"
    elif [ "$icount" -gt 1 ]; then
      finding "${icount} lines start implemented-by:, and exactly one may" "$implline"
    elif [ -z "$iagents" ]; then
      finding "the implemented-by: line must name claude, codex, or gemini, the agent that wrote the commits" "$implline"
    fi
  fi
  if [ "$rcount" -eq 0 ]; then
    finding "no line starts reviewed-by:" "$wantline"
  elif [ "$rcount" -gt 1 ]; then
    finding "${rcount} lines start reviewed-by:, and exactly one may" "$wantline"
  elif [ "$ragents" != claude ] && [ "$ragents" != codex ] && [ "$ragents" != gemini ]; then
    finding "the reviewed-by: line must name exactly one of claude, codex, and gemini" "$wantline"
  elif [ -n "$impl_single" ] && [ "$ragents" = "$impl_single" ]; then
    finding "the reviewed-by: line names ${ragents}, which the implemented-by: line names; another agent (${others}) reviews" "$wantline"
  fi
  if [ "$findings" -eq 0 ]; then
    echo "merge-check: the review check passes; reviewing agent: ${ragents}"
  elif [ "$draft" = true ]; then
    echo "merge-check: pull request #${pr} is a draft: a review finding does not fail a draft (${findings} above)"
  else
    echo "merge-check: FAIL: the review check has ${findings} finding(s); editing the description starts no run, so run the merge-check job again after the edit"
    failed=1
  fi
fi

# 2. Known-flake check.
# The whole label list, not a search: gh 2.97 prints nothing at all, not [], for
# a search that matches no label, which would read as a failed read.
read_gh "labels" 'type == "array" and all(.[]; (.name | type) == "string")' \
  gh label list --json name --limit "$limit"
if ! printf '%s' "$answer" | jq -e --arg l "$label" 'map(.name) | index($l)' >/dev/null; then
  if [ "$(printf '%s' "$answer" | jq 'length')" -ge "$limit" ]; then
    echo "merge-check: unavailable: the label list returned ${limit} labels and may be incomplete" >&2
    exit 2
  fi
  echo "merge-check: FAIL: the label ${label} is missing; without it an empty issue list would read as no known flake"
  exit 1
fi

read_gh "open ${label} issues" 'type == "array" and all(.[]; (.url | type) == "string" and (.number | type) == "number")' \
  gh issue list --label "$label" --state open --json number,url,title --limit "$limit"
issues=$answer
count=$(printf '%s' "$issues" | jq 'length')
if [ "$count" -ge "$limit" ]; then
  echo "merge-check: FAIL: the list of open ${label} issues returned ${count} of ${limit} asked for and may be incomplete"
  exit 1
fi
if [ "$count" -eq 0 ]; then
  echo "merge-check: no open ${label} issue"
  [ "$failed" -eq 0 ] || { echo "merge-check: FAILED"; exit 1; }
  echo "merge-check: ok"
  exit 0
fi

read_gh "pull request #${pr}" '(.closingIssuesReferences | type == "array") and all(.closingIssuesReferences[]; (.url | type) == "string")' \
  gh pr view "$pr" --json closingIssuesReferences
# A failed jq leaves an empty result, which would read as "every flake is closed".
if ! closing=$(printf '%s' "$answer" | jq -c '[.closingIssuesReferences[].url]'); then
  echo "merge-check: unavailable: jq failed reading the closing references of pull request #${pr}" >&2
  exit 2
fi

# Compared by URL, so the same number in another repository does not count.
if ! open_not_closed=$(printf '%s' "$issues" | jq -r --argjson c "$closing" '.[] | select(.url as $u | $c | index($u) | not) | "#\(.number) \(.url) \(.title)"'); then
  echo "merge-check: unavailable: jq failed comparing the open ${label} issues with the closing references of pull request #${pr}" >&2
  exit 2
fi
if [ -n "$open_not_closed" ]; then
  echo "merge-check: FAIL: known flakes are open and not closed by this pull request:"
  printf '%s\n' "$open_not_closed" | sed 's/^/  /'
  echo "merge-check: fix them in this pull request (Closes #n for each), or wait for the pull request that does"
  exit 1
fi

exempting=$(printf '%s' "$issues" | jq -r '[.[] | "#\(.number)"] | join(", ")')
printf '%s' "$issues" | jq -r --arg pr "$pr" '.[] | "merge-check: closing #\(.number) exempts pull request #\($pr) (\(.url))"'
echo "::warning::pull request #${pr} is exempt from the known-flake check by closing ${exempting}; the reviewer confirms the reproduction before and after the fix"
[ "$failed" -eq 0 ] || { echo "merge-check: FAILED"; exit 1; }
echo "merge-check: ok"
