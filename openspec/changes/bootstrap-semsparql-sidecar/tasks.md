# Bootstrap tasks

These are the implementation tasks from issue #1. Each numbered issue task is one reviewable PR. Dependencies run
0 → 1 → 2 → 3 → 4, with the MVP at 2.3. The source issue remains open. Tasks 0.1, 0.2, D.1, D.2, 1.1, 1.2, 2.1, 2.2,
and 5.1 (disposition) are complete: `go.mod` pins `semengine v0.1.0-alpha.2` and `semvocab v0.1.0`, the Go verification
gates and `internal/harness/contract` structural guards and D.2 upstream probes run in `task verify`, owner
consumer-contract rulings D.1a–D.1e are recorded in `design.md` and `specs/**`, `irimap` (`Mapper`,
`CanonicalNTriplesObject`, `BlankNodeID`), `reader` (`GraphReader`, `MemoryReader`, `MatchAllPattern`, `ErrNilContext`,
`ErrEntityNotFound`, `ErrScanBudgetExceeded`), and `sparql` AST/parser and plain-BGP `SELECT`/`ASK` planner/executor
(`Parse`, `Query`, `PlanBGP`, `Executor`, `ErrSyntax`, `ErrUnsupportedFeature`, `ErrResultLimitExceeded`,
`ErrScanBudgetExceeded`, `ErrQueryTimeout`) and their unit/property/fuzz proving tests run in `task verify` and
`task cover:check`, and `AGENTS.md` and `.agents/rules-index.md` are reconciled. Remaining runtime tasks `2.3`–`4.2`
proceed in phase order.

## Design readiness

- [x] D.1 Accept the reviewed design and settle exact HTTP error/truncation signals, body/query byte limits,
  per-form result counting and format compatibility, standalone subclass-path semantics and DESCRIBE scope.
  Owner rulings D.1a–D.1e are recorded in `design.md` and `specs/**`.
- [x] D.2 Recheck upstream tagged APIs/probes and record any missing exporter or capability-detection seam;
  file confirmed upstream gaps and attach their issue/release references to the dependent tasks.
  Verified in `internal/harness/contract/probes_test.go` (`TestProbeCanonicalNTriplesObjectSeam`,
  `TestProbeTerminalDatatypePropagationSeam`, `TestProbeSerializerInvalidTripleOmissionSeam`,
  `TestProbeServerPatternCapabilitySeam`); no new upstream release is required.

## 0. Bootstrap

- [x] 0.1 Add go.mod for github.com/c360studio/semsparql, pin semengine v0.1.0-alpha.2 and semvocab v0.1.0.
  Extend the existing Taskfile/CI with Go fmt/tidy/build/vet/lint/vuln, unit/repeat/integration/coverage
  gates; add TestAgentSetupParity and its sensitivity check; reconcile AGENTS.md against actual runtime state.
- [x] 0.2 File semengine#256 (SubjectIRI, blank-node/full-IRI export) and #259 (prefix Pattern support).
  Both are closed and their surfaces exist in v0.1.0-alpha.2; see inventory.md. Integration proof remains below.

## 1. Identity and readers

- [x] 1.1 Implement exporter-identical irimap and deterministic BlankNodeID after 0.1. Prove
  TestIRIMapperRoundTrip, TestPropIRIMapperRoundTrip, TestIRIMapperMatchesExporter and TestBlankNodeIDMultiValued;
  fuzz external IRI/ID parsing. Use 0-based decimal ASCII step-index encoding (`strconv.Itoa(stepIdx)`, D.1e) and
  the canonical N-Triples object contract verified in D.2 `TestProbeCanonicalNTriplesObjectSeam`.
- [x] 1.2 Add GraphReader and MemoryReader with GetEntity, GetBatch, ScanPrefix, ScanPattern and
  EntitiesByPredicate. Use shared validators/MatchEntityIDPattern. Test empty-sentinel distinction, invalid input,
  missing entities, deterministic ID sets and budget-aware scans, with no silently incomplete success.

## 2. SELECT/ASK MVP

- [x] 2.1 Implement AST/parser for the supported subset, with FuzzParseSPARQL accepted/rejected seeds and
  TestUnsupportedFeatureErrors. Include all listed FILTER functions; reject REDUCED and unsupported paths/features.
