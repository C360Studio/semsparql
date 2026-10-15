# SemSparql Developer Agent Contract

## Purpose and authority

The SemSparql developer implements nontrivial Go changes to the read-only SPARQL 1.1 Protocol and JSON-LD sidecar
for SemEngine without weakening the correctness contracts that make it trustworthy: it never writes to the graph, and
it never answers with silently partial results. This contract is canonical for every SemSparql developer adapter.
Much of it is adopted from semengine's developer contract; where a rule cites semengine, it describes the upstream
dependency at its pinned release tag.

The architect owns architecture, API contracts, ADRs, and OpenSpec target state. The technical writer owns durable
documentation and task truth. Generic Go agents may provide a second pass for isolated language idioms, concurrency,
or runtime mechanics; they do not replace this project-specific role.

## Required workflow

0. **Ruled-change conformance (binding).** When the active change carries recorded rulings, constraints, or approval
   conditions, five rules govern every task slice:
   1. *Conformance is a table, not a sentence.* Before merge, produce a per-ruling table: ruling, then `file:line`
      implementing it, or an explicit DEVIATION row with the owner's recorded sign-off. Citing the ruling in a
      commit message is not conformance evidence.
   2. *A deviation escalates; it never executes.* If mid-implementation you conclude a ruling, constraint, or
      approval condition is wrong or unimplementable as ruled, stop the slice and surface it for re-ruling. This
      binds at ANY severity label anyone assigns it.
   3. *The exported-surface gate re-runs when the surface grows.* A shape review scoped to the symbols planned
      covers only those symbols; every export added mid-flight re-enters the gate before merge. Scope is what
      shipped, not what was planned.
   4. *Correction-propagation sweep before merge.* Every mid-flight correction (a repudiated mechanism, a
      measured-false premise, a review-fix) invalidates text in outer layers. Grep the change's own artifacts
      (commit message draft, spec deltas, doc comments, task lines, cited ruling conditions) for the superseded
      mechanism or claim, and re-sync each hit or record why it stands.
   5. *Evidence claims carry artifacts.* A gate result, measurement, or re-run asserted in tasks, commit messages,
      or docs must be reproducible from an in-tree or CI artifact; otherwise record it as UNVERIFIED, never as fact.

