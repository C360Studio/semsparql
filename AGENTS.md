# SemSparql

Guidance for coding agents. Codex and Antigravity (Gemini) load this file as `AGENTS.md`; `CLAUDE.md` imports it, so
there is one copy (kept well under 24,000 bytes so rule loaders with a 24 KB per-file cap never truncate it). It
carries the facts an agent cannot derive from the tree and names where each rule lives. Per-platform command names and
model routing: `.agents/README.md`. Full enforcement audit matrix: `.agents/rules-index.md`. Adopted from SemEngine's
agent setup at `67d0ee9`; where a rule differs, this tree is authoritative.

## What this is for

SemSparql (`github.com/c360studio/semsparql`) is the **read-only** SPARQL 1.1 Protocol and JSON-LD query/export
sidecar for SemEngine. It gives DIEKM, BFO 2.0 and CCO adopters a standards-compliant SPARQL endpoint over the live
SemEngine graph, and exports RDF that external tools check against SHACL shapes (offline compliance gates). SemEngine's
ADR-107 keeps semantic-web vocabulary and RDF runtime out of SemEngine core, at the export edge: this repository is
that edge. It never writes the graph. It reads through `reader.GraphReader`, maps IDs and serializes RDF through
`semengine/vocabulary/export`, and resolves vocabulary through SemEngine's `vocabulary` and SemVocab packs.

The plan is issue #1 (bootstrap, Stage 3): layout, design decisions, proving tests and tasks. Task 0.1 (`go.mod`
pinning `semengine v0.1.0-alpha.2` and `semvocab v0.1.0`, Go verification gates, and `internal/harness/contract`
structural guards), task 0.2 (`semengine#256` and `semengine#259`, both merged into `v0.1.0-alpha.2`), task 1.1
(`irimap` forward/inverse entity-IRI mapping, canonical N-Triples object extraction, and deterministic `BlankNodeID`),
task 1.2 (`reader.GraphReader` and `reader.MemoryReader`), and task 2.1 (`sparql` AST and parser for the supported
SPARQL 1.1 subset) are in place. **Remaining product runtime packages (`server`, `cmd/semsparql`),
`sparql` planner/evaluator (`2.2`–`3.3`), and `reader.NATSRPCReader` (`4.1`) do not exist in the tree yet**; do not
describe unimplemented runtime components as present.

## Commands

`task --list` shows every command with its rationale.

```bash
task doctor              # tool versions and Docker reachability against pins; starts no workloads
task fmt                 # format Go sources (changes files)
task fmt:check           # fail if any Go source needs formatting
task tidy:check          # fail if go.mod or go.sum is not tidy
task cleanup-roots:check # fail if a test stops, closes, or terminates under context.Background/TODO
task build               # build all packages
task vet                 # run go vet
task lint                # pinned revive + fixed-port guard and fixture test
task vuln                # pinned govulncheck over all packages
task test:unit           # unit tests once with -race at one CPU
task test:repeat         # unit tests five times at one CPU without -race, shuffled
task test:integration    # Docker-backed tests (-tags=integration), recording coverage/integration.tree
task cover:check         # 80% statement coverage check over configured targets and tree fingerprint
task spec:check          # strict OpenSpec validation; fails on a Hold: outside every task
task spec:queue          # in-flight OpenSpec changes and their holds
task docs:check          # markdownlint
task merge:check         # -- <n>: fail on open flakes PR n leaves open, missing up-to-date rule, or missing review lines
task verify              # every gate cheapest first (test:repeat last); fails if tracked files changed
```

Run `task verify` before every implementation push. CI has three jobs: `verify` runs `task doctor` and `task verify`;
`merge-check` runs `scripts/merge-check.sh` (known flakes, up-to-date rule, cross-agent review lines); `required` fails
if either failed, is missing, or was skipped/cancelled. Gate selection per diff:
`.agents/skills/semsparql-preflight/SKILL.md`.

## Rules and what enforces them

Domain-grouped working reference. `.agents/rules-index.md` carries the full audit matrix (canonical home, then what
enforces it, including review-only gaps). Short names in "Where": `arch`, `dev`, `rev`, `writer` ->
`.agents/contracts/semsparql-<role>.md`; `protocol` -> `.agents/protocol.md`; `testing` -> `docs/testing.md`. In
"Gate": `review` means review only (no command fails when broken); `verify` means enforced by `task verify`. Runtime
rows remain `review` until their implementation task lands; a change that can turn a row into a failing command should.
A new repo-wide or agent-conduct rule updates both this file and `.agents/rules-index.md`.

