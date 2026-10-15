# Bootstrap sidecar design draft

## Status and evidence

This translates [issue #1](https://github.com/C360Studio/semsparql/issues/1) into proposed target behavior.
It is not an approved design or an implemented feature. The complete [inventory](inventory.md) is retained as
reviewed evidence, including collisions, adopter findings and unproved seams; nothing there is superseded silently.
Independent inventory review returned `INVENTORY PASS` at SHA-256
`3c89c52a5e7491ab764a60a3473d79f9e7a42af56c05dbbb023bc61c870d5c09`. Base is `b64f35e989a1b3fd12d81629ae1c339bf7ade823`.
Tests cited in the inventory were read, not run. No new library timing, failure or ordering guarantee is inferred.

## Dependencies and integration order

The planned module is `github.com/c360studio/semsparql`. SemEngine is pinned to `v0.1.0-alpha.2`
(`11da4721c2233a8e2b5b1d6236a97a44e06abbb6`), a published prerelease explicitly selected for this draft.
On 2026-10-15, GitHub reports semengine #235 merged, #256/#259 closed, and SemVocab #1 open with no tags.
The tagged inventory confirms SubjectIRI, blank-node/full-predicate export, and prefix Pattern support.
No SemVocab version is invented. Task 0.1 waits for its merge and release; later tasks depend on 0.1.
No local replacement, pseudo-version, fork or copy of either dependency is permitted.

PR #3 changes CI setup-node; PR #4 changes OpenSpec tooling. Neither overlaps these paths or capabilities.
There is no required merge order with this draft; if #4 lands first, revalidate with its new tooling pin.
Implementation proceeds 0 → 1 → 2 (MVP) → 3 → 4. Each issue task remains a reviewable implementation PR.
Phase 5 is optional and separately decided, never an implicit prerequisite for the initial sidecar.

## Options considered

1. Wait for SemVocab before writing any specification. This avoids provisional pack seams but delays review of
   issue #1's existing contracts. It does not satisfy the requested draft now.
2. Draft the issue's contracts now, preserve dependency/evidence holds, and implement only after approval and
   release readiness. This is the proposed approach and the scope of this work.
3. Extend an existing upstream owner when a consumed seam proves missing, then consume a released fix. This is
   the required route for exporter conversion or query capability gaps; it may require a later owner-approved
   pin change. Local reimplementation would violate the boundary and is not an option.

## Responsibilities and consumer surfaces

| Component | Job | Consumer / requirement home |
| --- | --- | --- |
| `irimap.Mapper` | Delegate forward mapping to SubjectIRI; validate inverse | Planner/exporter; iri-mapping requirements |
| `irimap.BlankNodeID` | Shared deterministic identity for folding and unfolding | Reification evaluator; iri-mapping requirements |
| `reader.GraphReader` | GetEntity, GetBatch, ScanPrefix, ScanPattern, EntitiesByPredicate | Planner; bgp-planner requirements |
| `MemoryReader` | Deterministic fixtures using shared match rules | Unit/property tests; reader parity scenario |
| `NATSRPCReader` | Declared entity, batch, prefix RPCs | Running sidecar; read-only reader requirement |
| `sparql` | AST/parser, type resolution, folding, planning, evaluation, graph construction | HTTP clients through server; all four capabilities |
| `server` | GET/POST /sparql, entity JSON-LD, POST /export | SPARQL clients and external SHACL operators |
| `cmd/semsparql` | Dependency registration, NATS/HTTP composition and lifecycle | Service operators; safe startup requirement |

These are proposed surfaces from issue #1, not existing symbols. Exact Go signatures, CLI flags and config keys
require consumer-contract review before implementation. There is no additional public API for hypothetical users.

## Read-only query execution

All graph reads pass through GraphReader. Remote reads use only verbs from `graph.QueryVerbs()`, with the
existing exact reader and classified RPC helpers. There are no bucket writes or mutation publishes.
Entity IDs, literal prefixes and six-position patterns use their distinct upstream validators at entry.
An intentional empty match-all prefix is a separate bounded operation, not a value passed as a valid literal prefix.
`EntitiesByPredicate` cannot invent a graph-index subject: until a verb is declared it uses a bounded reader scan.

Plan constrained access first: exact entity → type pattern → predicate selection → bounded scan.
Use opaque cursors until the server reports exhaustion; a short page is not exhaustion. Track missing batch
entities explicitly rather than treating a successful RPC as a complete hydration. Propagate classified failures.
Reads are live, not a transactional snapshot: inserts before an upstream keyset cursor may be missed. Determinism
means stable ordering for the same observed data, not consistency across concurrent graph mutations.
The scan budget bounds entities read, not SemEngine's internal key enumeration; that upstream cost is unbounded
by this budget and must not be advertised otherwise.

Ordinary flat-predicate lookup expands all dotted predicates sharing one StandardIRI. An unknown ordinary
predicate gives no matches only after virtual type and projection/unfolded-view handling has been considered.
No owl:sameAs/skos:exactMatch alias matching is introduced. Graph triples and ID/term sets are deduplicated as sets;
SPARQL solution sequences preserve multiplicity unless DISTINCT applies. UNION branch duplicates survive.
Order solutions deterministically before LIMIT/OFFSET, honoring explicit ORDER BY and stable ties.
REDUCED is not in issue #1's v1 subset and is rejected, not silently treated as DISTINCT.

## Identity, types and reification

Parse IDs with upstream named fields in `org.platform.system.domain.type.instance` order. Forward IRIs come from
`export.SubjectIRI`; the inverse accepts only the configured base's canonical entity IRIs and returns
`ErrNotEntityIRI` otherwise. One base is shared with exporter options. Never duplicate forward mapping.

Type metadata supplies ClassIRI and registered superclass membership. Plain `a` queries include registered
superclasses as issue #1's RDFS entailment policy. Bound type scans use `org.platform.*.domain.type.*`; absent
org/platform constraints become wildcards. Use server-side Pattern support, with no full-prefix filtering fallback.
Startup checks ancestor closure when a superclass is itself registered; packs must register all known ancestors.
Merge explicit and virtual RDF type triples as a graph set. Subclass paths have zero-length identity behavior;
standalone path evaluation over the registered class graph still needs an exact consumer contract before task 3.1.

Complete projection chains fold only when every declared step, optional class constraint and terminal predicate
is present. Bind intermediate variables to deterministic blank nodes even when projected. Partial chains, extra
constraints/FILTERs on intermediate nodes and variable predicates evaluate the virtual unfolded graph instead.
Every ambiguous ProjectionSpec match participates; folding must preserve the unfolded graph's solution multiset,
not manufacture duplicate graph triples. The independent oracle is a materialized unfolded fixture graph.

Blank-node identity follows issue #1:

```text
_:b_<hex(sha256(entityID NUL predicate NUL canonicalObject NUL stepIdx))[:32]>
```

The canonical object is its N-Triples term. Stable step-index encoding and the exporter-owned normalization seam
must be fixed by task 1.1 evidence before implementation. Different values of one predicate must not collide.

CONSTRUCT, DESCRIBE and export selection unfold applicable projections and virtual types, then use
`export.Serialize` for Turtle, N-Triples or JSON-LD. No RDF graph writer is added here.
Preserve the terminal RDF datatype/resource meaning. Issue #1's suggestion to blindly copy DataType is not proven
at this tag: pragmatic `datetime`/`entity_id` metadata differs from explicit RDF datatypes and `@id`.
Probe that seam, the canonical object seam, and invalid-triple omission before claiming complete output.
If upstream has no usable seam, file the gap there and await a release; do not create local conversion machinery.
DESCRIBE selection scope must be accepted before task 3.3; this draft does not promise an unspecified graph shape.

## HTTP behavior and operator defaults

Support SELECT, ASK, CONSTRUCT and DESCRIBE; PREFIX/BASE; BGP, OPTIONAL, UNION and the issue's FILTER subset;
DISTINCT, ORDER BY, LIMIT and OFFSET. Paths are restricted to `rdf:type/rdfs:subClassOf*` and `rdfs:subClassOf*`.
Unsupported features return a 400 naming the feature, including updates, SERVICE, named graphs, aggregates,
GROUP BY, subqueries, VALUES, BIND and arbitrary paths. This is a subset, not full SPARQL language conformance.

GET query parameters, raw application/sparql-query POST and form POST feed the same parse/evaluate path.
SELECT/ASK negotiate applicable SPARQL JSON/XML or CSV/TSV result formats; graph forms negotiate Turtle,
N-Triples or JSON-LD. Format/query-form compatibility is tested; never serialize graphs as result tables.
`GET /entities/{entity_id}.jsonld` returns unfolded triples and virtual types, 400 for invalid IDs, 404 for missing.
`POST /export` accepts exactly one prefix or CONSTRUCT selector and negotiates graph output. No GET export route.
SHACL runs in an external tool; documentation demonstrates producing a sh:ValidationReport there.

Defaults are 10s per query, 10,000 results and 100,000 entities read. Body and query length are also bounded;
concrete byte limits, result-count meaning per query form, HTTP error codes and truncation signalling remain
pre-implementation decisions in task D.1. A query LIMIT is not permission to breach resource limits.
By default a breached budget produces an identifiable error and no successful partial result. Only explicit
`X-Semsparql-Allow-Truncation: true` can permit partial output, and the response must identify it as incomplete.
Oversized or invalid requests never become partially evaluated successes. Test all defaults and boundaries.

Bind localhost by default. Refuse non-local binds without configured bearer-token or mTLS protection.
Validate configuration, pack ancestor closure and remote pattern capability before readiness. The library pin
alone is not server-version evidence; startup detection remains an explicit task 4.1 design/probe requirement.
Contexts enter operations as arguments; root context belongs to main. Cancellation must stop work, with bounded
shutdown and no detached query workers. Lifecycle tests assert observable completion, not a chosen lock design.

## Adoption and declared costs

Shared identities are consumed by planner, SELECT bindings, CONSTRUCT and every export endpoint; shared limits
apply to query and export paths; GraphReader covers all reads. These are in-change adopters, with no existing
runtime callers to migrate. No additional reusable primitive or cross-repository migration is introduced.
The optional KV replica needs a separate inventory before selecting an index design; it must declare entity/memory
ceilings, startup refusal above capacity and last-applied revision freshness, and remain replaceable by graph-index.

Virtual fallback and joins can be expensive; no performance claim is made without measurement. Stable ordering
may need bounded materialization. Budget checks must prevent memory growth from silently exceeding policy.
Operators discover unsafe binds/dependency failures at startup; clients discover unsupported features and budget
failures through protocol errors. Correctness must not depend on reading documentation or predicting scan size.

## Rulings and unresolved decisions

| Source / decision | Draft treatment |
| --- | --- |
| User: draft from issue #1 | Four capability deltas and tasks 0.1–5.1; no runtime implementation |
| User: semengine v0.1.0-alpha.2 | Exact prerelease pin above; no dependency file yet |
| Issue #1 dependency gate | SemVocab merge/release hold retained on 0.1 |
| Existing agent setup merged in PR #2 | Reuse it; Go gates and parity remain pending |
| Issue #1 datatype recipe vs pin evidence | Desired RDF meaning retained; algorithm unapproved until probe resolves mismatch |
| Optional KV reader | Separate disposition task, not silently made mandatory |

Owner acceptance is still needed. D.1 resolves the remaining HTTP/path/DESCRIBE semantics before their dependent
implementation tasks. Export and startup capability evidence holds remain independently visible in tasks.md.
