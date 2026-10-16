package sparql

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semvocab/packs"
	"github.com/c360studio/semvocab/packs/diekm"
	"pgregory.net/rapid"
)

func registerConstructDIEKMVocab(t *testing.T) func() {
	t.Helper()
	restore := vocabulary.SnapshotRegistry()
	vocabulary.ClearRegistry()
	if err := packs.RegisterBFOCCOCore(); err != nil {
		restore()
		t.Fatalf("RegisterBFOCCOCore: %v", err)
	}
	if err := diekm.Register(); err != nil {
		restore()
		t.Fatalf("diekm.Register: %v", err)
	}
	return restore
}

func TestConstructUnfoldsReifiedGraphForSHACL(t *testing.T) {
	defer registerConstructDIEKMVocab(t)()

	const (
		baseIRI = "https://example.org"
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idObs1  = "c360.ops.sat1.intel.observation.obs1"
	)

	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithIRI("https://example.org/onto#callsign"),
		vocabulary.WithDataType(vocabulary.DataTypeString))

	entAlpha := graph.EntityState{
		ID: idAlpha,
		Triples: []message.Triple{
			{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
			{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
			{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "Recon"},
			{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
			{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1825.0)},
		},
	}
	entObs1 := graph.EntityState{
		ID: idObs1,
		Triples: []message.Triple{
			{Subject: idObs1, Predicate: "c360.intel.callsign", Object: "OBS-1"},
		},
	}

	mr := mustMemoryReader(t, entAlpha, entObs1)
	mapper := irimap.NewMapper(baseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})

	alphaIRI, err := mapper.EntityIDToIRI(idAlpha)
	if err != nil {
		t.Fatalf("EntityIDToIRI: %v", err)
	}

	// Expected deterministic blank node IDs for alpha's projected predicates.
	roleISRCanon, err := irimap.CanonicalNTriplesObject(message.Triple{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"}, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesObject(ISR): %v", err)
	}
	roleReconCanon, err := irimap.CanonicalNTriplesObject(message.Triple{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "Recon"}, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesObject(Recon): %v", err)
	}
	alt1Canon, err := irimap.CanonicalNTriplesObject(message.Triple{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)}, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesObject(1500.5): %v", err)
	}
	alt2Canon, err := irimap.CanonicalNTriplesObject(message.Triple{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1825.0)}, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesObject(1825.0): %v", err)
	}

	bRoleISR, _ := irimap.BlankNodeID(idAlpha, "intel.role.assigned", roleISRCanon, 0)
	bRoleRecon, _ := irimap.BlankNodeID(idAlpha, "intel.role.assigned", roleReconCanon, 0)
	bAlt1Step0, _ := irimap.BlankNodeID(idAlpha, "intel.measurement.altitude-m", alt1Canon, 0)
	bAlt1Step1, _ := irimap.BlankNodeID(idAlpha, "intel.measurement.altitude-m", alt1Canon, 1)
	bAlt1Step2, _ := irimap.BlankNodeID(idAlpha, "intel.measurement.altitude-m", alt1Canon, 2)
	bAlt2Step0, _ := irimap.BlankNodeID(idAlpha, "intel.measurement.altitude-m", alt2Canon, 0)
	bAlt2Step1, _ := irimap.BlankNodeID(idAlpha, "intel.measurement.altitude-m", alt2Canon, 1)
	bAlt2Step2, _ := irimap.BlankNodeID(idAlpha, "intel.measurement.altitude-m", alt2Canon, 2)

	t.Run("UnfoldEntity and CONSTRUCT produce valid Turtle, N-Triples, and JSON-LD with real blank nodes and unmangled predicate IRIs", func(t *testing.T) {
		unfoldedAlpha, err := UnfoldEntity(&entAlpha, baseIRI)
		if err != nil {
			t.Fatalf("UnfoldEntity(entAlpha): %v", err)
		}

		// Execute CONSTRUCT selecting alpha's complete unfolded graph.
		resConstruct, err := exec.ExecuteQuery(context.Background(), `
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			CONSTRUCT { ?s ?p ?o }
			WHERE {
				?s a diekm:IntelligencePlatform ;
				   ?p ?o .
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(CONSTRUCT): %v", err)
		}
		if resConstruct.Form != QueryConstruct {
			t.Fatalf("Form = %v, want QueryConstruct", resConstruct.Form)
		}

		// Verify export.Serialize(..., export.Turtle, export.WithBaseIRI(baseIRI)) directly as required by issue #1 §3.
		turtleText, err := export.SerializeToString(resConstruct.Triples, export.Turtle, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(Turtle): %v", err)
		}
		ntText, err := export.SerializeToString(resConstruct.Triples, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(NTriples): %v", err)
		}
		jsonldText, err := export.SerializeToString(resConstruct.Triples, export.JSONLD, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(JSONLD): %v", err)
		}

		for name, text := range map[string]string{"Turtle": turtleText, "NTriples": ntText, "JSONLD": jsonldText} {
			if strings.Contains(text, "/subjects/_:") || strings.Contains(text, "<_:") {
				t.Fatalf("%s mangled blank node as IRI:\n%s", name, text)
			}
			if (name == "Turtle" || name == "NTriples") && strings.Contains(text, "\"_:b_") {
				t.Fatalf("%s mangled blank node as string literal:\n%s", name, text)
			}
			if name == "JSONLD" && strings.Contains(text, `"@value": "_:b_`) {
				t.Fatalf("JSONLD mangled blank node as @value literal:\n%s", text)
			}
			if strings.Contains(text, "/predicates/http") {
				t.Fatalf("%s mangled full predicate IRI:\n%s", name, text)
			}
			for _, wantBNode := range []string{
				bRoleISR, bRoleRecon,
				bAlt1Step0, bAlt1Step1, bAlt1Step2,
				bAlt2Step0, bAlt2Step1, bAlt2Step2,
			} {
				if !strings.Contains(text, wantBNode) {
					t.Fatalf("%s output missing deterministic blank node %s:\n%s", name, wantBNode, text)
				}
			}
		}

		// Independently parse Turtle, N-Triples, and JSON-LD and verify all three
		// yield the exact same RDF triple set as UnfoldEntity(entAlpha).
		wantNT, err := export.SerializeToString(unfoldedAlpha, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(unfoldedAlpha, NTriples): %v", err)
		}
		wantGraph, err := parseIndependentNTriples(wantNT)
		if err != nil {
			t.Fatalf("parseIndependentNTriples(wantNT): %v", err)
		}
		gotNTGraph, err := parseIndependentNTriples(ntText)
		if err != nil {
			t.Fatalf("parseIndependentNTriples(ntText): %v", err)
		}
		gotTurtleGraph, err := parseIndependentTurtle(turtleText)
		if err != nil {
			t.Fatalf("parseIndependentTurtle(turtleText): %v\nTurtle:\n%s", err, turtleText)
		}
		gotJSONLDGraph, err := parseIndependentJSONLD(jsonldText)
		if err != nil {
			t.Fatalf("parseIndependentJSONLD(jsonldText): %v\nJSON-LD:\n%s", err, jsonldText)
		}

		assertSameRDFGraph(t, "CONSTRUCT N-Triples vs UnfoldEntity", gotNTGraph, wantGraph)
		assertSameRDFGraph(t, "CONSTRUCT Turtle vs N-Triples", gotTurtleGraph, gotNTGraph)
		assertSameRDFGraph(t, "CONSTRUCT JSON-LD vs N-Triples", gotJSONLDGraph, gotNTGraph)

		// Verify specific virtual rdf:type and unfolded chain triples are present in the parsed Turtle graph.
		expectedTriples := []rdfTriple{
			{
				Subject:   Term{Kind: TermIRI, Value: alphaIRI},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Object:    Term{Kind: TermIRI, Value: "https://w3id.org/c360/diekm#IntelligencePlatform"},
			},
			{
				Subject:   Term{Kind: TermIRI, Value: alphaIRI},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Object:    Term{Kind: TermIRI, Value: "http://purl.obolibrary.org/obo/BFO_0000030"},
			},
			{
				Subject:   Term{Kind: TermIRI, Value: alphaIRI},
				Predicate: Term{Kind: TermIRI, Value: "http://purl.obolibrary.org/obo/BFO_0000196"},
				Object:    Term{Kind: TermBlankNode, Value: bRoleISR},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bRoleISR},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Object:    Term{Kind: TermIRI, Value: "http://purl.obolibrary.org/obo/BFO_0000023"},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bRoleISR},
				Predicate: Term{Kind: TermIRI, Value: "http://www.w3.org/2000/01/rdf-schema#label"},
				Object:    Term{Kind: TermLiteral, Value: "ISR", Datatype: XSDStringIRI},
			},
			{
				Subject:   Term{Kind: TermIRI, Value: alphaIRI},
				Predicate: Term{Kind: TermIRI, Value: "http://purl.obolibrary.org/obo/BFO_0000196"},
				Object:    Term{Kind: TermBlankNode, Value: bAlt1Step0},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bAlt1Step0},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Object:    Term{Kind: TermIRI, Value: "https://www.commoncoreontologies.org/ont00000787"},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bAlt1Step0},
				Predicate: Term{Kind: TermIRI, Value: "https://www.commoncoreontologies.org/ont00001904"},
				Object:    Term{Kind: TermBlankNode, Value: bAlt1Step1},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bAlt1Step1},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Object:    Term{Kind: TermIRI, Value: "https://www.commoncoreontologies.org/ont00001163"},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bAlt1Step1},
				Predicate: Term{Kind: TermIRI, Value: "http://purl.obolibrary.org/obo/BFO_0000084"},
				Object:    Term{Kind: TermBlankNode, Value: bAlt1Step2},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bAlt1Step2},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Object:    Term{Kind: TermIRI, Value: "https://www.commoncoreontologies.org/ont00000253"},
			},
			{
				Subject:   Term{Kind: TermBlankNode, Value: bAlt1Step2},
				Predicate: Term{Kind: TermIRI, Value: "https://w3id.org/c360/diekm#altitudeMetersValue"},
				Object:    Term{Kind: TermLiteral, Value: "1500.5", Datatype: XSDDoubleIRI},
			},
		}
		for _, want := range expectedTriples {
			if !slices.Contains(gotTurtleGraph, want) {
				t.Fatalf("parsed Turtle graph missing expected triple %+v", want)
			}
		}
	})

	t.Run("CONSTRUCT WHERE short form and explicit folded reified chain template", func(t *testing.T) {
		resFoldedConstruct, err := exec.ExecuteQuery(context.Background(), `
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX cco: <https://www.commoncoreontologies.org/>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			CONSTRUCT WHERE {
				?s a diekm:IntelligencePlatform ;
				   obo:BFO_0000196 ?alt .
				?alt a cco:ont00000787 ;
				     cco:ont00001904 ?mice .
				?mice a cco:ont00001163 ;
				      obo:BFO_0000084 ?ibe .
				?ibe a cco:ont00000253 ;
				     diekm:altitudeMetersValue ?val .
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(CONSTRUCT WHERE folded chain): %v", err)
		}
		turtleOut, err := export.SerializeToString(resFoldedConstruct.Triples, export.Turtle, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(Turtle): %v", err)
		}
		parsed, err := parseIndependentTurtle(turtleOut)
		if err != nil {
			t.Fatalf("parseIndependentTurtle: %v", err)
		}
		// 1 rdf:type on entity + 2 altitude values * 7 chain triples = 15 triples.
		if len(parsed) != 15 {
			t.Fatalf("len(parsed) = %d, want 15:\n%s", len(parsed), turtleOut)
		}
	})

	t.Run("CONSTRUCT template using projected StandardIRI unfolds into reified chain", func(t *testing.T) {
		resFlatConstruct, err := exec.ExecuteQuery(context.Background(), `
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			CONSTRUCT {
				?s diekm:assignedRole ?roleName .
			}
			WHERE {
				?s obo:BFO_0000196 ?r .
				?r a obo:BFO_0000023 ;
				   rdfs:label ?roleName .
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(CONSTRUCT flat StandardIRI): %v", err)
		}
		ntOut, err := export.SerializeToString(resFlatConstruct.Triples, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(NTriples): %v", err)
		}
		parsed, err := parseIndependentNTriples(ntOut)
		if err != nil {
			t.Fatalf("parseIndependentNTriples: %v", err)
		}
		if !slices.Contains(parsed, rdfTriple{
			Subject:   Term{Kind: TermBlankNode, Value: bRoleISR},
			Predicate: Term{Kind: TermIRI, Value: "http://www.w3.org/2000/01/rdf-schema#label"},
			Object:    Term{Kind: TermLiteral, Value: "ISR", Datatype: XSDStringIRI},
		}) {
			t.Fatalf("expected flat StandardIRI in CONSTRUCT template to unfold into reified chain, got:\n%s", ntOut)
		}
	})
}

