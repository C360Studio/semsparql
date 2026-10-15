#!/usr/bin/env bash
# Ported from SemStreams 5457b345 via SemEngine 67d0ee9; uses the repo-pinned OpenSpec CLI from node_modules.
# openspec-queue.sh — surface WHY each in-flight change is still open.
#
# `openspec list` renders a change as a bare progress fraction ("12/14
# tasks"). That fraction actively misleads when the remaining work is not
# work at all but a HALT condition, a deliberate not-done, or a red gate:
# it reads "almost done, just finish it" when the honest reading is
# "decide whether this change should still exist".
#
# Three costs, all observed in SemStreams before the port (2026-08-01):
#
#   1. `[~] 4.2 NOT DONE, deliberately` in lifecycle-operator-create
#      stopped the implementer and did NOT stop the archiver. Its spec
#      delta still REQUIRED the declined behavior, and a scenario that a
#      shipped test disproves came within one command of being published
#      into openspec/specs/ as permanent current truth.
#   2. predicate-raw-key-representation's task 4.3 is a conditional HALT
#      ("if the pre-v1 wipe window closed ... re-file as a post-v1
#      migration instead of executing a second wipe"). It was invisible:
#      the word is lowercase "halt:" mid-sentence, and a purpose-built
#      case-SENSITIVE grep for it returned zero lines. A filter that
#      matches nothing on a file that contains the thing is a broken
#      filter, not a clean file — hence -i throughout below.
#   3. The program baton compensated by re-narrating these caveats every
#      session, which is both a maintenance tax and a staleness tripwire
#      that fires on changes which are correctly parked.
#
# This script reads them from the SOURCE every run, so no handoff note has to carry them and cannot drift from them.
#
# A task is read whole: its checkbox line and every line wrapped or nested
# under it, up to the next task or the next line indented no deeper than
# the task. Labels come from the checkbox line alone. A `Hold:` (matched
# with its case) anywhere in an open task's block gets one BLOCKED line,
# numbered where the hold is written, with the task number and the text
# from `Hold:` on; the checkbox line is not shown as BLOCKED a second time.
#
# Exit status is advisory-by-default and deliberately so: this is a
# reporting aid for humans and session startup, not a merge gate. Use
# --strict to exit non-zero when any caveat is found (for CI or a
# pre-archive hook). Exit 2 means the queue could not be read.
#
# --check prints no queue; `task spec:check` runs it. A `Hold:` below a
# tasks.md's first "## " heading that lies in no task's block, ticked tasks
# included, is one the queue cannot show: each is printed as <path>:<line>:
# and the exit is 1. With none it prints "holds: ok (<n> tasks.md read)" and
# exits 0. An unreadable change list or tasks.md exits 2, as for the queue.
#
# Run from repo root:
#   scripts/openspec-queue.sh [--strict] [--stale-days N]
#   scripts/openspec-queue.sh --check

set -uo pipefail

STRICT=0
CHECK=0
STALE_DAYS=7

while [ $# -gt 0 ]; do
  case "$1" in
    --strict) STRICT=1; shift ;;
    --check) CHECK=1; shift ;;
    --stale-days)
      case "${2:-}" in
        '' | *[!0-9]*) echo "--stale-days needs a whole number of days" >&2; exit 2 ;;
      esac
      STALE_DAYS="$2"; shift 2 ;;
    -h|--help) sed -n '2,51p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

# Use the repo-pinned CLI (npm ci), never a globally installed one.
PATH="$PWD/node_modules/.bin:$PATH"
if [ ! -x node_modules/.bin/openspec ]; then
  echo "pinned openspec CLI missing: run npm ci" >&2
  exit 2
fi

CHANGES_DIR="openspec/changes"
[ -d "$CHANGES_DIR" ] || { echo "no $CHANGES_DIR (run from repo root)" >&2; exit 2; }

