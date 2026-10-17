# External SHACL Compliance Gate Guide

Shapes Constraint Language (SHACL, the W3C standard for validating that an RDF graph conforms to a set of class and
property rules called *shapes*) runs outside SemEngine and SemSparql. SemSparql's job in a compliance pipeline is to
export a complete, unfolded RDF graph in Turtle, N-Triples, or JSON-LD so an external SHACL CLI (`pyshacl`, Apache Jena
`shacl`, or TopBraid `shaclvalidate`) can check it and produce a standard `sh:ValidationReport`.

This page shows how to export unfolded DIEKM (Defense Intelligence Enterprise Knowledge Model), Basic Formal Ontology
2.0 (BFO 2.0), and Common Core Ontologies (CCO) graphs from SemSparql (`cmd/semsparql` or `server.Server` over
`POST /export` and `POST /sparql`) and validate them with an external SHACL tool.

## Why unfolding is required before SHACL validation

In SemEngine, an entity stores flat dotted predicates such as `intel.role.assigned` and
`intel.measurement.altitude-m`. Standard BFO 2.0 and CCO SHACL shapes do not validate flat dotted strings; they validate
multi-step reified blank-node chains (anonymous intermediate RDF nodes written `_:b_...` that connect an entity to its
`obo:BFO_0000023` Role or `cco:ont00000787` Altitude -> `cco:ont00001163` MeasurementInformationContentEntity ->
`cco:ont00000253` InformationBearingEntity chain).

When SemSparql executes `CONSTRUCT`, `DESCRIBE`, `GET /entities/{entity_id}.jsonld`, or `POST /export`,
[`sparql.UnfoldEntity`](../sparql/construct.go) and [`server.WriteGraphResult`](../server/negotiate.go) guarantee four
properties required by SHACL validators (verified by `TestConstructUnfoldsReifiedGraphForSHACL` and
`TestUnfoldTerminalDatatypePropagation` in [`sparql/construct_test.go`](../sparql/construct_test.go), using
`semengine v0.1.0-alpha.2` and `semvocab v0.2.1`):

1. **Virtual `rdf:type` materialization**: every entity receives its registered `ClassIRI` and transitive `Superclasses`
   (for example, `diekm:IntelligencePlatform`, `cco:ont00000713`, and `obo:BFO_0000030`) so `sh:targetClass` selectors
   match without redundant stored type triples.
2. **Real RDF blank nodes**: intermediate chain nodes use deterministic `_:b_<32hex>` blank-node labels computed by
   [`irimap.BlankNodeID`](../irimap/mapper.go) and are emitted by `semengine/vocabulary/export` as genuine RDF blank
   nodes (`_:b_...` in Turtle/N-Triples, `{"@id": "_:b_..."}` in JSON-LD), never as string literals or `/subjects/_:`
   IRIs.
3. **Unmangled ontology predicate IRIs**: full `http://` and `https://` step and value predicate IRIs from
   `ProjectionSpec` (`obo:BFO_0000196`, `cco:ont00001904`, `diekm:altitudeMetersValue`) pass through `export.Serialize`
   verbatim (`TestPinnedUpstreamSeamsSmoke` in
   [`internal/harness/contract/deps_test.go`](../internal/harness/contract/deps_test.go)).
4. **Terminal datatype propagation and loud failure on invalid triples**: terminal values preserve their XSD datatype
   (`xsd:double`, `xsd:integer`, `xsd:boolean`, `xsd:dateTime`, `rdf:JSON`) or `@id` entity reference, and
   `validateOutputTriple` in [`sparql/construct.go`](../sparql/construct.go) fails the export with HTTP `500`
   (`sparql_invalid_output_triple`) if any triple would be silently skipped by `export.Serialize`
   (`TestProbeSerializerInvalidTripleOmissionSeam` in
   [`internal/harness/contract/probes_test.go`](../internal/harness/contract/probes_test.go)).

## Step 1: Export an unfolded Turtle graph from SemSparql

Do not pass `X-Semsparql-Allow-Truncation: true` in a compliance gate. By leaving truncation disabled (the default),
SemSparql returns HTTP `422 Unprocessable Entity` (`sparql_result_limit_exceeded` or `sparql_scan_budget_exceeded`) or
`504 Gateway Timeout` (`sparql_query_timeout`) instead of a partial graph that could falsely pass a SHACL check.

### Option A: Export an entity prefix via `POST /export`

Send a JSON selector with `"prefix"` (1 to 6 dot-separated entity ID segments, no `*` wildcards) and request
`Accept: text/turtle`:

```bash
curl --fail -sS -X POST "http://127.0.0.1:8080/export" \
  -H "Content-Type: application/json" \
  -H "Accept: text/turtle" \
  -d '{"prefix":"c360.ops.uas1.intel.platform"}' \
  -o data-graph.ttl
```

### Option B: Export a subgraph via `CONSTRUCT`

Send either `POST /export` with `{"construct": "..."}` or `POST /sparql` (anchored by a selective class or predicate
pattern so the scan stays within `-max-scan-entities` and `-max-results`):

```bash
curl --fail -sS -X POST "http://127.0.0.1:8080/sparql" \
  -H "Content-Type: application/sparql-query" \
  -H "Accept: text/turtle" \
  --data-binary @- <<'SPARQL' > data-graph.ttl
PREFIX diekm: <https://w3id.org/c360/diekm#>
CONSTRUCT {
  ?s ?p ?o .
}
WHERE {
  ?s a diekm:IntelligencePlatform ;
     ?p ?o .
}
SPARQL
```

