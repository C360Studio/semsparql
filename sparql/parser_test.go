package sparql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/c360studio/semengine/pkg/errs"
	"pgregory.net/rapid"
)

func TestParseSPARQLSupportedSubset(t *testing.T) {
	t.Run("SELECT with BASE, PREFIX, BGP shorthands, paths, OPTIONAL, UNION, FILTER, ORDER BY, LIMIT, OFFSET", func(t *testing.T) {
		qStr := `
			# Leading comment
			BASE <https://c360.io/semengine/>
			PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX cco: <https://www.commoncoreontologies.org/>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>

			SELECT DISTINCT ?platform $role ?altMeters
			WHERE {
				?platform rdf:type/rdfs:subClassOf* obo:BFO_0000030 ;
				          a cco:Vehicle , <entities/c360/ops/sys1/intel/platform/uav1> .
				?sub rdfs:subClassOf* obo:BFO_0000001 .
				OPTIONAL {
					?platform obo:RO_0000053 $role .
				}
				{
					?platform diekm:altitudeMetersValue ?altMeters .
				} UNION {
					?platform cco:has_decimal_value ?altMeters .
				}
				FILTER (
					BOUND($role) &&
					REGEX(STR(?platform), "^https://c360\\.io/", "i") &&
					(LANG(?altMeters) = "" || DATATYPE(?altMeters) = xsd:double) &&
					!(?altMeters < -10.5 || ?altMeters >= +99999)
				)
			}
			ORDER BY ASC(?platform) DESC(?altMeters) STR($role)
			LIMIT 25
			OFFSET 5
		`
		q, err := Parse(qStr)
		if err != nil {
			t.Fatalf("Parse failed: %v", err)
		}
		if q.Form != QuerySelect {
			t.Fatalf("Form = %v, want QuerySelect", q.Form)
		}
		if !q.Distinct {
			t.Fatal("Distinct = false, want true")
		}
		if q.SelectAll {
			t.Fatal("SelectAll = true, want false")
		}
		wantVars := []string{"platform", "role", "altMeters"}
		if len(q.Variables) != len(wantVars) {
			t.Fatalf("Variables = %v, want %v", q.Variables, wantVars)
		}
		for i := range wantVars {
			if q.Variables[i] != wantVars[i] {
				t.Fatalf("Variables[%d] = %q, want %q", i, q.Variables[i], wantVars[i])
			}
		}
		if q.Base != "https://c360.io/semengine/" {
			t.Fatalf("Base = %q, want https://c360.io/semengine/", q.Base)
		}
		if len(q.Where.Elements) != 3 {
			t.Fatalf("len(Where.Elements) = %d, want 3 (BGP, OPTIONAL, UNION)", len(q.Where.Elements))
		}
		bgp, ok := q.Where.Elements[0].(BGPPattern)
		if !ok {
			t.Fatalf("Elements[0] = %T, want BGPPattern", q.Where.Elements[0])
		}
		if len(bgp.Triples) != 4 {
			t.Fatalf("len(bgp.Triples) = %d, want 4", len(bgp.Triples))
		}
		if !bgp.Triples[0].IsRDFTypeWithSubClassPath() {
			t.Fatalf("Triples[0] path = %v, want PathRDFTypeSubClassOfStar", bgp.Triples[0].Path)
		}
		if !bgp.Triples[1].IsPlainRDFType() || bgp.Triples[1].Object.Value != "https://www.commoncoreontologies.org/Vehicle" {
			t.Fatalf("Triples[1] = %+v, want plain rdf:type cco:Vehicle", bgp.Triples[1])
		}
		if !bgp.Triples[2].IsPlainRDFType() || bgp.Triples[2].Object.Value != "https://c360.io/semengine/entities/c360/ops/sys1/intel/platform/uav1" {
			t.Fatalf("Triples[2] = %+v, want base-resolved relative IRI", bgp.Triples[2])
		}
		if !bgp.Triples[3].IsStandaloneSubClassPath() {
			t.Fatalf("Triples[3] path = %v, want PathSubClassOfStar", bgp.Triples[3].Path)
		}
		if _, ok := q.Where.Elements[1].(OptionalPattern); !ok {
			t.Fatalf("Elements[1] = %T, want OptionalPattern", q.Where.Elements[1])
		}
		union, ok := q.Where.Elements[2].(UnionPattern)
		if !ok || len(union.Branches) != 2 {
			t.Fatalf("Elements[2] = %+v, want UnionPattern with 2 branches", q.Where.Elements[2])
		}
		if len(q.Where.Filters) != 1 {
			t.Fatalf("len(Where.Filters) = %d, want 1", len(q.Where.Filters))
		}
		if len(q.OrderBy) != 3 {
			t.Fatalf("len(OrderBy) = %d, want 3", len(q.OrderBy))
		}
		if q.OrderBy[0].Direction != OrderAsc || q.OrderBy[1].Direction != OrderDesc || q.OrderBy[2].Direction != OrderAsc {
			t.Fatalf("OrderBy directions = %+v, want [ASC, DESC, ASC]", q.OrderBy)
		}
		if q.Limit == nil || *q.Limit != 25 || q.Offset != 5 {
			t.Fatalf("Limit=%v Offset=%d, want Limit=25 Offset=5", q.Limit, q.Offset)
		}
	})

	t.Run("ASK, CONSTRUCT template and shorthand, DESCRIBE IRI and WHERE", func(t *testing.T) {
		askQ, err := Parse(`PREFIX cco: <https://www.commoncoreontologies.org/> ASK { ?s a/rdfs:subClassOf* cco:Vehicle }`)
		if err == nil {
			t.Fatalf("expected error for undeclared prefix rdfs in ASK, got %+v", askQ)
		}

		askQ, err = Parse(`
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX cco: <https://www.commoncoreontologies.org/>
			ASK WHERE { ?s a/rdfs:subClassOf* cco:Vehicle }
		`)
		if err != nil {
			t.Fatalf("Parse ASK failed: %v", err)
		}
		if askQ.Form != QueryAsk || !askQ.HasWhere {
			t.Fatalf("ASK query = %+v", askQ)
		}

		constructQ, err := Parse(`
			PREFIX cco: <https://www.commoncoreontologies.org/>
			CONSTRUCT {
				?s a cco:Vehicle ;
				   cco:has_text "hello\nworld"@en-US .
			}
			WHERE {
				?s a cco:Vehicle .
				FILTER REGEX("abc", "a")
			}
			OFFSET 2
			LIMIT 10
		`)
		if err != nil {
			t.Fatalf("Parse CONSTRUCT failed: %v", err)
		}
		if constructQ.Form != QueryConstruct || len(constructQ.Construct) != 2 {
			t.Fatalf("CONSTRUCT query = %+v, want 2 template triples", constructQ)
		}
		lit := constructQ.Construct[1].Object
		if lit.Kind != TermLiteral || lit.Value != "hello\nworld" || lit.Language != "en-us" || lit.Datatype != RDFLangStringIRI {
			t.Fatalf("CONSTRUCT literal = %+v, want language-tagged literal", lit)
		}
		if constructQ.Limit == nil || *constructQ.Limit != 10 || constructQ.Offset != 2 {
			t.Fatalf("CONSTRUCT Limit=%v Offset=%d, want 10 and 2", constructQ.Limit, constructQ.Offset)
		}

		shorthandQ, err := Parse(`
			PREFIX cco: <https://www.commoncoreontologies.org/>
			CONSTRUCT WHERE { ?s a cco:Vehicle . }
		`)
		if err != nil {
			t.Fatalf("Parse CONSTRUCT WHERE failed: %v", err)
		}
		if shorthandQ.Form != QueryConstruct || len(shorthandQ.Construct) != 1 {
			t.Fatalf("CONSTRUCT WHERE = %+v, want 1 template triple", shorthandQ)
		}

		describeIRI, err := Parse(`DESCRIBE <https://c360.io/semengine/entities/c360/ops/sys1/intel/platform/uav1>`)
		if err != nil {
			t.Fatalf("Parse DESCRIBE IRI failed: %v", err)
		}
		if describeIRI.Form != QueryDescribe || describeIRI.HasWhere || len(describeIRI.DescribeIRIs) != 1 {
			t.Fatalf("DESCRIBE IRI = %+v", describeIRI)
		}

		describeWhere, err := Parse(`
			PREFIX cco: <https://www.commoncoreontologies.org/>
			DESCRIBE DISTINCT ?s <https://c360.io/semengine/entities/c360/ops/sys1/intel/platform/uav1>
			WHERE { ?s a cco:Vehicle }
		`)
		if err != nil {
			t.Fatalf("Parse DESCRIBE WHERE failed: %v", err)
		}
		if describeWhere.Form != QueryDescribe || !describeWhere.Distinct || !describeWhere.HasWhere || len(describeWhere.Variables) != 1 || len(describeWhere.DescribeIRIs) != 1 {
			t.Fatalf("DESCRIBE WHERE = %+v", describeWhere)
		}
	})

	t.Run("Blank nodes, long literals, numeric/boolean literals, escapes, and parenthesized paths", func(t *testing.T) {
		q, err := Parse(`
			BASE <https://example.org/>
			BASE <sub/>
			PREFIX : <https://example.org/default#>
			PREFIX ex: <https://example.org/ns#>
			PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT * {
				_:b1.2 ex:prop [ ex:num 42 ; ex:pos +7 ; ex:neg -3.14 ; ex:negDbl -1.5e2 ; ex:dotDec .5 ;
				                 ex:bool true ; ex:boolF false ;
				                 ex:long """line1
line2""" ; ex:sq '''single\'quote\t\b\r\f\"\\\u0041\U00000042''' ;
				                 ex:dt1 "2026-04-15T00:00:00Z"^^<http://www.w3.org/2001/XMLSchema#dateTime> ;
				                 ex:dt2 "123"^^ex:customType ;
				                 ex:esc\-\.%20name <\u0069tem> ;
				                 :emptyPfx :val ;
				                 ex:anon [] ] .
				_:b1.2 :emptyPfx :val.
				[ a ex:StandaloneBNode ] .
				[ a ex:SubjectBNode ] ex:hasValue "ok" .
				{
					?s (rdf:type)/(rdfs:subClassOf*) ex:Thing .
					?s (rdfs:subClassOf)* ex:Other .
				}
				FILTER (?x <= +1 && ?x > -1 && ?x != 0 && BOUND(?x))
			}
			ORDER BY (?x)
		`)
		if err != nil {
			t.Fatalf("Parse rich lexical query failed: %v", err)
		}
		if !q.SelectAll || q.Base != "https://example.org/sub/" {
			t.Fatalf("unexpected query header: %+v", q)
		}
		if len(q.Where.Elements) != 2 {
			t.Fatalf("len(Where.Elements) = %d, want 2 (BGP + SubGroupPattern)", len(q.Where.Elements))
		}
		if _, ok := q.Where.Elements[1].(SubGroupPattern); !ok {
			t.Fatalf("Elements[1] = %T, want SubGroupPattern", q.Where.Elements[1])
		}
	})

	t.Run("DESCRIBE wildcard and prefixed name", func(t *testing.T) {
		q1, err := Parse(`DESCRIBE * WHERE { ?s ?p ?o }`)
		if err != nil || !q1.DescribeAll || !q1.HasWhere {
			t.Fatalf("DESCRIBE * = (%+v, %v)", q1, err)
		}
		q2, err := Parse(`PREFIX ex: <https://example.org/> DESCRIBE ex:uav1`)
		if err != nil || len(q2.DescribeIRIs) != 1 || q2.DescribeIRIs[0] != "https://example.org/uav1" {
			t.Fatalf("DESCRIBE ex:uav1 = (%+v, %v)", q2, err)
		}
	})
}