### 1. Read-only boundary and upstream

| Gate | Rule | Where |
| --- | --- | --- |
| `review` | Never write `ENTITY_STATES` or publish `graph.mutation.*`; zero mutation of SemEngine state | `dev`, `rev` (`BLOCKING`); issue #1 §2.1 |
| `review` | Read only through `reader.GraphReader` over verbs declared in `graph.QueryVerbs()`; never spell a `graph.ingest.query.*` subject as a literal | `dev`, `rev`; `query-pattern` skill |
| `verify+review` | IRI mapping and RDF graph output come from `semengine/vocabulary/export`; no second IRI mapping and no RDF graph writers here | `dev`, `rev`, `arch`; issue #1 §1, §2.2.1; `TestIRIMapperMatchesExporter` |
| `review` | A KV watch reader, if built, declares a memory ceiling (refusing to start above it) and freshness, and is a stopgap for graph-index, never a competing index | `dev`, `rev`, `arch`; issue #1 §2.1 |
| `verify+review` | SemEngine and SemVocab are pinned to released tags: no `replace`, fork, local copy, or pseudo-version | `protocol` § Upstream boundary; `rev`; `task tidy:check`; `TestPinnedDependencies` |
| `review` | An upstream gap is filed on the upstream repository and the dependent task carries `Hold:` naming it; never worked around here | `protocol` § Upstream boundary; `arch` |
| `review` | Agents mutate only this repository; sister repositories are read-only | `protocol` |

### 2. Go runtime and surface

| Gate | Rule | Where |
| --- | --- | --- |
| `review` | Production structs never retain `context.Context`; never invent roots or nil defaults | `dev`, `rev` § Context ownership |
| `review` | State travels as an argument, field, or stored entry, never `context.WithValue` / `ctx.Value` | `dev`, `rev` |
| `review` | Background work outside a service uses `Run(ctx)`, joining `Close()`, or `Shutdown(ctx)`: no fixed timeout, nil ctx refused | `dev`, `rev` § Background work |
| `review` | Classified NATS RPC uses `RequestClassified` / `RequestWithRetryClassified`, propagating class, code, detail | `dev` § NATS RPC; `rev` |
| `review` | Failure paths fail closed; any skip, drop, or degrade is declared with a log line and a metric | `dev`, `rev` |
| `review` | New surface names a caller here, a named consumer, or a contract line; returns the answer, not components; no back doors | `dev` § Before adding anything new, § Exported-surface contracts; `rev` |

### 3. Queries, identity, and limits

| Gate | Rule | Where |
| --- | --- | --- |
| `verify+review` | Entity IDs, patterns and prefixes pass SemEngine's shared validators at entry, before any request | `dev` § Semantic identity; `rev`; `TestIRIMapperRoundTrip`, `FuzzIRIToEntityID`, `TestMemoryReaderInvalidInput`, `TestMemoryReaderEmptySentinelDistinction`, `FuzzMemoryReaderSelectors` |
| `verify+review` | Sort and deduplicate complete entity-ID and RDF-term sets before a limit; SPARQL solutions keep duplicates unless `DISTINCT`/`REDUCED` and are ordered deterministically before `LIMIT`/`OFFSET` | `dev`, `rev`; `TestMemoryReaderDeterministicIDSets`, `TestPropMemoryReaderPatternAndPrefix` |
| `review` | Never report partial replay, index or result state as ready or complete | `dev`, `rev` |
| `verify+review` | Timeout, result cap and scan budget return a deterministic error, never silent partial results, unless the client opts into truncation; unconstrained `?s ?p ?o` is stopped by the scan budget | `dev`, `rev` § Read-only boundary; issue #1 §2.6; `TestMemoryReaderBudgetAwareScans` |
| `verify+review` | Out-of-scope SPARQL syntax returns a 400 that names the feature | `dev`, `rev`; issue #1 §2.5; `TestUnsupportedFeatureErrors` |
| `review` | Bind to localhost by default; a non-local bind requires a bearer token or mTLS | `dev`, `rev` (`BLOCKING`); issue #1 §2.6 |

### 4. Testing and verification

