# Bootstrap SemSparql sidecar

## Why

[Issue #1](https://github.com/C360Studio/semsparql/issues/1) defines Stage 3: a read-only SPARQL and JSON-LD
edge for DIEKM, BFO and CCO adopters, with RDF export for external SHACL validation. The repository currently
contains agent guidance and documentation tooling, but no Go runtime. This change records the proposed target
before implementation, keeping semantic-web execution outside SemEngine core (ADR-107).

## What Changes

- Define identity mapping, virtual RDF planning, projection folding/unfolding, and HTTP query/export requirements.
- Preserve issue #1's phased tasks, early SELECT/ASK MVP, proving tests and optional deferred KV reader.
- Pin the planned SemEngine dependency to **`v0.1.0-alpha.2`**, resolving to
  `11da4721c2233a8e2b5b1d6236a97a44e06abbb6`, as explicitly requested by the user.
- Keep bootstrap implementation blocked on SemVocab #1 merging and a released SemVocab tag being selected.
- Record unproved exporter and server-capability seams before they become implementation assumptions.

This is a local documentation draft. It adds no `go.mod`, runtime, dependency change, or current capability spec.
Drafting does not approve implementation. Do not archive or promote these deltas until the runtime exists and
its tasks are complete. A draft-document PR would address issue #1, not close it.

## Capabilities

### New Capabilities

- `iri-mapping`: exporter-identical entity IRIs, inverse validation, and stable projection blank-node identities.
- `bgp-planner`: read-only readers, vocabulary lookup, virtual types, bounded query evaluation and solution semantics.
- `reification-bridge`: complete-chain folding, equivalent fallback evaluation and typed RDF graph export.
- `sparql-protocol`: supported query subset, HTTP negotiation, export endpoints, limits and safe exposure.

### Modified Capabilities

None. No current capability specs exist.

## Impact

Future packages: `irimap`, `reader`, `sparql`, `server`, and `cmd/semsparql`; future docs cover architecture and
external SHACL gates. The existing agent/tooling setup is retained; Go gates and adapter parity checks join task 0.1.
SemEngine supplies graph query verbs, validators, registry metadata and RDF serialization. SemVocab supplies packs.
No upstream checkout is modified. No aliases, updates, named graphs, federation, embedded SHACL engine, or local
RDF writers are added. Full SPARQL 1.1 language support is not claimed; the HTTP protocol serves the v1 subset.

## Draft acceptance

The deliverable is proposal, design, task list, evidence inventory and four validated capability deltas.
Acceptance checks are `task verify`, `task spec:queue`, and whitespace checks, plus independent draft review.
Stop at the local draft: owner design acceptance, SemVocab release, runtime work and publication remain separate.
