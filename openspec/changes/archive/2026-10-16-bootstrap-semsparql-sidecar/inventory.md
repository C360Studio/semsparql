# Bootstrap sidecar inventory

base: b64f35e989a1b3fd12d81629ae1c339bf7ade823

SemEngine inspected: `v0.1.0-alpha.2`, commit `11da4721c2233a8e2b5b1d6236a97a44e06abbb6`.
SemVocab inspected: `v0.1.0`, commit `64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6` (`semvocab#1` closed via `semvocab#6`).
Scope: OpenSpec change packet + Task 0.1 (`go.mod`, Go verification gates, `TestAgentSetupParity`).
Source: [semsparql issue #1](https://github.com/C360Studio/semsparql/issues/1), read in full.
This materializes the architect's inventory, with line wrapping and condensed evidence tables.

## Current surface and claimed gap

At baseline `b64f35e989a1b3fd12d81629ae1c339bf7ade823`, `git ls-files` listed no `go.mod`, `*.go`, or runtime package.
Task 0.1 adds `go.mod` (`github.com/c360studio/semsparql`), Go verification gates, and `internal/harness/contract`
tests (`TestAgentSetupParity`, dependency pin checks, and gate sensitivity tests).
`git grep -n -E 'context.Context|/sparql|/export|GraphReader' -- '*.go' openspec/specs openspec/changes`
still returns no runtime callers, readers, parser, lifecycle owners, or local RDF writers before Phase 1.

- `AGENTS.md:11` describes the read-only SPARQL/JSON-LD sidecar purpose.
- `AGENTS.md:54` requires reads through `reader.GraphReader` and declared query verbs.
- `AGENTS.md:57` requires released dependency tags, without forks, replacements or pseudo-versions.
- `openspec/config.yaml:6` prohibits writes to `ENTITY_STATES` and `graph.mutation.*` publishes.

Upstream search (all upstream paths below refer to SemEngine at the pinned tag):

```sh
git --git-dir=/Users/coby/Code/c360/semengine/.git grep -n -E 'SPARQL|sparql|/sparql|SERVICE|CONSTRUCT' v0.1.0-alpha.2 -- graph vocabulary
```

Results are projection prose and GeoSPARQL mentions, not an execution API in those packages.
This is not a claim about every upstream package.

## Existing owners and tagged evidence

Prefix every upstream path in the first table below with `semengine@v0.1.0-alpha.2:`.

| Fact | Declaration | Test evidence |
| --- | --- | --- |
| Entity parsing | `pkg/types/entity_id.go:132`, `func ParseEntityID(s string) (EntityID, error)` | Entity contract tests in `pkg/types/entity_id_contract_test.go` |
| Literal prefix validation | `pkg/types/entity_id.go:189`, `func ValidateEntityIDPrefix(prefix string) error` | `entity_id_contract_test.go:171`, `TestValidateEntityIDPrefix` |
| Pattern validation | `pkg/types/entity_id.go:160`, `func ValidateEntityIDPattern(value string) error` | `entity_id_contract_test.go:118`, `TestValidateEntityIDPattern` |
| Pattern matching | `pkg/types/entity_id.go:169`, `func MatchEntityIDPattern(pattern, entityID string) (bool, error)` | `entity_id_contract_test.go:153`, `TestMatchEntityIDPattern`; `entity_id_prop_test.go:114` |
| Entity IRI mapping | `vocabulary/export/export.go:128`, `func SubjectIRI(subject, base string) string` | `export_test.go:394`, `TestSubjectIRI_MatchesDefaultExporter` |
| Canonical ID order | `vocabulary/export/export.go:138`, `base, eid.Org, eid.Platform, eid.System, eid.Domain, eid.Type, eid.Instance)` | `export_test.go:293`, `TestSubjectToIRIFollowsCanonicalOrder` |
| RDF graph serialization | `vocabulary/export/export.go:78`, `func Serialize(w io.Writer, triples []message.Triple, format Format, opts ...Option) error` | `export_test.go:476`, `TestSerialize_UnfoldedReifiedBlankNodeChain` |
| Full HTTP(S) predicate IRIs | Export resolution belongs to `vocabulary/export` | `export_test.go:649`, `TestResolvePredicateIRI_FullIRIPassThrough` |
| Predicate inverse lookup | `vocabulary/registry.go:446`, `func LookupByIRI(iri string) []string` | `vocabulary/lookup_iri_test.go:12`, `TestLookupByIRI` |
| Projection declaration | `vocabulary/projection.go:12`, `type ProjectionStep struct`; `:33`, `type ProjectionSpec struct` | `projection_test.go:85`, `TestProjectionSpecValidate` |
| Type metadata | `vocabulary/entity_type.go:15`, `type EntityTypeMetadata struct` | `entity_type_test.go:130`, `TestLookupEntityTypesByClassIRI` |
| Type enumeration | `vocabulary/entity_type.go:171`, `func ListRegisteredEntityTypes() []string` | Closure completeness is not promised |
| Class matching | `vocabulary/entity_type.go:196`, `if meta.ClassIRI == classIRI \|\| slices.Contains(meta.Superclasses, classIRI)` | Direct membership, not recursive traversal |
| Exact entity reader | `graph/exact_entity.go:37`, `func NewExactEntityReader(` | `exact_entity_test.go:58`, `TestExactEntityReaderReturnsValidatedEntityAndRevision` |
| Batch completeness | `graph/query_batch_types.go:43–53`, response includes `Missing []MissingEntity` | `processor/graph-ingest/query.go:154–158` populates missing entities; successful RPC alone is not completeness |
| Declared query verbs | `graph/query_verbs.go:29`, `func QueryVerbs() []QueryVerb` | `graph/query_verbs_test.go` |
| Prefix/pattern request | `graph/query_prefix_types.go:45`, `type PrefixQueryRequest struct`; `:55`, `Pattern string` | `query_prefix_types_test.go:125`, `TestPrefixQueryRequestPatternJSONRoundTrip` |
| Server pattern filtering | `processor/graph-ingest/query.go:279`, `keys, err = c.entityBucket.KeysByFilter(ctx, req.Pattern)` | `query_prefix_integration_test.go:215`, `TestIntegration_PrefixQuery_PatternAcrossSystems` |
| Pattern pagination | `graph/query_prefix_types.go:63`, `Cursor string`; `:80`, `NextCursor string` | `query_prefix_unit_test.go:397`, `TestQueryPrefix_PatternCursorPaging` |
| Classified RPC | `natsclient/errors.go:232`, `func (c *Client) RequestClassified(...)` | `errors_integration_test.go:24`, `TestIntegration_RequestClassified_RoundTripPreservesClass` |

`graph/query_verbs.go` declares only entity, batch, and prefix; no declared predicate/traversal verb is available.
`graph/query_prefix_types.go:24` prohibits parsing, constructing or modifying opaque cursors.
Lines 35–38 document keyset pagination: inserts before the cursor can be missed between requests.
Line 42 says the backend fetches all matching keys; entity budgets do not bound upstream key enumeration.
Lines 10 and 17 default/cap pages at 1,000. Short pages are not proof of exhaustion.
The literal-prefix validator rejects empty input and wildcards; `PrefixQueryRequest` separately permits an empty
prefix as a match-all sentinel. That sentinel is not a valid literal prefix and needs an explicit bounded-read contract.

Prefix every upstream path in the table below with `semvocab@v0.1.0:`.

| Fact | Declaration | Test evidence |
| --- | --- | --- |
| BFO/CCO core pack spec & registration | `packs/packs.go:25`, `func BFOCCOCoreSpec() (vocabulary.PackSpec, error)`; `:38`, `func RegisterBFOCCOCore(opts ...vocabulary.PackOption) error` | `packs/packs_test.go`, `internal/harness/contract/deps_test.go` |
| DIEKM pack spec, registration & SHACL shapes | `packs/diekm/diekm.go:34`, `func Spec() (vocabulary.PackSpec, error)`; `:49`, `func Register(opts ...vocabulary.PackOption) error`; `:62`, `func Shapes() []byte` | `packs/diekm/diekm_test.go`, `internal/harness/contract/deps_test.go` |
| BFO 2020 & CCO v2.2 prefixes | `packs/diekm/diekm.yaml:9–10`, `obo: http://purl.obolibrary.org/obo/` and `cco: https://www.commoncoreontologies.org/` | `packs/diekm/diekm_test.go` |
| DIEKM entity types & superclass closure | `packs/diekm/diekm.yaml:24` (`intel.platform`) and `:35` (`intel.observation`), each with full BFO 2020 + CCO v2.2 superclass closures | `packs/diekm/diekm_test.go` |
| DIEKM projections | `packs/diekm/diekm.yaml:46` (`intel.role.assigned`, 1-step) and `:58` (`intel.measurement.altitude-m`, 3-step ending in `https://w3id.org/c360/diekm#altitudeMetersValue`) | `packs/diekm/diekm_test.go` |

## Adjacent claims

Issue #1 defines `sparql-protocol`, `bgp-planner`, `reification-bridge`, and `iri-mapping`.
Task 0.1 pins `github.com/c360studio/semengine v0.1.0-alpha.2` and `github.com/c360studio/semvocab v0.1.0` in
`go.mod` and verifies their exported seams in `internal/harness/contract/deps_test.go`.
SemEngine #235 is merged; #256/#259 are closed and their surfaces exist in the inspected tag.
SemVocab #1 is closed via PR #6 and released at `v0.1.0`.

`docs/adr/107-semantic-web-vocabulary-lives-at-the-export-edge.md:59` states:
"Semantic-web vocabulary belongs at the export edge".
Lines 11–12 place packs in SemVocab and SPARQL execution in SemSparql.

GitHub open PR listing and complete file lists, checked 2026-10-15:

- PR #3: one file, `.github/workflows/ci.yml`, setup-node update.
- PR #4: two files, `package.json` and `package-lock.json`, OpenSpec update.

Neither claims runtime capabilities or overlaps this change's paths. Neither listing is truncated.

## Named consumers

No production callers exist here yet. Issue #1 names:

- SPARQL client authors: GET/form/raw POST `/sparql`, supported subset and result negotiation.
- DIEKM/BFO/CCO adopters: virtual types and reification.
- Offline SHACL operators: entity JSON-LD, `POST /export`, CONSTRUCT/DESCRIBE graphs.
- Service operators: bind/auth, base IRI, NATS, timeout, results, scan and input-size controls.

Issue #1 names planned internal consumers for mapper, reader, parser/planner/executor and unfolding.
No CLI flags or configuration key names are approved or implemented.

## Problem shapes and collisions

Closest admit/refuse examples: `graph/exact_entity_test.go:24`, invalid ID before transport, and
`:43`, nil context before transport. Closest bounded read is prefix pagination above.
Closest classified failure is `RequestClassified`. Existing shared interpreters are entity parsing,
exporter object classification, registry lookup and projection metadata.

| Dimension | Existing owner and evidence |
| --- | --- |
| Semantic class | Entity selection: graph-ingest; RDF interpretation: exporter; metadata: vocabulary |
| Owners | `graph/query_verbs.go:29`, `processor/graph-ingest/query.go:279`, `vocabulary/export/object.go:88`, `vocabulary/entity_type.go:186` |
| Status | Exact replies validate revision (`graph/exact_entity_test.go:85`); prefix offers continuation, not snapshots |
| Lifecycle | No local reader/cache; upstream queries are request scoped; deferred KV lifecycle is unproved |
| Readers | Query RPC clients, exporter callers; local runtime callers absent |
| Writers | Graph-ingest persists state; `vocabulary/projection.go:31–32` describes expansion without persisted blank nodes |
| Recovery | Keyset caveat documented; snapshot consistency and cache reconnect/resync not promised |

Adjacent declarations exist in `graph/index_status.go`, `graph/query_index_types.go` and
`graph/readiness/index_status.go`; they are not evidence of a declared index query verb.
`git ls-tree -r --name-only v0.1.0-alpha.2` found no `processor/graph-index` implementation.
Optional task 5.1 needs a deeper owner/lifecycle/freshness inventory before design or implementation.

## Boundary check

| Boundary | Current status |
| --- | --- |
| Zero mutation | Holds: no runtime; `AGENTS.md:53` prohibits mutation |
| Reads through GraphReader | Standing constraint, not implementation (`AGENTS.md:54`) |
| No RDF writers / one IRI mapping | Holds: upstream Serialize and SubjectIRI exist |
| Not a competing index | At risk for deferred 5.1; cache evidence absent |
| Loud limits | At risk until HTTP contract is specified; a page is not a complete query result |
| Safe exposure | Standing constraint: localhost or bearer/mTLS (`AGENTS.md:81`) |
| Upstream untouched | Holds: only tagged Git objects inspected |

## Adopter seams

| Adopter | Knowledge currently required | Default and discovery |
| --- | --- | --- |
| Client author | Subset, format, limits, entailment, truncation opt-in | No endpoint; issue prose only; more than two correctness facts is an adoption finding |
| SHACL operator | Selection body, formats, unfolding, external validator | No endpoint; issue prose only |
| Service operator | Dependencies, auth/bind, budgets, base IRI, NATS | No boot path; required refusals unproved |

Correctness discovery is currently documentation only. Typed errors and startup refusal are desired, not existing.
Clients should not have to predict result size or scan cost to avoid incomplete answers.

## Open evidence questions

1. Resolved in D.2 (`TestProbeTerminalDatatypePropagationSeam` in `internal/harness/contract/probes_test.go`):
   `vocabulary/predicates.go:161` declares `DataTypeEntityID = "entity_id"`; `:171` declares
   `DataTypeDateTime = "datetime"`. Exporter `object.go:125` handles explicit `Triple.Datatype`, while `:158` handles
   pragmatic declarations on registered dotted predicates. Blindly copying pragmatic `DataType` into `Triple.Datatype`
   emits invalid RDF (e.g. `^^<float>`), and leaving `Triple.Datatype` empty on an unfolded terminal triple whose
   `Predicate` is a full `ValuePredicateIRI` loses declared `datetime`/`int`/`json` types. Normalizing the terminal
   `(Object, Datatype)` from the exporter's own N-Triples rendering of the source triple preserves exact RDF object
   semantics across N-Triples, Turtle, and JSON-LD without upstream changes.
2. Resolved in D.2 (`TestProbeCanonicalNTriplesObjectSeam` in `internal/harness/contract/probes_test.go`): although
   `vocabulary/export/ntriples.go:46` (`formatNTriplesObject`) is unexported, calling `export.SerializeToString` in
   `export.NTriples` format with `export.WithBaseIRI(baseIRI)` on a single triple `{Subject: "_:s", Predicate: pred,
   Object: obj, Datatype: dt}` emits `_:s <predIRI> <canonicalNTriplesObject> .\n` (where `predIRI` contains no `>`),
   yielding the exact exporter-owned canonical N-Triples object token for `BlankNodeID` without copying RDF formatter
   logic.
3. Resolved in D.2 (`TestProbeServerPatternCapabilitySeam` in `internal/harness/contract/probes_test.go`): sending
   `PrefixQueryRequest{Pattern: "invalid", Limit: 1}` to `graph.ingest.query.prefix` returns classified
   `errs.ErrorInvalid` (`semtypes.ErrorCodeEntityIDPatternInvalid`) with zero bucket I/O on a pattern-capable server
   (`processor/graph-ingest/query.go:262–265`), whereas a pre-#259 server without `PrefixQueryRequest.Pattern` ignores
   the unknown JSON field and treats it as an empty-prefix query (`err == nil`). Combining that negative probe with
   `PrefixQueryRequest{Pattern: "*.*.*.*.*.*", Limit: 1}` establishes server `Pattern` capability at startup.
4. Resolved at `semvocab@v0.1.0` (`64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6`): `packs.BFOCCOCoreSpec` /
   `packs.RegisterBFOCCOCore` and `diekm.Spec` / `diekm.Register` / `diekm.Shapes` are pinned in `go.mod` and
   verified by `TestPinnedUpstreamSeamsSmoke` in `internal/harness/contract/deps_test.go`.
5. Resolved in D.1 (`design.md` and `specs/**`): standalone `rdfs:subClassOf*` evaluates over the registered
   `ClassIRI` + `Superclasses` closure with reflexive `(C, C)`; `DESCRIBE` emits the outgoing entity CBD + virtual
   `rdf:type` + unfolded blank-node chains; explicit and virtual types merge as graph sets; solution sequences
   preserve duplicate multiplicity unless `DISTINCT`; HTTP error statuses use `400`/`406`/`413`/`422`/`504` and
   opt-in truncation emits `X-Semsparql-Truncated: true` and `X-Semsparql-Truncation-Reason: result_limit|scan_budget`.
6. Cursor pagination gives no snapshot guarantee. Entity budgets do not bound server key scans.
7. `vocabulary/entity_type.go:14` has a stale positional comment; use named parser fields instead.
8. Resolved in D.1 (`design.md` and `specs/sparql-protocol/spec.md`): default max SPARQL query length is `64 KiB` and
   default max `POST /export` body is `256 KiB` (`413 Content Too Large` when exceeded); non-local binds require bearer
   token or mTLS protection at startup.
9. Resolved in D.2 (`TestProbeSerializerInvalidTripleOmissionSeam` in `internal/harness/contract/probes_test.go`):
   `vocabulary/export/export.go:77` and `ntriples.go:15–22` silently omit triples with empty `Subject`/`Predicate`,
   `nil` `Object`, or `NaN`/`Inf` floats. Checking each triple via single-triple
   `export.SerializeToString(..., export.NTriples, export.WithBaseIRI(baseIRI))` returns `""` for every omitted triple,
   giving the sidecar an exporter-owned loud-failure check before writing HTTP responses.
10. Task 0.1 contract tests and D.2 upstream probe tests (`internal/harness/contract`) run in `task verify`; runtime
    sidecar packages (Phases 1–4) remain planned.
