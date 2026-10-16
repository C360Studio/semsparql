# Task D.2 Developer Notes — Upstream API & Exporter Seam Probes

## Summary

Executed and committed the four `D.2` upstream probes against `github.com/c360studio/semengine v0.1.0-alpha.2`
(`11da4721c2233a8e2b5b1d6236a97a44e06abbb6`) and `github.com/c360studio/semvocab v0.1.0`
(`64d3b8bc789c3e178a4cfb92465db4a02b1bd3f6`) in `internal/harness/contract/probes_test.go`. All four seams are
satisfied by the pinned upstream releases without requiring a new upstream issue or release, unblocking Tasks `1.1`
and `4.1` and resolving the `D.2` portion of Task `3.3`.

## Probe Findings

1. **Canonical N-Triples Object Normalization (`TestProbeCanonicalNTriplesObjectSeam`, unblocks Task `1.1`)**
   - Although `formatNTriplesObject` in `semengine/vocabulary/export/ntriples.go` is unexported, calling
     `export.SerializeToString([]message.Triple{{Subject: "_:s", Predicate: pred, Object: obj, Datatype: dt}},
     export.NTriples, export.WithBaseIRI(baseIRI))` emits `_:s <predIRI> <canonicalNTriplesObject> .\n`.
   - Because `resolvePredicateIRI` guarantees `predIRI` contains no `>` character, slicing between the first `"> "`
     and trailing `" .\n"` returns the exact exporter-owned N-Triples object token across all 7 pragmatic `DataType`s
     (`string`, `int`, `float`, `bool`, `datetime`, `json`, `entity_id`), explicit `@id` / XSD datatypes, and blank
     nodes, with zero copied classification or escaping logic.

2. **Terminal Datatype Propagation (`TestProbeTerminalDatatypePropagationSeam`, unblocks `D.2` hold on Task `3.3`)**
   - Confirmed both negative findings from `inventory.md`:
     1. Copying pragmatic `vocabulary.PredicateMetadata.DataType` (`"float"`, `"datetime"`, `"entity_id"`) directly
        into `Triple.Datatype` emits invalid RDF (`^^<float>`).
     2. Leaving `Triple.Datatype` empty on an unfolded terminal triple whose `Predicate` is a full `ValuePredicateIRI`
        (not keyed in `vocabulary.GetPredicateMetadata`) loses declared `datetime`, `int` (when stored as JSON
        `float64`), and `json` datatypes.
   - Confirmed the exporter-owned propagation rule: derive the unfolded terminal triple's `(Object, Datatype)` from
     the exporter's canonical N-Triples rendering of the source triple (`propagateTerminalObject`), preserving
     identical RDF object output across `export.NTriples`, `export.Turtle`, and `export.JSONLD`.

3. **Serializer Silent-Omission Guard (`TestProbeSerializerInvalidTripleOmissionSeam`, unblocks `D.2` hold on `3.3`)**
   - `export.Serialize` silently omits triples with empty `Subject`/`Predicate`, `nil` `Object`, or `NaN`/`Inf`
     floating-point values (`writeNTriples` and `groupTriples` share the exact same filter).
   - Validating each candidate triple via single-triple `export.SerializeToString(..., export.NTriples, ...)` returns
     `""` for every omitted triple, enabling a loud pre-serialization failure before emitting HTTP 200 responses.

4. **Server Pattern-Capability Detection (`TestProbeServerPatternCapabilitySeam`, unblocks Task `4.1`)**
   - `TestProbeServerPatternCapabilitySeam` models both responder contracts in-process (later proven against live
     `graph-ingest` in `TestRunEndToEndWithLiveNATSAndGraphIngest` and against a legacy responder in
     `TestRunRefusesIncompatibleLegacyNATSResponder`): on `semengine >= v0.1.0-alpha.2`
     (`processor/graph-ingest/query.go:250–266`), sending `PrefixQueryRequest{Pattern: "invalid", Limit: 1}` to
     `graph.ingest.query.prefix` returns a classified `errs.ErrorInvalid` with code
     `semtypes.ErrorCodeEntityIDPatternInvalid` (`"entity_id_pattern_invalid"`).
   - A pre-`semengine#259` server whose request struct lacks `Pattern` ignores the unknown `"pattern"` JSON field and
     treats the request as `{Prefix: "", Limit: 1}`, returning `err == nil`.
   - Pairing the negative probe (`{Pattern: "invalid", Limit: 1}` -> `ErrorCodeEntityIDPatternInvalid`) with a valid
     probe (`{Pattern: "*.*.*.*.*.*", Limit: 1}` -> `err == nil`) provides a deterministic startup check for
     `NATSRPCReader` without any full-prefix filtering fallback.

## Verification & Mutation Evidence

- **Mutation Check**:
  - Backed up `internal/harness/contract/probes_test.go`
    (`sha256: 5e54338fe8384bd810e0b10042fd15ee04908b41ee83f4639167c79f4b25427d`) to `$TMPDIR`.
  - Mutated `term.Datatype = dtIRI` to `term.Datatype = ""` in `propagateTerminalObject`.
  - Ran `go test ./internal/harness/contract -run TestProbeTerminalDatatypePropagationSeam`; the test failed on
    `declared_datetime_string` and `declared_json`.
  - Restored from backup and verified exact `shasum -a 256` match
    (`5e54338fe8384bd810e0b10042fd15ee04908b41ee83f4639167c79f4b25427d`).
- **Full Gate Suite**:
  - `task spec:queue` and `task verify` pass cleanly.
