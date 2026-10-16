package sparql

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semvocab/packs/diekm"
	"pgregory.net/rapid"
)

type fatalfT interface {
	Helper()
	Fatalf(format string, args ...any)
}

type callRecordingReader struct {
	reader.GraphReader
	mu               sync.Mutex
	scanPatterns     []string
	predicateLookups []string
}

func (r *callRecordingReader) ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error) {
	r.mu.Lock()
	r.scanPatterns = append(r.scanPatterns, pattern)
	r.mu.Unlock()
	return r.GraphReader.ScanPattern(ctx, pattern, budget)
}

func (r *callRecordingReader) EntitiesByPredicate(ctx context.Context, predicate string, budget int) ([]graph.EntityState, error) {
	r.mu.Lock()
	r.predicateLookups = append(r.predicateLookups, predicate)
	r.mu.Unlock()
	return r.GraphReader.EntitiesByPredicate(ctx, predicate, budget)
}

func (r *callRecordingReader) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scanPatterns = nil
	r.predicateLookups = nil
}

func (r *callRecordingReader) takePredicates() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.predicateLookups)
}

// materializeUnfoldedGraph independently constructs the mathematical set of
// unfolded RDF triples for entities using the specification's BlankNodeID and
// virtual rdf:type formulas, without invoking FoldProjections or Executor.
func materializeUnfoldedGraph(t testing.TB, entities []graph.EntityState, mapper *irimap.Mapper) []rdfTriple {
	t.Helper()
	baseIRI := mapper.BaseIRI()
	rdfTypeTerm := Term{Kind: TermIRI, Value: RDFTypeIRI}

	seen := make(map[rdfTriple]struct{})
	var triples []rdfTriple
	add := func(rt rdfTriple) {
		if _, exists := seen[rt]; !exists {
			seen[rt] = struct{}{}
			triples = append(triples, rt)
		}
	}

	for _, ent := range entities {
		entIRI, err := mapper.EntityIDToIRI(ent.ID)
		if err != nil {
			t.Fatalf("EntityIDToIRI(%q): %v", ent.ID, err)
		}
		entSubj := Term{Kind: TermIRI, Value: entIRI}

		for _, clsIRI := range VirtualTypeIRIsForEntityID(ent.ID) {
			add(rdfTriple{
				Subject:   entSubj,
				Predicate: rdfTypeTerm,
				Object:    Term{Kind: TermIRI, Value: clsIRI},
			})
		}

		for _, tr := range ent.Triples {
			subjTerm := entSubj
			subjEntityID := ent.ID
			if tr.Subject != "" && tr.Subject != ent.ID {
				if len(tr.Subject) >= 2 && tr.Subject[:2] == "_:" {
					subjTerm = Term{Kind: TermBlankNode, Value: tr.Subject}
				} else {
					subjTerm = Term{Kind: TermIRI, Value: export.SubjectIRI(tr.Subject, baseIRI)}
					subjEntityID = tr.Subject
				}
			}

			predTerm, objTerm, canonObj := oracleTripleTerms(t, tr, baseIRI)

			meta := vocabulary.GetPredicateMetadata(tr.Predicate)
			if meta != nil && meta.Projection != nil && len(meta.Projection.Steps) > 0 && subjTerm.Kind == TermIRI {
				curSubj := subjTerm
				for stepIdx, step := range meta.Projection.Steps {
					bnodeID, err := irimap.BlankNodeID(subjEntityID, tr.Predicate, canonObj, stepIdx)
					if err != nil {
						t.Fatalf("BlankNodeID: %v", err)
					}
					bnodeTerm := Term{Kind: TermBlankNode, Value: bnodeID}
					add(rdfTriple{
						Subject:   curSubj,
						Predicate: Term{Kind: TermIRI, Value: step.PredicateIRI},
						Object:    bnodeTerm,
					})
					if step.NodeClassIRI != "" {
						add(rdfTriple{
							Subject:   bnodeTerm,
							Predicate: rdfTypeTerm,
							Object:    Term{Kind: TermIRI, Value: step.NodeClassIRI},
						})
						for _, supIRI := range SuperclassesForClassIRI(step.NodeClassIRI) {
							add(rdfTriple{
								Subject:   bnodeTerm,
								Predicate: rdfTypeTerm,
								Object:    Term{Kind: TermIRI, Value: supIRI},
							})
						}
					}
					if stepIdx == len(meta.Projection.Steps)-1 {
						add(rdfTriple{
							Subject:   bnodeTerm,
							Predicate: Term{Kind: TermIRI, Value: step.ValuePredicateIRI},
							Object:    objTerm,
						})
					}
					curSubj = bnodeTerm
				}
				continue
			}

			add(rdfTriple{
				Subject:   subjTerm,
				Predicate: predTerm,
				Object:    objTerm,
			})
			if predTerm.Kind == TermIRI && predTerm.Value == RDFTypeIRI && objTerm.Kind == TermIRI {
				for _, supIRI := range SuperclassesForClassIRI(objTerm.Value) {
					add(rdfTriple{
						Subject:   subjTerm,
						Predicate: rdfTypeTerm,
						Object:    Term{Kind: TermIRI, Value: supIRI},
					})
				}
			}
		}
	}
	return triples
}

func oracleTripleTerms(t testing.TB, tr message.Triple, baseIRI string) (predTerm, objTerm Term, canonObj string) {
	t.Helper()
	predIRI, canon, err := irimap.CanonicalNTriplesPredicateAndObject(tr, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesPredicateAndObject(%+v): %v", tr, err)
	}
	obj, err := parseTestNTriplesObject(canon)
	if err != nil {
		t.Fatalf("parseTestNTriplesObject(%q): %v", canon, err)
	}
	return Term{Kind: TermIRI, Value: predIRI}, obj, canon
}