func TestUnfoldTerminalDatatypePropagation(t *testing.T) {
	defer registerConstructDIEKMVocab(t)()

	const (
		baseIRI = "https://example.org"
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idBravo = "c360.ops.uas1.intel.platform.bravo"
	)

	// Register projected predicates covering:
	// 1. Pragmatic "datetime" (vocabulary.DataTypeDateTime)
	// 2. Explicit "xsd:dateTime"
	// 3. Pragmatic "entity_id" (vocabulary.DataTypeEntityID)
	// 4. Explicit "iri"
	// 5. Pragmatic "int" on JSON float64(42)
	// 6. Pragmatic "bool"
	// 7. Pragmatic "json"
	vocabulary.Register("intel.temporal.observed-at",
		vocabulary.WithDataType(vocabulary.DataTypeDateTime),
		vocabulary.WithIRI("https://example.org/onto#observedAt"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000199",
					NodeClassIRI:      "http://purl.obolibrary.org/obo/BFO_0000202",
					ValuePredicateIRI: "https://example.org/onto#instantDateTime",
				},
			},
		}))
	vocabulary.Register("intel.temporal.reported-at",
		vocabulary.WithIRI("https://example.org/onto#reportedAt"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000199",
					NodeClassIRI:      "http://purl.obolibrary.org/obo/BFO_0000202",
					ValuePredicateIRI: "https://example.org/onto#reportedDateTime",
				},
			},
		}))
	vocabulary.Register("intel.rel.tracked-target",
		vocabulary.WithDataType(vocabulary.DataTypeEntityID),
		vocabulary.WithIRI("https://example.org/onto#trackedTarget"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/RO_0000056",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00000037",
					ValuePredicateIRI: "http://purl.obolibrary.org/obo/RO_0000057",
				},
			},
		}))
	vocabulary.Register("intel.rel.command-target",
		vocabulary.WithIRI("https://example.org/onto#commandTarget"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/RO_0000056",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00000005",
					ValuePredicateIRI: "https://example.org/onto#hasTargetResource",
				},
			},
		}))
	vocabulary.Register("intel.metric.sortie-count",
		vocabulary.WithDataType(vocabulary.DataTypeInt),
		vocabulary.WithIRI("https://example.org/onto#sortieCount"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000196",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00001163",
					ValuePredicateIRI: "https://example.org/onto#integerValue",
				},
			},
		}))
	vocabulary.Register("intel.status.active",
		vocabulary.WithDataType(vocabulary.DataTypeBool),
		vocabulary.WithIRI("https://example.org/onto#active"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000196",
					NodeClassIRI:      "http://purl.obolibrary.org/obo/BFO_0000019",
					ValuePredicateIRI: "https://example.org/onto#booleanValue",
				},
			},
		}))
	vocabulary.Register("intel.meta.raw-payload",
		vocabulary.WithDataType(vocabulary.DataTypeJSON),
		vocabulary.WithIRI("https://example.org/onto#rawPayload"),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000196",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00000253",
					ValuePredicateIRI: "https://example.org/onto#jsonValue",
				},
			},
		}))

	ent := graph.EntityState{
		ID: idAlpha,
		Triples: []message.Triple{
			{Subject: idAlpha, Predicate: "intel.temporal.observed-at", Object: "2026-10-15T12:34:56Z"},
			{Subject: idAlpha, Predicate: "intel.temporal.reported-at", Object: "2026-10-15T13:00:00Z", Datatype: "xsd:dateTime"},
			{Subject: idAlpha, Predicate: "intel.rel.tracked-target", Object: idBravo},
			{Subject: idAlpha, Predicate: "intel.rel.command-target", Object: idBravo, Datatype: "iri"},
			{Subject: idAlpha, Predicate: "intel.metric.sortie-count", Object: float64(42)},
			{Subject: idAlpha, Predicate: "intel.status.active", Object: true},
			{Subject: idAlpha, Predicate: "intel.meta.raw-payload", Object: `{"sensor":"eo"}`},
			{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
			{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
		},
	}

	triples, err := UnfoldEntity(&ent, baseIRI)
	if err != nil {
		t.Fatalf("UnfoldEntity: %v", err)
	}

	bravoIRI := export.SubjectIRI(idBravo, baseIRI)

	ntOut, err := export.SerializeToString(triples, export.NTriples, export.WithBaseIRI(baseIRI))
	if err != nil {
		t.Fatalf("SerializeToString(NTriples): %v", err)
	}
	turtleOut, err := export.SerializeToString(triples, export.Turtle, export.WithBaseIRI(baseIRI))
	if err != nil {
		t.Fatalf("SerializeToString(Turtle): %v", err)
	}
	jsonldOut, err := export.SerializeToString(triples, export.JSONLD, export.WithBaseIRI(baseIRI))
	if err != nil {
		t.Fatalf("SerializeToString(JSONLD): %v", err)
	}

	// Ensure no pragmatic DataType string ever leaked as a datatype IRI.
	for name, out := range map[string]string{"NTriples": ntOut, "Turtle": turtleOut, "JSONLD": jsonldOut} {
		for _, bad := range []string{"^^<float>", "^^<datetime>", "^^<entity_id>", "^^<iri>", "^^<@id>", "^^<int>", "^^<bool>", "^^<json>"} {
			if strings.Contains(out, bad) {
				t.Fatalf("%s leaked pragmatic datatype %s:\n%s", name, bad, out)
			}
		}
	}

	ntGraph, err := parseIndependentNTriples(ntOut)
	if err != nil {
		t.Fatalf("parseIndependentNTriples: %v\n%s", err, ntOut)
	}
	turtleGraph, err := parseIndependentTurtle(turtleOut)
	if err != nil {
		t.Fatalf("parseIndependentTurtle: %v\n%s", err, turtleOut)
	}
	jsonldGraph, err := parseIndependentJSONLD(jsonldOut)
	if err != nil {
		t.Fatalf("parseIndependentJSONLD: %v\n%s", err, jsonldOut)
	}

	assertSameRDFGraph(t, "Turtle vs N-Triples", turtleGraph, ntGraph)
	assertSameRDFGraph(t, "JSON-LD vs N-Triples", jsonldGraph, ntGraph)

	// Check each terminal predicate's parsed object Term across the graph.
	wantTerminalObjects := map[string]Term{
		"https://example.org/onto#instantDateTime": {
			Kind:     TermLiteral,
			Value:    "2026-10-15T12:34:56Z",
			Datatype: XSDDateTimeIRI,
		},
		"https://example.org/onto#reportedDateTime": {
			Kind:     TermLiteral,
			Value:    "2026-10-15T13:00:00Z",
			Datatype: XSDDateTimeIRI,
		},
		"http://purl.obolibrary.org/obo/RO_0000057": {
			Kind:  TermIRI,
			Value: bravoIRI,
		},
		"https://example.org/onto#hasTargetResource": {
			Kind:  TermIRI,
			Value: bravoIRI,
		},
		"https://example.org/onto#integerValue": {
			Kind:     TermLiteral,
			Value:    "42",
			Datatype: XSDIntegerIRI,
		},
		"https://example.org/onto#booleanValue": {
			Kind:     TermLiteral,
			Value:    "true",
			Datatype: XSDBooleanIRI,
		},
		"https://example.org/onto#jsonValue": {
			Kind:     TermLiteral,
			Value:    `{"sensor":"eo"}`,
			Datatype: "http://www.w3.org/1999/02/22-rdf-syntax-ns#JSON",
		},
		"https://w3id.org/c360/diekm#altitudeMetersValue": {
			Kind:     TermLiteral,
			Value:    "1500.5",
			Datatype: XSDDoubleIRI,
		},
		"http://www.w3.org/2000/01/rdf-schema#label": {
			Kind:     TermLiteral,
			Value:    "ISR",
			Datatype: XSDStringIRI,
		},
	}

	for predIRI, wantObj := range wantTerminalObjects {
		found := false
		for _, rt := range turtleGraph {
			if rt.Predicate.Value == predIRI {
				if rt.Subject.Kind != TermBlankNode || !strings.HasPrefix(rt.Subject.Value, "_:b_") {
					t.Fatalf("predicate %s subject = %+v, want deterministic blank node", predIRI, rt.Subject)
				}
				if rt.Object != wantObj {
					t.Fatalf("predicate %s object = %+v, want %+v", predIRI, rt.Object, wantObj)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("terminal predicate %s not found in unfolded graph", predIRI)
		}
	}
}

func TestDescribeOutgoingEntityCBDAndInvalidTripleGuard(t *testing.T) {
	defer registerConstructDIEKMVocab(t)()

	const (
		baseIRI = "https://example.org"
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idBravo = "c360.ops.uas1.intel.platform.bravo"
	)

	vocabulary.Register("c360.intel.tracks",
		vocabulary.WithDataType(vocabulary.DataTypeEntityID),
		vocabulary.WithIRI("https://example.org/onto#tracks"))

	entAlpha := graph.EntityState{
		ID: idAlpha,
		Triples: []message.Triple{
			{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
			{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
		},
	}
	// Bravo has an incoming reference to Alpha; DESCRIBE <alpha> must emit only Alpha's outgoing CBD (D.1d).
	entBravo := graph.EntityState{
		ID: idBravo,
		Triples: []message.Triple{
			{Subject: idBravo, Predicate: "c360.intel.tracks", Object: idAlpha},
		},
	}

	mr := mustMemoryReader(t, entAlpha, entBravo)
	mapper := irimap.NewMapper(baseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})

	alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
	bravoIRI, _ := mapper.EntityIDToIRI(idBravo)
	missingIRI, _ := mapper.EntityIDToIRI("c360.ops.uas1.intel.platform.missing")

	t.Run("DESCRIBE <iri> emits outgoing entity CBD + virtual rdf:type + unfolded chains and ignores incoming references", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`DESCRIBE <%s>`, alphaIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery(DESCRIBE): %v", err)
		}
		wantAlpha, err := UnfoldEntity(&entAlpha, baseIRI)
		if err != nil {
			t.Fatalf("UnfoldEntity(entAlpha): %v", err)
		}
		if len(res.Triples) != len(wantAlpha) {
			t.Fatalf("len(DESCRIBE triples) = %d, want %d", len(res.Triples), len(wantAlpha))
		}
		ntOut, err := export.SerializeToString(res.Triples, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString: %v", err)
		}
		if strings.Contains(ntOut, bravoIRI) {
			t.Fatalf("DESCRIBE <%s> leaked incoming reference from <%s>:\n%s", alphaIRI, bravoIRI, ntOut)
		}
	})

	t.Run("DESCRIBE absent entity IRI and non-entity IRI contribute 0 triples without failing", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			DESCRIBE <%s> <https://example.org/non-entity/resource>
		`, missingIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery(DESCRIBE missing/non-entity): %v", err)
		}
		if len(res.Triples) != 0 {
			t.Fatalf("expected 0 triples for missing/non-entity IRIs, got %d", len(res.Triples))
		}
	})

	t.Run("DESCRIBE ?var and DESCRIBE * with WHERE clause", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			DESCRIBE ?target
			WHERE {
				?src ex:tracks ?target .
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(DESCRIBE ?target): %v", err)
		}
		wantAlpha, _ := UnfoldEntity(&entAlpha, baseIRI)
		if len(res.Triples) != len(wantAlpha) {
			t.Fatalf("len(DESCRIBE ?target) = %d, want %d", len(res.Triples), len(wantAlpha))
		}

		resStar, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			DESCRIBE *
			WHERE {
				?src ex:tracks ?target .
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(DESCRIBE *): %v", err)
		}
		wantBravo, _ := UnfoldEntity(&entBravo, baseIRI)
		if len(resStar.Triples) != len(wantAlpha)+len(wantBravo) {
			t.Fatalf("len(DESCRIBE *) = %d, want %d", len(resStar.Triples), len(wantAlpha)+len(wantBravo))
		}
	})

	t.Run("CONSTRUCT and DESCRIBE enforce MaxResults on deduplicated output triples and support opt-in truncation", func(t *testing.T) {
		execCap := NewExecutor(mr, Config{
			Mapper:     mapper,
			MaxResults: 5,
		})
		_, err := execCap.ExecuteQuery(context.Background(), fmt.Sprintf(`DESCRIBE <%s>`, alphaIRI))
		if !errors.Is(err, ErrResultLimitExceeded) {
			t.Fatalf("DESCRIBE over MaxResults: err = %v, want ErrResultLimitExceeded", err)
		}

		resTrunc, err := execCap.ExecuteQuery(
			context.Background(),
			fmt.Sprintf(`DESCRIBE <%s>`, alphaIRI),
			WithAllowTruncation(true),
		)
		if err != nil {
			t.Fatalf("DESCRIBE with truncation: %v", err)
		}
		if !resTrunc.Truncated || resTrunc.TruncationReason != TruncationReasonResultLimit || len(resTrunc.Triples) != 5 {
			t.Fatalf("DESCRIBE truncated = %+v (len=%d), want Truncated=true reason=result_limit len=5", resTrunc, len(resTrunc.Triples))
		}
	})

	t.Run("ValidateTripleSerializable and UnfoldEntity fail loudly on triples that export.Serialize would silently omit", func(t *testing.T) {
		invalidCases := []struct {
			name string
			tr   message.Triple
		}{
			{
				name: "empty subject",
				tr:   message.Triple{Subject: "", Predicate: "https://example.org/p", Object: "v"},
			},
			{
				name: "empty predicate",
				tr:   message.Triple{Subject: idAlpha, Predicate: "", Object: "v"},
			},
			{
				name: "nil object",
				tr:   message.Triple{Subject: idAlpha, Predicate: "https://example.org/p", Object: nil},
			},
			{
				name: "NaN float64",
				tr:   message.Triple{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: math.NaN()},
			},
			{
				name: "Inf float64",
				tr:   message.Triple{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: math.Inf(1)},
			},
			{
				name: "relative pragmatic datatype",
				tr:   message.Triple{Subject: idAlpha, Predicate: "https://example.org/p", Object: 1500.5, Datatype: "float"},
			},
		}
		for _, tc := range invalidCases {
			t.Run(tc.name, func(t *testing.T) {
				err := validateTripleSerializable(tc.tr, baseIRI)
				if !errors.Is(err, ErrInvalidOutputTriple) {
					t.Fatalf("validateTripleSerializable(%s) = %v, want ErrInvalidOutputTriple", tc.name, err)
				}
				badEnt := graph.EntityState{
					ID:      idAlpha,
					Triples: []message.Triple{tc.tr},
				}
				if _, err := UnfoldEntity(&badEnt, baseIRI); !errors.Is(err, ErrInvalidOutputTriple) {
					t.Fatalf("UnfoldEntity(%s) = %v, want ErrInvalidOutputTriple", tc.name, err)
				}
			})
		}
	})

	t.Run("UnfoldEntity and CONSTRUCT preserve literals with embedded quote-at and quote-caret sequences and emit intermediate node superclasses", func(t *testing.T) {
		vocabulary.RegisterEntityType("intel.rolenode",
			vocabulary.WithClassIRI("http://purl.obolibrary.org/obo/BFO_0000023"),
			vocabulary.WithSuperclass("http://purl.obolibrary.org/obo/BFO_0000017"),
		)
		trickyVals := []string{
			`say "@bob" now`,
			`say "^^<https://example.org/dt>" now`,
			`slash \ and "quote" and "@tag"`,
		}
		trickyEnt := graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "intel.role.assigned", Object: trickyVals[0]},
				{Subject: idAlpha, Predicate: "intel.role.assigned", Object: trickyVals[1]},
				{Subject: idAlpha, Predicate: "intel.role.assigned", Object: trickyVals[2]},
			},
		}
		unfolded, err := UnfoldEntity(&trickyEnt, baseIRI)
		if err != nil {
			t.Fatalf("UnfoldEntity(trickyEnt): %v", err)
		}
		ntOut, err := export.SerializeToString(unfolded, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			t.Fatalf("SerializeToString(NTriples): %v", err)
		}
		parsed, err := parseIndependentNTriples(ntOut)
		if err != nil {
			t.Fatalf("parseIndependentNTriples: %v", err)
		}
		for _, wantVal := range trickyVals {
			foundVal := false
			for _, rt := range parsed {
				if rt.Predicate.Value == "http://www.w3.org/2000/01/rdf-schema#label" &&
					rt.Object.Kind == TermLiteral &&
					rt.Object.Value == wantVal &&
					rt.Object.Datatype == XSDStringIRI &&
					rt.Object.Language == "" {
					foundVal = true
				}
			}
			if !foundVal {
				t.Fatalf("unfolded graph missing exact xsd:string literal %q; parsed=%+v", wantVal, parsed)
			}
		}
		foundInterSuper := false
		for _, rt := range parsed {
			if rt.Subject.Kind == TermBlankNode &&
				rt.Predicate.Value == RDFTypeIRI &&
				rt.Object.Value == "http://purl.obolibrary.org/obo/BFO_0000017" {
				foundInterSuper = true
			}
		}
		if !foundInterSuper {
			t.Fatalf("expected intermediate blank node to include registered superclass BFO_0000017, got %+v", parsed)
		}
	})
}

func TestPropUnfoldAndConstructGraphEquivalence(t *testing.T) {
	defer registerConstructDIEKMVocab(t)()

	const baseIRI = "https://example.org"
	mapper := irimap.NewMapper(baseIRI)

	rapid.Check(t, func(rt *rapid.T) {
		numEntities := rapid.IntRange(1, 3).Draw(rt, "numEntities")
		entities := make([]graph.EntityState, 0, numEntities)
		for i := 0; i < numEntities; i++ {
			id := fmt.Sprintf("c360.ops.uas%d.intel.platform.uav%d", i+1, i+1)
			roles := rapid.SliceOfN(rapid.SampledFrom([]string{"ISR", "Recon", "Relay"}), 1, 2).Draw(rt, fmt.Sprintf("roles_%d", i))
			alts := rapid.SliceOfN(rapid.SampledFrom([]float64{1000.0, 1500.5, 2250.25}), 1, 2).Draw(rt, fmt.Sprintf("alts_%d", i))

			var triples []message.Triple
			for _, r := range roles {
				triples = append(triples, message.Triple{
					Subject:   id,
					Predicate: "intel.role.assigned",
					Object:    r,
				})
			}
			for _, a := range alts {
				triples = append(triples, message.Triple{
					Subject:   id,
					Predicate: "intel.measurement.altitude-m",
					Object:    a,
				})
			}
			entities = append(entities, graph.EntityState{
				ID:      id,
				Triples: triples,
			})
		}

		mr, err := reader.NewMemoryReader(entities...)
		if err != nil {
			rt.Fatalf("reader.NewMemoryReader: %v", err)
		}
		exec := NewExecutor(mr, Config{Mapper: mapper})

		resConstruct, err := exec.ExecuteQuery(context.Background(), `CONSTRUCT WHERE { ?s ?p ?o }`)
		if err != nil {
			rt.Fatalf("CONSTRUCT WHERE { ?s ?p ?o }: %v", err)
		}
		resDescribe, err := exec.ExecuteQuery(context.Background(), `DESCRIBE * WHERE { ?s a <https://w3id.org/c360/diekm#IntelligencePlatform> }`)
		if err != nil {
			rt.Fatalf("DESCRIBE *: %v", err)
		}

		ntConstruct, err := export.SerializeToString(resConstruct.Triples, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			rt.Fatalf("SerializeToString(NTriples): %v", err)
		}
		turtleConstruct, err := export.SerializeToString(resConstruct.Triples, export.Turtle, export.WithBaseIRI(baseIRI))
		if err != nil {
			rt.Fatalf("SerializeToString(Turtle): %v", err)
		}
		jsonldConstruct, err := export.SerializeToString(resConstruct.Triples, export.JSONLD, export.WithBaseIRI(baseIRI))
		if err != nil {
			rt.Fatalf("SerializeToString(JSONLD): %v", err)
		}
		ntDescribe, err := export.SerializeToString(resDescribe.Triples, export.NTriples, export.WithBaseIRI(baseIRI))
		if err != nil {
			rt.Fatalf("SerializeToString(DESCRIBE NTriples): %v", err)
		}

		gNT, err := parseIndependentNTriples(ntConstruct)
		if err != nil {
			rt.Fatalf("parseIndependentNTriples: %v", err)
		}
		gTurtle, err := parseIndependentTurtle(turtleConstruct)
		if err != nil {
			rt.Fatalf("parseIndependentTurtle: %v", err)
		}
		gJSONLD, err := parseIndependentJSONLD(jsonldConstruct)
		if err != nil {
			rt.Fatalf("parseIndependentJSONLD: %v", err)
		}
		gDescribe, err := parseIndependentNTriples(ntDescribe)
		if err != nil {
			rt.Fatalf("parseIndependentNTriples(DESCRIBE): %v", err)
		}

		if !equalRDFGraphs(gNT, gTurtle) {
			rt.Fatalf("Turtle graph (%d) != N-Triples graph (%d)", len(gTurtle), len(gNT))
		}
		if !equalRDFGraphs(gNT, gJSONLD) {
			rt.Fatalf("JSON-LD graph (%d) != N-Triples graph (%d)", len(gJSONLD), len(gNT))
		}
		if !equalRDFGraphs(gNT, gDescribe) {
			rt.Fatalf("DESCRIBE graph (%d) != CONSTRUCT graph (%d)", len(gDescribe), len(gNT))
		}
	})
}

// --- Independent RDF parsers for N-Triples, Turtle, and JSON-LD ---

func assertSameRDFGraph(t *testing.T, label string, got, want []rdfTriple) {
	t.Helper()
	if !equalRDFGraphs(got, want) {
		t.Fatalf("%s: RDF graphs differ:\n  got (%d):  %+v\n  want (%d): %+v", label, len(got), got, len(want), want)
	}
}

func equalRDFGraphs(a, b []rdfTriple) bool {
	ca := canonicalizeRDFGraph(a)
	cb := canonicalizeRDFGraph(b)
	return slices.Equal(ca, cb)
}

func canonicalizeRDFGraph(triples []rdfTriple) []rdfTriple {
	out := make([]rdfTriple, 0, len(triples))
	seen := make(map[rdfTriple]struct{}, len(triples))
	for _, tr := range triples {
		norm := normalizeRDFTripleForComparison(tr)
		if _, ok := seen[norm]; !ok {
			seen[norm] = struct{}{}
			out = append(out, norm)
		}
	}
	slices.SortFunc(out, func(x, y rdfTriple) int {
		if c := compareTermsTotalOrder(x.Subject, true, y.Subject, true); c != 0 {
			return c
		}
		if c := compareTermsTotalOrder(x.Predicate, true, y.Predicate, true); c != 0 {
			return c
		}
		return compareTermsTotalOrder(x.Object, true, y.Object, true)
	})
	return out
}

func normalizeRDFTripleForComparison(rt rdfTriple) rdfTriple {
	if rt.Object.Kind == TermLiteral {
		if rt.Object.Datatype == "" && rt.Object.Language == "" {
			rt.Object.Datatype = XSDStringIRI
		}
		if rt.Object.Datatype == XSDDoubleIRI {
			if f, err := strconv.ParseFloat(rt.Object.Value, 64); err == nil {
				s := fmt.Sprintf("%g", f)
				if !strings.Contains(s, ".") && !strings.Contains(s, "e") && !strings.Contains(s, "E") {
					s += ".0"
				}
				rt.Object.Value = s
			}
		}
	}
	return rt
}

func parseIndependentNTriples(input string) ([]rdfTriple, error) {
	var out []rdfTriple
	lines := strings.Split(input, "\n")
	for lineNo, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasSuffix(line, ".") {
			return nil, fmt.Errorf("line %d: missing trailing '.' in %q", lineNo+1, line)
		}
		body := strings.TrimSpace(line[:len(line)-1])
		subj, rest, err := parseNTSubject(body)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		pred, rest, err := parseNTPredicate(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		obj, err := parseTestNTriplesObject(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
		out = append(out, rdfTriple{
			Subject:   subj,
			Predicate: pred,
			Object:    obj,
		})
	}
	return canonicalizeRDFGraph(out), nil
}

func parseTestNTriplesObject(s string) (Term, error) {
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return Term{Kind: TermIRI, Value: s[1 : len(s)-1]}, nil
	}
	if strings.HasPrefix(s, "_:") {
		return Term{Kind: TermBlankNode, Value: s}, nil
	}
	if !strings.HasPrefix(s, `"`) {
		return Term{}, fmt.Errorf("invalid N-Triples object %q", s)
	}
	var lexBuf strings.Builder
	closeQuote := -1
	for i := 1; i < len(s); i++ {
		if s[i] == '"' {
			closeQuote = i
			break
		}
		if s[i] == '\\' {
			i++
			if i >= len(s) {
				return Term{}, fmt.Errorf("unterminated escape in %q", s)
			}
			switch s[i] {
			case '\\', '"':
				lexBuf.WriteByte(s[i])
			case 'n':
				lexBuf.WriteByte('\n')
			case 'r':
				lexBuf.WriteByte('\r')
			case 't':
				lexBuf.WriteByte('\t')
			default:
				return Term{}, fmt.Errorf("invalid escape \\%c in %q", s[i], s)
			}
			continue
		}
		lexBuf.WriteByte(s[i])
	}
	if closeQuote < 0 {
		return Term{}, fmt.Errorf("unterminated quoted literal %q", s)
	}
	tail := s[closeQuote+1:]
	dt := XSDStringIRI
	lang := ""
	if tail != "" {
		if strings.HasPrefix(tail, "^^<") && strings.HasSuffix(tail, ">") {
			dt = tail[3 : len(tail)-1]
			if strings.HasPrefix(dt, "@") && len(dt) > 1 {
				lang = strings.ToLower(dt[1:])
				dt = RDFLangStringIRI
			}
		} else if strings.HasPrefix(tail, "@") && len(tail) > 1 {
			lang = strings.ToLower(tail[1:])
			dt = RDFLangStringIRI
		} else {
			return Term{}, fmt.Errorf("unexpected literal suffix %q in %q", tail, s)
		}
	}
	return Term{Kind: TermLiteral, Value: lexBuf.String(), Datatype: dt, Language: lang}, nil
}

func parseNTSubject(s string) (Term, string, error) {
	if strings.HasPrefix(s, "<") {
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return Term{}, "", errors.New("unterminated subject IRI")
		}
		return Term{Kind: TermIRI, Value: s[1:end]}, s[end+1:], nil
	}
	if strings.HasPrefix(s, "_:") {
		idx := strings.IndexAny(s, " \t")
		if idx < 0 {
			return Term{}, "", errors.New("missing predicate after blank node subject")
		}
		return Term{Kind: TermBlankNode, Value: s[:idx]}, s[idx:], nil
	}
	return Term{}, "", fmt.Errorf("invalid N-Triples subject in %q", s)
}

func parseNTPredicate(s string) (Term, string, error) {
	if !strings.HasPrefix(s, "<") {
		return Term{}, "", fmt.Errorf("expected <predicate>, got %q", s)
	}
	end := strings.IndexByte(s, '>')
	if end < 0 {
		return Term{}, "", errors.New("unterminated predicate IRI")
	}
	return Term{Kind: TermIRI, Value: s[1:end]}, s[end+1:], nil
}

// parseIndependentTurtle parses a W3C Turtle document into canonical rdfTriples
// using an independent test-only scanner (without calling sparql's production lexer).
func parseIndependentTurtle(input string) ([]rdfTriple, error) {
	toks, err := scanTestTurtleTokens(input)
	if err != nil {
		return nil, err
	}
	prefixes := make(map[string]string)
	pos := 0
	peek := func() string {
		if pos >= len(toks) {
			return ""
		}
		return toks[pos]
	}
	take := func() string {
		t := peek()
		if pos < len(toks) {
			pos++
		}
		return t
	}
	resolveTok := func() (Term, error) {
		return resolveTestTurtleToken(take(), prefixes)
	}

	var out []rdfTriple
	for peek() != "" {
		if strings.EqualFold(peek(), "@prefix") || strings.EqualFold(peek(), "PREFIX") {
			kw := take()
			pfxTok := take()
			colon := strings.IndexByte(pfxTok, ':')
			if colon < 0 {
				return nil, fmt.Errorf("invalid prefix name %q", pfxTok)
			}
			iriTok := take()
			if !strings.HasPrefix(iriTok, "<") || !strings.HasSuffix(iriTok, ">") {
				return nil, fmt.Errorf("expected <namespace>, got %q", iriTok)
			}
			prefixes[pfxTok[:colon]] = iriTok[1 : len(iriTok)-1]
			if strings.HasPrefix(kw, "@") && peek() == "." {
				take()
			}
			continue
		}

		subj, err := resolveTok()
		if err != nil {
			return nil, err
		}
		for {
			pred, err := resolveTok()
			if err != nil {
				return nil, err
			}
			for {
				obj, err := resolveTok()
				if err != nil {
					return nil, err
				}
				out = append(out, rdfTriple{Subject: subj, Predicate: pred, Object: obj})
				if peek() == "," {
					take()
					continue
				}
				break
			}
			if peek() == ";" {
				take()
				if peek() == "." {
					break
				}
				continue
			}
			break
		}
		if take() != "." {
			return nil, errors.New("expected '.' at end of Turtle statement")
		}
	}
	return canonicalizeRDFGraph(out), nil
}

func resolveTestTurtleToken(raw string, prefixes map[string]string) (Term, error) {
	if raw == "" {
		return Term{}, errors.New("unexpected EOF in Turtle term")
	}
	if strings.HasPrefix(raw, "<") && strings.HasSuffix(raw, ">") {
		return Term{Kind: TermIRI, Value: raw[1 : len(raw)-1]}, nil
	}
	if strings.HasPrefix(raw, "_:") {
		return Term{Kind: TermBlankNode, Value: raw}, nil
	}
	if strings.HasPrefix(raw, `"`) {
		return parseTestTurtleLiteral(raw, prefixes)
	}
	if raw == "a" {
		return Term{Kind: TermIRI, Value: RDFTypeIRI}, nil
	}
	if raw == "true" || raw == "false" {
		return Term{Kind: TermLiteral, Value: raw, Datatype: XSDBooleanIRI}, nil
	}
	if colon := strings.IndexByte(raw, ':'); colon >= 0 {
		pfx := raw[:colon]
		local := raw[colon+1:]
		ns, ok := prefixes[pfx]
		if !ok {
			return Term{}, fmt.Errorf("undeclared Turtle prefix %q in %q", pfx, raw)
		}
		return Term{Kind: TermIRI, Value: ns + local}, nil
	}
	if _, err := strconv.ParseInt(raw, 10, 64); err == nil {
		return Term{Kind: TermLiteral, Value: raw, Datatype: XSDIntegerIRI}, nil
	}
	if _, err := strconv.ParseFloat(raw, 64); err == nil {
		if strings.ContainsAny(raw, "eE") {
			return Term{Kind: TermLiteral, Value: raw, Datatype: XSDDoubleIRI}, nil
		}
		return Term{Kind: TermLiteral, Value: raw, Datatype: XSDDecimalIRI}, nil
	}
	return Term{}, fmt.Errorf("unrecognized Turtle token %q", raw)
}

func parseTestTurtleLiteral(raw string, prefixes map[string]string) (Term, error) {
	if idx := strings.LastIndex(raw, `"^^`); idx >= 1 {
		dtRef := raw[idx+3:]
		if !strings.HasPrefix(dtRef, "<") {
			if colon := strings.IndexByte(dtRef, ':'); colon >= 0 {
				ns, ok := prefixes[dtRef[:colon]]
				if !ok {
					return Term{}, fmt.Errorf("undeclared datatype prefix in %q", raw)
				}
				raw = raw[:idx+3] + "<" + ns + dtRef[colon+1:] + ">"
			}
		}
	}
	return parseTestNTriplesObject(raw)
}

func scanTestTurtleTokens(s string) ([]string, error) {
	var toks []string
	i := 0
	for i < len(s) {
		ch := s[i]
		if ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r' {
			i++
			continue
		}
		if ch == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			continue
		}
		if ch == ';' || ch == ',' {
			toks = append(toks, string(ch))
			i++
			continue
		}
		if ch == '.' {
			if i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == '\t' || s[i+1] == '\n' || s[i+1] == '\r' {
				toks = append(toks, ".")
				i++
				continue
			}
		}
		if ch == '<' {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				return nil, errors.New("unterminated <IRI> in Turtle")
			}
			toks = append(toks, s[i:i+end+1])
			i += end + 1
			continue
		}
		if ch == '"' {
			start := i
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == '"' {
					i++
					break
				}
				i++
			}
			if i+1 < len(s) && s[i] == '^' && s[i+1] == '^' {
				i += 2
				if i < len(s) && s[i] == '<' {
					end := strings.IndexByte(s[i:], '>')
					if end < 0 {
						return nil, errors.New("unterminated datatype <IRI> in Turtle")
					}
					i += end + 1
				} else {
					for i < len(s) && !strings.ContainsRune(" \t\n\r;,.#", rune(s[i])) {
						i++
					}
				}
			} else if i < len(s) && s[i] == '@' {
				for i < len(s) && !strings.ContainsRune(" \t\n\r;,.#", rune(s[i])) {
					i++
				}
			}
			toks = append(toks, s[start:i])
			continue
		}
		start := i
		for i < len(s) && !strings.ContainsRune(" \t\n\r;,\"#", rune(s[i])) {
			if s[i] == '.' && (i+1 >= len(s) || strings.ContainsRune(" \t\n\r", rune(s[i+1]))) {
				break
			}
			i++
		}
		toks = append(toks, s[start:i])
	}
	return toks, nil
}

