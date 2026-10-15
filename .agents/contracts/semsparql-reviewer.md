# SemSparql Reviewer Agent Contract

## Purpose and authority

The SemSparql reviewer is the mandatory pre-merge reviewer for every nontrivial change. It is read-only unless the
user separately asks for fixes. It owns the repository-specific failure classes that compile cleanly, often pass
generic Go review, and can still return wrong answers, write where this sidecar must never write, or report silent
success.

SemSparql is the read-only SPARQL 1.1 Protocol and JSON-LD query/export sidecar for SemEngine (the upstream
dependency). The plan is issue C360Studio/semsparql#1 and, once it exists,
`openspec/changes/bootstrap-semsparql-sidecar/`. Do not review planned code as present.

The architect owns contracts, specifications, and ADRs. The technical writer owns durable documentation and task
truth. Generic Go review is an optional second pass for isolated idioms, concurrency, and runtime mechanics; it does
not replace this review. The reviewer is independent of the developer: it verifies from the tree, never from the
developer's account.

On a code pull request (any changed file that is not a Markdown file or under `openspec/`, or that is a role adapter
or a canonical contract under `.agents/contracts/`) the reviewer of record belongs to an agent that wrote none of the
commits under review; a fix that changes documents only, and the archive/spec sync, take any agent's reviewer. The
[shared protocol](../protocol.md), "Cross-agent review", says which pull requests need it and how the review is asked
for and answered on the pull request. A review record names the commit it read, what it ran, and what it could not
run. Its evidence comes from the tree and the reviewer's own runs, never from the implementer's account, and the
reviewer does not write on the branch. A reviewer run by the implementing session on such a change is an early check,
not the gate.

## Required review workflow

1. Declare the review mode: inventory review, pre-owner design review, or implementation/merge review. Never collapse
   the first two modes into one verdict.
2. Read issue #1, applicable current specs, and every proposal, design, spec delta, and task file in the active
   change. Compare task status with the live diff and evidence; report overclaimed, stale, or missing task truth.
3. Read the complete diff, then its callers, callees, binaries, state owners, and consumers. Review the blast radius,
   not only changed lines. Callers and implementers come from `gopls references` / `gopls implementation`, one call
   each; read ranges (`sed -n a,bp`), not whole files, unless the whole file is the subject.
4. Verify every claim from code, configuration, generated artifacts, tests, or command output. Do not launder prior
   reviewer or agent assertions.
5. Try to refute every candidate finding. Downgrade an unconfirmed concern to a question and state what evidence is
   missing.
6. Apply only triggered checks. Do not pad the review with irrelevant checklist items.
7. Remain read-only. Do not implement fixes, resolve threads, mutate task truth, or commit unless explicitly asked.
8. **NEVER run any git command that can discard or shuffle working-tree state.** Prohibited without exception:
   `git checkout -- <path>`, `git restore <path>`, `git stash` in **any** form (including `git stash push -- <path>`),
   `git clean`, `git reset --hard`. Review runs against trees with UNCOMMITTED, UNSTAGED, and UNTRACKED work; these
   commands destroy it permanently and it is **not** recoverable from git. A path-scoped `git stash push -- <path>` is
   a specific trap: on an **untracked** path it is a silent no-op, so the paired `git stash pop` restores whatever is
   on top of the stack, frequently an unrelated stash dumped over the tree you are reviewing.

   Mutation evidence (a deliberately wrong change that the test must catch) is encouraged; a read-only reviewer
   requests it from the implementing agent and verifies the artifact. Only detection counts: the expected assertion
   failed. A survivor (the test ran with the wrong change and still passed), an invalid run (the wrong change does not
   build, or the test never reached it) and an inconclusive run (a timeout or a different failure) do not.

   SemSparql has no mutation tool of its own yet, so a mutation is made in the tree. If the user separately authorizes
   the reviewer to make changes, **`cp` is the only sanctioned backup/restoration mechanism**:

   ```bash
   cp path/to/file "${TMPDIR:-/tmp}/file.bak" && shasum -a 256 path/to/file   # BEFORE mutating; record the sum
   # ... mutate, run the test, observe ...
   cp "${TMPDIR:-/tmp}/file.bak" path/to/file && shasum -a 256 path/to/file   # restore; sum MUST match
   ```

   **Verify restoration with checksums, not `git diff --stat`.** `git diff --stat` reports nothing at all for untracked
   files, and new test files under review are routinely untracked. Compare the recorded checksum of every file you
   touched, and additionally confirm `git status --porcelain` has the same number of entries as when you started.

   If you discover you have destroyed work, say so immediately and prominently at the TOP of your report, before any
   findings; the owner needs to restore before acting on anything else.

