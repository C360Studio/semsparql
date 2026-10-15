# Bootstrap SemSparql sidecar

## Why

[Issue #1](https://github.com/C360Studio/semsparql/issues/1) defines Stage 3: a read-only SPARQL and JSON-LD
edge for DIEKM, BFO and CCO adopters, with RDF export for external SHACL validation. This change records the
target specification and completes Task 0.1 (`go.mod` dependency pins, Go verification gates, and structural
guards), keeping semantic-web execution outside SemEngine core (ADR-107).

## What Changes

- Define identity mapping, virtual RDF planning, projection folding/unfolding, and HTTP query/export requirements.
- Preserve issue #1's phased tasks, early SELECT/ASK MVP, proving tests and optional deferred KV reader.
- Pin SemEngine in `go.mod` to **`v0.1.0-alpha.2`** (`11da4721c2233a8e2b5b1d6236a97a44e06abbb6`) and SemVocab
  to **`v0.1.0`** (`64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6`).
- Complete Task 0.1: add `go.mod`/`go.sum`, `revive.toml`, Go Taskfile/CI gates (`fmt`, `fmt:check`, `tidy:check`,
  `cleanup-roots:check`, `build`, `vet`, `lint`, `vuln`, `test:unit`, `test:repeat`, `test:integration`,
  `cover:check`), `internal/harness/contract` structural guards (`TestAgentSetupParity`,
  `TestPinnedDependencies`, `TestNoSleepsInTests`, `TestNoSkippedTests`, `TestNoHiddenTests`,
  `TestCleanupRootsCheckSensitivity`, `TestCoverCheck*`) with paired sensitivity tests, and reconcile `AGENTS.md`
  and `.agents/rules-index.md`.
- Record unproved exporter and server-capability seams as explicit holds before product runtime packages (`1.1`–`4.2`)
  begin.

No product runtime packages (`irimap`, `reader`, `sparql`, `server`, `cmd/semsparql`) or current capability specs
are added yet. Do not archive or promote these capability deltas until the runtime tasks are complete.

## Capabilities

### New Capabilities

- `iri-mapping`: exporter-identical entity IRIs, inverse validation, and stable projection blank-node identities.
- `bgp-planner`: read-only readers, vocabulary lookup, virtual types, bounded query evaluation and solution semantics.
- `reification-bridge`: complete-chain folding, equivalent fallback evaluation and typed RDF graph export.
- `sparql-protocol`: supported query subset, HTTP negotiation, export endpoints, limits and safe exposure.

### Modified Capabilities

None. No current capability specs exist.

## Impact

Task 0.1 adds `go.mod`, `go.sum`, `revive.toml`, `scripts/`, and `internal/harness/contract/`. Future tasks add
`irimap`, `reader`, `sparql`, `server`, and `cmd/semsparql`, plus architecture and external SHACL documentation.
SemEngine supplies graph query verbs, validators, registry metadata and RDF serialization. SemVocab supplies
`bfo-cco-core` and `diekm` packs and embedded SHACL shapes. No upstream checkout is modified. No aliases, updates,
named graphs, federation, embedded SHACL engine, or local RDF writers are added. Full SPARQL 1.1 language support
is not claimed; the HTTP protocol serves the v1 subset.

## Acceptance

For this slice (OpenSpec target + Task 0.1), acceptance checks are `task doctor`, `task verify` (running
`spec:check`, `docs:check`, `fmt:check`, `tidy:check`, `cleanup-roots:check`, `build`, `vet`, `lint`, `vuln`,
`test:unit`, `test:integration`, `cover:check`, and `test:repeat`), and `task spec:queue` on a clean working tree.