# Caveat classes, most severe first. A line matching an earlier pattern is
# reported under that class and not re-reported under a later one.
#
# These are matched ONLY against unchecked/partial task lines, because a
# COMPLETED task mentioning "blocked" is describing history, not a live
# condition — matching it would produce the noise that gets a report
# ignored, which is the failure mode this script exists to avoid.
#
# Word-boundary matching, NOT substring. The first draft used a bare
# `*fail*` glob and classified "converts a posture into a boot failure"
# as RED — SemStreams' fixture test caught it (not ported yet). Over-broad matching is not a
# harmless surplus here: a report that cries wolf on ordinary prose stops
# being read, which returns us to the invisible-caveat state this script
# exists to fix. "failure" as a noun in prose is not a red gate; "FAILED"
# and "is failing" are.
label_for() {
  local t=$1
  printf '%s' "$t" | grep -qiE '\bhalt(s|ed|ing)?\b'                  && { echo "HALT";    return; }
  printf '%s' "$t" | grep -qiE '\bred\b|\bfailed\b|\bfailing\b'       && { echo "RED";     return; }
  printf '%s' "$t" | grep -qiE '\bhold\b|\bblocked\b|\bblocking\b'    && { echo "BLOCKED"; return; }
  printf '%s' "$t" | grep -qiE '\bdeliberate|\bnot done\b|\bwont ?do\b' && { echo "WONTDO";  return; }
  printf '%s' "$t" | grep -qiE 'still open'                          && { echo "OPEN-Q";  return; }
  echo ""
}

# scan_tasks FILE prints one tab-separated row per line of interest, tasks
# in line order:
#   open  <line> <checkbox line>  an open task ("- [ ]" or "- [~]") with no hold
#   held  <line> <checkbox line>  an open task with a hold; its hold row follows
#   hold  <line> <task number> <the block's text from its first Hold: on>
#   stray <line>                  a Hold: below the first "## " heading, in no block
# A task is a line that begins, after any indentation, with "- [", one
# character and "]", ticked tasks included. Its block runs from that line up
# to the next task, or the next non-blank line indented no deeper than the
# task, whichever comes first.
scan_tasks() {
  python3 - "$1" <<'PY'
import re, sys

TASK = re.compile(r"\s*- \[(.)\]")
tasks, strays = [], []
task, below = None, False
with open(sys.argv[1], encoding="utf-8") as f:
    for n, line in enumerate(f, 1):
        line = line.rstrip("\n")
        depth = len(line) - len(line.lstrip())
        m = TASK.match(line)
        if m:
            words = line[m.end():].split()
            task = {"line": n, "open": m.group(1) in " ~", "first": line, "depth": depth,
                    "number": words[0] if words else "", "hold": 0, "text": []}
            tasks.append(task)
        elif task and line.strip() and depth <= task["depth"]:
            task = None
        if task is None:
            if below and "Hold:" in line:
                strays.append(n)
        elif task["hold"]:
            task["text"].append(line)
        elif "Hold:" in line:
            task["hold"] = n
            task["text"].append(line[line.index("Hold:"):])
        if line.startswith("## "):
            below = True
for t in tasks:
    if not t["open"]:
        continue
    if t["hold"]:
        print("held\t%d\t%s" % (t["line"], t["first"]))
        print("hold\t%d\t%s %s" % (t["hold"], t["number"], " ".join(t["text"])))
    else:
        print("open\t%d\t%s" % (t["line"], t["first"]))
for n in strays:
    print("stray\t%d" % n)
PY
}

now_epoch=$(date -u +%s)
found_any=0
change_count=0

if [ "$CHECK" -eq 0 ]; then
  printf '\n%s\n' "openspec queue — why each in-flight change is still open"
  printf '%s\n\n' "-------------------------------------------------------"
fi

# An unavailable read is unavailable, never an empty queue: a failing CLI,
# non-JSON output, or a missing parser exits 2 with the cause on stderr.
# "(queue is empty)" is printed only after the JSON parsed and `changes` is [].
if json=$(openspec list --json 2>&1); then status=0; else status=$?; fi
if [ "$status" -ne 0 ]; then
  printf 'queue unavailable: openspec list --json exited %s\n%s\n' "$status" "$json" >&2
  exit 2
fi

