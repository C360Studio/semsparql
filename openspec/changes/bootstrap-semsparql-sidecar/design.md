# Bootstrap sidecar design draft

## Status and evidence

This translates [issue #1](https://github.com/C360Studio/semsparql/issues/1) into proposed target behavior.
It is not an approved design or an implemented feature. The complete [inventory](inventory.md) is retained as
reviewed evidence, including collisions, adopter findings and unproved seams; nothing there is superseded silently.
Independent inventory review returned `INVENTORY PASS` at SHA-256
`3c89c52a5e7491ab764a60a3473d79f9e7a42af56c05dbbb023bc61c870d5c09`. Base is `b64f35e989a1b3fd12d81629ae1c339bf7ade823`.
Tests cited in the inventory were read, not run. No new library timing, failure or ordering guarantee is inferred.

## Dependencies and integration order

The module is `github.com/c360studio/semsparql` (`go.mod` on `go 1.26.9`, added in task 0.1). SemEngine is pinned
to `v0.1.0-alpha.2` (`11da4721c2233a8e2b5b1d6236a97a44e06abbb6`), and SemVocab is pinned to `v0.1.0`
(`64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6`), tagged after `semvocab#6` merged Phase 1 BFO/CCO catalogs and built-in
packs (`semvocab#1` remains open as the Stage 2 epic for Phase 2's ahead-of-time SHACL compiler, which SemSparql does
not import). The tagged SemEngine inventory confirms `SubjectIRI`, blank-node/full-predicate export, and prefix
`Pattern` support. The tagged SemVocab inventory confirms `packs.BFOCCOCoreSpec()` / `packs.RegisterBFOCCOCore(...)`
and `diekm.Spec()` / `diekm.Register(...)` / `diekm.Shapes()`, with BFO 2020 bound to `obo` (`semvocab#1` ruling Q9),
full BFO 2020 + CCO v2.2 superclass closures on `intel.platform` and `intel.observation`, and projected predicates
`intel.role.assigned` and `intel.measurement.altitude-m` (hyphenated per `semvocab#1` ruling Q11). No local
replacement, pseudo-version, fork or copy of either dependency is permitted (`TestPinnedDependencies`).

PR #3 changes CI setup-node; PR #4 changes OpenSpec tooling. Neither overlaps these paths or capabilities.
There is no required merge order with this change; if #4 lands first, revalidate with its new tooling pin.
Implementation proceeds 0 → 1 → 2 (MVP) → 3 → 4. Task 0.1 (`go.mod`, Go gates, and `internal/harness/contract`
structural guards), task 0.2, task 1.1 (`irimap`), task 1.2 (`reader.GraphReader` and `reader.MemoryReader`), task 2.1
(`sparql` AST and parser), task 2.2 (`sparql` plain-BGP `SELECT`/`ASK` planner and executor), and task 2.3 (`server`
`GET`/`POST /sparql`, `SELECT`/`ASK` result negotiation and serialization, bounded input/query budgets, and
localhost/auth bind defaults) are complete; each remaining issue task is a reviewable implementation PR. Phase 5 is
optional and separately decided, never an implicit prerequisite for the initial sidecar.

## Options considered

1. Wait for all D.1/D.2 runtime rulings before landing `go.mod` and Go verification gates. This leaves the repository
   without `go.mod`, `TestAgentSetupParity`, or structural guards even after `semvocab v0.1.0` is tagged.
2. Pin `semengine v0.1.0-alpha.2` and `semvocab v0.1.0` in `go.mod` now and complete task 0.1's Go verification gates
   and structural guards while keeping runtime tasks (1.1–4.2) gated on their D.1/D.2 holds. This is the owner-selected
   scope.
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

`irimap` (`Mapper`, `CanonicalNTriplesObject`, `BlankNodeID`), `reader` (`GraphReader`, `MemoryReader`,
`MatchAllPattern`, `ErrNilContext`, `ErrEntityNotFound`, `ErrScanBudgetExceeded`), `sparql` AST/parser and plain-BGP
`SELECT`/`ASK` planner/executor (`Parse`, `Query`, `PlanBGP`, `Executor`, `ErrSyntax`, `ErrUnsupportedFeature`,
`ErrResultLimitExceeded`, `ErrScanBudgetExceeded`, `ErrQueryTimeout`), and `server` `/sparql` `SELECT`/`ASK` MVP
(`Server`, `Config`, `ValidateConfig`, `NegotiateAccept`, `WriteSelectAskResult`, `ErrNotAcceptable`,
`ErrUnsafeExposure`, `ErrQueryTooLarge`) are implemented in tasks 1.1–2.3; the remaining rows are proposed surfaces
from issue #1. There is no additional public API for hypothetical users.

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
Merge explicit and virtual RDF type triples as a graph set. Per owner ruling D.1c, standalone `?sub rdfs:subClassOf*
?sup` (not paired with `rdf:type`) evaluates over the finite registered ontology class graph in `vocabulary`
(`ClassIRI` and `Superclasses` across `ListRegisteredEntityTypes()`): reflexive zero-length `(C, C)` holds for every
registered class/superclass IRI `C` (and for any bound IRI `C`), and `(sub, sup)` holds whenever `sub == sup` or `sup`
is in `sub`'s registered `Superclasses`.

Complete projection chains fold only when every declared step, optional class constraint and terminal predicate
is present. Bind intermediate variables to deterministic blank nodes even when projected. Partial chains, extra
constraints/FILTERs on intermediate nodes and variable predicates evaluate the virtual unfolded graph instead.
Every ambiguous ProjectionSpec match participates; folding must preserve the unfolded graph's solution multiset,
not manufacture duplicate graph triples. The independent oracle is a materialized unfolded fixture graph.

Blank-node identity follows issue #1:

```text
_:b_<hex(sha256(entityID NUL predicate NUL canonicalObject NUL stepIdx))[:32]>
```

Per owner ruling D.1e, `stepIdx` is encoded as a 0-based decimal ASCII string (`strconv.Itoa(i)`: `"0"`, `"1"`, `"2"`,
...) matching `ProjectionSpec.Steps[i]`. The canonical object is its N-Triples term. D.2 probe
`TestProbeCanonicalNTriplesObjectSeam` confirms that calling `export.SerializeToString` in `export.NTriples` format
with `export.WithBaseIRI(baseIRI)` on a single source triple `{Subject: "_:s", Predicate: pred, Object: obj,
Datatype: dt}` yields `_:s <predIRI> <canonicalObject> .\n` (where `predIRI` contains no `>`), providing an
exporter-owned canonical object token without copying RDF classification or escaping logic. Different values of one
predicate do not collide.

CONSTRUCT, DESCRIBE and export selection unfold applicable projections and virtual types, then use
`export.Serialize` for Turtle, N-Triples or JSON-LD. No RDF graph writer is added here.
Preserve the terminal RDF datatype/resource meaning. D.2 probe `TestProbeTerminalDatatypePropagationSeam` proves
that blindly copying pragmatic `vocabulary.PredicateMetadata.DataType` (`"float"`, `"datetime"`, `"entity_id"`) into
`Triple.Datatype` emits invalid RDF (`^^<float>`), and leaving `Triple.Datatype` empty on an unfolded terminal triple
whose `Predicate` is a full `ValuePredicateIRI` loses declared `datetime`/`int`/`json` types. Normalizing the terminal
`(Object, Datatype)` from the exporter's own N-Triples classification of the source triple preserves exact RDF object
semantics across N-Triples, Turtle, and JSON-LD. D.2 probe `TestProbeSerializerInvalidTripleOmissionSeam` confirms that
checking each triple with single-triple `export.SerializeToString(..., export.NTriples, ...)` detects 100% of triples
that `export.Serialize` would silently omit (`Subject == ""`, `Predicate == ""`, `Object == nil`, `NaN`/`Inf` floats)
so the sidecar fails loudly before writing an HTTP 200 response. Per owner ruling D.1d, `DESCRIBE <iri>` and
`DESCRIBE ?var` emit the outgoing Concise Bounded Description (CBD) for each bound canonical entity IRI: all stored
triples where the entity is subject, virtual `rdf:type` triples (`ClassIRI` + registered `Superclasses`), and all
unfolded reification blank-node chains (`_:b_...` triples). Non-entity IRIs or entity IDs absent from `GraphReader`
contribute 0 triples (no HTTP 404 on `DESCRIBE`), and no incoming-reference scan is performed.

## HTTP behavior and operator defaults

Support SELECT, ASK, CONSTRUCT and DESCRIBE; PREFIX/BASE; BGP, OPTIONAL, UNION and the issue's FILTER subset;
DISTINCT, ORDER BY, LIMIT and OFFSET. Paths are restricted to `rdf:type/rdfs:subClassOf*` and `rdfs:subClassOf*`.
Unsupported features return a 400 naming the feature, including updates, SERVICE, named graphs, aggregates,
GROUP BY, subqueries, VALUES, BIND and arbitrary paths. This is a subset, not full SPARQL language conformance.

GET query parameters, raw application/sparql-query POST and form POST feed the same parse/evaluate path.
SELECT/ASK negotiate applicable SPARQL JSON/XML or CSV/TSV result formats; graph forms negotiate Turtle,
N-Triples or JSON-LD. Format/query-form compatibility is tested; never serialize graphs as result tables.
Per owner ruling D.1a, an `Accept` header incompatible with the query form returns `406 Not Acceptable`.
`GET /entities/{entity_id}.jsonld` returns unfolded triples and virtual types, 400 for invalid IDs, 404 for missing.
`POST /export` accepts exactly one prefix or CONSTRUCT selector and negotiates graph output. No GET export route.
SHACL runs in an external tool; documentation demonstrates producing a sh:ValidationReport there.

Defaults are 10s per query, 10,000 results and 100,000 entities read. Per owner ruling D.1b, the default maximum
SPARQL query length (GET `query` parameter or POST body) is `64 KiB` (65,536 bytes) and the default maximum
`POST /export` JSON selector body is `256 KiB` (262,144 bytes); requests exceeding these caps are rejected with
`413 Content Too Large`. `max_results` (default 10,000) counts solution bindings for `SELECT` (bounding both output
rows and the internal pre-`ORDER BY` / pre-`DISTINCT` candidate buffer), 1 boolean result for `ASK`, and deduplicated
output RDF triples (including virtual `rdf:type` and unfolded blank-node triples) for `CONSTRUCT`, `DESCRIBE`, and
`POST /export`. A query LIMIT is not permission to breach resource limits.
Per owner ruling D.1a, by default a breached `max_results` or `max_scan_entities` budget returns
`422 Unprocessable Entity` and a breached query timeout returns `504 Gateway Timeout`, with an error body naming the
breached budget (`result_limit_exceeded`, `scan_budget_exceeded`, `query_timeout`) and no partial success. Only
explicit `X-Semsparql-Allow-Truncation: true` permits partial output when `max_results` or `max_scan_entities` is
reached, returning a buffered `200 OK` with `X-Semsparql-Truncated: true` and
`X-Semsparql-Truncation-Reason: result_limit|scan_budget`. Oversized or invalid requests never become partially
evaluated successes. Test all defaults and boundaries.

Bind localhost by default. Refuse non-local binds without configured bearer-token or mTLS protection.
Validate configuration, pack ancestor closure and remote pattern capability before readiness. D.2 probe
`TestProbeServerPatternCapabilitySeam` verifies that sending `PrefixQueryRequest{Pattern: "invalid", Limit: 1}` over
`graph.ingest.query.prefix` returns classified `errs.ErrorInvalid` (`semtypes.ErrorCodeEntityIDPatternInvalid`) with
zero bucket I/O on a pattern-capable server, whereas a legacy pre-#259 server ignores the unknown `"pattern"` field
and returns `err == nil`. Pairing that negative probe with `PrefixQueryRequest{Pattern: "*.*.*.*.*.*", Limit: 1}`
establishes server `Pattern` capability at startup without a full-prefix filtering fallback.
Contexts enter operations as arguments; root context belongs to main. Cancellation must stop work, with bounded
shutdown and no detached query workers. Lifecycle tests assert observable completion, not a chosen lock design.

## Adoption and declared costs

Shared identities are consumed by planner, SELECT bindings, CONSTRUCT and every export endpoint; shared limits
apply to query and export paths; GraphReader covers all reads. These are in-change adopters, with no existing
runtime callers to migrate. No additional reusable primitive or cross-repository migration is introduced.
Per owner ruling D.1e, optional Task `5.1` (`KVWatchReader`) is formally deferred to a separate follow-up change after
Phases 1–4 ship; `bootstrap-semsparql-sidecar` remains stateless (`MemoryReader` and `NATSRPCReader` only).

Virtual fallback and joins can be expensive; no performance claim is made without measurement. Stable ordering
may need bounded materialization. Budget checks must prevent memory growth from silently exceeding policy.
Operators discover unsafe binds/dependency failures at startup; clients discover unsupported features and budget
failures through protocol errors. Correctness must not depend on reading documentation or predicting scan size.

## Rulings and unresolved decisions

| Source / decision | Treatment |
| --- | --- |
| User: draft from issue #1 + execute 0.1, D.2, and D.1 | Four capability deltas; 0.1, 0.2, D.1, D.2, and 5.1 completed; runtime tasks 1.1–4.2 unblocked |
| User: semengine v0.1.0-alpha.2 | Pinned in `go.mod` (`11da4721c2233a8e2b5b1d6236a97a44e06abbb6`) |
| User: semvocab v0.1.0 | Pinned in `go.mod` (`64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6`); pack seams inspected |
| Existing agent setup merged in PR #2 | Reconciled with Go gates and `internal/harness/contract` structural guards in task 0.1 |
| Issue #1 datatype recipe vs pin evidence | Resolved in D.2 (`TestProbeTerminalDatatypePropagationSeam`): derive terminal `(Object, Datatype)` from exporter N-Triples rendering of the source triple |
| D.1a HTTP error & truncation protocol | 400 invalid/unsupported, 406 incompatible Accept, 413 oversized input, 422 result/scan budget breach, 504 timeout; opt-in truncation emits `X-Semsparql-Truncated: true` and `X-Semsparql-Truncation-Reason: result_limit\|scan_budget` |
| D.1b Input byte caps & result counting | 64 KiB max SPARQL query, 256 KiB max `/export` body; `max_results` counts bindings for `SELECT` and deduplicated triples for graph forms |
| D.1c Standalone `rdfs:subClassOf*` | Evaluates over the registered `ClassIRI` + `Superclasses` closure in `vocabulary` with reflexive `(C, C)` |
| D.1d `DESCRIBE` scope | Outgoing entity CBD + virtual `rdf:type` + unfolded blank-node chains; absent/non-entity IRIs yield 0 triples |
| D.1e `BlankNodeID` `stepIdx` & optional `5.1` | 0-based decimal ASCII `stepIdx` (`strconv.Itoa(i)`); `KVWatchReader` (`5.1`) deferred to a separate change |

All D.1 consumer-contract rulings and D.2 upstream probes are complete; runtime tasks `1.1`–`4.2` have no open holds.