// evalOverMaterializedOracle evaluates a parsed SELECT/ASK query directly over
// a pre-materialized unfolded RDF triple set without using FoldProjections,
// planBGP, or GraphReader.
func evalOverMaterializedOracle(t testing.TB, q *Query, graphTriples []rdfTriple) *Result {
	t.Helper()
	solutions := evalGroupOracle(q.Where, []Solution{{}}, graphTriples)
	if q.Form == QueryAsk {
		return &Result{
			Form:      QueryAsk,
			AskResult: len(solutions) > 0,
		}
	}
	vars := selectVariables(q)
	sortSolutions(solutions, q.OrderBy, vars)

	projected := make([]Solution, 0, len(solutions))
	for _, sol := range solutions {
		p := make(Solution, len(vars))
		for _, v := range vars {
			if val, ok := sol[v]; ok {
				p[v] = val
			}
		}
		projected = append(projected, p)
	}
	if q.Distinct {
		projected = deduplicateSolutions(projected, vars)
	}
	if q.Offset > 0 {
		if q.Offset >= len(projected) {
			projected = []Solution{}
		} else {
			projected = projected[q.Offset:]
		}
	}
	if q.Limit != nil {
		if *q.Limit <= 0 {
			projected = []Solution{}
		} else if *q.Limit < len(projected) {
			projected = projected[:*q.Limit]
		}
	}
	return &Result{
		Form:      QuerySelect,
		Variables: vars,
		Solutions: projected,
	}
}

func evalGroupOracle(g GroupPattern, current []Solution, graphTriples []rdfTriple) []Solution {
	solutions := current
	for _, el := range g.Elements {
		if len(solutions) == 0 {
			break
		}
		switch p := el.(type) {
		case BGPPattern:
			for _, tp := range p.Triples {
				var next []Solution
				for _, sol := range solutions {
					for _, rt := range graphTriples {
						if ext, ok := matchTriplePattern(tp, rt, sol); ok {
							next = append(next, ext)
						}
					}
				}
				solutions = next
			}
		case SubGroupPattern:
			inner := evalGroupOracle(p.Group, []Solution{{}}, graphTriples)
			solutions = joinSolutionsOracle(solutions, inner)
		case OptionalPattern:
			var out []Solution
			for _, sol := range solutions {
				ext := evalGroupOracle(p.Group, []Solution{sol.Clone()}, graphTriples)
				if len(ext) == 0 {
					out = append(out, sol.Clone())
				} else {
					out = append(out, ext...)
				}
			}
			solutions = out
		case UnionPattern:
			var unioned []Solution
			for _, b := range p.Branches {
				unioned = append(unioned, evalGroupOracle(b, []Solution{{}}, graphTriples)...)
			}
			solutions = joinSolutionsOracle(solutions, unioned)
		}
	}
	if len(g.Filters) == 0 || len(solutions) == 0 {
		return solutions
	}
	var filtered []Solution
	for _, sol := range solutions {
		if matchesAllFilters(g.Filters, sol) {
			filtered = append(filtered, sol)
		}
	}
	return filtered
}

func joinSolutionsOracle(left, right []Solution) []Solution {
	var out []Solution
	for _, l := range left {
		for _, r := range right {
			if merged, ok := mergeCompatibleSolutions(l, r); ok {
				out = append(out, merged)
			}
		}
	}
	return out
}

func assertSolutionsEqual(t fatalfT, got, want *Result, contextMsg string) {
	t.Helper()
	if got.Form != want.Form {
		t.Fatalf("%s: Form = %v, want %v", contextMsg, got.Form, want.Form)
	}
	if got.Form == QueryAsk {
		if got.AskResult != want.AskResult {
			t.Fatalf("%s: AskResult = %v, want %v", contextMsg, got.AskResult, want.AskResult)
		}
		return
	}
	if !slices.Equal(got.Variables, want.Variables) {
		t.Fatalf("%s: Variables = %v, want %v", contextMsg, got.Variables, want.Variables)
	}
	if len(got.Solutions) != len(want.Solutions) {
		t.Fatalf("%s: len(Solutions) = %d, want %d\ngot:  %+v\nwant: %+v",
			contextMsg, len(got.Solutions), len(want.Solutions), got.Solutions, want.Solutions)
	}
	for i := range got.Solutions {
		gSol := got.Solutions[i]
		wSol := want.Solutions[i]
		if len(gSol) != len(wSol) {
			t.Fatalf("%s: row %d len = %d, want %d\ngot:  %+v\nwant: %+v",
				contextMsg, i, len(gSol), len(wSol), gSol, wSol)
		}
		for k, wVal := range wSol {
			gVal, ok := gSol[k]
			if !ok || gVal != wVal {
				t.Fatalf("%s: row %d var %q = %+v (ok=%v), want %+v\ngot:  %+v\nwant: %+v",
					contextMsg, i, k, gVal, ok, wVal, got.Solutions, want.Solutions)
			}
		}
	}
}

