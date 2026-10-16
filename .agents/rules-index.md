# Rules index (SemSparql)

The full enforcement audit matrix behind the domain-grouped rule summary in `AGENTS.md`. Every row in `AGENTS.md` has
exactly one row here, in the same numbered domain and order. "Canonical home" is where the rule is written in full;
"Enforced by" names the command or test that fails when the rule is broken, or says "review only". Adopted from
SemEngine's `.agents/rules-index.md` at `67d0ee9`.

Task 0.1 of issue #1 added `go.mod`, Go verification gates, and `internal/harness/contract` structural guards; Task 1.1
added `irimap` (`TestIRIMapperRoundTrip`, `TestPropIRIMapperRoundTrip`, `TestIRIMapperMatchesExporter`,
`TestBlankNodeIDMultiValued`, and `FuzzIRIToEntityID`); Task 1.2 added `reader` (`GraphReader`, `MemoryReader`,
`TestMemoryReaderEmptySentinelDistinction`, `TestMemoryReaderInvalidInput`,
`TestMemoryReaderMissingEntitiesAndImmutability`, `TestMemoryReaderDeterministicIDSets`,
`TestMemoryReaderBudgetAwareScans`, `TestPropMemoryReaderPatternAndPrefix`, and `FuzzMemoryReaderSelectors`). Where
issue #1 names a runtime proving test not yet in the tree, the row lists it as *planned*: a planned test is not
enforcement until it is in the tree and run by `task verify`. A change that can turn a "review only" row into a failing
command should, and updates this row and the `AGENTS.md` gate in the same change.

## 1. Read-only boundary and upstream

| Rule | Canonical home | Enforced by |
| --- | --- | --- |
| Never write `ENTITY_STATES` or publish `graph.mutation.*` | developer contract § Read-only boundary and query limits; reviewer contract § Read-only boundary and query review (`BLOCKING`); issue #1 §2.1 | review only; planned: an import/call guard refusing SemEngine's mutation and KV-write APIs outside tests |
| Read only through `reader.GraphReader` over verbs declared in `graph.QueryVerbs()`; no literal `graph.ingest.query.*` subject | developer contract § Read-only boundary and query limits; `.agents/skills/query-pattern/SKILL.md` | review only; planned: a structural guard on subject literals, like SemEngine's `TestReservedSubjectsDeclaredOnce` |
| IRI mapping and RDF graph output come from `semengine/vocabulary/export`; no second IRI mapping, no RDF graph writers | developer and architect contracts; issue #1 §1, §2.2.1 | `TestIRIMapperMatchesExporter` and `TestPropIRIMapperRoundTrip` (`irimap/mapper_test.go`, in `task verify`) for byte-identical subject IRIs; absence of local RDF graph writers is review only (planned: task 3.3) |
| A KV watch reader declares a memory ceiling and freshness, and is a stopgap, not a competing index | developer and architect contracts; issue #1 §2.1 | review only (task 5.1 is optional and deferred) |
| SemEngine and SemVocab pinned to released tags; no `replace`, fork, copy, or pseudo-version | `.agents/protocol.md` § Upstream boundary; reviewer contract | `task tidy:check` and `TestPinnedDependencies` / `TestPinnedDependenciesSensitivity` (`internal/harness/contract/deps_test.go`, in `task verify`) refuse `replace`/`exclude` directives and pseudo-versions in `go.mod`; fork or local copy is review only |
| Upstream gaps filed upstream; the dependent task carries `Hold:` naming the issue | `.agents/protocol.md` § Upstream boundary; architect contract § Boundary check | `task spec:check` fails on a `Hold:` outside a task; that the gap was filed upstream is review only |
| Agents mutate only this repository; sister repositories are read-only | `.agents/protocol.md` | review only |

## 2. Go runtime and surface

| Rule | Canonical home | Enforced by |
| --- | --- | --- |
| Production structs never retain `context.Context`; no invented roots or nil defaults | developer and reviewer contracts § Context ownership | review only; planned: a retained-context guard like SemEngine's `TestNoRetainedContext` |
| State travels as an argument, field, or stored entry, never as a `context` value | developer and reviewer contracts | review only |
| Background work outside a service uses `Run(ctx)`, a joining `Close()`, or `Shutdown(ctx)`; no fixed timeout; nil ctx refused | developer and reviewer contracts § Background work | review only; each site's `synctest` test once written |
| Classified NATS RPC uses `RequestClassified` / `RequestWithRetryClassified`, propagating class, code, detail | developer contract § NATS RPC; reviewer contract § NATS RPC error contract | review only |
| Failure paths fail closed; a skip, drop, or degrade is declared with a log line and a metric | developer contract § Guarantee and signal contracts; reviewer contract § Guarantee, signal, and issue review | review only; each declared degrade's own signal test, mutation-checked |
| New surface names a caller, consumer, or contract line; returns the answer; no back doors | developer contract § Before adding anything new, § Exported-surface contracts; reviewer contract § Exported-surface review | review only |