func TestUnsupportedFeatureErrors(t *testing.T) {
	cases := []struct {
		name        string
		query       string
		wantFeature string
	}{
		{
			name:        "REDUCED modifier in SELECT",
			query:       `SELECT REDUCED ?s WHERE { ?s ?p ?o }`,
			wantFeature: "REDUCED",
		},
		{
			name:        "REDUCED modifier in DESCRIBE",
			query:       `DESCRIBE REDUCED ?s WHERE { ?s ?p ?o }`,
			wantFeature: "REDUCED",
		},
		{
			name:        "SPARQL Update INSERT DATA",
			query:       `INSERT DATA { <https://example.org/s> <https://example.org/p> "v" }`,
			wantFeature: "SPARQL Update",
		},
		{
			name:        "SPARQL Update DELETE WHERE",
			query:       `DELETE WHERE { ?s ?p ?o }`,
			wantFeature: "SPARQL Update",
		},
		{
			name:        "SPARQL Update LOAD",
			query:       `LOAD <https://example.org/data.ttl>`,
			wantFeature: "SPARQL Update",
		},
		{
			name:        "SPARQL Update CLEAR",
			query:       `CLEAR ALL`,
			wantFeature: "SPARQL Update",
		},
		{
			name:        "SERVICE federated query",
			query:       `SELECT ?s WHERE { SERVICE <https://example.org/sparql> { ?s ?p ?o } }`,
			wantFeature: "SERVICE",
		},
		{
			name:        "GRAPH named graph pattern",
			query:       `SELECT ?s WHERE { GRAPH <https://example.org/g> { ?s ?p ?o } }`,
			wantFeature: "GRAPH",
		},
		{
			name:        "FROM dataset clause",
			query:       `SELECT ?s FROM <https://example.org/g> WHERE { ?s ?p ?o }`,
			wantFeature: "FROM",
		},
		{
			name:        "FROM NAMED dataset clause",
			query:       `SELECT ?s FROM NAMED <https://example.org/g> WHERE { ?s ?p ?o }`,
			wantFeature: "FROM",
		},
		{
			name:        "GROUP BY modifier",
			query:       `SELECT ?s WHERE { ?s ?p ?o } GROUP BY ?s`,
			wantFeature: "GROUP BY",
		},
		{
			name:        "HAVING modifier",
			query:       `SELECT ?s WHERE { ?s ?p ?o } HAVING (?s = ?o)`,
			wantFeature: "HAVING",
		},
		{
			name:        "COUNT aggregate in SELECT",
			query:       `SELECT (COUNT(?s) AS ?c) WHERE { ?s ?p ?o }`,
			wantFeature: "COUNT",
		},
		{
			name:        "Bare COUNT aggregate in SELECT",
			query:       `SELECT COUNT(?s) WHERE { ?s ?p ?o }`,
			wantFeature: "COUNT",
		},
		{
			name:        "SUM aggregate in FILTER",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER (SUM(?o) > 10) }`,
			wantFeature: "SUM",
		},
		{
			name:        "Subquery inside WHERE",
			query:       `SELECT ?s WHERE { { SELECT ?s WHERE { ?s ?p ?o } } }`,
			wantFeature: "subquery",
		},
		{
			name:        "Direct SELECT inside WHERE",
			query:       `SELECT ?s WHERE { ?s ?p ?o . SELECT ?x WHERE { ?x ?y ?z } }`,
			wantFeature: "subquery",
		},
		{
			name:        "VALUES inline data in WHERE",
			query:       `SELECT ?s WHERE { ?s ?p ?o VALUES ?s { <https://example.org/s1> } }`,
			wantFeature: "VALUES",
		},
		{
			name:        "Trailing VALUES clause",
			query:       `SELECT ?s WHERE { ?s ?p ?o } VALUES ?s { <https://example.org/s1> }`,
			wantFeature: "VALUES",
		},
		{
			name:        "BIND in WHERE",
			query:       `SELECT ?s ?v WHERE { ?s ?p ?o BIND(?o AS ?v) }`,
			wantFeature: "BIND",
		},
		{
			name:        "Projection expression AS in SELECT",
			query:       `SELECT (?o AS ?v) WHERE { ?s ?p ?o }`,
			wantFeature: "BIND",
		},
		{
			name:        "MINUS graph pattern",
			query:       `SELECT ?s WHERE { ?s ?p ?o MINUS { ?s ?q ?r } }`,
			wantFeature: "MINUS",
		},
		{
			name:        "FILTER EXISTS",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER EXISTS { ?s ?q ?r } }`,
			wantFeature: "EXISTS",
		},
		{
			name:        "FILTER NOT EXISTS",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER NOT EXISTS { ?s ?q ?r } }`,
			wantFeature: "EXISTS",
		},
		{
			name:        "FILTER IN operator",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER (?o IN ("a", "b")) }`,
			wantFeature: "IN",
		},
		{
			name:        "FILTER NOT IN operator",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER (?o NOT IN ("a", "b")) }`,
			wantFeature: "IN",
		},
		{
			name:        "Unsupported FILTER function COALESCE",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER (COALESCE(?o, "x") = "x") }`,
			wantFeature: "COALESCE",
		},
		{
			name:        "Custom IRI function call in FILTER",
			query:       `SELECT ?s WHERE { ?s ?p ?o FILTER (<https://example.org/fn>(?o)) }`,
			wantFeature: "custom function",
		},
		{
			name:        "Variable in property path",
			query:       `SELECT ?s WHERE { ?s ?p* ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Arbitrary sequence property path",
			query:       `PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ex:p/ex:q ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Alternative property path",
			query:       `PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ex:p|ex:q ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Inverse property path",
			query:       `PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ^ex:p ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "One-or-more property path on subClassOf",
			query:       `PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#> SELECT ?s WHERE { ?s rdfs:subClassOf+ ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Zero-or-one property path",
			query:       `PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#> SELECT ?s WHERE { ?s rdfs:subClassOf? ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Zero-or-more property path on non-subClassOf predicate",
			query:       `PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ex:p* ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Negated property set path",
			query:       `PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#> SELECT ?s WHERE { ?s !rdf:type ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Property path in CONSTRUCT template",
			query:       `PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#> CONSTRUCT { ?s rdfs:subClassOf* ?o } WHERE { ?s rdfs:subClassOf* ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "Property path in CONSTRUCT WHERE shorthand",
			query:       `PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#> CONSTRUCT WHERE { ?s rdfs:subClassOf* ?o }`,
			wantFeature: "property path",
		},
		{
			name:        "RDF collection list in BGP subject",
			query:       `PREFIX ex: <https://example.org/> SELECT ?s WHERE { (1 2) ex:p ?s }`,
			wantFeature: "RDF collection",
		},
		{
			name:        "RDF collection list in BGP object",
			query:       `PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ex:p (1 2) }`,
			wantFeature: "RDF collection",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.query)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded with %+v, want ErrUnsupportedFeature naming %q", tc.query, got, tc.wantFeature)
			}
			if !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("Parse(%q) err = %v, want errors.Is(err, ErrUnsupportedFeature)", tc.query, err)
			}
			var ce *errs.ClassifiedError
			if !errors.As(err, &ce) || ce.Class != errs.ErrorInvalid || ce.Code != ErrorCodeUnsupportedFeature {
				t.Fatalf("Parse(%q) classified err = %+v, want ErrorInvalid/%s", tc.query, ce, ErrorCodeUnsupportedFeature)
			}
			if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.wantFeature)) {
				t.Fatalf("Parse(%q) err = %q, does not name feature %q", tc.query, err.Error(), tc.wantFeature)
			}
			featDetail, _ := ce.Detail[DetailKeyFeature].(string)
			if !strings.Contains(strings.ToLower(featDetail), strings.ToLower(tc.wantFeature)) {
				t.Fatalf("Parse(%q) Detail[%q] = %q, does not name feature %q", tc.query, DetailKeyFeature, featDetail, tc.wantFeature)
			}
		})
	}
}