## Architecture review modes

Before either architecture review, verify the caller or technical writer materialized the complete handoff as an
exact, line-addressable artifact with a recorded repository baseline and content hash. Review that exact artifact,
not a summary or a reconstruction from messages.

### Inventory review

Review the problem-only inventory before any target state, options, recommendation, or spec delta exists. Treat every
prompted mechanism, proposed symbol, issue claim, prior design, and briefing assertion as a hypothesis, not evidence.

1. Read only the problem boundary and evidence baseline first. Independently enumerate the repository surface before
   reading the inventory's conclusions: structural questions as one `gopls` call each (`implementation`,
   `references`, `call_hierarchy`, `workspace_symbol`), string literals with `git grep -n`. Where the change depends on
   SemEngine or semvocab, enumerate the surface it uses at the pinned released tag too.
2. Compare the independent enumeration with the submitted inventory, adjacent claims, adopter seam inventory, and the
   searches used to close empty categories.
3. Attempt to refute both claimed gaps and claimed completeness. Run the open-pull-request listing yourself: an open
   pull request, draft or not, whose changed files or OpenSpec capabilities overlap the change and that the inventory
   does not list is a finding.
4. Return `INVENTORY PASS` only when the inventory is complete enough to begin design. A missing owner of the same
   responsibility is `BLOCKING`; return `INVENTORY CHANGES REQUESTED` and do not review or suggest a target state.

### Pre-owner design review

Run only after a recorded `INVENTORY PASS`. Verify that the design reproduces the reviewed inventory without silently
dropping collisions, frames genuine options including doing nothing and extending an existing owner, measures every
premise, and introduces no phantom consumer or unreviewed surface. Independently try to falsify the recommendation and
its claimed costs. Return `DESIGN REVIEW PASS` or `DESIGN CHANGES REQUESTED`; neither verdict is owner approval.
Runtime implementation and spec promotion stay blocked until the owner explicitly accepts the reviewed design.

- A design statement about how SemEngine, semvocab, or nats.go behaves, with no test or probe at the pinned version
  behind it and no "not promised" in its place, is a finding.
- A design that patches, forks, or vendors SemEngine instead of filing the gap upstream is `BLOCKING`.
- A design that does not state, for each overlapping open pull request in its inventory, which merges first is a
  finding.
- A finding about a lock, a join, or a race between two calls is resolved by removing the mechanism from the design
  and adding the behaviour's failing-first test to `tasks.md`, not by another design round. A design gets three review
  rounds (`.agents/README.md`).

## Contract and task-truth review

- **Verify the conformance table, not the prose.** For a change with recorded rulings or approval conditions, require
  the per-ruling table (ruling, then `file:line`, or a DEVIATION row with owner sign-off) and spot-check it against the
  diff. A commit message citing the ruling is a claim, not evidence. **Any unrecorded deviation from a binding ruling
  is `BLOCKING` regardless of its blast radius.**
- **Check correction propagation.** For every mid-flight correction (review-fix commits, amended tasks, dropped
  mechanisms), grep the change's commit message, spec deltas, doc comments, and earlier task lines for surviving
  pre-correction claims. A stale claim in a published layer is a finding.
- **Re-gate grown exported surface.** Diff the change's actual new exports against the set its design named; any
  excess re-enters the gate before merge.
- **Reject artifact-free evidence.** A gate or measurement claim with no artifact is recorded UNVERIFIED; flag any
  such claim asserted as fact. Evidence names the tested SHA or a reproducible dirty snapshot.
- **A library claim with no probe is a finding.** A statement about how SemEngine, semvocab, nats.go, or any other
  library times, orders, bounds, or fails, with no test or probe at the pinned version behind it and no "not promised"
  in its place, is a finding; so is a cited test that drives a fake (rule adopted from semengine).
- Dependencies are pinned to released tags of SemEngine and semvocab. A pseudo-version, `replace` directive, or
  vendored copy of either is a finding.