- [x] 2.2 Implement plain-BGP SELECT/ASK planning and execution using MemoryReader, including OPTIONAL, UNION,
  FILTER, DISTINCT, ORDER BY, LIMIT/OFFSET. Prove TestLookupByIRIMultiMatchUnion and TestQueryLimits; explicitly
  test duplicate UNION solutions survive, DISTINCT removes duplicates, and unknown ordinary predicate IRIs yield
  no matches after virtual type/projection handling.
- [ ] 2.3 Add GET/raw/form POST /sparql with negotiated JSON/XML/CSV/TSV results, bounded inputs (64 KiB max query),
  query budgets, localhost/auth defaults and cancellation. Prove the SELECT/ASK portion of
  TestSPARQL11ProtocolConformance, TestQueryLimits and TestUnsupportedFeatureErrors, including 400/406/413/422/504
  statuses and opt-in `X-Semsparql-Truncated: true` / `X-Semsparql-Truncation-Reason` headers (D.1a, D.1b). This is
  the MVP.

## 3. Virtual RDF and graph output

- [ ] 3.1 Implement virtual rdf:type/subclass resolution and startup ancestor-closure validation. Prove
  TestVirtualRDFTypeAndSubclassResolution and TestSuperclassClosureStartupCheck, including wildcard systems,
  unbound classes, zero-length paths, standalone `rdfs:subClassOf*` over the registered class closure (D.1c) and
  explicit/virtual graph-set merging.
- [ ] 3.2 Implement complete-chain folding and equivalent fallback evaluation. Prove
  TestBGPFoldingCollapsesDIEKMChains plus Rapid equivalence against an independently materialized unfolded graph,
  including partial chains, missing type constraints, projected intermediates, extra constraints/FILTERs,
  ambiguity, variable predicates and multi-valued sources. Preserve solution multiplicity.
- [ ] 3.3 Add CONSTRUCT/DESCRIBE (outgoing entity CBD + virtual rdf:type + unfolded blank-node chains, D.1d),
  unfolding, entity JSON-LD, POST /export (256 KiB max body, D.1b), architecture documentation and the external
  SHACL guide. Prove TestConstructUnfoldsReifiedGraphForSHACL and TestUnfoldTerminalDatatypePropagation across
  Turtle/N-Triples/JSON-LD; extend protocol tests to graph formats, invalid/missing IDs and selection bodies.
  Validate complete output, including absence of silently dropped invalid triples (D.2
  `TestProbeSerializerInvalidTripleOmissionSeam`), using an independent RDF parser.

## 4. Live sidecar

- [ ] 4.1 Add NATSRPCReader and cmd/semsparql using declared entity/batch/prefix verbs and classified RPC.
  Prove TestScanPatternReaderParity against real NATS and graph-ingest, across opaque cursor pages and wildcard
  systems; test missing batches, errors, cancellation, short pages with continuation, shutdown and no writes.
  Prove startup refuses an incompatible server without a full-prefix filtering fallback (using the D.2
  `TestProbeServerPatternCapabilitySeam` check).
- [ ] 4.2 Run end-to-end tests using the released SemVocab DIEKM pack and a live NATS/graph-ingest fixture,
  covering the complete issue #1 proving-test set. Record Rapid seeds/check counts, fuzz exploration vs replay,
  independent oracles and failing regression/mutation evidence. Run all implemented gates through task verify.
  Use ephemeral listeners, explicit synchronization and bounded cleanup; do not skip tests or use fixed sleeps.

## 5. Optional reader and reconciliation

- [x] 5.1 Record the owner's optional KVWatchReader disposition: defer it to a separate change, or inventory and
  design it separately before implementation with a memory/entity ceiling, startup refusal, revision freshness,
  reconnect/resync tests and replacement by graph-index. A documented deferral completes this disposition task;
  it does not claim a KVWatchReader was implemented. (Owner ruling D.1e: deferred to a separate change after
  Phases 1–4 ship.)

## 6. Reconciliation

- [ ] 6.1 After implemented scope and optional disposition are complete, reconcile task truth and docs, verify
  the runtime, then archive this change and promote only behavior proven by code to openspec/specs.
  Do not archive merely because the draft documentation is complete; final CI/merge checks belong on the PR.
