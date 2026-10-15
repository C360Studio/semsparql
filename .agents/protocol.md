# Shared work protocol (Claude, Codex, and Gemini)

Canonical. `CLAUDE.md` and `AGENTS.md` point to this file and carry the four gates (clarify, claim, merge, close)
inline; edit the protocol here only. Read it before taking, landing, or closing work; the pickup and handoff skills
read it. Adopted from SemEngine's `.agents/protocol.md` at `67d0ee9`; where a rule differs, this file is
authoritative for SemSparql.

State that all agents must see lives in the repository's tools, never in a prose document or an agent's private
memory. Each question has one home, and each home is a `gh` or `task` query. There is no `/tickets` state.

- **What is wanted, what kind, is it decided:** a GitHub issue with labels `type:epic`, `status:needs-decision`,
  `status:blocked`. `status:needs-decision` is the owner's docket; a ruling is posted as an issue comment and the
  label removed. `status:blocked` names its blocker in a comment.
- **An epic:** a tracking issue labeled `type:epic` whose body carries a task list of `#n` children. GitHub renders
  the progress; there is no separate epic document. Issue #1 (the bootstrap plan) is the first.
- **What gates a release:** a GitHub milestone. An epic sits in exactly one milestone.
- **Who has claimed what:** a **draft PR** on an agent-prefixed branch (`claude/…`, `codex/…`, `gemini/…`) opened at the
  start of the work, with `Closes #n` for a leaf issue or `Addresses #n` for an epic. No draft PR, no claim.
  Design-phase work claims the same way; the OpenSpec change is its first content commit. A stop-point goes in the PR
  description.
