# Reification bridge

## ADDED Requirements

### Requirement: Complete-chain folding

The evaluator SHALL fold only complete ProjectionSpec chains, including declared node classes and the terminal
value predicate, while preserving the bindings and multiplicity of the virtual unfolded graph.

#### Scenario: Complete chain

- **WHEN** all projection steps and required type/value patterns match
- **THEN** the chain can use its flat predicate while returning the same bindings as independent unfolded evaluation.

#### Scenario: Projected intermediate

- **WHEN** an intermediate variable is projected and otherwise used only as a chain join key
- **THEN** the binding is its deterministic blank-node ID, identical to unfolding.

#### Scenario: Ambiguous chain

- **WHEN** a BGP fragment matches several ProjectionSpecs
- **THEN** every match participates with graph-set and solution-multiset semantics preserved.

### Requirement: Equivalent fallback

Unsupported folding shapes SHALL evaluate the virtual unfolded graph rather than silently yielding no matches.

#### Scenario: Partial chain

- **WHEN** a required step or class pattern is absent
- **THEN** the partial pattern is evaluated over unfolded candidates, with budgets enforced.

#### Scenario: Extra intermediate use

- **WHEN** an intermediate node has an extra triple constraint or FILTER
- **THEN** folding for that chain is disabled and fallback honors the constraint.

#### Scenario: Variable predicate

- **WHEN** a query uses a variable predicate
- **THEN** it sees the unfolded view, subject to scan budgets, and is not folded.

### Requirement: Typed unfolding

Graph-producing operations SHALL unfold projections and virtual types (including registered superclasses on entity and
intermediate blank-node classes) using deterministic blank nodes. Terminal objects SHALL preserve source RDF
datatype/resource meaning through a verified exporter-owned seam.

#### Scenario: Datatype and resource preservation

- **WHEN** datetime and entity-reference source values become terminal projected triples
- **THEN** Turtle, N-Triples and JSON-LD preserve xsd:dateTime and resource IRIs; pragmatic metadata is not blindly
  copied as an RDF datatype.

#### Scenario: Multiple values

- **WHEN** one predicate has multiple objects
- **THEN** each reified chain retains its own stable blank nodes and terminal value.

### Requirement: Exporter-owned complete RDF output

CONSTRUCT, DESCRIBE, entity JSON-LD and export SHALL use vocabulary/export for graph serialization.
A successful serializer return SHALL NOT alone be treated as proof of complete output.

#### Scenario: SHACL input graph

- **WHEN** a selected graph contains unfolded chains and virtual types
- **THEN** independent RDF parsing observes real blank nodes, unchanged full predicate IRIs and the intended typed
  graph.

#### Scenario: Invalid output triple

- **WHEN** a generated triple would be silently omitted by the upstream serializer (empty subject/predicate, nil object
  or NaN/Inf float)
- **THEN** pre-serialization N-Triples validation detects the empty serializer output and the sidecar fails loudly
  rather than returning a truncated HTTP 200 graph.