## 3. Queries, identity, and limits

| Rule | Canonical home | Enforced by |
| --- | --- | --- |
| Entity IDs, patterns and prefixes pass SemEngine's shared validators at entry | developer contract § Semantic identity; reviewer contract § Read-only boundary and query review | `TestIRIMapperRoundTrip` and `FuzzIRIToEntityID` (`irimap/mapper_test.go`), `TestMemoryReaderInvalidInput`, `TestMemoryReaderEmptySentinelDistinction`, and `FuzzMemoryReaderSelectors` (`reader/reader_test.go`, in `task verify`) for `irimap` and `reader` entry validation; `NATSRPCReader` entry validation is planned (task 4.1) |
| Sort and deduplicate complete entity-ID and RDF-term sets before a limit; SPARQL solutions keep duplicates unless `DISTINCT`/`REDUCED` (SPARQL 1.1 §15.3) and are ordered deterministically before `LIMIT`/`OFFSET` | developer contract § Semantic identity; reviewer contract § Read-only boundary and query review | `TestMemoryReaderDeterministicIDSets` and `TestPropMemoryReaderPatternAndPrefix` (`reader/reader_test.go`, in `task verify`) for sorted/deduplicated entity-ID sets in `MemoryReader`; planned: `TestScanPatternReaderParity` (task 4.1) across NATS cursor pages and a duplicate-preserving `UNION` case in executor tests (task 2.2) |
| Never report partial replay, index or result state as ready or complete | developer contract § Guarantee and signal contracts | review only |
| Limits give deterministic errors, no silent truncation unless opted in; `?s ?p ?o` stopped by the scan budget | developer contract § Read-only boundary and query limits; issue #1 §2.6 | `TestMemoryReaderBudgetAwareScans` (`reader/reader_test.go`, in `task verify`) for reader scan budgets; planned: `TestQueryLimits` (task 2.2/2.3) for executor/HTTP query limits |
| Out-of-scope SPARQL returns a 400 naming the feature | developer and reviewer contracts; issue #1 §2.5 | review only; planned: `TestUnsupportedFeatureErrors` (task 2.3) |
| Localhost bind by default; non-local bind needs a bearer token or mTLS | developer contract § Read-only boundary and query limits; reviewer contract (`BLOCKING`); issue #1 §2.6 | review only; planned: a server test refusing a non-local bind without credentials (task 2.3) |

## 4. Testing and verification

| Rule | Canonical home | Enforced by |
| --- | --- | --- |
| No `time.Sleep` in a test file | `docs/testing.md`; developer contract § Test and operational fidelity | `TestNoSleepsInTests` and `TestNoSleepsInTestsSensitivity` (`internal/harness/contract/testtext_test.go`, in `task verify`) |
| Tests bind no fixed address or port | `docs/testing.md`; developer contract | `scripts/lint-test-ports.sh` and `scripts/lint-test-ports_fixture_test.sh` in `task lint` (`task verify`); indirect or non-`net.Listen` forms are review only |
| No `Skip`/`Skipf`/`SkipNow`; no build tag other than `integration` | `docs/testing.md`; developer contract | `TestNoSkippedTests`, `TestNoSkippedTestsSensitivity`, `TestNoHiddenTests`, and `TestNoHiddenTestsSensitivity` (`internal/harness/contract/testtext_test.go`, in `task verify`); helper indirection and filename platform suffixes are review only |
| Test cleanup never stops, closes, or terminates under an unbounded context | `docs/testing.md`; developer contract § Context ownership | `scripts/cleanup-roots-check.sh` in `task cleanup-roots:check` (`task verify`) and `TestCleanupRootsCheckSensitivity` (`internal/harness/contract/cleanuproots_test.go`); multi-line or indirect calls are review only |
| Independent oracle; tests shown able to fail | `docs/testing.md` "Show that the test can fail"; developer and reviewer contracts | review only |
| Every external-input parser ships a `Fuzz*` target with accepted and rejected seed classes | developer contract § Test and operational fidelity; reviewer contract § Test fidelity | `FuzzIRIToEntityID` (`irimap/mapper_test.go`) and `FuzzMemoryReaderSelectors` (`reader/reader_test.go`, in `task verify`); other external-input parsers are review only (planned: `FuzzParseSPARQL` in task 2.1) |
| Generated-check decision recorded; Rapid seed and checks completed recorded; histories against a reference model | `docs/testing.md` "Decide whether generated checks are needed", "Running and replaying a Rapid test" | `TestPropIRIMapperRoundTrip` (`irimap/mapper_test.go`) and `TestPropMemoryReaderPatternAndPrefix` (`reader/reader_test.go`, in `task verify`); recording of decision/seed/checks is review only; planned: folding equivalence (task 3.2) |
| Mutation checks report observed outcomes; in-tree wrong changes use `cp` backup and verified SHA-256 restore | developer contract § Required workflow; `docs/testing.md` "Show that the test can fail" | review only (SemSparql has no `task mutate:check`) |
| NATS key, filter and query-verb claims proven against a real NATS server and graph-ingest | developer contract § Semantic identity | review only; planned: integration fixture (task 4.2) |
| Never run a git command that can discard working-tree state | developer, reviewer and architect contracts | review only |