func TestParseSPARQLSyntaxErrors(t *testing.T) {
	badQueries := []string{
		"",
		"\xff\xfe",
		"   # comment only\n",
		"BASE <relative/without/prior/base> SELECT * WHERE { ?s ?p ?o }",
		"PREFIX bad <https://example.org/> SELECT * WHERE { ?s ?p ?o }",
		"PREFIX a.: <https://example.org/> SELECT * WHERE { ?s ?p ?o }",
		"UNKNOWN_FORM ?s WHERE { ?s ?p ?o }",
		"SELECT WHERE { ?s ?p ?o }",
		"SELECT ?s WHERE { ?s ?p }",
		"SELECT ?s WHERE { ?s ?p ?o",
		"SELECT ?s WHERE { [] . }",
		"SELECT ?s WHERE { _: ?p ?o }",
		"SELECT ?s WHERE { ?s ?p $ }",
		"SELECT ?s WHERE { ?s <http://example.org/bad iri> ?o }",
		"SELECT ?s WHERE { ?s <http://example.org/\\u0020> ?o }",
		"SELECT ?s WHERE { ?s <http://example.org/\\x> ?o }",
		"SELECT ?s WHERE { ?s <http://example.org/unterminated }",
		"SELECT ?s WHERE { ?s <relative/no/base> ?o }",
		"SELECT ?s WHERE { ?s unknown:pred ?o }",
		"SELECT ?s WHERE { ?s ex:bad%2G ?o }",
		"SELECT ?s WHERE { ?s ex:bad\\q ?o }",
		"SELECT ?s WHERE { \"literal\" <https://example.org/p> ?o }",
		"SELECT ?s WHERE { ?s \"literal\" ?o }",
		"SELECT ?s WHERE { ?s ?p \"unterminated }",
		"SELECT ?s WHERE { ?s ?p \"unterminated\n\" }",
		"SELECT ?s WHERE { ?s ?p \"bad\\qescape\" }",
		"SELECT ?s WHERE { ?s ?p \"bad\\u12Z4\" }",
		"SELECT ?s WHERE { ?s ?p \"hi\"@ }",
		"SELECT ?s WHERE { ?s ?p \"hi\"@en- }",
		"SELECT ?s WHERE { ?s ?p \"hi\"@en^^<http://www.w3.org/2001/XMLSchema#string> }",
		"SELECT ?s WHERE { ?s ?p \"hi\"^^123 }",
		"SELECT ?s WHERE { ?s ?p 1.5e }",
		"SELECT ?s WHERE { ?s ?p - }",
		"SELECT ?s WHERE { ?s ?p ?o FILTER (?s & ?p) }",
		"SELECT ?s WHERE { ?s ?p ?o FILTER 123 }",
		"SELECT ?s WHERE { ?s ?p ?o FILTER (BOUND(\"notavar\")) }",
		"SELECT ?s WHERE { ?s ?p ?o FILTER (REGEX()) }",
		"SELECT ?s WHERE { ?s ?p ?o FILTER (REGEX(?s)) }",
		"SELECT ?s WHERE { ?s ?p ?o FILTER (STR(?s, ?p)) }",
		"CONSTRUCT { FILTER (?s = ?o) } WHERE { ?s ?p ?o }",
		"SELECT ?s WHERE { ?s ?p ?o } ORDER ?s",
		"SELECT ?s WHERE { ?s ?p ?o } ORDER BY",
		"SELECT ?s WHERE { ?s ?p ?o } ORDER BY 123",
		"SELECT ?s WHERE { ?s ?p ?o } LIMIT -1",
		"SELECT ?s WHERE { ?s ?p ?o } LIMIT 10 LIMIT 20",
		"SELECT ?s WHERE { ?s ?p ?o } OFFSET 5 OFFSET 10",
		"SELECT ?s WHERE { ?s ?p ?o } EXTRA_TOKEN",
		"DESCRIBE",
	}

	for _, q := range badQueries {
		got, err := Parse(q)
		if err == nil {
			t.Fatalf("Parse(%q) succeeded with %+v, want ErrSyntax", q, got)
		}
		if !errors.Is(err, ErrSyntax) {
			t.Fatalf("Parse(%q) err = %v, want errors.Is(err, ErrSyntax)", q, err)
		}
		if errors.Is(err, ErrUnsupportedFeature) {
			t.Fatalf("Parse(%q) err = %v, unexpectedly matched ErrUnsupportedFeature", q, err)
		}
	}
}