// TestBGPFoldingCollapsesDIEKMChains proves Task 3.2's complete-chain folding
// and equivalent fallback evaluation across 1-step and 3-step DIEKM chains,
// projected intermediates, partial chains, missing type constraints, extra
// constraints and FILTERs on intermediates, ambiguous ProjectionSpecs, variable
// predicates, and multi-valued predicates.
func TestBGPFoldingCollapsesDIEKMChains(t *testing.T) {
	restore := vocabulary.SnapshotRegistry()
	defer restore()
	vocabulary.ClearRegistry()

	noPrefixes := vocabulary.WithPrefixRegistrar(func([]vocabulary.PrefixSpec) error { return nil })
	if err := diekm.Register(noPrefixes); err != nil {
		t.Fatalf("diekm.Register: %v", err)
	}
	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithIRI("https://example.org/onto#callsign"),
		vocabulary.WithDataType(vocabulary.DataTypeString))

	const (
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idBravo = "c360.ops.uas2.intel.platform.bravo"
	)

	entities := []graph.EntityState{
		{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
				{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
				{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "recon"},
				{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
				{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(2200.0)},
			},
		},
		{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "BRAVO-2"},
				{Subject: idBravo, Predicate: "intel.role.assigned", Object: "strike"},
				{Subject: idBravo, Predicate: "intel.measurement.altitude-m", Object: float64(900.0)},
			},
		},
	}

	mapper := irimap.NewMapper(testBaseIRI)
	mr := mustMemoryReader(t, entities...)
	rec := &callRecordingReader{GraphReader: mr}
	exec := NewExecutor(rec, Config{Mapper: mapper})
	oracleGraph := materializeUnfoldedGraph(t, entities, mapper)

	t.Run("complete 1-step DIEKM role chain folds and matches unfolded oracle with and without projected intermediate", func(t *testing.T) {
		for _, projectInter := range []bool{false, true} {
			selectClause := "SELECT ?platform ?role"
			if projectInter {
				selectClause = "SELECT ?platform ?roleNode ?role"
			}
			queryStr := fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				%s WHERE {
					?platform obo:BFO_0000196 ?roleNode .
					?roleNode a obo:BFO_0000023 ;
					          rdfs:label ?role .
				}
				ORDER BY ?platform ?role
			`, selectClause)

			q, err := Parse(queryStr)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			bgp := q.Where.Elements[0].(BGPPattern)
			folded := FoldProjections(bgp)
			if len(folded.Chains) != 1 || len(folded.RemainingTriples) != 0 {
				t.Fatalf("FoldProjections: chains=%d remaining=%d, want 1 and 0", len(folded.Chains), len(folded.RemainingTriples))
			}
			if !slices.Equal(folded.Chains[0].Predicates, []string{"intel.role.assigned"}) {
				t.Fatalf("FoldedChain.Predicates = %v, want [intel.role.assigned]", folded.Chains[0].Predicates)
			}
			plan := planBGP(bgp, mapper)
			if len(plan.Steps) != 1 || plan.Steps[0].Folded == nil || plan.Steps[0].Kind != accessPredicate {
				t.Fatalf("planBGP steps = %+v, want 1 folded accessPredicate step", plan.Steps)
			}

			rec.reset()
			got, err := exec.Execute(context.Background(), q)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if gotPreds := rec.takePredicates(); !slices.Equal(gotPreds, []string{"intel.role.assigned"}) {
				t.Fatalf("EntitiesByPredicate calls = %v, want [intel.role.assigned]", gotPreds)
			}
			want := evalOverMaterializedOracle(t, q, oracleGraph)
			assertSolutionsEqual(t, got, want, fmt.Sprintf("1-step role projectInter=%v", projectInter))

			if projectInter {
				// Verify deterministic BlankNodeID on multi-valued role triples on idAlpha.
				bnodeISR, err := irimap.BlankNodeID(idAlpha, "intel.role.assigned", `"ISR"`, 0)
				if err != nil {
					t.Fatalf("BlankNodeID ISR: %v", err)
				}
				bnodeRecon, err := irimap.BlankNodeID(idAlpha, "intel.role.assigned", `"recon"`, 0)
				if err != nil {
					t.Fatalf("BlankNodeID recon: %v", err)
				}
				if bnodeISR == bnodeRecon {
					t.Fatalf("multi-valued BlankNodeIDs collided: %q", bnodeISR)
				}
				if got.Solutions[0]["roleNode"].Value != bnodeISR || got.Solutions[1]["roleNode"].Value != bnodeRecon {
					t.Fatalf("projected ?roleNode bindings = [%s, %s], want [%s, %s]",
						got.Solutions[0]["roleNode"].Value, got.Solutions[1]["roleNode"].Value, bnodeISR, bnodeRecon)
				}
			}
		}
	})

	t.Run("complete 3-step DIEKM altitude chain folds and matches unfolded oracle with and without projected intermediates", func(t *testing.T) {
		for _, projectInter := range []bool{false, true} {
			selectClause := "SELECT ?platform ?altMeters"
			if projectInter {
				selectClause = "SELECT ?platform ?altNode ?mice ?ibe ?altMeters"
			}
			queryStr := fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX cco: <https://www.commoncoreontologies.org/>
				PREFIX diekm: <https://w3id.org/c360/diekm#>
				%s WHERE {
					?platform obo:BFO_0000196 ?altNode .
					?altNode a cco:ont00000787 ;
					         cco:ont00001904 ?mice .
					?mice a cco:ont00001163 ;
					      obo:BFO_0000084 ?ibe .
					?ibe a cco:ont00000253 ;
					     diekm:altitudeMetersValue ?altMeters .
				}
				ORDER BY ?platform ?altMeters
			`, selectClause)

			q, err := Parse(queryStr)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			bgp := q.Where.Elements[0].(BGPPattern)
			folded := FoldProjections(bgp)
			if len(folded.Chains) != 1 || len(folded.RemainingTriples) != 0 {
				t.Fatalf("FoldProjections: chains=%d remaining=%d, want 1 and 0", len(folded.Chains), len(folded.RemainingTriples))
			}
			if !slices.Equal(folded.Chains[0].Predicates, []string{"intel.measurement.altitude-m"}) {
				t.Fatalf("FoldedChain.Predicates = %v, want [intel.measurement.altitude-m]", folded.Chains[0].Predicates)
			}

			rec.reset()
			got, err := exec.Execute(context.Background(), q)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if gotPreds := rec.takePredicates(); !slices.Equal(gotPreds, []string{"intel.measurement.altitude-m"}) {
				t.Fatalf("EntitiesByPredicate calls = %v, want [intel.measurement.altitude-m]", gotPreds)
			}
			want := evalOverMaterializedOracle(t, q, oracleGraph)
			assertSolutionsEqual(t, got, want, fmt.Sprintf("3-step altitude projectInter=%v", projectInter))
		}
	})

	t.Run("partial chains and missing NodeClassIRI constraints do not fold and evaluate over virtual unfolded graph", func(t *testing.T) {
		partialQueries := []struct {
			name  string
			query string
		}{
			{
				name: "partial chain missing terminal step 2",
				query: `
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX cco: <https://www.commoncoreontologies.org/>
					SELECT ?platform ?altNode ?mice WHERE {
						?platform obo:BFO_0000196 ?altNode .
						?altNode a cco:ont00000787 ;
						         cco:ont00001904 ?mice .
						?mice a cco:ont00001163 .
					}
					ORDER BY ?platform ?altNode ?mice
				`,
			},
			{
				name: "partial chain starting at intermediate step 1",
				query: `
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX cco: <https://www.commoncoreontologies.org/>
					PREFIX diekm: <https://w3id.org/c360/diekm#>
					SELECT ?altNode ?mice ?ibe ?altMeters WHERE {
						?altNode cco:ont00001904 ?mice .
						?mice a cco:ont00001163 ;
						      obo:BFO_0000084 ?ibe .
						?ibe a cco:ont00000253 ;
						     diekm:altitudeMetersValue ?altMeters .
					}
					ORDER BY ?altMeters
				`,
			},
			{
				name: "missing NodeClassIRI constraint on step 1 (?mice)",
				query: `
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX cco: <https://www.commoncoreontologies.org/>
					PREFIX diekm: <https://w3id.org/c360/diekm#>
					SELECT ?platform ?altMeters WHERE {
						?platform obo:BFO_0000196 ?altNode .
						?altNode a cco:ont00000787 ;
						         cco:ont00001904 ?mice .
						?mice obo:BFO_0000084 ?ibe .
						?ibe a cco:ont00000253 ;
						     diekm:altitudeMetersValue ?altMeters .
					}
					ORDER BY ?platform ?altMeters
				`,
			},
			{
				name: "missing NodeClassIRI constraint on 1-step role chain",
				query: `
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
					SELECT ?platform ?role WHERE {
						?platform obo:BFO_0000196 ?roleNode .
						?roleNode rdfs:label ?role .
					}
					ORDER BY ?platform ?role
				`,
			},
		}

		for _, tc := range partialQueries {
			t.Run(tc.name, func(t *testing.T) {
				q, err := Parse(tc.query)
				if err != nil {
					t.Fatalf("Parse: %v", err)
				}
				bgp := q.Where.Elements[0].(BGPPattern)
				folded := FoldProjections(bgp)
				if len(folded.Chains) != 0 {
					t.Fatalf("expected 0 folded chains for %s, got %+v", tc.name, folded.Chains)
				}

				got, err := exec.Execute(context.Background(), q)
				if err != nil {
					t.Fatalf("Execute: %v", err)
				}
				want := evalOverMaterializedOracle(t, q, oracleGraph)
				if len(want.Solutions) == 0 {
					t.Fatalf("oracle returned 0 solutions for %s; test must exercise non-empty matches", tc.name)
				}
				assertSolutionsEqual(t, got, want, tc.name)
			})
		}
	})

	t.Run("extra constraints or FILTERs on intermediate nodes disable folding and honor the constraint via fallback", func(t *testing.T) {
		// 1. Extra triple constraint on intermediate node ?altNode (?altNode a ?anyClass).
		qExtra, err := Parse(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX cco: <https://www.commoncoreontologies.org/>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?platform ?altClass ?altMeters WHERE {
				?platform obo:BFO_0000196 ?altNode .
				?altNode a cco:ont00000787 ;
				         a ?altClass ;
				         cco:ont00001904 ?mice .
				?mice a cco:ont00001163 ;
				      obo:BFO_0000084 ?ibe .
				?ibe a cco:ont00000253 ;
				     diekm:altitudeMetersValue ?altMeters .
			}
			ORDER BY ?platform ?altMeters ?altClass
		`)
		if err != nil {
			t.Fatalf("Parse extra triple constraint: %v", err)
		}
		if folded := FoldProjections(qExtra.Where.Elements[0].(BGPPattern)); len(folded.Chains) != 0 {
			t.Fatalf("expected extra triple constraint on intermediate node to disable folding, got %+v", folded.Chains)
		}
		gotExtra, err := exec.Execute(context.Background(), qExtra)
		if err != nil {
			t.Fatalf("Execute extra triple constraint: %v", err)
		}
		wantExtra := evalOverMaterializedOracle(t, qExtra, oracleGraph)
		assertSolutionsEqual(t, gotExtra, wantExtra, "extra triple constraint on ?altNode")

		// 2. FILTER referencing intermediate nodes (?r1 != ?r2) disables folding at execution time and filters accurately.
		qFilterInter, err := Parse(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT ?platform ?r1 ?role1 ?r2 ?role2 WHERE {
				?platform obo:BFO_0000196 ?r1 .
				?r1 a obo:BFO_0000023 ;
				    rdfs:label ?role1 .
				?platform obo:BFO_0000196 ?r2 .
				?r2 a obo:BFO_0000023 ;
				    rdfs:label ?role2 .
				FILTER(?r1 != ?r2)
			}
			ORDER BY ?platform ?role1 ?role2
		`)
		if err != nil {
			t.Fatalf("Parse filter on intermediate: %v", err)
		}
		filterDisallowed := map[string]bool{"r1": true, "r2": true}
		if folded := FoldProjections(qFilterInter.Where.Elements[0].(BGPPattern), filterDisallowed); len(folded.Chains) != 0 {
			t.Fatalf("expected FILTER on intermediate variables to disable folding, got %+v", folded.Chains)
		}
		gotFilterInter, err := exec.Execute(context.Background(), qFilterInter)
		if err != nil {
			t.Fatalf("Execute filter on intermediate: %v", err)
		}
		wantFilterInter := evalOverMaterializedOracle(t, qFilterInter, oracleGraph)
		if len(wantFilterInter.Solutions) != 2 {
			t.Fatalf("expected 2 solutions for ?r1 != ?r2 on multi-valued idAlpha, got %d", len(wantFilterInter.Solutions))
		}
		assertSolutionsEqual(t, gotFilterInter, wantFilterInter, "FILTER(?r1 != ?r2) on intermediate nodes")
	})

	t.Run("variable predicates never fold and return the unfolded view under scan budget", func(t *testing.T) {
		alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
		qVarPred, err := Parse(fmt.Sprintf(`
			SELECT ?p ?o WHERE {
				<%s> ?p ?o .
			}
			ORDER BY ?p ?o
		`, alphaIRI))
		if err != nil {
			t.Fatalf("Parse variable predicate: %v", err)
		}
		if folded := FoldProjections(qVarPred.Where.Elements[0].(BGPPattern)); len(folded.Chains) != 0 {
			t.Fatalf("variable predicate must not fold, got %+v", folded.Chains)
		}
		gotVarPred, err := exec.Execute(context.Background(), qVarPred)
		if err != nil {
			t.Fatalf("Execute variable predicate: %v", err)
		}
		wantVarPred := evalOverMaterializedOracle(t, qVarPred, oracleGraph)
		assertSolutionsEqual(t, gotVarPred, wantVarPred, "variable predicate on alphaIRI")

		// Verify scan budget stops unconstrained `?s ?p ?o` over unfolded view.
		tightExec := NewExecutor(mr, Config{
			Mapper:          mapper,
			MaxScanEntities: 1,
		})
		_, err = tightExec.ExecuteQuery(context.Background(), `SELECT ?s ?p ?o WHERE { ?s ?p ?o }`)
		if err == nil {
			t.Fatal("expected scan budget error on unconstrained ?s ?p ?o with MaxScanEntities=1")
		}
	})

	t.Run("ambiguous ProjectionSpecs union all matching predicates while preserving graph-set and solution-multiset semantics", func(t *testing.T) {
		// Register a second dotted predicate with the exact same 1-step ProjectionSpec as intel.role.assigned.
		roleMeta := vocabulary.GetPredicateMetadata("intel.role.assigned")
		if roleMeta == nil || roleMeta.Projection == nil {
			t.Fatal("missing intel.role.assigned metadata")
		}
		vocabulary.Register("intel.role.secondary",
			vocabulary.WithDataType(vocabulary.DataTypeString),
			vocabulary.WithIRI("https://w3id.org/c360/diekm#secondaryRole"),
			vocabulary.WithProjection(*roleMeta.Projection))

		ambigEntities := []graph.EntityState{
			{
				ID: idAlpha,
				Triples: []message.Triple{
					// Duplicate identical triple on the SAME predicate (must deduplicate as a graph set -> 1 chain):
					{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
					{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
					// Same value "ISR" on a DIFFERENT predicate sharing the ProjectionSpec (produces distinct BlankNodeID -> 2nd chain):
					{Subject: idAlpha, Predicate: "intel.role.secondary", Object: "ISR"},
					// Distinct value on secondary predicate:
					{Subject: idAlpha, Predicate: "intel.role.secondary", Object: "escort"},
				},
			},
		}
		ambigMR := mustMemoryReader(t, ambigEntities...)
		ambigRec := &callRecordingReader{GraphReader: ambigMR}
		ambigExec := NewExecutor(ambigRec, Config{Mapper: mapper})
		ambigOracle := materializeUnfoldedGraph(t, ambigEntities, mapper)

		// Without DISTINCT: "ISR" appears twice (once from intel.role.assigned, once from intel.role.secondary),
		// while the duplicate (idAlpha, intel.role.assigned, "ISR") is deduplicated as a graph set, plus "escort" = 3 solutions.
		qMultiset, err := Parse(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT ?platform ?role WHERE {
				?platform obo:BFO_0000196 ?roleNode .
				?roleNode a obo:BFO_0000023 ;
				          rdfs:label ?role .
			}
			ORDER BY ?platform ?role
		`)
		if err != nil {
			t.Fatalf("Parse ambiguous multiset: %v", err)
		}
		folded := FoldProjections(qMultiset.Where.Elements[0].(BGPPattern))
		if len(folded.Chains) != 1 || !slices.Equal(folded.Chains[0].Predicates, []string{"intel.role.assigned", "intel.role.secondary"}) {
			t.Fatalf("FoldedChain.Predicates = %v, want [intel.role.assigned intel.role.secondary]", folded.Chains)
		}

		ambigRec.reset()
		gotMultiset, err := ambigExec.Execute(context.Background(), qMultiset)
		if err != nil {
			t.Fatalf("Execute ambiguous multiset: %v", err)
		}
		if gotPreds := ambigRec.takePredicates(); !slices.Equal(gotPreds, []string{"intel.role.assigned", "intel.role.secondary"}) {
			t.Fatalf("EntitiesByPredicate = %v, want [intel.role.assigned intel.role.secondary]", gotPreds)
		}
		wantMultiset := evalOverMaterializedOracle(t, qMultiset, ambigOracle)
		if len(wantMultiset.Solutions) != 3 {
			t.Fatalf("expected 3 solutions (2 ISR + 1 escort), got %d: %+v", len(wantMultiset.Solutions), wantMultiset.Solutions)
		}
		assertSolutionsEqual(t, gotMultiset, wantMultiset, "ambiguous ProjectionSpec multiset")

		// With DISTINCT: "ISR" and "escort" -> 2 solutions.
		qDistinct, err := Parse(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT DISTINCT ?platform ?role WHERE {
				?platform obo:BFO_0000196 ?roleNode .
				?roleNode a obo:BFO_0000023 ;
				          rdfs:label ?role .
			}
			ORDER BY ?platform ?role
		`)
		if err != nil {
			t.Fatalf("Parse ambiguous distinct: %v", err)
		}
		gotDistinct, err := ambigExec.Execute(context.Background(), qDistinct)
		if err != nil {
			t.Fatalf("Execute ambiguous distinct: %v", err)
		}
		wantDistinct := evalOverMaterializedOracle(t, qDistinct, ambigOracle)
		if len(wantDistinct.Solutions) != 2 {
			t.Fatalf("expected 2 distinct solutions, got %d", len(wantDistinct.Solutions))
		}
		assertSolutionsEqual(t, gotDistinct, wantDistinct, "ambiguous ProjectionSpec DISTINCT")
	})

	t.Run("bound-subject folded chains, joined type-plus-chain planning, typeless 1-step specs, and overlap guards", func(t *testing.T) {
		alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)

		// 1. Concrete entity IRI subject on folded chain plans as AccessExactEntity.
		qExact, err := Parse(fmt.Sprintf(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX cco: <https://www.commoncoreontologies.org/>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?altMeters WHERE {
				<%s> obo:BFO_0000196 ?altNode .
				?altNode a cco:ont00000787 ;
				         cco:ont00001904 ?mice .
				?mice a cco:ont00001163 ;
				      obo:BFO_0000084 ?ibe .
				?ibe a cco:ont00000253 ;
				     diekm:altitudeMetersValue ?altMeters .
			}
			ORDER BY ?altMeters
		`, alphaIRI))
		if err != nil {
			t.Fatalf("Parse exact chain: %v", err)
		}
		planExact := planBGP(qExact.Where.Elements[0].(BGPPattern), mapper)
		if len(planExact.Steps) != 1 || planExact.Steps[0].Kind != accessExactEntity || planExact.Steps[0].EntityID != idAlpha {
			t.Fatalf("planBGP exact chain = %+v, want 1 accessExactEntity step for %s", planExact.Steps, idAlpha)
		}
		gotExact, err := exec.Execute(context.Background(), qExact)
		if err != nil {
			t.Fatalf("Execute exact chain: %v", err)
		}
		if len(gotExact.Solutions) != 2 {
			t.Fatalf("expected 2 altitudes for alpha, got %d", len(gotExact.Solutions))
		}

		// 2. Joined `?platform a diekm:IntelligencePlatform` + folded chain orders accessTypePattern before bound-subject FoldedChain.
		qJoined, err := Parse(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX cco: <https://www.commoncoreontologies.org/>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?platform ?altMeters WHERE {
				?platform obo:BFO_0000196 ?altNode .
				?altNode a cco:ont00000787 ;
				         cco:ont00001904 ?mice .
				?mice a cco:ont00001163 ;
				      obo:BFO_0000084 ?ibe .
				?ibe a cco:ont00000253 ;
				     diekm:altitudeMetersValue ?altMeters .
				?platform a diekm:IntelligencePlatform .
			}
			ORDER BY ?platform ?altMeters
		`)
		if err != nil {
			t.Fatalf("Parse joined type + chain: %v", err)
		}
		planJoined := planBGPWithScope(qJoined.Where.Elements[0].(BGPPattern), mapper, "c360", "ops")
		if len(planJoined.Steps) != 2 || planJoined.Steps[0].Kind != accessTypePattern || planJoined.Steps[1].Folded == nil {
			t.Fatalf("planBGPWithScope joined = %+v, want accessTypePattern followed by FoldedChain", planJoined.Steps)
		}
		gotJoined, err := exec.Execute(context.Background(), qJoined)
		if err != nil {
			t.Fatalf("Execute joined type + chain: %v", err)
		}
		if len(gotJoined.Solutions) != 3 {
			t.Fatalf("expected 3 altitude solutions across alpha and bravo, got %d", len(gotJoined.Solutions))
		}

		// 3. 1-step ProjectionSpec without NodeClassIRI folds when no rdf:type is present and refuses to fold if rdf:type is present.
		vocabulary.Register("intel.tag.untyped",
			vocabulary.WithDataType(vocabulary.DataTypeString),
			vocabulary.WithProjection(vocabulary.ProjectionSpec{
				Steps: []vocabulary.ProjectionStep{
					{
						PredicateIRI:      "https://example.org/onto#hasTagNode",
						ValuePredicateIRI: "https://example.org/onto#tagValue",
					},
				},
			}))
		qTypeless, err := Parse(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?v WHERE {
				?s ex:hasTagNode ?n .
				?n ex:tagValue ?v .
			}
		`)
		if err != nil {
			t.Fatalf("Parse typeless: %v", err)
		}
		if f := FoldProjections(qTypeless.Where.Elements[0].(BGPPattern)); len(f.Chains) != 1 {
			t.Fatalf("expected typeless 1-step spec to fold, got %+v", f)
		}
		qTypelessWithExtraType, err := Parse(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?v WHERE {
				?s ex:hasTagNode ?n .
				?n a ex:SomeClass ;
				   ex:tagValue ?v .
			}
		`)
		if err != nil {
			t.Fatalf("Parse typeless with extra type: %v", err)
		}
		if f := FoldProjections(qTypelessWithExtraType.Where.Elements[0].(BGPPattern)); len(f.Chains) != 0 {
			t.Fatalf("expected typeless 1-step spec with extra rdf:type constraint not to fold, got %+v", f.Chains)
		}

		// 4. Unprojected StandardIRI overlap and sub-chain overlap guards disable folding so no matches are missed.
		vocabulary.Register("intel.flat.overlap",
			vocabulary.WithDataType(vocabulary.DataTypeString),
			vocabulary.WithIRI("https://example.org/onto#hasTagNode"))
		if f := FoldProjections(qTypeless.Where.Elements[0].(BGPPattern)); len(f.Chains) != 0 {
			t.Fatalf("expected unprojected StandardIRI overlap to disable folding, got %+v", f.Chains)
		}
	})
}