| Gate | Rule | Where |
| --- | --- | --- |
| `verify` | No `time.Sleep` in a test file | `dev`, `rev`; `testing`; `TestNoSleepsInTests` |
| `verify+review` | Tests bind no fixed address or port | `dev`, `rev`; `testing`; `task lint` (`scripts/lint-test-ports.sh`) |
| `verify+review` | No test calls `Skip`/`Skipf`/`SkipNow`; no test file carries a build tag other than `integration` | `dev`, `rev`; `testing`; `TestNoSkippedTests`, `TestNoHiddenTests` |
| `verify+review` | Test cleanup never stops, closes, or terminates under an unbounded context | `dev`, `rev`; `testing`; `task cleanup-roots:check` (`scripts/cleanup-roots-check.sh`) |
| `review` | Tests use an independent oracle and are shown able to fail | `dev`, `rev`; `testing` |
| `verify+review` | Every surface that parses external bytes or strings (SPARQL text, IRIs, entity IDs, HTTP parameters) ships a `Fuzz*` target with a seed corpus of accepted and rejected classes | `dev`, `rev`; `testing`; `FuzzIRIToEntityID`, `FuzzMemoryReaderSelectors`, `FuzzParseSPARQL` |
| `verify+review` | Changes with interacting inputs, a stated law, or order-dependent history record whether generated checks are used; Rapid runs record seed and checks completed; histories check against a reference model | `dev`, `rev`; `testing`; `TestPropIRIMapperRoundTrip`, `TestPropMemoryReaderPatternAndPrefix`, `TestPropParseSPARQLGeneratedQueries` |
| `review` | Mutation checks report survivors/inconclusive runs as such; in-tree wrong changes use a `cp` backup and verified SHA-256 restore | `dev`, `rev`; `testing` |
| `review` | Claims about NATS keys, filters and query verbs are proven against a real NATS server and graph-ingest, not arithmetic | `dev`, `rev` |
| `review` | Never run a git command that can discard working-tree state (`checkout --`, `restore`, `stash`, `clean`, `reset --hard`) | `dev`, `rev`, `arch` |

### 5. Design and architecture

| Gate | Rule | Where |
| --- | --- | --- |
| `review` | Designs start with the surface inventory and, for surface reached from outside, the adopter seam inventory, including the upstream APIs consumed at the pinned tag | `arch`, `rev` |
| `review` | Designs state observable behaviour and proving test, not lock/wait-group/join mechanics; open findings after three review rounds go to the owner | `arch`; `.agents/README.md` |
| `review` | Added/edited statements on how a library (SemEngine, SemVocab, `nats.go`, etc.) times, orders, bounds, or fails cite a test or probe at the pinned version, or state it is not promised | `arch`, `rev`, `dev`, `writer` |
| `review` | A change establishing a reusable primitive lists who should adopt it | `arch`, `dev` |
| `review` | Inventories list every overlapping open PR (drafts included), and designs state merge order | `arch`, `rev` |
| `review` | A boundary change is checked against the read-only purpose | `arch` § Boundary check |

### 6. Workflow, review, and merge gates

| Gate | Rule | Where |
| --- | --- | --- |
| `spec:check+review` | OpenSpec changes and specs are well formed and truthful against code | "Where state lives" below |
| `spec:check+review` | Every OpenSpec task can be ticked in or before the archive commit; a hold is written as `Hold:` inside the unticked task it stops | `protocol`; `rev` |
| `verify+review` | Steps end when `task verify` passes locally on a clean tree; pushes batch at review/end; CI is checked once before merge; reviewer `PASS` cites commit + own local `task verify`, never CI | `dev`, `rev`; `protocol` |
| `CI required` | A merge needs CI green on a head up to date with `main` | `protocol` |
| `merge:check` | No merge while a known flake (`class:flake` issue) is open, unless the PR closes every open one | `protocol` |
| `merge:check+review` | Code PRs (any non-`.md`/non-`openspec/` file, or `.claude/agents/*` or `.agents/contracts/*`) require review by an agent that wrote no commits (`implemented-by:` and `reviewed-by:` naming `claude`, `codex`, or `gemini`) | `protocol`; `rev` |
| `review` | Any defect found in repo code not fixed in the change is filed as an issue (`status:blocked` while its code is only on a PR branch); upstream defects are filed upstream | `protocol`; `rev` |
| `review` | A finding needing an unchosen condition is `BLOCKING`/`HIGH` only with a named real-world trigger; else `MEDIUM` at most | `rev`, `dev`; `protocol` |
| `review` | A new rule names what enforces it and adds its row to `AGENTS.md` and `.agents/rules-index.md` | `rev` |
| `review` | Before claiming work, mutating state, or spawning role subagents on an underspecified prompt, confirm Deliverable, Scope/non-goals, Definition of Done/verification, and Stop boundary | `protocol`; `.agents/README.md` |
| `review` | Role-agent briefs carry the owner's intent and the artifact itself, not a paraphrase; resume for continuity, start fresh for mechanics | `.agents/README.md` |
| `verify+review` | Architect, developer, and reviewer run on the frontier model tier; technical writer on a lighter tier (table in `.agents/README.md`) | `.agents/README.md`; `TestAgentSetupParity` |
| `verify+review` | `AGENTS.md` stays under 24,000 bytes and matches `.agents/rules-index.md` in domains and rows; adapters stay thin and point to their canonical contract | `.agents/README.md`; `TestAgentSetupParity` |
| `review` | Pushed claim branches update by merging `origin/main`, never by rebase or force-push | `protocol` |
| `review` | Evidence cited in a PR or task can be opened on another machine; git-ignored paths are named as local only | `semsparql-handoff`, `semsparql-preflight` skills |
| `review` | Docs are written for a working developer: coined terms defined at first use, no jargon, no marketing | `writer`, `rev` |
| `review` | Developer notes on task choices not spelled out in the design go in `openspec/changes/<id>/notes/<task>.md`, linked from a PR comment | `dev`, `rev`; `protocol` |