For an entity `c360.ops.uas1.intel.platform.alpha` with `intel.role.assigned = "ISR"` and
`intel.measurement.altitude-m = 1500.5` (when `semsparql` is started with `-base-iri https://semengine.local`; the
default `-base-iri` is `https://semengine.semanticstream.ing`), `POST /export` with
`{"prefix":"c360.ops.uas1.intel.platform"}` writes `data-graph.ttl`:

```turtle
@prefix cco: <https://www.commoncoreontologies.org/> .
@prefix diekm: <https://w3id.org/c360/diekm#> .
@prefix obo: <http://purl.obolibrary.org/obo/> .
@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

<https://semengine.local/entities/c360/ops/uas1/intel/platform/alpha>
    obo:BFO_0000196 _:b_8cb019e77929b0089fccd8ae382917a6 ;
    obo:BFO_0000196 _:b_3558ea38d8b288b9599e255479294a15 ;
    rdf:type diekm:IntelligencePlatform ;
    rdf:type cco:ont00000713 ;
    rdf:type obo:BFO_0000030 ;
    rdf:type cco:ont00000995 ;
    rdf:type obo:BFO_0000040 ;
    rdf:type obo:BFO_0000004 ;
    rdf:type obo:BFO_0000002 ;
    rdf:type obo:BFO_0000001 .

_:b_8cb019e77929b0089fccd8ae382917a6 rdf:type obo:BFO_0000023 ;
    rdfs:label "ISR" .

_:b_3558ea38d8b288b9599e255479294a15 rdf:type cco:ont00000787 ;
    cco:ont00001904 _:b_790d53f4470b28b163f5471686dc3f00 .

_:b_790d53f4470b28b163f5471686dc3f00 obo:BFO_0000084 _:b_34cf3b3999e253c2df0a8c52721eb6b0 ;
    rdf:type cco:ont00001163 .

_:b_34cf3b3999e253c2df0a8c52721eb6b0 rdf:type cco:ont00000253 ;
    diekm:altitudeMetersValue "1500.5"^^xsd:double .
```

## Step 2: Author a BFO / CCO / DIEKM SHACL shapes file

Save the following SHACL shapes graph as `diekm-shapes.ttl`. It requires every `diekm:IntelligencePlatform` to bear at
least one `obo:BFO_0000023` (`Role`) with a non-empty `rdfs:label`, and requires every unfolded `cco:ont00000787`
(`Altitude`) chain to terminate in an `xsd:double` `diekm:altitudeMetersValue`:

```turtle
@prefix sh: <http://www.w3.org/ns/shacl#> .
@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .
@prefix rdfs: <http://www.w3.org/2000/01/rdf-schema#> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .
@prefix obo: <http://purl.obolibrary.org/obo/> .
@prefix cco: <https://www.commoncoreontologies.org/> .
@prefix diekm: <https://w3id.org/c360/diekm#> .

diekm:IntelligencePlatformShape
    a sh:NodeShape ;
    sh:targetClass diekm:IntelligencePlatform ;
    sh:property [
        sh:path obo:BFO_0000196 ;
        sh:qualifiedValueShape [
            sh:class obo:BFO_0000023 ;
            sh:property [
                sh:path rdfs:label ;
                sh:datatype xsd:string ;
                sh:minCount 1 ;
            ]
        ] ;
        sh:qualifiedMinCount 1 ;
    ] .

diekm:AltitudeChainShape
    a sh:NodeShape ;
    sh:targetClass cco:ont00000787 ;
    sh:property [
        sh:path cco:ont00001904 ;
        sh:minCount 1 ;
        sh:node [
            sh:class cco:ont00001163 ;
            sh:property [
                sh:path obo:BFO_0000084 ;
                sh:minCount 1 ;
                sh:node [
                    sh:class cco:ont00000253 ;
                    sh:property [
                        sh:path diekm:altitudeMetersValue ;
                        sh:datatype xsd:double ;
                        sh:minCount 1 ;
                    ]
                ]
            ]
        ]
    ] .
```

## Step 3: Run an external SHACL validator to produce `sh:ValidationReport`

Using `pyshacl` or Apache Jena's `shacl` CLI:

```bash
# pyshacl (exits non-zero when sh:conforms is false)
pyshacl -s diekm-shapes.ttl -df turtle -sf turtle -f turtle -o validation-report.ttl data-graph.ttl

# Or Apache Jena shacl CLI (process exit code on sh:conforms false is not promised across Jena versions;
# inspect sh:conforms true in validation-report.ttl in CI gates)
shacl validate --shapes diekm-shapes.ttl --data data-graph.ttl > validation-report.ttl
grep -Eq 'sh:conforms[[:space:]]+true' validation-report.ttl
```

When `data-graph.ttl` conforms to `diekm-shapes.ttl`, the validator writes a `sh:ValidationReport` with
`sh:conforms true`:

```turtle
@prefix rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> .
@prefix sh:  <http://www.w3.org/ns/shacl#> .
@prefix xsd: <http://www.w3.org/2001/XMLSchema#> .

[ rdf:type     sh:ValidationReport ;
  sh:conforms  true
] .
```

If an entity violates a shape (for example, an `IntelligencePlatform` has no `intel.role.assigned` triple), `pyshacl`
exits non-zero and the report emits `sh:conforms false` alongside a `sh:ValidationResult` naming the offending entity
IRI and `sh:sourceShape`.