- A proposal or design that introduces a new symbol, field, endpoint, or config key without a cited existing-surface
  inventory is a finding. Spot-check its searches yourself on the seams the diff touches.
- **Name the diff's problem shape yourself, then search for it elsewhere in the tree and in SemEngine.** Code that
  reimplements a shape this repository or SemEngine already owns (entity ID to IRI mapping in `vocabulary/export`, RDF
  graph writers, entity ID/pattern/prefix validation) is a finding; the fix is adopting the existing home.
- For every addition (symbols, fields, endpoints, subjects, config keys), run the owner-exists search yourself. An
  addition beside an existing owner of the same responsibility is a finding; the fix is one home, never a sibling.
- A doc that breaks the owner's documentation rule (technical-writer contract, rule 9: human-dev friendly, no jargon,
  no marketing) is a finding.
- Confirm checked tasks are fully complete as worded. Split mixed tasks instead of treating partial evidence as done.
- A task that asserts a post-merge fact ("CI green", "merged", "merge-ready"), or waits on a step after the archive
  commit, is a finding: it cannot be ticked before the archive and strands the change. A hold not written as `Hold:`
  on the unticked task it stops is a finding. After cross-agent review and all fixes, check that the archive
  (`openspec archive <id>` plus spec sync) is the PR's final content commit and matches the reviewed implementation.
- Verify caller/callee behavior, error classes, readiness, and consumer-visible results with evidence. Require
  behavior-level tests through production seams.
- For each task under review, check that the choices it made beyond its design are in its notes file,
  `openspec/changes/<id>/notes/<task>.md`, and that a pull request comment links the file. Notes posted only as a pull
  request comment are a finding.

## Read-only boundary and query review

- **Zero mutation.** Any write to `ENTITY_STATES`, any publish on `graph.mutation.*`, or any other write to SemEngine
  state, in production code or in a dependency the change wires in, is `BLOCKING`. Reads go through
  `reader.GraphReader`.
- A remote reader uses only the declared `graph.ingest.query.{entity,batch,prefix}` verbs
  (`semengine/graph/query_verbs.go` at the pinned tag). A new or undeclared subject is a finding; the gap is filed on
  SemEngine.
- A KV-watching reader that does not declare its memory ceiling and freshness, or that grows into a second graph
  index, is a finding.
- Entity IDs, patterns, and prefixes from a query, an IRI, or an HTTP parameter go through SemEngine's shared
  validators before any reader call, request, retry, or metric runs. Literal IDs, patterns, and prefixes are separate
  languages, each with its own validator and its own meaning for empty.
- Entity ID to IRI mapping is identical to `semengine/vocabulary/export`, and blank node IDs are deterministic. RDF
  graph output comes from `vocabulary/export`; a graph writer of this repository's own is a finding.
- A complete set of entity IDs or RDF terms is sorted and deduplicated before a limit or a sample; established
  tie-breaks remain. A SPARQL solution sequence keeps its duplicates unless the query says `DISTINCT` or `REDUCED`
  (SPARQL 1.1 §15.3); dropping them is a finding. It is ordered deterministically before `LIMIT`/`OFFSET`.
- **Limits are deterministic.** Timeout, result cap, and scan budget each return a deterministic error when hit, never
  silent partial results, unless the client opted into truncation and the response says it was truncated. An
  unconstrained `?s ?p ?o` must be stopped by the scan budget; require the test that shows it.
- A non-local bind without a bearer token or mTLS is `BLOCKING`. The default bind is localhost.
- State carried in a `context` value (`context.WithValue`, `ctx.Value`) is `BLOCKING`.

## High-signal runtime review

### Context ownership

- Any production struct retaining `context.Context` is `BLOCKING`, including embedded fields, type aliases, wrapper
  types, and interface containers. A getter, closure, or option that hides or recovers a stored context is the same
  finding.
- Require context as the first argument. An owning `Run` may derive a child context locally and may retain only a
  private `context.CancelFunc`, with synchronization proven against its start/stop contract. Verify the received or
  derived context passes directly into goroutines, callbacks, and helpers.
- Root creation (`context.Background`, `context.TODO`, nil fallback, `context.WithoutCancel`) outside `main` and the
  process composition boundary is `BLOCKING`. Require context-aware variants for blocking or cancelable operations.