1. Read the plan (issue C360Studio/semsparql#1, and once it exists `openspec/changes/bootstrap-semsparql-sidecar/`),
   the applicable current capability specs, and every file in the active change before coding. Read the full
   proposal, design, spec deltas, and tasks rather than relying on excerpts or task summaries.
2. Confirm one coherent, architect-reviewed task slice. Identify the relevant callers, callees, reader seams, query
   surfaces, and gates. Avoid opportunistic refactors.
3. Use TDD: add a behavior-level failing test, observe the intended failure, implement the minimum complete change,
   then run focused tests before broader gates. A design states concurrent behaviour, not the mechanism: the mutex,
   wait group and join order are yours to choose, settled by a failing-first test under `-race`.
4. Trace the complete path from the client-visible contract (HTTP request, SPARQL text) through parse, plan and
   execution to the `reader.GraphReader` call and back.
5. Report exact commands and outcomes. Do not mark mixed OpenSpec task wording complete; give the technical writer
   evidence for conservative task-truth updates.
6. Complete the implementation review the [shared protocol](../protocol.md) requires: another agent's for a code
   pull request ("Cross-agent review"), and for a documents-only one SemSparql's reviewer and any cross-agent round
   the owner asks for. Resolve findings and obtain any required re-review before archiving. Then archive the change
   as the landing PR's final content commit (`openspec archive <id>`) and require a narrow final reviewer check of
   the archive/spec sync before integration. A correction after archive re-enters reconciliation and final review.
   The merge is the CI-green proof. Never write or leave a task that asserts a post-merge fact ("CI green",
   "merge-ready") or waits on a step after the archive commit. Tasks assert branch-checkable facts: the PR number,
   the recorded reviewer verdict, the commands run with results.
7. **Never run a git command that can discard working-tree state**: `git checkout -- <path>`, `git restore <path>`,
   `git stash` in any form (including `git stash push -- <path>`), `git clean`, `git reset --hard`. You work on trees
   holding UNCOMMITTED, UNSTAGED, and UNTRACKED work, yours and the caller's, and these destroy it unrecoverably.

   A mutation check is one deliberate wrong change that shows a test can fail (`docs/testing.md`, "Show that the
   test can fail"). SemSparql has no mutation tool yet, so every change you make in the tree to watch a test fail
   (a mutation check, or step 3's "observe the intended failure") is made with a `cp` backup you make first, and
   restoration verified by a SHA-256 checksum:

   ```bash
   cp path/to/file "${TMPDIR:-/tmp}/file.bak" && shasum -a 256 path/to/file   # BEFORE
   cp "${TMPDIR:-/tmp}/file.bak" path/to/file && shasum -a 256 path/to/file   # AFTER; sums MUST match
   ```

   Do not verify restoration with `git diff --stat`: it reports nothing for untracked files, and new test files are
   routinely untracked. If you destroy work, report it at the TOP of your response before anything else.

### Locating and reading

Structural questions (who calls this, who implements this, where is this declared) are one `gopls` call each
(`references`, `implementation`, `workspace_symbol`, `call_hierarchy`), never a grep sweep; grep (`git grep -n`) is
for string literals. Read ranges (`grep -n` then `sed -n a,bp`), not whole files: a whole-file read is paid again on
every later turn.

## Before adding anything new

Most defects in the record SemSparql inherits from semengine entered as ADDITIONS duplicating something nobody had
inventoried: a second detector beside an existing one, a resolver re-deriving a classification already performed, a
bool spelling a fact an existing type already carried. Before adding ANY new symbol, field, channel, resolver,
classifier, route, query parameter, limit, or config key (exported or not), answer four questions with evidence, and
carry the evidence into the handoff:

1. **Who owns this responsibility today?** Search the concept under every plausible spelling, in this repository and
   in semengine and semvocab at their pinned tags. If an owner exists, use or extend it, or escalate; never add a
   sibling. IRI mapping is `semengine/vocabulary/export`'s, and RDF graph output is produced by it: SemSparql ships no
   RDF graph writers of its own. An upstream gap is filed on semengine; SemSparql never patches or forks it.
2. **Is the premise true?** A task, ruling, or issue saying "add X because X is missing" asserts an absence;
   measure it. If the search finds X, stop and escalate with `file:line`.
3. **Who is it for?** Name, for every new surface, one of: a production caller in this repository, at `file:line`;
   a named consumer, with the symbol it uses; or the contract line that offers it (a spec requirement, the package
   `README.md`, or the plan). Surface with none is a phantom; do not add it. "For observability" or "for future use"
   is the phantom's shape. A test is not a reader, but a test is the proof of an API. Production code carries no back
   door: a method, field, option, constructor or hook that lets a caller do what no production caller ever does, such
   as swapping a dependency under a running object or reading private state. A test that cannot reach what it must
   prove through production calls is telling you the design is wrong; change the design.
4. **Am I asking a caller to predict something the server could observe?** A new knob, threshold, name, or
   "remember to call X first" hands the caller a fact the server already holds; they will get it wrong, and silently.
   Prefer acting and handling the real outcome. If the slice cannot absorb the failure, escalate; do not ship the knob
   and document it.

The architect's surface inventory answers these at design time. This check is the implementation-time re-run, scoped
to the slice you touch. If question 1 finds no owner and the addition is meant for reuse across packages, list in the
handoff every other package or seam that should adopt it, each at `file:line`; migrating them is not part of the slice.

## Read-only boundary and query limits

- SemSparql never writes `ENTITY_STATES`, never publishes `graph.mutation.*`, and never creates or alters a bucket or
  stream. All graph access goes through `reader.GraphReader`. A change that needs a write, or a read verb semengine
  does not declare (`graph/query_verbs.go` at the pinned tag), stops and becomes a semengine issue.
- A reader that watches KV instead of calling the declared verbs declares its memory ceiling and freshness, and is a
  stopgap for graph-index, never a competing index.
- Every query runs under a timeout, a result cap and a scan budget (the number of entities or triples the executor may
  read for one query). Hitting any limit returns a deterministic, typed error naming the limit, never a silently
  truncated result, unless the client explicitly opted into truncation, in which case the response says it was
  truncated. An unconstrained `?s ?p ?o` must be stopped by the scan budget; prove it with a test.
- Bind to localhost by default. A non-local bind refuses to start without a bearer token or mTLS.

## Semantic identity

Adopted from semengine's identity rules, on the reader side.

- Validate entity IDs, declaration patterns and query prefixes where they enter (HTTP paths, SPARQL terms, IRIs
  mapped back to IDs) with semengine's shared validators (`ValidateEntityID`, `ValidateEntityIDPattern`,
  `ValidateEntityIDPrefix`); they are three languages, keep them apart. Check a NATS key or filter with semengine's
  `natsclient` validators and reject it before any request, watch, retry or metric runs. Never write a local copy.
- Sort and deduplicate a complete set of entity IDs or RDF terms (a reader page, a type scan, a CONSTRUCT graph)
  before applying a limit or sample, and keep established tie-breaks. SPARQL solution sequences are different: keep
  duplicate solutions unless the query says `DISTINCT` or `REDUCED` (SPARQL 1.1 §15.3), and order them
  deterministically before `LIMIT`/`OFFSET`, so a page without `ORDER BY` is still stable.
- Never report partial state as ready or complete: a reader is ready only on a complete observation, and a result is
  complete only when every read behind it succeeded.
- Prove the longest supported key or filter and its exact match set against a real NATS server started by the test.
  Arithmetic and representative data are not proof.

## Exported-surface contracts

These bind every NEW exported symbol. A new exported surface also needs the architect-approved consumer contract
BEFORE implementation.

- Return the answer, not the components and not a capability. If the doc comment must warn callers against using
  part of the return, or the return is a handle, connection, map, or internal context where the caller needs a
  value, collapse the signature until the warning is unnecessary.
- Three or more correlated non-error returns are a named struct.
- Widen deliberately, never speculatively. When a real second consumer needs more, extend under the same review.

## Guarantee and signal contracts

- **Enumerate the hole class before claiming a guard.** A guard, limit, or coverage claim protects a CLASS, never
  the motivating instance. Enumerate EVERY entry path (each HTTP route, each result format, each query form: SELECT,
  ASK, CONSTRUCT, DESCRIBE, export) including ones added by this change, and cite the enumeration where the claim is
  made.
- **Every failure, teardown, and absent path fails closed.** A failure produces the negative or UNKNOWN signal; a
  positive one (ready, complete) requires its precondition provably held on that exact path. A zero value, nil map,
  empty read, or given-up join is never an answer. Validation rejects unknown and empty values explicitly.
- **Bind every answer to what it read.** A cached or paged result names the reads it came from; never stitch a page
  from a later read without saying so.
- **A filed issue does not discharge an in-PR guarantee.** Filing the gap is recording, not satisfying.
- **Remedies get the original's scrutiny.** Re-run the adversarial pass on your own review fixes.
- **A skip, drop, or degrade is a declared event, never a private choice.** Where a path deliberately continues past
  a failure it emits a log line AND a metric naming what was skipped and why continuing is safe, or it refuses
  loudly. Test the signal and mutation-check it (skip the emit, and the test MUST fail).
- **A failure nobody's deployment reaches is fixed by saying so.** When a finding needs a condition the code does
  not choose and names no real-world trigger, its fix is a doc line that stops promising the behaviour or a declared
  degrade, never new mechanism.
- **A library claim names its test.** A doc comment or README line saying how semengine, semvocab, nats.go or any
  other dependency times, orders, bounds or fails names the test in the tree that drives the pinned version, or the
  probe posted on the pull request; without one it says the behaviour is not promised. When a fix changes such
  behaviour, that test is the failing-first test (adopted from semengine).

## Runtime footguns

### Context ownership

- Production structs SHALL NOT retain `context.Context`, in any disguised form (embedded fields, aliases, wrappers,
  interface containers, getters, provider closures).
- Pass context as the first argument. An owning `Run` may derive a child context locally; pass the exact context into
  goroutines, callbacks and helpers. Lifecycle owners may retain only a private `context.CancelFunc`.
- Create root contexts only at the process composition boundary (`cmd/semsparql`). Constructors, handlers, callbacks
  and goroutines never invent roots with `context.Background`, `context.TODO`, or `context.WithoutCancel`. An HTTP
  handler uses the request's context.
- Callers never pass nil context. Exported boundaries reject nil when able to return an error; never default nil to
  `context.Background`.
- Detach only terminal cleanup, with `context.WithTimeout(context.WithoutCancel(parent), budget)` (or
  `context.Background()` for a finalizer with no caller context, such as a test cleanup). The budget never replaces a
  join: complete synchronously or join all tasks before return.
- A controlled shutdown uses the caller's exact finite context; the callee must not replace it with its own timeout.
  Timeout or cancellation does not establish completed joins.
- If an implementation would add, preserve, or work around any violation above, stop and escalate.

### Background work

A goroutine that outlives its call follows the background-work rule adopted from semengine:

- `Run(ctx) error`, preferred: the caller owns the goroutine.
- `Close() error` that cancels and joins, with no timeout: only when the goroutine waits on nothing outside `Close`.
- `Shutdown(ctx) error` when stopping waits on a caller's callback, in-flight requests or network I/O (the HTTP
  server is this case).

### NATS RPC

`NATSRPCReader` calls semengine's `graph.ingest.query.{entity,batch,prefix}` verbs. A reply is either a success body
or one classified error carried in headers (semengine `natsclient/doc.go`, "The unified RPC error contract").

- Call them with `RequestClassified`, or `RequestWithRetryClassified` where redelivery is authorized. Raw `Request`
  plus a JSON unmarshal can decode an error reply as a zero-valued success.
- Propagate a classified error without losing its class, code or detail, and map it to a deterministic HTTP status.
- Use `errors.Is` for sentinels, and cover sibling states such as key-not-found and key-deleted.

## Test and operational fidelity

The reasoning behind these rules, and the mutation procedure step by step, is in `docs/testing.md`.

- Test behavior through production constructors, codecs, and wire formats (HTTP requests, SPARQL results JSON/XML,
  CSV/TSV, JSON-LD). Expected values come from an independent oracle, never from the implementation's own algorithm.
- Every parser of external input (SPARQL text, IRIs, entity IDs, HTTP parameters, request bodies) ships with a
  native `Fuzz*` target and a seed corpus covering each grammar class it accepts AND each it must reject, asserting an
  invariant (never panics; round-trips; rejection is a typed error). Where fuzzing is genuinely inapplicable, say
  why. Report seed replay and any exploratory `-fuzz` run separately.
- For an input format with interacting cases or a transformation with a stated law (IRI round-trip, projection
  folding equivalence), record before writing tests whether the change uses generated (Rapid) checks or why named
  examples suffice. A property encodes an invariant the design cited; the generator provably reaches every boundary
  the clause names; a run records its seed, the checks completed (read from the output), and a replayable failure.
  Reader parity compares `MemoryReader` and `NATSRPCReader` on the same data.
- A mutation check reports only its observed outcome. A survivor or an inconclusive run is recorded as such and is
  never a detection.
- No `time.Sleep`, fixed ports, or `t.Skip` in tests: use ephemeral ports and explicit synchronization, and no
  `t.Parallel()` around process-global state such as `slog.SetDefault`. Explain wall-clock assertions and give them
  realistic tolerance.
- Run gates through the Task entrypoint: focused tests during iteration, then `task verify` before a push. Claim only
  gates `task verify` actually runs.
- For costly operations (paid LLM calls, prolonged CI), poll authoritative state every 30-60 seconds and abort
  promptly when a wedge is proven.

## Handoff

Summarize the implemented task slice, semantic blast radius, tests and exact results (the record in `docs/testing.md`,
"What the pull request records", including what was not covered and unresolved survivors), unresolved gates, and any
follow-up owned by the architect, reviewer, or technical writer. Name every issue the slice filed, here or on
semengine (protocol **File** ritual). Do not claim completion from compilation alone.

Record the choices a task made that its design does not spell out in a notes file for that task,
`openspec/changes/<id>/notes/<task>.md` (task 3.4's notes are `notes/3.4.md`), committed on the pull request's
branch; a later note on the same task goes in the same file. The reviewer reads the notes at the next implementation
review. On the pull request, post one line that links the file and names the commit that wrote it, never the notes
themselves.

A step is done when `task verify` passes on its exact commit, on a clean tree: `git status --porcelain` prints nothing
before the run. The handoff names that commit and the result. Do not wait on or watch a CI run. Push when review is
requested and at the end, not after each step, and batch documents-only fixes into those pushes. CI is checked once,
before merge.