func TestPropParseSPARQLGeneratedQueries(t *testing.T) {
	varGen := rapid.SampledFrom([]string{"s", "p", "o", "platform", "role", "altMeters", "mice", "cls"})
	predLocalGen := rapid.SampledFrom([]string{"Vehicle", "Altitude", "RO_0000053", "is_measured_by", "has_decimal_value"})
	builtinGen := rapid.SampledFrom([]BuiltinFunc{FuncBound, FuncRegex, FuncStr, FuncLang, FuncDatatype})

	rapid.Check(t, func(rt *rapid.T) {
		form := rapid.SampledFrom([]QueryForm{QuerySelect, QueryAsk, QueryConstruct, QueryDescribe}).Draw(rt, "form")
		distinct := rapid.Bool().Draw(rt, "distinct")
		subjVar := varGen.Draw(rt, "subj_var")
		objVar := varGen.Draw(rt, "obj_var")
		predLocal := predLocalGen.Draw(rt, "pred_local")
		pathChoice := rapid.IntRange(0, 2).Draw(rt, "path_choice")
		hasOptional := rapid.Bool().Draw(rt, "has_optional")
		hasUnion := rapid.Bool().Draw(rt, "has_union")
		builtin := builtinGen.Draw(rt, "builtin")
		hasOrder := rapid.Bool().Draw(rt, "has_order")
		orderDesc := rapid.Bool().Draw(rt, "order_desc")
		limitVal := rapid.IntRange(0, 500).Draw(rt, "limit")
		offsetVal := rapid.IntRange(0, 100).Draw(rt, "offset")

		var sb strings.Builder
		sb.WriteString("PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>\n")
		sb.WriteString("PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>\n")
		sb.WriteString("PREFIX cco: <https://www.commoncoreontologies.org/>\n")

		switch form {
		case QuerySelect:
			sb.WriteString("SELECT ")
			if distinct {
				sb.WriteString("DISTINCT ")
			}
			fmt.Fprintf(&sb, "?%s ?%s WHERE {\n", subjVar, objVar)
		case QueryAsk:
			distinct = false
			sb.WriteString("ASK WHERE {\n")
		case QueryConstruct:
			distinct = false
			fmt.Fprintf(&sb, "CONSTRUCT { ?%s cco:%s ?%s . } WHERE {\n", subjVar, predLocal, objVar)
		case QueryDescribe:
			sb.WriteString("DESCRIBE ")
			if distinct {
				sb.WriteString("DISTINCT ")
			}
			fmt.Fprintf(&sb, "?%s WHERE {\n", subjVar)
		}

		var wantPath PathKind
		switch pathChoice {
		case 0:
			fmt.Fprintf(&sb, "  ?%s cco:%s ?%s .\n", subjVar, predLocal, objVar)
			wantPath = PathNone
		case 1:
			fmt.Fprintf(&sb, "  ?%s rdf:type/rdfs:subClassOf* ?%s .\n", subjVar, objVar)
			wantPath = PathRDFTypeSubClassOfStar
		case 2:
			fmt.Fprintf(&sb, "  ?%s rdfs:subClassOf* ?%s .\n", subjVar, objVar)
			wantPath = PathSubClassOfStar
		}

		if hasOptional {
			fmt.Fprintf(&sb, "  OPTIONAL { ?%s a cco:%s . }\n", subjVar, predLocal)
		}
		if hasUnion {
			fmt.Fprintf(&sb, "  { ?%s cco:%s \"alpha\" . } UNION { ?%s cco:%s 42 . }\n", subjVar, predLocal, subjVar, predLocal)
		}

		switch builtin {
		case FuncBound:
			fmt.Fprintf(&sb, "  FILTER (BOUND(?%s))\n", objVar)
		case FuncRegex:
			fmt.Fprintf(&sb, "  FILTER (REGEX(STR(?%s), \"^https://\", \"i\"))\n", subjVar)
		case FuncStr:
			fmt.Fprintf(&sb, "  FILTER (STR(?%s) != \"\")\n", subjVar)
		case FuncLang:
			fmt.Fprintf(&sb, "  FILTER (LANG(?%s) = \"en\")\n", objVar)
		case FuncDatatype:
			fmt.Fprintf(&sb, "  FILTER (DATATYPE(?%s) = <http://www.w3.org/2001/XMLSchema#integer>)\n", objVar)
		}

		sb.WriteString("}\n")

		if form != QueryAsk {
			if hasOrder {
				if orderDesc {
					fmt.Fprintf(&sb, "ORDER BY DESC(?%s)\n", subjVar)
				} else {
					fmt.Fprintf(&sb, "ORDER BY ASC(?%s)\n", subjVar)
				}
			}
			fmt.Fprintf(&sb, "LIMIT %s\nOFFSET %s\n", strconv.Itoa(limitVal), strconv.Itoa(offsetVal))
		}

		parsed, err := Parse(sb.String())
		if err != nil {
			rt.Fatalf("Parse failed on generated query:\n%s\nerr: %v", sb.String(), err)
		}
		if parsed.Form != form {
			rt.Fatalf("Form = %v, want %v", parsed.Form, form)
		}
		if parsed.Distinct != distinct {
			rt.Fatalf("Distinct = %v, want %v", parsed.Distinct, distinct)
		}
		if len(parsed.Where.Elements) == 0 {
			rt.Fatal("Where.Elements is empty")
		}
		bgp, ok := parsed.Where.Elements[0].(BGPPattern)
		if !ok || len(bgp.Triples) != 1 {
			rt.Fatalf("Elements[0] = %+v, want 1-triple BGP", parsed.Where.Elements[0])
		}
		if bgp.Triples[0].Path != wantPath {
			rt.Fatalf("Triples[0].Path = %v, want %v", bgp.Triples[0].Path, wantPath)
		}
		if len(parsed.Where.Filters) != 1 {
			rt.Fatalf("len(Where.Filters) = %d, want 1", len(parsed.Where.Filters))
		}
		if form != QueryAsk {
			if parsed.Limit == nil || *parsed.Limit != limitVal || parsed.Offset != offsetVal {
				rt.Fatalf("Limit=%v Offset=%d, want %d and %d", parsed.Limit, parsed.Offset, limitVal, offsetVal)
			}
		}
	})
}