// parseIndependentJSONLD parses a JSON-LD document (@context + single node or @graph)
// into canonical rdfTriples.
func parseIndependentJSONLD(input string) ([]rdfTriple, error) {
	dec := json.NewDecoder(strings.NewReader(input))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}

	prefixes := make(map[string]string)
	if ctxRaw, ok := doc["@context"].(map[string]any); ok {
		for k, v := range ctxRaw {
			switch tv := v.(type) {
			case string:
				prefixes[k] = tv
			case map[string]any:
				if id, ok := tv["@id"].(string); ok {
					prefixes[k] = id
				}
			}
		}
	}

	expandCompact := func(term string) string {
		if strings.HasPrefix(term, "_:") || strings.HasPrefix(term, "http://") || strings.HasPrefix(term, "https://") || strings.HasPrefix(term, "urn:") {
			return term
		}
		if colon := strings.IndexByte(term, ':'); colon > 0 {
			pfx := term[:colon]
			if ns, ok := prefixes[pfx]; ok {
				return ns + term[colon+1:]
			}
		}
		return term
	}

	var nodes []map[string]any
	if graphRaw, ok := doc["@graph"].([]any); ok {
		for _, item := range graphRaw {
			if m, ok := item.(map[string]any); ok {
				nodes = append(nodes, m)
			}
		}
	} else if _, hasID := doc["@id"]; hasID {
		nodes = append(nodes, doc)
	}

	var out []rdfTriple
	for _, node := range nodes {
		rawID, _ := node["@id"].(string)
		if rawID == "" {
			continue
		}
		expandedSubj := expandCompact(rawID)
		subjTerm := Term{Kind: TermIRI, Value: expandedSubj}
		if strings.HasPrefix(expandedSubj, "_:") {
			subjTerm = Term{Kind: TermBlankNode, Value: expandedSubj}
		}

		for k, rawVal := range node {
			if k == "@id" || k == "@context" {
				continue
			}
			predIRI := expandCompact(k)
			if k == "@type" {
				predIRI = RDFTypeIRI
			}
			predTerm := Term{Kind: TermIRI, Value: predIRI}

			var items []any
			if arr, ok := rawVal.([]any); ok {
				items = arr
			} else {
				items = []any{rawVal}
			}

			for _, item := range items {
				objTerm, err := jsonldValueToTerm(item, expandCompact)
				if err != nil {
					return nil, err
				}
				out = append(out, rdfTriple{
					Subject:   subjTerm,
					Predicate: predTerm,
					Object:    objTerm,
				})
			}
		}
	}
	return canonicalizeRDFGraph(out), nil
}

