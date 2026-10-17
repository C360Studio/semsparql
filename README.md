# SemSparql

SemSparql (`github.com/c360studio/semsparql`) is a read-only HTTP server and Go library that exposes a [W3C SPARQL 1.1
Protocol](https://www.w3.org/TR/sparql11-protocol/) endpoint and RDF export routes over a live SemEngine graph.

SemEngine stores entities using six-part dot-separated identifiers (`org.platform.system.domain.type.instance`) and flat
dotted predicates (`intel.measurement.altitude-m`) in NATS JetStream rather than storing an RDF graph. SemSparql sits at
the read edge: it translates SPARQL 1.1 queries (`SELECT`, `ASK`, `CONSTRUCT`, `DESCRIBE`) into bounded NATS RPC reads
against SemEngine's `graph-ingest` query service, expands entity types and ontology reification chains on demand using
registered SemVocab vocabulary packs (Basic Formal Ontology 2.0, Common Core Ontologies, and DIEKM), and serializes
results using SemEngine's own exporter (`semengine/vocabulary/export`). It never writes to the graph.

## Running the server (`cmd/semsparql`)

Build or run the binary against a NATS server where SemEngine's `graph-ingest` query responder is running (requires
`semengine >= v0.1.0-alpha.2`):

```bash
go run ./cmd/semsparql \
  -nats-url nats://127.0.0.1:4222 \
  -bind 127.0.0.1:8080
```

At startup, `semsparql` registers the built-in BFO/CCO Core and DIEKM vocabulary packs (`semvocab v0.2.1`), verifies
that every registered entity type's superclass list is transitively closed, and probes the NATS responder to confirm
server-side wildcard pattern support before opening the HTTP listener (`TestRunEndToEndWithLiveNATSAndGraphIngest` and
`TestRunRefusesIncompatibleLegacyNATSResponder` in [`cmd/semsparql/main_test.go`](cmd/semsparql/main_test.go)).

### Command-line flags

| Flag | Default | Description |
| --- | --- | --- |
| `-bind` | `127.0.0.1:8080` | HTTP listen address. Non-loopback addresses require `-bearer-token` (`SEMSPARQL_BEARER_TOKEN`) or mTLS (`-tls-cert`, `-tls-key`, `-tls-client-ca`). |
| `-nats-url` | `nats://127.0.0.1:4222` | NATS server URL. |
| `-nats-timeout` | `10s` | Per-request NATS RPC timeout. |
| `-nats-loss-grace` | `30s` | Grace period after NATS disconnect before shutting down the HTTP server (`0` disables). |
| `-base-iri` | `https://semengine.semanticstream.ing` (`vocabulary.SemEngineBase`) | Base IRI used for canonical entity IRIs (`{base}/entities/{org}/{platform}/{system}/{domain}/{type}/{instance}`). |
| `-bearer-token` | `""` | Bearer token required on HTTP requests (prefer setting `SEMSPARQL_BEARER_TOKEN` in the environment). |
| `-tls-cert` | `""` | Path to server TLS certificate PEM file. |
| `-tls-key` | `""` | Path to server TLS private key PEM file. |
| `-tls-client-ca` | `""` | Path to client CA bundle PEM file for mutual TLS (mTLS) client verification. |
| `-query-timeout` | `10s` | Per-query execution timeout (`504 Gateway Timeout` on breach). |
| `-max-results` | `10000` | Maximum solution bindings (`SELECT`) or deduplicated RDF triples (`CONSTRUCT`, `DESCRIBE`, `/export`) per request (`422 Unprocessable Entity` on breach). |
| `-max-scan-entities` | `100000` | Maximum entities read per request (`422 Unprocessable Entity` on breach). |
| `-max-query-bytes` | `65536` (`64 KiB`) | Maximum `/sparql` query size in bytes (`413 Content Too Large` on breach). |
| `-max-export-bytes` | `262144` (`256 KiB`) | Maximum `POST /export` request body size in bytes (`413 Content Too Large` on breach). |

## HTTP endpoints and examples