- **Target state, task truth, holds:** the OpenSpec change inside that PR; `task spec:queue`, run in the claim's
  worktree, shows its holds. The archive (`openspec archive <id>` + spec sync) is the landing PR's last commit,
  reviewed with the code. Every task can be ticked in or before the archive commit. No task may assert a post-merge
  fact ("CI green", "merge-ready") or wait on a step that follows the archive (the check of the archive, undraft, the
  final CI run, the merge): such a task strands the change, and those steps are recorded on the PR. A hold is written
  in the unticked task it stops, on any of its lines, as `Hold:` followed by what it waits for (an issue or PR number,
  or the owner's ruling). The queue reads every line of an unticked task and prints each hold from `Hold:` on. A hold
  in a heading or a paragraph outside every task does not show, and `task spec:check` fails on it.
- **Why:** an ADR, or the owner's ruling comment on the issue.

## Upstream boundary

SemSparql is a read-only consumer of SemEngine and SemVocab, pinned to their released tags. Agents mutate only this
repository: SemEngine, SemVocab and other sister repositories are read-only. A gap upstream (a missing export, a query
verb that cannot express a scan) is filed as an issue on the upstream repository, as issue #1 did with semengine#256
and semengine#259, and the dependent task here carries a `Hold:` naming it. It is never worked around with a local
copy, a fork, a `replace` directive, or a pseudo-version pin.

## Work lifecycle

- **Start:** `gh issue list --state open` · `gh pr list` (drafts are claims; skip them) · `task spec:queue` ·
  `gh run list --branch main --limit 3` · `gh issue list --label status:needs-decision`.
- **Clarify:** before claiming work, mutating state, or spawning role subagents, read any named issue, PR, or file so
  you never ask a question the repository already answers, then verify four boundaries are clear: (1) **Deliverable**
  (answer/audit, issue, OpenSpec design, docs-only PR, or code PR), (2) **Scope and non-goals** (in-bounds
  packages/files and what must not change), (3) **Definition of Done and verification** (observable completion criteria
  and `task` gates), and (4) **Stop boundary** (pause after inventory/design for an owner decision vs run through to a
  ready PR). When a prompt leaves any of the four ambiguous, stop first, propose a concise 3–4 bullet recommended
  Definition of Done the owner can approve or adjust in one reply, and ask at most 1–3 targeted questions; once set,
  run autonomously to that stop boundary without pausing on routine steps.
- **Take work:** an unclaimed issue, then a dedicated worktree on an agent-prefixed branch (`claude/<topic>`,
  `codex/<topic>`, `gemini/<topic>`; the prefix names who claimed it), then push, then a draft PR with `Closes #n` or
  `Addresses #n`, then work. One claimed PR owns one worktree. When multiple agents share a host, the primary checkout
  is discovery-only and no agent commits from it. Immediately before every commit and push, verify that the worktree's
  current branch is the draft PR head; a mismatch stops the operation. The prefix says who claimed the work, never
  whether that agent's worktree is idle.
- **Worktree hygiene:** for standalone CLI sessions, the claim's worktree lives at a durable sibling path
  (`git worktree add ../semsparql-wt/<branch> -b <branch> origin/main`), never under `/private/tmp`, which a reboot
  purges; Enjoy desktop sessions use `move_to_worktree`, which creates `.worktrees/<branch>` (ignored in `.gitignore`
  alongside `.enjoy-logs/`). Run `git worktree remove` when the PR merges. Heavy local gates run one agent at a time on
  a shared host: worktrees fix the git collision, not the CPU one. CI is the arbiter; a local red under contention is
  not a finding.
- **File:** a defect in code in this repository that the change does not fix is always filed as an issue. A defect is
  a behaviour that breaks what the code promises: a contract line (a spec requirement, a package `README.md`), a doc
  comment, or a reasonable reading of the code. It is filed whatever found it: a test, a review, or a coverage pass.
  Check for an existing issue first (`gh issue list --search`). A defect that a pull request can retire on its own is
  filed alone, so `Closes #n` can close it. When the defect's code exists only on a pull request's branch, the issue is
  labelled `status:blocked` with that pull request named as its blocker, and the pull request body links it as a
  follow-up with no closing keyword (`Follow-up after this pull request merges: #n`): a `Closes`, `Fixes` or `Resolves`
  there would close the issue when the pull request merges. The label comes off when that pull request merges,
  removed by whoever merges it or by the next session that reads the issue. A defect in SemEngine or SemVocab is filed
  upstream ("Upstream boundary" above). Filing tracks a defect; it does not satisfy a guarantee the pull request
  claims, and a finding about the defect keeps the severity the reviewer contract gives it.

  Route every other finding before opening an issue. A defect is not a residual: a residual is a cost or loose end of
  a decision this change just made deliberately, not code that behaves wrongly, and it belongs in a doc comment at
  the line or in the change's `design.md`. A consequence an owner ruling already states needs nothing; the ruling is
  the record. An unmeasured cost becomes a *Declared cost* section in the design. Of the rest, only an architectural
  finding (it crosses files, would need its evidence re-collected to re-derive, or changes what someone should not do
  next) becomes an issue. Ask the owner before filing when placement is a genuine scheduling call.
- **Severity of unchosen conditions:** a finding whose failure needs a condition the code does not choose (a library's
  timing or ordering, a rare network or operating-system state, an injected mid-operation fault, or a call sequence
  only a test makes) is `BLOCKING` or `HIGH` only when it names a real-world trigger; otherwise it is `MEDIUM` at most,
  and its fix is a doc line that stops promising the behaviour or a declared degrade, never new mechanism (reviewer
  contract § Finding and verdict format).
- **Land:** implementation review (another agent's for a code pull request: "Cross-agent review" below), then, for a
  documents-only change, the owner-run cross-agent round where the owner asks for it, then fixes and re-review, then
  archive as the final content commit, then a narrow reviewer check of the archive/spec sync (any agent's reviewer),
  then undraft, then CI green on a head that is up to date with `main` and with **no known flake open** ("Known flakes"
  below), then `task merge:check -- <n>` immediately before merging, in a shell where `GITHUB_ACTIONS` is not set, then
  squash merge. A green run while a known flake is open is not a fix: a re-run or a new push only rolls the dice again.
  Fix the flake; there is no other way past it. A correction after archive re-enters reconciliation and final review; no
  later content commit bypasses the archive/spec-sync check. State `implemented-by:` in the PR body, naming the
  implementing agent with the word `claude`, `codex`, or `gemini` beside the model or persona (`implemented-by: gemini
  (semsparql-developer)`). Bring a pushed branch up to date by merging `origin/main` into it; do not rebase or
  force-push it. A review record names the commit it read, and a rebase leaves that record pointing at a commit the
  branch no longer has. The squash merge keeps `main` linear either way.
  The developer's notes for an implementation review, the choices a task made that its design does not spell out, are
  a file per task, `openspec/changes/<id>/notes/<task>.md`, with a one-line pull request comment that links it
  (developer contract § Handoff); they archive with the change.
- **Cross-agent review:** on a code pull request the implementation review, and the re-review of a fix that changes
  code, are done by an agent that wrote none of the commits under review, as the pull request's `implemented-by:` line
  records; the re-review of a fix that changes documents only, and the check of the archive/spec sync, are any
  agent's reviewer's. The rule is enforced for code and waived for a documents-only pull request: every file the pull
  request changes is a Markdown file or is under `openspec/`, and none is a role adapter under `.claude/agents/` or a
  canonical role contract under `.agents/contracts/`. Any other changed file makes it a code pull request: a `.go`
  file; `go.mod`; a script; `Taskfile.yml`; the CI workflow; `package.json`; `.claude/agents/*`; `.codex/agents/*`;
  `.agents/contracts/*`. If multiple agents wrote commits, the owner names the reviewer on the issue. A pull request no
  agent wrote (a dependency bot's) is reviewed by any agent's reviewer, and the record names which. The check below
  provides for this only when GitHub reports the author as a bot. A code change the owner would write by hand is given
  to an agent session to make: the commits are then that agent's, `implemented-by:` names it truthfully, and another
  agent reviews. A code pull request by a user with no `implemented-by:` line naming an agent fails the check once it
  is not a draft. A documents-only pull request keeps SemSparql's own reviewer. The implementing session may run its
  own reviewer as it works; those reviews find defects early and are not the gate. An agent does not start another
  platform's agent, so the pull request carries both halves:
  - The implementer asks with a PR comment headed `Review request`. It names the kind of review, the commit to read,
    the diff range, the implementer's `task verify` result on that commit (run on a clean tree), and the issue or
    ruling the change answers. It does not say what the reviewer should conclude.
  - The reviewing session answers with a PR comment headed `Review record`: its reviewer's report as written, in the
    reviewer contract's format, not a summary. It names the commit read, what was run, and what could not be run.
  - Evidence comes from the tree and the reviewer's own runs, not from the implementer's account. For anything
    `task verify` runs, the evidence is the reviewer's run on the named commit; without one, the implementer's result
    is recorded as reported by the implementer, never as passed. The implementer does not wait on or watch a CI run
    before asking, and the record does not rest on one: the CI run before merge is the check. A check the reviewer
    could not run and CI does not run is recorded as not run.
  - A record covers the commit it names. A later content commit, one that changes the pull request's own diff against
    `main`, needs a re-review, and the archive commit needs the archive check. A later commit that changes code goes
    back to the reviewing agent for re-review. A later commit that changes documents only, and the check of the
    archive/spec sync, are reviewed by any agent's reviewer; the record names which. A merge of `origin/main` is not a
    content commit when `main` changed no file the pull request changes: the record carries over, and CI on the merged
    head is the check. When `main` changed a file the pull request also changes, that file needs a re-review.
  - The reviewer does not write on the branch. Findings go back to the implementer, who keeps write ownership. If
    the agents disagree on a finding, it goes to the owner on the issue, labelled `status:needs-decision`.
  - The implementer writes one `reviewed-by:` line in the PR body, beside `implemented-by:`, once the reviewing
    agent's newest review record approves; it names that agent with the word `claude`, `codex`, or `gemini`, beside the
    model or persona taken from the record.
  - The check: `scripts/merge-check.sh`, run by CI's `merge-check` job and by `task merge:check -- <n>`, reads the
    pull request's description and its list of changed files. On a code pull request the description must have
    exactly one line starting `implemented-by:` that names at least one agent (not read when GitHub reports the
    author as a bot) and exactly one line starting `reviewed-by:` that names exactly one agent: one that
    `implemented-by:` does not name when `implemented-by:` names a single agent, or any of the three when it names more
    than one or the author is a bot. A line starts in the first column; an agent name is `claude`, `codex`, or `gemini`
    in any letter case, not part of a longer word such as `codex2` or `gemini2`; nothing else in either line is read. On
    a draft the findings are printed and fail nothing; on a pull request that is not a draft they fail `merge-check`,
    and `Required` with it. Marking a draft ready starts the CI run that applies the check; editing the description or
    adding a comment starts no run, so after the edit run the failed `merge-check` job again.
  - What stays review only: whether the lines are true (the owner and the agents use one GitHub login, so the script
    cannot tell who wrote a commit, a description or a comment); that a review record exists, the commit it read,
    and the re-review after a later content commit; the record's verdict, scope and content; that the owner named the
    reviewer when multiple agents wrote commits; a pull request that changes the script, the workflow or their tests,
    which runs its own copies; and the moment after marking ready before the new run registers, which the Land steps
    of waiting for CI and `task merge:check -- <n>` cover.
- **Known flakes:** a known flake is an open issue labelled `class:flake`. The label is for a failure that a pull
  request in this repository can end: a test or check that passes and fails on the same tree. A network fetch that
  did not answer is not one. Record it as a comment on the pull request it hit, with the run's link, and re-run when
  the remote answers; the second failure of the same fetch gets an ordinary issue, without the label. A red you
  cannot explain by your diff is filed before the next push. While a known flake is open, CI's `merge-check` job,
  and `Required` with it, fails on every pull request except one whose description closes every open flake
  (`Closes #n` for each). No comment or label on a pull request exempts it. A pull request that closes a flake shows
  the reproduction: the command, and how often it failed before the fix and after. A flake that comes back after its
  fix reopens its issue. A property-based test that fails on one run and passes on the next is a known flake too: the
  issue records the seed, and the fix keeps the failing input as a named test (`docs/testing.md`, "Running and
  replaying a Rapid test").
- **Close:** the squash merge of a PR that declared `Closes #n` at review time closes that issue; the merge is the
  authorization. The declaration must predate the review rounds that cover it. A PR that only `Addresses` an epic
  closes nothing. A close with no merged PR behind it (duplicate, stale, fixed elsewhere) takes the owner's word on
  the issue itself; an approval of adjacent work (a PR, a review round, a design, a waiver) never widens into a
  close, and a bare "approved" closes nothing. A `class:flake` issue is closed, or its label removed or renamed, only
  by a merged fix or on the owner's word: any of the three lifts the `merge-check` stop for every pull request.

## Verification

CI has three jobs. `verify` runs `task verify`, the same commands as a local run. `merge-check` runs
`scripts/merge-check.sh`: it fails while a known flake is open that the pull request does not close, fails unless
the rules on `main` require an up-to-date head, and fails a code pull request that is not a draft unless its
`implemented-by:` and `reviewed-by:` lines name the agents as "Cross-agent review" says. It reads GitHub, so it is
not part of `task verify`. `required` fails if `verify` or `merge-check` failed, is missing, or was skipped or
cancelled; it is the check the ruleset on `main` requires. Gate selection and evidence recording are in
[semsparql-preflight](skills/semsparql-preflight/SKILL.md).

There is no program baton document.