func FuzzParseSPARQL(f *testing.F) {
	seeds := []string{
		// Accepted seeds across all supported v1 forms and features:
		`SELECT * WHERE { ?s ?p ?o }`,
		`PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
		 PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
		 PREFIX obo: <http://purl.obolibrary.org/obo/>
		 SELECT DISTINCT ?s WHERE { ?s rdf:type/rdfs:subClassOf* obo:BFO_0000030 } LIMIT 10 OFFSET 0`,
		`PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
		 SELECT ?sub ?sup WHERE { ?sub rdfs:subClassOf* ?sup } ORDER BY ?sub DESC(?sup)`,
		`PREFIX cco: <https://www.commoncoreontologies.org/>
		 ASK { ?s a cco:Vehicle ; cco:is_measured_by [ a cco:Altitude ] }`,
		`PREFIX cco: <https://www.commoncoreontologies.org/>
		 CONSTRUCT { ?s a cco:Vehicle } WHERE { { ?s a cco:Vehicle } UNION { ?s a cco:Platform } OPTIONAL { ?s cco:label ?lbl } FILTER (BOUND(?lbl) && REGEX(STR(?lbl), "uav", "i") && LANG(?lbl) = "en" && DATATYPE(?lbl) != cco:Other) }`,
		`DESCRIBE <https://c360.io/semengine/entities/c360/ops/sys1/intel/platform/uav1>`,
		// Rejected seeds covering unsupported features and malformed syntax:
		`SELECT REDUCED ?s WHERE { ?s ?p ?o }`,
		`INSERT DATA { <https://example.org/s> <https://example.org/p> "v" }`,
		`SELECT ?s WHERE { SERVICE <https://example.org/sparql> { ?s ?p ?o } }`,
		`SELECT ?s WHERE { GRAPH <https://example.org/g> { ?s ?p ?o } }`,
		`SELECT ?s WHERE { ?s ?p ?o } GROUP BY ?s`,
		`SELECT (COUNT(?s) AS ?c) WHERE { ?s ?p ?o }`,
		`SELECT ?s WHERE { { SELECT ?s WHERE { ?s ?p ?o } } }`,
		`SELECT ?s WHERE { ?s ?p ?o VALUES ?s { <https://example.org/s1> } }`,
		`SELECT ?s ?v WHERE { ?s ?p ?o BIND(?o AS ?v) }`,
		`PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ex:p/ex:q ?o }`,
		`PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ex:p|ex:q ?o }`,
		`PREFIX ex: <https://example.org/> SELECT ?s WHERE { ?s ^ex:p ?o }`,
		`PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#> SELECT ?s WHERE { ?s rdfs:subClassOf+ ?o }`,
		"",
		"SELECT ?s WHERE { ?s ?p \"unterminated",
		"\x00\xff",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, query string) {
		q, err := Parse(query)
		if err != nil {
			if q != nil {
				t.Fatalf("Parse(%q) returned non-nil Query alongside error %v", query, err)
			}
			if !errors.Is(err, ErrSyntax) && !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("Parse(%q) error %v did not match ErrSyntax or ErrUnsupportedFeature", query, err)
			}
			return
		}
		if q == nil {
			t.Fatalf("Parse(%q) returned (nil, nil)", query)
		}
		if q.Form < QuerySelect || q.Form > QueryDescribe {
			t.Fatalf("Parse(%q) returned invalid QueryForm %v", query, q.Form)
		}
		if q.Prefixes == nil {
			t.Fatalf("Parse(%q) returned nil Prefixes map", query)
		}
	})
}
