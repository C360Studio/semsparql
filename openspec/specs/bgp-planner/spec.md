# bgp-planner Specification

## Purpose
Defines read-only `GraphReader` storage access, `StandardIRI` predicate resolution, virtual `rdf:type` and
`rdfs:subClassOf*` ancestry evaluation, SPARQL solution semantics, and bounded query evaluation for SemSparql.

## Requirements

### Requirement: Read-only graph access

All evaluation SHALL read through GraphReader and SHALL NOT write SemEngine state or publish mutations.
Remote readers SHALL use only declared query verbs and propagate classified failures.

#### Scenario: Invalid selector

- **WHEN** an invalid entity ID, literal prefix or pattern reaches a reader boundary
- **THEN** the matching upstream validator rejects it before transport; explicit bounded match-all is distinct from a
  literal prefix.

#### Scenario: Read failure

- **WHEN** an RPC fails or a requested batch entity is reported missing
- **THEN** the missing/error state remains explicit; no zero-valued entity or complete-success claim replaces it.

#### Scenario: Paged scan

- **WHEN** a prefix/pattern response contains a continuation cursor, even on a short page
- **THEN** the reader follows the opaque cursor under the query budget; it does not infer exhaustion from page size.

#### Scenario: Pattern parity

- **WHEN** MemoryReader and NATSRPCReader scan the same stable fixture across cursor pages
- **THEN** their sorted entity-ID sets agree, including wildcard system positions; no client-side full-prefix fallback
  occurs.

### Requirement: Predicate IRI matching

For ordinary flat predicates, the planner SHALL expand every StandardIRI match from LookupByIRI and SHALL NOT
infer alias matches. Virtual type and projection/unfolded-view predicates SHALL be handled before concluding that
an IRI has no matches.

#### Scenario: Multiple predicates

- **WHEN** one IRI maps to two dotted predicates
- **THEN** both sources contribute to the virtual graph and query results.

#### Scenario: Unknown predicate

- **WHEN** an ordinary predicate IRI has no registered match after virtual type and projection handling
- **THEN** that pattern yields no matching solution, not a lookup error.

### Requirement: Virtual types and ancestry

Type evaluation SHALL combine explicit stored `rdf:type` triples on candidate entities and virtual types derived from
entity IDs as graph sets, keep bound registered-class `?s a <C>` and `?s rdf:type/rdfs:subClassOf* <C>` scans selective
(`org.platform.*.domain.type.*` plus registered projection node-class predicates) without falling back to a full-graph
scan, support registered superclass entailment, and require complete registered ancestor closure at startup.

#### Scenario: Bound class scan

- **WHEN** a query constrains `?s rdf:type <C>` or `?s rdf:type/rdfs:subClassOf* <C>` for a registered class or
  projection node class `<C>`
- **THEN** the planner and executor read only matching `org.platform.*.domain.type.*` patterns and projection predicates
  (charging the scan budget) so non-matching entities never cross NATS, and merge virtual and explicit `rdf:type`
  triples on matched candidate entities.

#### Scenario: Unbound class

- **WHEN** a query requests an entity’s class or supported superclass path
- **THEN** metadata supplies ClassIRI and applicable registered ancestors; zero-length path identity is retained.

#### Scenario: Standalone subclass path

- **WHEN** a query evaluates `?sub rdfs:subClassOf* ?sup` without an `rdf:type` prefix
- **THEN** evaluation runs over the finite registered ontology class graph in `vocabulary` (`ClassIRI` and
  `Superclasses` across `ListRegisteredEntityTypes()`), matching reflexive `(C, C)` for every registered or bound class
  IRI `C` and `(sub, sup)` whenever `sup` is in `sub`'s registered `Superclasses`.

#### Scenario: Missing ancestor closure

- **WHEN** a registered superclass has a registered ancestor absent from its child’s closure
- **THEN** startup fails with the missing relationship identified rather than advertising readiness.

### Requirement: Solution semantics

Evaluation SHALL support BGP joins, OPTIONAL, UNION, supported FILTERs, DISTINCT, ORDER BY and slicing.
RDF graph/ID sets SHALL be deduplicated, while solution sequences SHALL preserve multiplicity without DISTINCT.

#### Scenario: Duplicate UNION branches

- **WHEN** two identical UNION branches produce the same solution
- **THEN** both solution occurrences survive unless DISTINCT is requested.

#### Scenario: Deterministic slicing

- **WHEN** the same observed graph is evaluated with LIMIT/OFFSET
- **THEN** solutions are ordered deterministically, respecting ORDER BY and stable ties, before slicing.

#### Scenario: Live graph changes

- **WHEN** the upstream graph changes between cursor pages
- **THEN** no snapshot-isolation guarantee is made; deterministic output ordering does not imply a transactional
  snapshot.

### Requirement: Bounded evaluation

The executor SHALL enforce query timeout, result cap (bounding both intermediate solution buffers and output rows/triples)
and entities-read budget across plans and fallback paths. Default breaches SHALL return identifiable errors, not
successful partial results.

#### Scenario: Unconstrained scan

- **WHEN** a BGP contains only unconstrained `?s ?p ?o` patterns with no selective anchor, or a selective scan requires
  more than the available entity budget
- **THEN** standalone unconstrained `?s ?p ?o` is rejected at plan time (`ErrUnconstrainedScan`) and budget-exceeding
  scans stop with `ErrScanBudgetExceeded`; evaluation never silently returns an unflagged prefix of the solution
  sequence.

#### Scenario: Resource exhaustion

- **WHEN** a join, scan or result production reaches a configured query limit
- **THEN** it fails closed unless explicit truncation consent and signalling meet the protocol contract; query LIMIT
  is not consent.