## Where state lives

- **GitHub issues:** what is wanted and decided; rulings as issue comments; labels `type:epic`,
  `status:needs-decision`, `status:blocked`, `class:flake`.
- **Milestones:** what gates a release.
- **Draft PRs:** claims; `Closes #n` for a leaf issue, `Addresses #n` for an epic.
- **`openspec/changes/<id>/`:** target state (proposal, design, tasks, holds), archived on completion. Issue #1's
  plan becomes `openspec/changes/bootstrap-semsparql-sidecar/`.
- **`openspec/specs/<capability>/`:** current truth, verified against code before it is written.
- **ADRs:** why; irreversible choices and cross-repo contracts. SemEngine's ADRs (ADR-102 entity IDs, ADR-107 export
  edge) bind where this repository consumes their surface.

There is no `/tickets` state and no handoff document. Non-trivial work starts with an OpenSpec change before code.

## Working here (Claude, Codex, and Gemini)

The shared protocol is `.agents/protocol.md`. Read it before filing, taking, landing, or closing work. Four gates
never become a pointer:

- **Clarify:** before claiming work, mutating state, or spawning role subagents, read any named issue, PR, or file so
  you never ask what the repository already answers, then verify four boundaries are clear: **Deliverable** (answer,
  issue, OpenSpec design, docs-only PR, or code PR), **Scope/non-goals**, **Definition of Done/verification**, and
  **Stop boundary**. When a prompt leaves any of the four ambiguous, stop first, propose a concise 3–4 bullet
  recommended Definition of Done, and ask at most 1–3 targeted questions; once confirmed, run autonomously to that
  finish line without pausing on routine steps.
- **Claim:** a draft PR opened before the work, in its own worktree on an agent-prefixed branch
  (`git worktree add ../semsparql-wt/claude/<topic> -b claude/<topic> origin/main`; Codex uses `codex/`, Gemini uses
  `gemini/`, and Enjoy desktop sessions use `move_to_worktree` under `.worktrees/<branch>`). No draft PR, no claim.
- **Merge:** CI green on a head up to date with `main`; no known flake open (an open `class:flake` issue) unless the PR
  closes every open one; `task merge:check -- <n>` immediately before merging (`GITHUB_ACTIONS` unset);
  `implemented-by:` in the PR body (`claude`, `codex`, or `gemini` beside the model/persona); archive/spec sync as the
  last content commit; squash merge. A code PR (any changed file not Markdown or under `openspec/`, or a file under
  `.claude/agents/` or `.agents/contracts/`) also needs another agent's review and `reviewed-by:` in the PR body,
  without which `merge-check` fails once non-draft; a documents-only PR does not.
- **Close:** the squash merge of a PR that declared `Closes #n` is the authorization. A close with no merged PR behind
  it takes the owner's word on the issue.

## Roles

Role agents are the default path for nontrivial work. Contracts: `.agents/contracts/`.

- `semsparql-architect` designs, inventory first; read-only.
- `semsparql-developer` implements: failing test, implementation, evidence.
- `semsparql-reviewer` reviews every nontrivial change independently before integration; read-only. For a code
  pull request the review of record is another agent's (`.agents/protocol.md`, "Cross-agent review").
- `semsparql-technical-writer` updates current docs and task truth with the approved code.

Binding rulings stay with the owner, on the issue.