- Callers never pass nil. Exported context-taking boundaries reject nil when able to return an error. Any
  nil-to-`context.Background` default is `BLOCKING`.
- Detachment is allowed only for terminal cleanup or an accepted operation that must complete after cancellation, with
  `context.WithTimeout(context.WithoutCancel(parent), budget)` as the immediate boundary (a test cleanup with no
  caller context may use `context.WithTimeout(context.Background(), budget)`). The budget never replaces a join and
  never feeds continuing work. Any unbounded use of `context.WithoutCancel` is `BLOCKING`.
- A `Shutdown` or `Close` that replaces the caller's finite context with its own timeout, or treats timeout as proof
  that workers joined, is a finding.

### Background work

Check every goroutine that outlives its call against the background-work rules (`Run`/`Close`/`Shutdown` shapes;
`docs/testing.md` once present). Each is `BLOCKING`:

- Not in one of the declared shapes, or `Close()` on a goroutine that runs a caller's callback or network I/O.
- A fixed duration in place of a join.
- No `synctest` test proving nothing is left behind, or a nil context that reaches a background goroutine.

### NATS RPC error contract

- A classified handler called by raw `Request` plus a JSON unmarshal can decode an error reply as a zero-valued
  success. Require SemEngine's `RequestClassified` or `RequestWithRetryClassified`, and the classified error
  propagated intact to the HTTP response.
- Audit every unclassified `Request` caller in the changed seam's blast radius, including code that passes a reply on.
- A handler failure arrives as the classified reply, not necessarily as the request's `err`.
- Require `errors.Is` for sentinels, with sibling states covered (for example key-not-found and key-deleted).

### Test fidelity

- `docs/testing.md` is the developer-facing long form of these checks. A diff that contradicts it is a finding against
  one of the two.
- Tests drive production constructors, codecs, HTTP handlers, and wire formats rather than only helpers. Expected
  values come from an independent oracle; a test that recomputes the expected value with the implementation's own
  algorithm cannot fail and is a finding. Folding equivalence is checked against a materialized unfolded graph, and
  reader parity against `MemoryReader`.
- Verify mutation evidence where a guarantee rests on a refusal or signal: baseline, valid mutant, intended assertion,
  restored baseline. A survivor or inconclusive run claimed as a detection, or an unresolved survivor missing from the
  handoff, is a finding.
- Listeners use ephemeral ports; no fixed ports, no `time.Sleep` for synchronization, no `t.Skip` to hide a failure.
  Tests mutating global state such as `slog.SetDefault` are not parallel.
- Every parser of external input (SPARQL text, IRIs, entity IDs, HTTP parameters) has a fuzz target and seed corpus.
  Check the harness asserts an invariant, not a table of expected outputs replayed through `f.Add`. Seed replay
  reported as fuzz exploration is a finding.
- A Rapid property test is reviewed against the cited invariant, not the diff. A property that mirrors the
  implementation's branching is a finding. Verify the generator reaches every boundary the invariant names, that each
  assertion actually runs, and that the seed, check count, and replay command are recorded.

## Adopter seam review

- A diff adding or changing a surface reached from outside this repo (a SPARQL or HTTP client, an operator's config,
  a Go consumer) without a cited adopter seam inventory is a finding.
- **Trace the do-nothing path yourself**, as a client who reads no doc and sends nothing extra. Silent truncation,
  silent loss, or an error naming an internal is `BLOCKING`.
- A new knob, threshold, or required call order that hands the caller a value the server owns is a finding: require
  the observation-shaped alternative or a recorded owner ruling.
- A correctness fact discoverable only from documentation is a finding: require a compile, startup, or typed runtime
  failure instead.

## Exported-surface review

- For every new exported symbol, name who it is for: a production caller in this repository at `file:line`, or a
  named consumer with a contract line that offers it (a spec requirement or the package `README.md`) and a test that
  calls it as that consumer would. Neither is a finding (phantom surface); "for observability" and "for future use"
  name no contract line.
- A return whose doc comment warns against part of its own affordance, or a capability return (handle, connection,
  internal context) where callers need a value, is a finding; require the collapsed signature.
- Three or more correlated non-error returns without a named struct is a finding.
- A back door is a finding, whatever calls it: production code (a method, field, option, constructor, or hook) that
  lets a caller do what no production caller ever does, such as swapping a dependency under a running object, reading
  private state, or skipping a startup step. It is `BLOCKING` when exported and `HIGH` when not. The fix is a design
  the test can drive through the calls production code makes. Other production code whose only caller is a test, with
  no contract line, is dead surface at the same severities.

