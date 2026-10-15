# SPARQL protocol and export

## ADDED Requirements

### Requirement: Supported query subset

The service SHALL accept SELECT, ASK, CONSTRUCT and DESCRIBE with PREFIX/BASE, BGPs, OPTIONAL, UNION,
FILTER comparison/logical/bound/regex/str/lang/datatype, DISTINCT, ORDER BY, LIMIT and OFFSET.
Paths SHALL be limited to `rdf:type/rdfs:subClassOf*` and `rdfs:subClassOf*`.

#### Scenario: Unsupported syntax

- **WHEN** a query uses updates, SERVICE, GRAPH/named graphs, aggregates/GROUP BY, subqueries, VALUES, BIND, REDUCED
  or other paths
- **THEN** the service returns HTTP 400 naming the unsupported feature, rather than ignoring it or returning empty
  success.

### Requirement: SPARQL HTTP transport and negotiation

GET query parameters, application/sparql-query POST and application/x-www-form-urlencoded POST SHALL share
query semantics. Responses SHALL negotiate formats appropriate to the query form.

#### Scenario: Equivalent transports

- **WHEN** the same SELECT or ASK is sent through each supported transport
- **THEN** the query produces equivalent results in applicable SPARQL JSON/XML or CSV/TSV representations.

#### Scenario: Graph negotiation

- **WHEN** CONSTRUCT or DESCRIBE requests Turtle, N-Triples or JSON-LD
- **THEN** the response uses the negotiated RDF format and matching media type, through vocabulary/export.

#### Scenario: Malformed or incompatible request

- **WHEN** a request has invalid query parameters, malformed syntax or an incompatible requested representation
- **THEN** it returns an identifiable protocol error rather than a misleading success or wrong format.

### Requirement: Entity and selected graph export

GET /entities/{entity_id}.jsonld SHALL return unfolded entity data and virtual types. POST /export SHALL
accept exactly one JSON prefix or CONSTRUCT selector and negotiate Turtle, N-Triples or JSON-LD.

#### Scenario: Entity errors

- **WHEN** an entity ID is invalid or its entity is absent
- **THEN** the endpoint returns 400 or 404 respectively; a valid existing entity produces JSON-LD.

#### Scenario: Export selection

- **WHEN** a valid prefix or CONSTRUCT selector is supplied
- **THEN** only its bounded selected graph is exported, with virtual types and unfolding; no GET /export.ttl route is
  introduced.

#### Scenario: External validation

- **WHEN** an operator exports a graph for SHACL compliance
- **THEN** documentation shows an external validator producing sh:ValidationReport; the sidecar does not execute SHACL.

### Requirement: Explicit limits and truncation

The service SHALL default to a 10s query timeout, 10,000 maximum results and 100,000 entities read, and
SHALL bound request bodies and query length. Default limit failures SHALL NOT return successful partial results.

#### Scenario: Default breach

- **WHEN** a timeout, result cap or scan budget is reached without truncation consent
- **THEN** the response identifies the breached limit as an error and does not claim a complete result.

#### Scenario: Explicit truncation

- **WHEN** X-Semsparql-Allow-Truncation: true is present and partial output is returned
- **THEN** the response unambiguously signals incompleteness; absence of the header never enables truncation.

#### Scenario: Oversized input

- **WHEN** body size or query length exceeds its configured maximum
- **THEN** the request is rejected before partial evaluation can be reported as success.

### Requirement: Safe startup and cancellation

The default bind SHALL be localhost. A non-local bind SHALL require bearer-token or mTLS protection.
Startup SHALL validate pack closure and remote pattern support before readiness; request cancellation SHALL stop work.

#### Scenario: Unsafe exposure

- **WHEN** configuration requests a non-local bind without authentication
- **THEN** startup refuses with an actionable error.

#### Scenario: Incompatible graph server

- **WHEN** the running server lacks required Pattern capability despite the local library pin
- **THEN** startup refuses with a clear compatibility error; no broad-scan workaround is used.

#### Scenario: Cancelled request

- **WHEN** the client disconnects or query context expires
- **THEN** evaluation and reader work stop and no detached query worker remains after bounded shutdown.