| Route | Methods | Purpose | Supported `Accept` Formats |
| --- | --- | --- | --- |
| `/sparql` | `GET`, `POST` | Execute a SPARQL 1.1 `SELECT`, `ASK`, `CONSTRUCT`, or `DESCRIBE` query | `SELECT`: `application/sparql-results+json` (default), `application/sparql-results+xml`, `text/csv`, `text/tab-separated-values`<br>`ASK`: `application/sparql-results+json` (default), `application/sparql-results+xml`<br>`CONSTRUCT` / `DESCRIBE`: `text/turtle` (default), `application/n-triples`, `application/ld+json` |
| `/entities/{entity_id}.jsonld` | `GET` | Export a single six-part entity ID as an unfolded JSON-LD document | `application/ld+json` |
| `/export` | `POST` | Export an unfolded RDF graph for a JSON `{"prefix": "..."}` or `{"construct": "..."}` selector | `text/turtle` (default), `application/n-triples`, `application/ld+json` |

### 1. Run a `SELECT` query (`POST /sparql` or `GET /sparql?query=...`)

In SemEngine, an `intel.platform` entity stores flat predicates (`intel.role.assigned`, `intel.measurement.altitude-m`)
and encodes its class in its six-part entity ID (`c360.ops.uas1.intel.platform.alpha`). You can query it using standard
BFO 2.0, CCO, and DIEKM ontology classes and reified blank-node chains:

```bash
curl --fail -sS -X POST "http://127.0.0.1:8080/sparql" \
  -H "Content-Type: application/sparql-query" \
  -H "Accept: application/sparql-results+json" \
  --data-binary @- <<'SPARQL'
PREFIX obo: <http://purl.obolibrary.org/obo/>
PREFIX cco: <https://www.commoncoreontologies.org/>
PREFIX diekm: <https://w3id.org/c360/diekm#>

SELECT ?platform ?alt WHERE {
  ?platform a diekm:IntelligencePlatform ;
            obo:BFO_0000196 ?altNode .
  ?altNode a cco:ont00000787 ;
           cco:ont00001904 ?ice .
  ?ice a cco:ont00001163 ;
       obo:BFO_0000084 ?ibe .
  ?ibe a cco:ont00000253 ;
       diekm:altitudeMetersValue ?alt .
}
ORDER BY ?platform
SPARQL
```

When a query writes the complete ontology chain for `intel.measurement.altitude-m`, the planner folds the chain into a
single direct predicate lookup while still binding every intermediate variable to its deterministic blank node. If a
query inspects a partial chain or filters an intermediate node, the executor automatically falls back to evaluating the
virtual unfolded RDF graph.

### 2. Fetch a single entity as JSON-LD (`GET /entities/{entity_id}.jsonld`)

```bash
curl --fail -sS "http://127.0.0.1:8080/entities/c360.ops.uas1.intel.platform.alpha.jsonld"
```

### 3. Export an unfolded RDF graph (`POST /export`)

Export all entities under a dot-separated entity ID prefix (1 to 6 segments, no `*` wildcards) as Turtle, N-Triples, or
JSON-LD for external SHACL validation:

```bash
curl --fail -sS -X POST "http://127.0.0.1:8080/export" \
  -H "Content-Type: application/json" \
  -H "Accept: text/turtle" \
  -d '{"prefix":"c360.ops.uas1.intel.platform"}'
```

Or pass a `CONSTRUCT` query in the JSON body:

```bash
curl --fail -sS -X POST "http://127.0.0.1:8080/export" \
  -H "Content-Type: application/json" \
  -H "Accept: application/n-triples" \
  -d '{"construct":"PREFIX diekm: <https://w3id.org/c360/diekm#> CONSTRUCT { ?s ?p ?o } WHERE { ?s a diekm:IntelligencePlatform ; ?p ?o }"}'
```

By default, if a query or export exceeds `-max-results` or `-max-scan-entities`, SemSparql returns `422 Unprocessable
Entity` (`result_limit_exceeded` or `scan_budget_exceeded`) rather than returning a partial result that could silently
pass downstream checks. Callers that explicitly want a bounded partial response can send the request header
`X-Semsparql-Allow-Truncation: true`; when truncated, the `200 OK` response includes `X-Semsparql-Truncated: true` and
`X-Semsparql-Truncation-Reason: result_limit` (or `scan_budget`).

## Using SemSparql as a Go library

You can evaluate SPARQL 1.1 queries directly in Go against an in-memory entity fixture (`reader.NewMemoryReader`) or a
NATS client (`reader.NewNATSRPCReader`):