func jsonldValueToTerm(item any, expandCompact func(string) string) (Term, error) {
	switch v := item.(type) {
	case string:
		return Term{Kind: TermLiteral, Value: v, Datatype: XSDStringIRI}, nil
	case bool:
		return Term{Kind: TermLiteral, Value: strconv.FormatBool(v), Datatype: XSDBooleanIRI}, nil
	case json.Number:
		s := v.String()
		if strings.ContainsAny(s, ".eE") {
			return Term{Kind: TermLiteral, Value: s, Datatype: XSDDoubleIRI}, nil
		}
		return Term{Kind: TermLiteral, Value: s, Datatype: XSDIntegerIRI}, nil
	case map[string]any:
		if id, ok := v["@id"].(string); ok {
			expanded := expandCompact(id)
			if strings.HasPrefix(expanded, "_:") {
				return Term{Kind: TermBlankNode, Value: expanded}, nil
			}
			return Term{Kind: TermIRI, Value: expanded}, nil
		}
		if val, ok := v["@value"]; ok {
			lex := fmt.Sprintf("%v", val)
			dt := XSDStringIRI
			if rawType, ok := v["@type"].(string); ok {
				dt = expandCompact(rawType)
			}
			lang := ""
			if rawLang, ok := v["@language"].(string); ok {
				lang = strings.ToLower(rawLang)
				dt = RDFLangStringIRI
			}
			return Term{Kind: TermLiteral, Value: lex, Datatype: dt, Language: lang}, nil
		}
	}
	return Term{}, fmt.Errorf("unsupported JSON-LD value %#v", item)
}
