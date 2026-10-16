---
name: query-pattern
description: >-
  Choose how semsparql reads the SemEngine graph: a reader.GraphReader over graph-ingest's declared request verbs,
  the typed exact-entity reader, or the deferred KV watch replica. Use when planner, executor or server code needs
  entities.
---

# How semsparql reads the graph

Adapted from semengine's `query-pattern` skill (semengine `67d0ee9`). semsparql is a read-only consumer: every read
goes through `reader.GraphReader` (issue #1, §2.1), and nothing here writes `ENTITY_STATES` or publishes
`graph.mutation.*`. `reader.GraphReader` and `reader.MemoryReader` exist in `reader/` (task 1.2); `NATSRPCReader`
(task 4.1) and the deferred `KVWatchReader` (task 5.1) do not exist in this tree yet.

## What exists upstream

The upstream reads are semengine's, at the pinned tag. Every verb graph-ingest serves is declared once, in
`graph.QueryVerbs()` (`graph/query_verbs.go` in semengine), with its subject, responder, and request and reply types.

| Way | For | What you get |
| --- | --- | --- |
| `graph.NewExactEntityReader` | One entity by ID | The validated entity and its `ENTITY_STATES` revision (`graph.ExactEntity`) |
| graph-ingest's `batch` and `prefix` verbs | Several entities by ID, or a page of entities under a literal ID prefix | `graph.EntityBatchResponse`, `graph.PrefixQueryResponse` |
| Direct `ENTITY_STATES` watch | Only the deferred `KVWatchReader` (task 5.1) | Raw entries; no query contract |

## Choosing a `GraphReader` implementation

1. **Tests and fixtures:** `MemoryReader`. Pattern matching uses semengine's `types.MatchEntityIDPattern`, so it agrees
   with what NATS KV would match. Reader parity (`TestScanPatternReaderParity`) proves the two agree, page by page.
2. **Production:** `NATSRPCReader`. One entity by ID goes through `graph.NewExactEntityReader`. Batches and prefixes go
   as a classified request (`RequestClassified`, developer contract § NATS RPC) on the subject read from
   `graph.QueryVerbs()`. Never spell a `graph.ingest.query.*` subject as a literal.
3. **Fixed-position wildcard scans (`<org>.<platform>.*.<domain>.<type>.*`):** need the `pattern` field from
   semengine#259. Until that is released, `ScanPattern` has no NATS backing: say so, and don't emulate it in
   production by scanning everything.
4. **Anything else:** there is no admitted read. A graph-index predicate or traversal verb is used only once semengine
   declares it in `query_verbs.go`. The `KVWatchReader` is optional and deferred. When it is built, it must declare a
   memory ceiling (refusing to start above it) and report freshness (the last revision applied). It is a stopgap that
   graph-index replaces, never a competing index.

## Points that hold

- Validate entity IDs, patterns and prefixes with semengine's validators (`ValidateEntityID`,
  `ValidateEntityIDPattern`, `ValidateEntityIDPrefix`) before any request leaves.
- The `prefix` verb sorts before it pages and clamps its limit to `graph.MaxPrefixQueryLimit`. Page with the cursor it
  returns. Every entity read counts against the query's scan budget, and hitting the budget is a deterministic error,
  never a silently short answer.
- Which reader you use and where its answer comes from are separate decisions. A SPARQL query plan picks the narrowest
  read available: exact ID, then type pattern, then predicate, then a budgeted scan (issue #1, §1 `planner.go`).