```go
package main

import (
    "context"
    "fmt"
    "log"

    "github.com/c360studio/semengine/graph"
    "github.com/c360studio/semengine/message"
    "github.com/c360studio/semengine/vocabulary"
    "github.com/c360studio/semsparql/irimap"
    "github.com/c360studio/semsparql/reader"
    "github.com/c360studio/semsparql/sparql"
    "github.com/c360studio/semvocab/packs"
    "github.com/c360studio/semvocab/packs/diekm"
)

func main() {
    if err := packs.RegisterBFOCCOCore(); err != nil {
        log.Fatal(err)
    }
    if err := diekm.Register(); err != nil {
        log.Fatal(err)
    }

    const id = "c360.ops.uas1.intel.platform.alpha"
    mr, err := reader.NewMemoryReader(graph.EntityState{
        ID: id,
        Triples: []message.Triple{
            {Subject: id, Predicate: "intel.role.assigned", Object: "ISR"},
            {Subject: id, Predicate: "intel.measurement.altitude-m", Object: 1500.5},
        },
    })
    if err != nil {
        log.Fatal(err)
    }

    exec := sparql.NewExecutor(mr, sparql.Config{
        Mapper: irimap.NewMapper(vocabulary.SemEngineBase),
    })

    res, err := exec.ExecuteQuery(context.Background(), `
        PREFIX diekm: <https://w3id.org/c360/diekm#>
        SELECT ?s WHERE { ?s a diekm:IntelligencePlatform }
    `)
    if err != nil {
        log.Fatal(err)
    }
    for _, row := range res.Solutions {
        fmt.Println(row["s"].Value)
    }
}
```

## Supported SPARQL 1.1 subset

- **Query forms:** `SELECT`, `ASK`, `CONSTRUCT` (including `CONSTRUCT WHERE`), and `DESCRIBE` (outgoing Concise Bounded
  Description for each matched entity IRI, including virtual `rdf:type` triples and unfolded blank-node chains).
- **Prologue & graph patterns:** `BASE`, `PREFIX`, Basic Graph Patterns (BGPs), `OPTIONAL`, `UNION`, nested `{ ... }`
  group patterns, and `FILTER` (`=`, `!=`, `<`, `<=`, `>`, `>=`, `+`, `-`, `*`, `/`, `&&`, `||`, `!`, `BOUND`, `STR`,
  `LANG`, `DATATYPE`, `REGEX`).
- **Property paths:** `a` (`rdf:type`), `rdf:type/rdfs:subClassOf*`, and standalone `rdfs:subClassOf*` over the
  registered vocabulary class hierarchy.
- **Solution modifiers:** `DISTINCT`, `ORDER BY` (`ASC`/`DESC`), `LIMIT`, and `OFFSET`.
- **Rejected out-of-scope features (`400 Bad Request`):** SPARQL Update (`INSERT`, `DELETE`, `LOAD`, `CLEAR`, `DROP`,
  `CREATE`, `COPY`, `MOVE`, `ADD`), `SERVICE`, `GRAPH`, `FROM` / `FROM NAMED`, `GROUP BY`, `HAVING`, aggregates
  (`COUNT`, `SUM`, `AVG`, `MIN`, `MAX`, `SAMPLE`, `GROUP_CONCAT`), subqueries, `VALUES`, `BIND`, `MINUS`, `REDUCED`, and
  arbitrary property paths.
- **Unconstrained triple patterns (`?s ?p ?o`):** Evaluated as a bounded full scan (`AccessFullScan`) under
  `-max-scan-entities` and `-max-results`; when either budget is exceeded, the server fails closed with `422
  Unprocessable Entity` (`scan_budget_exceeded` or `result_limit_exceeded`) unless `X-Semsparql-Allow-Truncation: true`
  is supplied.

## Further documentation

- [Architecture and Package Guide](docs/architecture.md) — package layout (`irimap`, `reader`, `sparql`, `server`,
  `cmd/semsparql`), entity-ID-to-IRI mapping, deterministic blank nodes, query access planning, chain folding, and
  resource limits.
- [External SHACL Compliance Gate Guide](docs/shacl-compliance-gate.md) — how to export unfolded Turtle, N-Triples, or
  JSON-LD graphs from SemSparql and validate them with `pyshacl` or Apache Jena `shacl`.
- [Testing and Verification Guide](docs/testing.md) — unit, property-based (`rapid`), fuzz, and live NATS/DIEKM
  integration test suites (`task verify`).