# Parse with python3 (also used below for timestamps; task doctor checks it is
# present) so a missing jq does not silently degrade this to nothing.
if rows=$(printf '%s' "$json" | python3 -c '
import json,sys
try:
    d = json.load(sys.stdin)
except Exception as e:
    print("queue unavailable: openspec output is not JSON: %s" % e, file=sys.stderr)
    sys.exit(2)
if not isinstance(d.get("changes"), list):
    print("queue unavailable: openspec output has no \"changes\" list", file=sys.stderr)
    sys.exit(2)
for c in d["changes"]:
    print("\t".join([
        str(c.get("name","")),
        str(c.get("completedTasks","?")),
        str(c.get("totalTasks","?")),
        str(c.get("lastModified","")),
    ]))
'); then :; else exit 2; fi

if [ "$CHECK" -eq 1 ]; then
  read_count=0
  misplaced=0
  while IFS=$'\t' read -r name _; do
    [ -n "$name" ] || continue
    tasks_file="$CHANGES_DIR/$name/tasks.md"
    [ -f "$tasks_file" ] || continue
    scan=$(scan_tasks "$tasks_file") || { echo "queue unavailable: cannot read $tasks_file" >&2; exit 2; }
    read_count=$((read_count + 1))
    while IFS=$'\t' read -r kind lineno _; do
      [ "$kind" = stray ] || continue
      printf '%s:%s: Hold: outside every task; task spec:queue cannot show it. Put it in the task it stops.\n' \
        "$tasks_file" "$lineno"
      misplaced=1
    done <<< "$scan"
  done <<< "$rows"
  [ "$misplaced" -eq 0 ] || exit 1
  printf 'holds: ok (%d tasks.md read)\n' "$read_count"
  exit 0
fi

if [ -z "$rows" ]; then
  printf '  (queue is empty)\n\n'
  exit 0
fi

while IFS=$'\t' read -r name done total modified; do
  [ -n "$name" ] || continue
  change_count=$((change_count + 1))

  age_note=""
  if [ -n "$modified" ]; then
    mod_epoch=$(python3 -c "
import sys,datetime
try:
    s='$modified'.replace('Z','+00:00')
    print(int(datetime.datetime.fromisoformat(s).timestamp()))
except Exception:
    print(0)
" 2>/dev/null)
    if [ "${mod_epoch:-0}" -gt 0 ]; then
      age_days=$(( (now_epoch - mod_epoch) / 86400 ))
      [ "$age_days" -ge "$STALE_DAYS" ] && age_note="   [stale: ${age_days}d]"
    fi
  fi

  printf '  %-42s %s/%s%s\n' "$name" "$done" "$total" "$age_note"

  tasks_file="$CHANGES_DIR/$name/tasks.md"
  if [ ! -f "$tasks_file" ]; then
    printf '      (no tasks.md)\n\n'
    continue
  fi

  scan=$(scan_tasks "$tasks_file") || { echo "queue unavailable: cannot read $tasks_file" >&2; exit 2; }

  # Unchecked "- [ ]" and partial "- [~]" tasks only. [~] is ALWAYS a
  # caveat regardless of wording — it means a deliberate decision was
  # recorded, and that decision has to be propagated into the spec delta
  # before this change can be archived.
  caveats=0
  while IFS=$'\t' read -r kind lineno text; do
    marker=""
    case "$kind" in
      open|held)
        case "$text" in
          *'- [~]'*) marker="WONTDO" ;;
        esac
        [ -z "$marker" ] && marker="$(label_for "$text")"
        # The task's hold row says BLOCKED; its checkbox line does not say it again.
        [ "$kind" = held ] && [ "$marker" = BLOCKED ] && marker=""
        ;;
      hold) marker="BLOCKED" ;;
    esac
    [ -z "$marker" ] && continue

    # Trim leading list syntax and squeeze whitespace for a compact line.
    clean=$(printf '%s' "$text" \
      | sed -e 's/^[[:space:]]*- \[[^]]*\][[:space:]]*//' \
            -e 's/\*\*//g' \
            -e 's/[[:space:]][[:space:]]*/ /g')
    printf '      %-8s L%-5s %.104s\n' "$marker" "$lineno" "$clean"
    caveats=$((caveats + 1))
    found_any=1
  done <<< "$scan"

  if [ "$caveats" -eq 0 ]; then
    printf '      %-8s no halt/hold/deliberate marker in the open tasks\n' "ok"
  fi
  printf '\n'
done <<< "$rows"

printf -- '-------------------------------------------------------\n'
printf '  %d change(s) in flight.\n' "$change_count"
if [ "$found_any" -eq 1 ]; then
  printf '  Read the flagged lines before treating any fraction above as "almost done".\n'
fi
printf '\n'

if [ "$STRICT" -eq 1 ] && [ "$found_any" -eq 1 ]; then
  exit 1
fi
exit 0