## 5. Design and architecture

| Rule | Canonical home | Enforced by |
| --- | --- | --- |
| Surface inventory first, then adopter seam inventory, including upstream APIs at the pinned tag | architect contract § The surface inventory, § The adopter seam inventory; reviewer contract § Inventory review | review only |
| Behaviour and proving test, not mechanism; three review rounds, then the owner | architect contract § Design discipline; `.agents/README.md` | review only |
| Library behaviour claims cite a test or probe at the pinned version, or say not promised | architect contract § Design discipline; developer contract § Guarantee and signal contracts; technical-writer contract | review only |
| A reusable primitive lists its adopters | architect contract § The adoption sweep; developer contract § Before adding anything new | review only |
| Inventories list overlapping open PRs; designs state merge order | architect contract § The surface inventory | review only |
| A boundary change is checked against the read-only purpose | architect contract § Boundary check | review only |

## 6. Workflow, review, and merge gates

| Rule | Canonical home | Enforced by |
| --- | --- | --- |
| OpenSpec changes and specs are well formed and truthful against code | `AGENTS.md` § Where state lives | `task spec:check` (`openspec validate --all --strict`) for form; truth against code is review only |
| Every task tickable before archive; `Hold:` inside the unticked task it stops | `.agents/protocol.md`; reviewer contract | `task spec:check` (`scripts/openspec-queue.sh --check`) for holds outside tasks; post-merge task wording is review only |
| A step ends when `task verify` passes on a clean tree; reviewer `PASS` cites its own run, never CI | developer contract § Handoff; reviewer contract; `.agents/protocol.md` | `task verify` (`scripts/verify.sh`, fails on tracked-file change); the clean-tree and citation parts are review only |
| A merge needs CI green on an up-to-date head | `.agents/protocol.md` § Verification | CI `Required`, enforced only once the `main` ruleset requires it with the strict setting; `scripts/merge-check.sh` fails while that rule is missing |
| No merge while a known flake is open, unless the PR closes every open one | `.agents/protocol.md` "Known flakes" | `scripts/merge-check.sh` (CI `merge-check`, `task merge:check`) |
| Code PRs need review by an agent that wrote no commits | `.agents/protocol.md` "Cross-agent review"; reviewer contract | `scripts/merge-check.sh` for the `implemented-by:`/`reviewed-by:` lines; their truth and the review record are review only |
| Unfixed defects filed as issues; upstream defects filed upstream | `.agents/protocol.md` "File"; reviewer contract | review only |
| Unchosen-condition findings are `BLOCKING`/`HIGH` only with a named real-world trigger | reviewer contract § Finding and verdict format; `.agents/protocol.md` | review only |
| A new rule names its enforcement and adds rows here and in `AGENTS.md` | reviewer contract § Contract and task-truth review | review only |
| Clarify Deliverable, Scope, Definition of Done and Stop boundary before work | `.agents/protocol.md` "Clarify"; `.agents/README.md` | review only |
| Briefs carry intent and the artifact; resume for continuity, fresh for mechanics | `.agents/README.md` § Orchestrating role agents | review only |
| Model tier and effort routing per role | `.agents/README.md` § Model routing | `TestAgentSetupParity` and `TestAgentSetupParitySensitivity` (`internal/harness/contract/agentsetup_test.go`, in `task verify`) for the Claude and Codex adapters; Gemini routing is review only |
| `AGENTS.md` under 24,000 bytes and matching this file; adapters thin and pointing to their contract | `.agents/README.md` § Read-only parity smoke | `TestAgentSetupParity` and `TestAgentSetupParitySensitivity` (`internal/harness/contract/agentsetup_test.go`, in `task verify`); semantic correspondence of individual rows is review only |
| Claim branches update by merging `origin/main`, never rebase or force-push | `.agents/protocol.md` "Land" | review only |
| Cited evidence can be opened on another machine; ignored paths named local only | `semsparql-handoff` and `semsparql-preflight` skills | review only |
| Docs for a working developer: terms defined, no jargon, no marketing | technical-writer contract; reviewer contract | review only; `task docs:check` covers Markdown form only |
| Task choices not in the design go in `openspec/changes/<id>/notes/<task>.md`, linked from a PR comment | developer contract § Handoff; `.agents/protocol.md` "Land" | review only |