// TestPropBGPFoldingEquivalence proves via Rapid property testing that
// evaluating queries with BGP projection folding enabled returns the exact same
// bindings and solution multiplicity as evaluating the same queries over an
// independently materialized unfolded RDF graph across complete 1-step/3-step
// chains, projected intermediates, partial chains, missing type constraints,
// extra intermediate constraints/FILTERs, ambiguous ProjectionSpecs, variable
// predicates, and multi-valued predicates.
func TestPropBGPFoldingEquivalence(t *testing.T) {
	restore := vocabulary.SnapshotRegistry()
	defer restore()
	vocabulary.ClearRegistry()

	noPrefixes := vocabulary.WithPrefixRegistrar(func([]vocabulary.PrefixSpec) error { return nil })
	if err := diekm.Register(noPrefixes); err != nil {
		t.Fatalf("diekm.Register: %v", err)
	}
	roleMeta := vocabulary.GetPredicateMetadata("intel.role.assigned")
	if roleMeta == nil || roleMeta.Projection == nil {
		t.Fatal("missing intel.role.assigned projection metadata")
	}
	vocabulary.Register("intel.role.secondary",
		vocabulary.WithDataType(vocabulary.DataTypeString),
		vocabulary.WithIRI("https://w3id.org/c360/diekm#secondaryRole"),
		vocabulary.WithProjection(*roleMeta.Projection))
	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithDataType(vocabulary.DataTypeString),
		vocabulary.WithIRI("https://example.org/onto#callsign"))

	mapper := irimap.NewMapper(testBaseIRI)

	rapid.Check(t, func(rt *rapid.T) {
		nEntities := rapid.IntRange(1, 3).Draw(rt, "nEntities")
		entities := make([]graph.EntityState, 0, nEntities)

		rolePool := []string{"ISR", "recon", "strike"}
		altPool := []float64{500.0, 1500.5, 3000.25}

		for i := 0; i < nEntities; i++ {
			id := fmt.Sprintf("c360.ops.uas%d.intel.platform.node%d", i+1, i+1)
			var triples []message.Triple
			triples = append(triples, message.Triple{
				Subject:   id,
				Predicate: "c360.intel.callsign",
				Object:    fmt.Sprintf("NODE-%d", i+1),
			})

			nRoles := rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("nRoles_%d", i))
			for r := 0; r < nRoles; r++ {
				val := rapid.SampledFrom(rolePool).Draw(rt, fmt.Sprintf("role_%d_%d", i, r))
				triples = append(triples, message.Triple{
					Subject:   id,
					Predicate: "intel.role.assigned",
					Object:    val,
				})
				if rapid.Bool().Draw(rt, fmt.Sprintf("dupRole_%d_%d", i, r)) {
					triples = append(triples, message.Triple{
						Subject:   id,
						Predicate: "intel.role.assigned",
						Object:    val,
					})
				}
			}

			if rapid.Bool().Draw(rt, fmt.Sprintf("hasSecondaryRole_%d", i)) {
				val := rapid.SampledFrom(rolePool).Draw(rt, fmt.Sprintf("secRole_%d", i))
				triples = append(triples, message.Triple{
					Subject:   id,
					Predicate: "intel.role.secondary",
					Object:    val,
				})
			}

			nAlts := rapid.IntRange(0, 2).Draw(rt, fmt.Sprintf("nAlts_%d", i))
			for a := 0; a < nAlts; a++ {
				val := rapid.SampledFrom(altPool).Draw(rt, fmt.Sprintf("alt_%d_%d", i, a))
				triples = append(triples, message.Triple{
					Subject:   id,
					Predicate: "intel.measurement.altitude-m",
					Object:    val,
				})
			}

			entities = append(entities, graph.EntityState{ID: id, Triples: triples})
		}

		mr, err := reader.NewMemoryReader(entities...)
		if err != nil {
			rt.Fatalf("NewMemoryReader: %v", err)
		}
		exec := NewExecutor(mr, Config{Mapper: mapper})
		oracleGraph := materializeUnfoldedGraph(t, entities, mapper)

		shape := rapid.IntRange(0, 7).Draw(rt, "queryShape")
		distinct := rapid.Bool().Draw(rt, "distinct")
		projectInter := rapid.Bool().Draw(rt, "projectInter")
		distinctKw := ""
		if distinct {
			distinctKw = "DISTINCT "
		}

		var (
			queryStr     string
			expectFolded int
		)

		switch shape {
		case 0:
			// Complete 1-step role chain (ambiguous across intel.role.assigned + intel.role.secondary).
			vars := "?s ?role"
			if projectInter {
				vars = "?s ?roleNode ?role"
			}
			queryStr = fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				SELECT %s%s WHERE {
					?s obo:BFO_0000196 ?roleNode .
					?roleNode a obo:BFO_0000023 ;
					          rdfs:label ?role .
				}
				ORDER BY %s
			`, distinctKw, vars, vars)
			expectFolded = 1

		case 1:
			// Complete 3-step altitude chain.
			vars := "?s ?alt"
			if projectInter {
				vars = "?s ?altNode ?mice ?ibe ?alt"
			}
			queryStr = fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX cco: <https://www.commoncoreontologies.org/>
				PREFIX diekm: <https://w3id.org/c360/diekm#>
				SELECT %s%s WHERE {
					?s obo:BFO_0000196 ?altNode .
					?altNode a cco:ont00000787 ;
					         cco:ont00001904 ?mice .
					?mice a cco:ont00001163 ;
					      obo:BFO_0000084 ?ibe .
					?ibe a cco:ont00000253 ;
					     diekm:altitudeMetersValue ?alt .
				}
				ORDER BY %s
			`, distinctKw, vars, vars)
			expectFolded = 1

		case 2:
			// Combined complete 1-step role chain + complete 3-step altitude chain on same subject ?s.
			vars := "?s ?role ?alt"
			if projectInter {
				vars = "?s ?roleNode ?role ?altNode ?alt"
			}
			queryStr = fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX cco: <https://www.commoncoreontologies.org/>
				PREFIX diekm: <https://w3id.org/c360/diekm#>
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				SELECT %s%s WHERE {
					?s obo:BFO_0000196 ?roleNode .
					?roleNode a obo:BFO_0000023 ;
					          rdfs:label ?role .
					?s obo:BFO_0000196 ?altNode .
					?altNode a cco:ont00000787 ;
					         cco:ont00001904 ?mice .
					?mice a cco:ont00001163 ;
					      obo:BFO_0000084 ?ibe .
					?ibe a cco:ont00000253 ;
					     diekm:altitudeMetersValue ?alt .
				}
				ORDER BY %s
			`, distinctKw, vars, vars)
			expectFolded = 2

		case 3:
			// Partial chain (missing terminal step 2).
			queryStr = fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX cco: <https://www.commoncoreontologies.org/>
				SELECT %s?s ?altNode ?mice WHERE {
					?s obo:BFO_0000196 ?altNode .
					?altNode a cco:ont00000787 ;
					         cco:ont00001904 ?mice .
					?mice a cco:ont00001163 .
				}
				ORDER BY ?s ?altNode ?mice
			`, distinctKw)
			expectFolded = 0

		case 4:
			// Missing NodeClassIRI constraint on step 0 (?altNode).
			queryStr = fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX cco: <https://www.commoncoreontologies.org/>
				PREFIX diekm: <https://w3id.org/c360/diekm#>
				SELECT %s?s ?alt WHERE {
					?s obo:BFO_0000196 ?altNode .
					?altNode cco:ont00001904 ?mice .
					?mice a cco:ont00001163 ;
					      obo:BFO_0000084 ?ibe .
					?ibe a cco:ont00000253 ;
					     diekm:altitudeMetersValue ?alt .
				}
				ORDER BY ?s ?alt
			`, distinctKw)
			expectFolded = 0

		case 5:
			// Extra triple constraint on intermediate node ?roleNode.
			queryStr = fmt.Sprintf(`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				SELECT %s?s ?roleNode ?cls ?role WHERE {
					?s obo:BFO_0000196 ?roleNode .
					?roleNode a obo:BFO_0000023 ;
					          a ?cls ;
					          rdfs:label ?role .
				}
				ORDER BY ?s ?roleNode ?cls ?role
			`, distinctKw)
			expectFolded = 0

		case 6:
			// FILTER on intermediate variable ?roleNode disables folding; FILTER on terminal ?role still matches oracle.
			filterInter := rapid.Bool().Draw(rt, "filterInter")
			if filterInter {
				queryStr = fmt.Sprintf(`
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
					SELECT %s?s ?roleNode ?role WHERE {
						?s obo:BFO_0000196 ?roleNode .
						?roleNode a obo:BFO_0000023 ;
						          rdfs:label ?role .
						FILTER(BOUND(?roleNode))
					}
					ORDER BY ?s ?roleNode ?role
				`, distinctKw)
				expectFolded = 0
			} else {
				queryStr = fmt.Sprintf(`
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
					SELECT %s?s ?role WHERE {
						?s obo:BFO_0000196 ?roleNode .
						?roleNode a obo:BFO_0000023 ;
						          rdfs:label ?role .
						FILTER(?role != "nonexistent")
					}
					ORDER BY ?s ?role
				`, distinctKw)
				expectFolded = 1
			}

		case 7:
			// Variable predicate query over unfolded graph.
			queryStr = fmt.Sprintf(`
				SELECT %s?s ?p ?o WHERE {
					?s ?p ?o .
				}
				ORDER BY ?s ?p ?o
			`, distinctKw)
			expectFolded = 0
		}

		q, err := Parse(queryStr)
		if err != nil {
			rt.Fatalf("Parse: %v\nquery:\n%s", err, queryStr)
		}

		filterVars := make(map[string]bool)
		for _, f := range q.Where.Filters {
			collectFilterExprVars(f, filterVars)
		}
		folded := FoldProjections(q.Where.Elements[0].(BGPPattern), filterVars)
		if len(folded.Chains) != expectFolded {
			rt.Fatalf("shape %d: len(FoldProjections.Chains) = %d, want %d\nquery:\n%s",
				shape, len(folded.Chains), expectFolded, queryStr)
		}

		got, err := exec.Execute(context.Background(), q)
		if err != nil {
			rt.Fatalf("Execute: %v\nquery:\n%s", err, queryStr)
		}
		want := evalOverMaterializedOracle(t, q, oracleGraph)
		assertSolutionsEqual(rt, got, want, fmt.Sprintf("shape=%d distinct=%v projectInter=%v", shape, distinct, projectInter))
	})
}