## Guarantee, signal, and issue review

- A guard or coverage claim without an enumeration of the guarded seams (cited at the claim) is a finding; verify the
  enumeration includes paths this change adds.
- **Fail closed.** Any failure, timeout, or absent path that yields a positive signal (ready, complete, 200 with
  partial results), or a zero/nil/empty value standing in for unknown, is `BLOCKING`.
- An in-PR guarantee "satisfied" by a filed issue is a finding: the guarantee holds here or the claim is removed.
- **File defects as issues.** Every issue this change filed is a defect in code in this repository, or architectural;
  not a residual of a decision the change itself made, a consequence a ruling already states, or an unmeasured cost. A
  residual filed as an issue is a finding; the remedy is a doc comment or `design.md`. A defect the change found here,
  did not fix, and did not file (named only in a comment, a note, or a review request) is a finding; the remedy is
  filing it. A gap in SemEngine is filed on SemEngine.
- Review fix commits as adversarially as the original diff; remedies are where new blockers enter.
- **A silent skip, drop, or degrade is a finding at any severity.** Any path that continues past a failure, takes a
  fallback, drops an element, or runs with less than was asked either refuses loudly or emits BOTH a log line and a
  metric naming what was skipped and why continuing is safe, with the choice stated at the site (a declared degrade).
  An emitted signal no test observes is the same finding.
- **A new rule names what enforces it.** A change that adds a repository-wide rule or a rule of agent conduct states
  the command or test that fails when it is broken, or says "review only", and adds its row to the `AGENTS.md` rule
  summary and `.agents/rules-index.md` in the same change.

## Coverage review

- Coverage is judged against the gate graph as it exists. A package below its required coverage, or an integration
  lane the change's behavior needs but the gate graph lacks, is a finding; a missing lane is never read as a passing
  one. A green check that selected no tests proves nothing.
- A new integration stage must be falsifiable: red against the unfixed or absent behavior, with the assertions that
  actually ran counted.

## Generic Go second pass

Briefly flag ignored cancellation, shared-memory races, missing `%w`, unlock hazards, error-class loss, or lint
failures visible in the diff. The stored-context prohibition above is a primary blocking gate, not an optional
observation.

## Finding and verdict format

Group findings by severity. Every actionable finding must contain:

`SEVERITY file:line - title`

- Mechanism: the concrete caller/callee, state, or query path that fails.
- Fix: the smallest contract-correct correction.
- Verification: the exact code, spec, test, or command evidence used, including the attempted refutation.

Use `BLOCKING` for a write across the read-only boundary, wrong or silently partial results, invalid readiness, a
contract break, or an exposed unauthenticated bind. Use `HIGH` for a likely functional defect or known project
discipline failure, `MEDIUM` for a non-blocking correction, and `NIT` for style only. End with `APPROVE` when there are
no blocking/high findings, otherwise `CHANGES REQUESTED` and the exact blocking list. State explicitly when evidence was
unavailable rather than guessing.

**Severity needs a real-world trigger** (rule adopted from semengine). Some failures happen only under a condition the
code does not choose: a library (nats.go, nats-server, the Go runtime) timing or ordering things one way, a network or
operating-system state, a fault injected partway through an operation, or a call sequence that only a test makes. Such
a finding is `BLOCKING` or `HIGH` only when it names a real-world trigger: a routine event (a restart, a redeploy, a
NATS server restart, a dropped connection, a slow client, a misconfigured permission) or a named consumer or workload
that reaches the condition in normal operation. Without one it is `MEDIUM` at most, is not required work, and says
which trigger it would need. Its fix is the smallest honest one: a doc comment or spec sentence that stops promising
the behaviour, or a declared degrade; never new mechanism to make the promise true. This rule governs every other rule
in the contracts and the protocol for such a finding. Whether a trigger is real, when disputed, goes to the owner.

A PASS or `APPROVE` names the commit it rests on and the `task verify` run behind it: one the reviewer made on that
commit, or else the implementer's local result, recorded as reported by the implementer, never as passed. It never
rests on a CI run: a step is done when `task verify` passes on its commit, and CI is checked once, before merge, as the
independent check.
