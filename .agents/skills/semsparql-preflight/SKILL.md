---
name: semsparql-preflight
description: >-
  Select and run the existing SemSparql verification gates for a concrete diff and record their evidence.
  Use before implementation pushes or when assessing local readiness.
---

# Verify a SemSparql change

The [shared protocol](../../protocol.md) owns claims and landing. Read it first. This skill selects existing checks and
records evidence; it grants no merge, tag or cleanup authority.

## Establish the diff and its owner

Work in the claim's worktree. Verify its branch, HEAD, dirty/untracked files, upstream and PR target before
choosing checks. Compare the committed diff against the actual PR base, including a non-default target in a
stack; also inspect the staged and unstaged work. Do not assume the discovery checkout's local `main` is current.
Before every commit or push, verify the branch still matches the claimed PR head.

## Select the existing gates

One Task entrypoint serves local work and CI. `task --list` shows what exists; inspect the `Taskfile.yml` and the
workflows under `.github/` when exact coverage matters. A task name or old successful log does not prove parity
with current CI.

| Task | What it does |
| --- | --- |
| `task doctor` | Reports tool versions and Docker reachability against their pins; starts no workloads |
| `task fmt` / `task fmt:check` | Formats Go sources / fails if any Go source needs formatting |
| `task tidy:check` | Fails if `go.mod` or `go.sum` is not tidy |
| `task cleanup-roots:check` | Fails if any `*_test.go` passes `context.Background()` or `context.TODO()` to `Stop`/`Close`/`Terminate` |
| `task build` / `task vet` | Builds all packages / runs `go vet` |
| `task lint` | Runs pinned `revive` plus `scripts/lint-test-ports.sh` and its fixture test |
| `task vuln` | Runs pinned `govulncheck` over all packages |
| `task test:unit` | Runs unit tests once with `-race` at one CPU |
| `task test:repeat` | Runs unit tests five times at one CPU without `-race`, shuffled (`-- <pkgs>` to focus) |
| `task test:integration` | Runs `-tags=integration` tests, writing `coverage/integration.coverprofile` and `coverage/integration.tree` (`-- <pkgs>` to focus) |
| `task cover:check` | Enforces 80% statement coverage on configured targets in `scripts/cover-check.sh` and checks `coverage/integration.tree` |
| `task spec:check` | Validates all OpenSpec changes and specs strictly, and fails on a `Hold:` written outside every task |
| `task spec:queue` | Shows each in-flight OpenSpec change with its holds and blocking conditions |
| `task docs:check` | Lints Markdown with `markdownlint-cli2` |
| `task merge:check -- <n>` | Fails while an open `class:flake` issue is not closed by PR `n`, or while the rules on `main` do not require an up-to-date head, or while PR `n` is a code pull request, not a draft, whose `implemented-by:` and `reviewed-by:` lines do not name the implementing and reviewing agents; reads GitHub |
| `task verify` | The checks above except `doctor`, `fmt`, `spec:queue` and `merge:check`, cheapest first (`test:repeat` last); prints each step's time; fails on tracked-file change |

CI has three jobs: `verify` runs `task doctor` and `task verify`; `merge-check` runs `scripts/merge-check.sh` for the
pull request (on a push to `main`, only its up-to-date half); `required` fails if `verify` or `merge-check` failed, is
missing, or was skipped or cancelled.

Select by what the diff changes:

- **Documentation, skill or contract instructions only:** `task docs:check`, `task test:unit` (for
  `TestAgentSetupParity`), and `git diff --check`; add `task spec:check` when `openspec/` changed.
- **An OpenSpec change:** `task spec:check`, then read `task spec:queue` in this worktree.
- **Go behavior:** `task test:unit`, `task vet`, `task lint`, `task build`; focused tests first during iteration.
- **Docker-backed behavior (any `*_integration_test.go`):** `task test:integration -- <pkgs>` to focus, then
  `task cover:check`.
- **Dependencies or `go.mod`:** `task tidy:check` and `task vuln` in addition to the Go gates.
- **Before any implementation push:** `task verify`.
- **Immediately before merging:** `task merge:check -- <n>`, in a shell where `GITHUB_ACTIONS` is not set. It sees a
  flake filed, a closing line removed, or a `reviewed-by:` line added, since the pull request's last CI run.

Do not hand-run a narrower command in place of a gate CI runs: the task owns flags and pins.

## Protect shared test infrastructure

Serialize heavy work on a shared host. Docker inspection is diagnostic:
container age, a timeout signature, or a name pattern does not establish that a resource is abandoned or that a
failure is infrastructure-only. Preserve other sessions' resources. Cleanup is limited to an identified completed or
abandoned run this session is authorized to clean up; host-wide pruning and stopping an unknown port holder are not
preflight steps.

## Report evidence and remaining gates

Record the tested HEAD (or dirty snapshot), exact command, exit status, assertions/tests actually exercised,
and log/artifact location. A log under a path git ignores (such as `coverage/`) is on this host only: say so,
and put the output a reader needs in the PR. Separate failure, skip, no selected tests and compilation-only results.
When the implementation changes, older green results remain evidence for the older revision. Preserve the command's
failure status when filtering output; do not infer success from a quiet log.

Resolve failures from evidence; a successful re-run is not a fix for a flake. A red that your diff does not explain
is filed as an issue before the next push, labelled `class:flake` when it is a test or check that passes and fails
on the same tree. A network fetch that did not answer is not a flake: follow the protocol's "Known flakes" item. An
open flake is fixed; nothing else gets a pull request past it, and `merge-check` fails every pull request that does
not close it. Long or paid runs need the role contract's active progress checks and bounded stopping behavior.

After local verification, assess the PR's current-head hosted checks and the protocol's review/archive gates.
Local green is not merge authorization. Follow existing user authorization without asking for it again, while
preserving all outstanding required gates.
