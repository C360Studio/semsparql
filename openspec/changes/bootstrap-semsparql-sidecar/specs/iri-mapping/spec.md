# IRI mapping

## ADDED Requirements

### Requirement: Exporter-identical entity identity

Entity IRIs SHALL use export.SubjectIRI and upstream entity parsing, with a single base shared by all outputs.

#### Scenario: Default and custom bases

- **WHEN** a valid six-part ID is mapped with either configured base
- **THEN** the IRI is byte-identical to the pinned exporter subject IRI, in org/platform/system/domain/type/instance
  order.

### Requirement: Strict inverse mapping

The mapper SHALL round-trip canonical entity IRIs and reject other IRIs with ErrNotEntityIRI.

#### Scenario: Round trip

- **WHEN** any valid ID is mapped and inverted under the same base
- **THEN** the original ID is returned; generated tests cover the accepted ID grammar.

#### Scenario: Foreign or noncanonical IRI

- **WHEN** an IRI is outside the configured canonical entity namespace or decodes to an invalid ID
- **THEN** it is rejected before a graph request; the mapper does not accept subject-fallback IRIs as entity IDs.

### Requirement: Stable projection blank nodes

Folding and unfolding SHALL share deterministic blank-node identity based on entity ID, dotted predicate,
canonical N-Triples object and step index, using issue #1’s NUL-separated SHA-256 formula.

#### Scenario: Multi-valued predicate

- **WHEN** one entity has two different values for the same projected predicate
- **THEN** their intermediate blank-node IDs differ, and repeated folding/unfolding of each value gives the same IDs.

#### Scenario: Canonical term input

- **WHEN** a typed literal or entity reference supplies the hash object
- **THEN** its RDF-term identity agrees with exporter output; a verified exporter-owned normalization seam is required.
