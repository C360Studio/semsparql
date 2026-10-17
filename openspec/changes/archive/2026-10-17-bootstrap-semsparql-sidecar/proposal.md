# Bootstrap SemSparql sidecar

## Why

[Issue #1](https://github.com/C360Studio/semsparql/issues/1) defines Stage 3: a read-only SPARQL and JSON-LD
edge for DIEKM, BFO and CCO adopters, with RDF export for external SHACL validation. This change delivers the
complete Stage 3 bootstrap sidecar (`0.1`–`4.2`, `5.1`, and `6.1`), keeping semantic-web execution outside SemEngine
core (ADR-107).

## What Changes

- Define and implement identity mapping, virtual RDF planning, projection folding/unfolding, and HTTP query/export
  capabilities (`iri-mapping`, `bgp-planner`, `reification-bridge`, and `sparql-protocol`).
- Pin SemEngine in `go.mod` to **`v0.1.0-alpha.2`** (`11da4721c2233a8e2b5b1d6236a97a44e06abbb6`) and SemVocab
  to **`v0.1.0`** (`64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6`).
- Complete Tasks `0.1`–`0.2`, `D.1`–`D.2`, `1.1` (`irimap`), `1.2` (`reader.GraphReader` and `reader.MemoryReader`),
  `2.1` (`sparql` AST and parser), `2.2` (`sparql` BGP planner and executor), `2.3` (`server` `/sparql` endpoint,
  negotiation, and limits), `3.1` (`sparql` virtual `rdf:type` and `rdfs:subClassOf*` resolution and startup
  `ValidateSuperclassClosure`), `3.2` (`sparql` complete-chain `ProjectionSpec` folding and virtual-unfolded fallback),
  `3.3` (`sparql` `CONSTRUCT`/`DESCRIBE`, `UnfoldEntity`, terminal datatype propagation, and invalid-triple validation;
  `server` `/entities/{entity_id}.jsonld` and `POST /export`; `docs/architecture.md` and
  `docs/shacl-compliance-gate.md`), `4.1` (`reader.NATSRPCReader` and `cmd/semsparql`), `4.2` (`-tags=integration` live
  NATS/DIEKM end-to-end suite in `server/diekm_integration_test.go`), `5.1` (formal deferral of optional
  `KVWatchReader`), and `6.1` (reconciliation and OpenSpec archive).

## Capabilities

### New Capabilities

- `iri-mapping`: exporter-identical entity IRIs, inverse validation, and stable projection blank-node identities.
- `bgp-planner`: read-only readers, vocabulary lookup, virtual types, bounded query evaluation and solution semantics.
- `reification-bridge`: complete-chain folding, equivalent fallback evaluation and typed RDF graph export.
- `sparql-protocol`: supported query subset, HTTP negotiation, export endpoints, limits and safe exposure.

### Modified Capabilities

None.

## Impact

Adds `go.mod`, `go.sum`, `revive.toml`, `scripts/`, `internal/harness/contract/`, `irimap/`, `reader/`, `sparql/`,
`server/`, `cmd/semsparql/`, `docs/architecture.md`, and `docs/shacl-compliance-gate.md`. SemEngine supplies graph
query verbs, validators, registry metadata and RDF serialization. SemVocab supplies `bfo-cco-core` and `diekm` packs
and embedded SHACL shapes. No upstream checkout is modified. No aliases, updates, named graphs, federation, embedded
SHACL engine, or local RDF writers are added. Full SPARQL 1.1 language support is not claimed; the HTTP protocol serves
the v1 subset.

## Acceptance

Acceptance checks are `task doctor` and `task verify` (running `spec:check`, `docs:check`, `fmt:check`, `tidy:check`,
`cleanup-roots:check`, `build`, `vet`, `lint`, `vuln`, `test:unit`, `test:integration`, `cover:check`, and
`test:repeat`) on a clean working tree.
