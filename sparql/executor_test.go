package sparql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"pgregory.net/rapid"
)

const testBaseIRI = "https://semengine.local"

func registerTestVocab(t *testing.T) func() {
	t.Helper()
	restore := vocabulary.SnapshotRegistry()
	vocabulary.ClearRegistry()

	// Two distinct dotted predicates sharing one StandardIRI for TestLookupByIRIMultiMatchUnion.
	vocabulary.Register("c360.sensor.temp-c",
		vocabulary.WithIRI("https://example.org/onto#temperatureCelsius"),
		vocabulary.WithDataType(vocabulary.DataTypeFloat))
	vocabulary.Register("legacy.telemetry.temp-c",
		vocabulary.WithIRI("https://example.org/onto#temperatureCelsius"),
		vocabulary.WithDataType(vocabulary.DataTypeFloat))

	// Additional registered predicates covering strings, ints, bools, datetimes, and entity_id links.
	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithIRI("https://example.org/onto#callsign"),
		vocabulary.WithDataType(vocabulary.DataTypeString))
	vocabulary.Register("c360.intel.priority",
		vocabulary.WithIRI("https://example.org/onto#priority"),
		vocabulary.WithDataType(vocabulary.DataTypeInt))
	vocabulary.Register("c360.intel.active",
		vocabulary.WithIRI("https://example.org/onto#active"),
		vocabulary.WithDataType(vocabulary.DataTypeBool))
	vocabulary.Register("c360.intel.observed-at",
		vocabulary.WithIRI("https://example.org/onto#observedAt"),
		vocabulary.WithDataType(vocabulary.DataTypeDateTime))
	vocabulary.Register("c360.intel.tracks",
		vocabulary.WithIRI("https://example.org/onto#tracks"),
		vocabulary.WithDataType(vocabulary.DataTypeEntityID))
	vocabulary.Register("c360.intel.note",
		vocabulary.WithIRI("https://example.org/onto#note"),
		vocabulary.WithDataType(vocabulary.DataTypeString))

	return restore
}

func mustMemoryReader(t *testing.T, entities ...graph.EntityState) *reader.MemoryReader {
	t.Helper()
	mr, err := reader.NewMemoryReader(entities...)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}
	return mr
}

func TestLookupByIRIMultiMatchUnion(t *testing.T) {
	defer registerTestVocab(t)()

	const sharedIRI = "https://example.org/onto#temperatureCelsius"
	matches := vocabulary.LookupByIRI(sharedIRI)
	wantPreds := []string{"c360.sensor.temp-c", "legacy.telemetry.temp-c"}
	if !slices.Equal(matches, wantPreds) {
		t.Fatalf("vocabulary.LookupByIRI(%q) = %v, want %v", sharedIRI, matches, wantPreds)
	}

	const (
		idAlpha   = "c360.ops.uas1.intel.platform.alpha"
		idBravo   = "c360.ops.uas1.intel.platform.bravo"
		idCharlie = "c360.ops.uas1.intel.platform.charlie"
		idDelta   = "c360.ops.uas1.intel.platform.delta"
	)

	mr := mustMemoryReader(t,
		graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.sensor.temp-c", Object: float64(21.5)},
			},
		},
		graph.EntityState{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "legacy.telemetry.temp-c", Object: float64(18.0)},
			},
		},
		graph.EntityState{
			ID: idCharlie,
			Triples: []message.Triple{
				{Subject: idCharlie, Predicate: "c360.sensor.temp-c", Object: float64(25.0)},
				{Subject: idCharlie, Predicate: "legacy.telemetry.temp-c", Object: float64(26.5)},
			},
		},
		graph.EntityState{
			// Identical values across both dotted predicates map to the same virtual RDF triple
			// and must be deduplicated as a graph set.
			ID: idDelta,
			Triples: []message.Triple{
				{Subject: idDelta, Predicate: "c360.sensor.temp-c", Object: float64(30.0)},
				{Subject: idDelta, Predicate: "legacy.telemetry.temp-c", Object: float64(30.0)},
			},
		},
	)

	mapper := irimap.NewMapper(testBaseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})

	// 1. Verify planner access classification for bound subject, predicate lookup, full scan, and unknown predicate.
	qPlan, err := Parse(`
		PREFIX ex: <https://example.org/onto#>
		SELECT ?s ?t WHERE {
			?s ex:temperatureCelsius ?t .
			<https://semengine.local/entities/c360/ops/uas1/intel/platform/alpha> ex:temperatureCelsius ?t2 .
			?s ?anyPred ?anyObj .
		}
	`)
	if err != nil {
		t.Fatalf("Parse plan query: %v", err)
	}
	bgp := qPlan.Where.Elements[0].(BGPPattern)
	plan := planBGP(bgp, mapper)
	if len(plan.Steps) != 3 {
		t.Fatalf("len(plan.Steps) = %d, want 3", len(plan.Steps))
	}
	if plan.Steps[0].Kind != accessExactEntity || plan.Steps[0].EntityID != idAlpha {
		t.Fatalf("plan.Steps[0] = %+v, want accessExactEntity on %s", plan.Steps[0], idAlpha)
	}
	if plan.Steps[1].Kind != accessPredicate || !slices.Equal(plan.Steps[1].Predicates, wantPreds) {
		t.Fatalf("plan.Steps[1] = %+v, want accessPredicate with %v", plan.Steps[1], wantPreds)
	}
	if plan.Steps[2].Kind != accessFullScan {
		t.Fatalf("plan.Steps[2] = %+v, want accessFullScan", plan.Steps[2])
	}

	// 2. Unbound subject query expands both dotted predicates and unions results.
	res, err := exec.ExecuteQuery(context.Background(), `
		PREFIX ex: <https://example.org/onto#>
		SELECT ?s ?temp WHERE {
			?s ex:temperatureCelsius ?temp .
		}
		ORDER BY ?s ?temp
	`)
	if err != nil {
		t.Fatalf("ExecuteQuery unbound multi-match: %v", err)
	}

	alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
	bravoIRI, _ := mapper.EntityIDToIRI(idBravo)
	charlieIRI, _ := mapper.EntityIDToIRI(idCharlie)
	deltaIRI, _ := mapper.EntityIDToIRI(idDelta)

	type pair struct{ s, temp string }
	got := make([]pair, 0, len(res.Solutions))
	for _, sol := range res.Solutions {
		got = append(got, pair{s: sol["s"].Value, temp: sol["temp"].Value})
	}
	want := []pair{
		{s: alphaIRI, temp: "21.5"},
		{s: bravoIRI, temp: "18.0"},
		{s: charlieIRI, temp: "25.0"},
		{s: charlieIRI, temp: "26.5"},
		{s: deltaIRI, temp: "30.0"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("multi-match union solutions = %v, want %v", got, want)
	}

	// 3. Bound subject query on idCharlie also expands both dotted predicates.
	resBound, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
		PREFIX ex: <https://example.org/onto#>
		SELECT ?temp WHERE {
			<%s> ex:temperatureCelsius ?temp .
		}
		ORDER BY DESC(?temp)
	`, charlieIRI))
	if err != nil {
		t.Fatalf("ExecuteQuery bound multi-match: %v", err)
	}
	if len(resBound.Solutions) != 2 || resBound.Solutions[0]["temp"].Value != "26.5" || resBound.Solutions[1]["temp"].Value != "25.0" {
		t.Fatalf("bound multi-match solutions = %+v, want [26.5, 25.0]", resBound.Solutions)
	}
}

func TestUnknownOrdinaryPredicateYieldsNoMatches(t *testing.T) {
	defer registerTestVocab(t)()

	const idAlpha = "c360.ops.uas1.intel.platform.alpha"
	mr := mustMemoryReader(t, graph.EntityState{
		ID: idAlpha,
		Triples: []message.Triple{
			{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "HAWK-1"},
			{Subject: idAlpha, Predicate: "unregistered.custom.prop", Object: "local-only"},
		},
	})
	mapper := irimap.NewMapper(testBaseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})

	// Planner classifies an unknown ordinary predicate IRI as accessEmpty.
	q, err := Parse(`SELECT ?s ?o WHERE { ?s <https://example.org/onto#unknownPredicate> ?o }`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan := planBGP(q.Where.Elements[0].(BGPPattern), mapper)
	if len(plan.Steps) != 1 || plan.Steps[0].Kind != accessEmpty {
		t.Fatalf("planBGP for unknown predicate = %+v, want accessEmpty", plan.Steps)
	}

	// Unbound subject with unknown predicate -> 0 solutions, 0 entities scanned, no error.
	res, err := exec.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("Execute unknown predicate: %v", err)
	}
	if len(res.Solutions) != 0 {
		t.Fatalf("len(res.Solutions) = %d, want 0", len(res.Solutions))
	}
	if res.EntitiesScanned != 0 {
		t.Fatalf("EntitiesScanned = %d, want 0", res.EntitiesScanned)
	}

	// Bound subject with unknown predicate -> 0 solutions, no error.
	alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
	resBound, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(
		`SELECT ?o WHERE { <%s> <https://example.org/onto#unknownPredicate> ?o }`, alphaIRI,
	))
	if err != nil {
		t.Fatalf("ExecuteQuery bound unknown predicate: %v", err)
	}
	if len(resBound.Solutions) != 0 {
		t.Fatalf("len(resBound.Solutions) = %d, want 0", len(resBound.Solutions))
	}

	// ASK with unknown predicate -> false, no error.
	resAsk, err := exec.ExecuteQuery(context.Background(),
		`ASK WHERE { ?s <https://example.org/onto#unknownPredicate> ?o }`,
	)
	if err != nil {
		t.Fatalf("ExecuteQuery ASK unknown predicate: %v", err)
	}
	if resAsk.AskResult {
		t.Fatal("AskResult = true, want false")
	}
}

func TestSelectAndAskExecution(t *testing.T) {
	defer registerTestVocab(t)()

	const (
		idAlpha   = "c360.ops.uas1.intel.platform.alpha"
		idBravo   = "c360.ops.uas1.intel.platform.bravo"
		idCharlie = "c360.ops.uas1.intel.platform.charlie"
	)

	mr := mustMemoryReader(t,
		graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
				{Subject: idAlpha, Predicate: "c360.intel.priority", Object: float64(10)},
				{Subject: idAlpha, Predicate: "c360.intel.active", Object: true},
				{Subject: idAlpha, Predicate: "c360.intel.observed-at", Object: "2026-10-15T10:00:00Z"},
				{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: idBravo},
				{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: idCharlie},
				{Subject: idAlpha, Predicate: "c360.intel.note", Object: " Commander ", Datatype: "@en"},
			},
		},
		graph.EntityState{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "bravo-2"},
				{Subject: idBravo, Predicate: "c360.intel.priority", Object: float64(20)},
				{Subject: idBravo, Predicate: "c360.intel.active", Object: false},
				{Subject: idBravo, Predicate: "c360.intel.observed-at", Object: "2026-10-15T12:00:00Z"},
			},
		},
		graph.EntityState{
			ID: idCharlie,
			Triples: []message.Triple{
				{Subject: idCharlie, Predicate: "c360.intel.callsign", Object: "CHARLIE-3"},
				{Subject: idCharlie, Predicate: "c360.intel.priority", Object: float64(5)},
				{Subject: idCharlie, Predicate: "c360.intel.observed-at", Object: "2026-10-15T08:00:00Z"},
			},
		},
	)

	mapper := irimap.NewMapper(testBaseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})
	alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
	bravoIRI, _ := mapper.EntityIDToIRI(idBravo)
	charlieIRI, _ := mapper.EntityIDToIRI(idCharlie)

	t.Run("BGP join with dynamic bound-subject batch hydration", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?target ?callsign ?prio WHERE {
				<%s> ex:tracks ?target .
				?target ex:callsign ?callsign ;
				        ex:priority ?prio .
			}
			ORDER BY ASC(?prio)
		`, alphaIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery: %v", err)
		}
		if !slices.Equal(res.Variables, []string{"target", "callsign", "prio"}) {
			t.Fatalf("Variables = %v, want [target callsign prio]", res.Variables)
		}
		if len(res.Solutions) != 2 {
			t.Fatalf("len(Solutions) = %d, want 2: %+v", len(res.Solutions), res.Solutions)
		}
		if res.Solutions[0]["target"].Value != charlieIRI || res.Solutions[0]["callsign"].Value != "CHARLIE-3" || res.Solutions[0]["prio"].Value != "5" {
			t.Fatalf("Solutions[0] = %+v, want charlie/CHARLIE-3/5", res.Solutions[0])
		}
		if res.Solutions[1]["target"].Value != bravoIRI || res.Solutions[1]["callsign"].Value != "bravo-2" || res.Solutions[1]["prio"].Value != "20" {
			t.Fatalf("Solutions[1] = %+v, want bravo/bravo-2/20", res.Solutions[1])
		}
	})

	t.Run("OPTIONAL left-outer join with scoped FILTER", func(t *testing.T) {
		// Charlie has no ex:active triple; Bravo has ex:active = false; Alpha has ex:active = true.
		// Inside OPTIONAL, FILTER(?active = true) rejects Bravo's optional binding while preserving Bravo's row with ?active unbound.
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?callsign ?active WHERE {
				?s ex:callsign ?callsign .
				OPTIONAL {
					?s ex:active ?active .
					FILTER(?active = true)
				}
			}
			ORDER BY ?callsign
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery OPTIONAL: %v", err)
		}
		if len(res.Solutions) != 3 {
			t.Fatalf("len(Solutions) = %d, want 3: %+v", len(res.Solutions), res.Solutions)
		}
		// ALPHA-1 -> active="true"; CHARLIE-3 -> unbound; bravo-2 -> unbound.
		if res.Solutions[0]["callsign"].Value != "ALPHA-1" || res.Solutions[0]["active"].Value != "true" {
			t.Fatalf("Solutions[0] = %+v", res.Solutions[0])
		}
		if _, ok := res.Solutions[1]["active"]; ok || res.Solutions[1]["callsign"].Value != "CHARLIE-3" {
			t.Fatalf("Solutions[1] = %+v, want CHARLIE-3 with unbound active", res.Solutions[1])
		}
		if _, ok := res.Solutions[2]["active"]; ok || res.Solutions[2]["callsign"].Value != "bravo-2" {
			t.Fatalf("Solutions[2] = %+v, want bravo-2 with unbound active", res.Solutions[2])
		}
	})

	t.Run("UNION duplicate preservation vs DISTINCT", func(t *testing.T) {
		queryDup := `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s WHERE {
				{ ?s ex:callsign "ALPHA-1" }
				UNION
				{ ?s ex:priority 10 }
				UNION
				{ ?s ex:callsign "ALPHA-1" }
			}
		`
		resDup, err := exec.ExecuteQuery(context.Background(), queryDup)
		if err != nil {
			t.Fatalf("ExecuteQuery UNION without DISTINCT: %v", err)
		}
		if len(resDup.Solutions) != 3 {
			t.Fatalf("len(resDup.Solutions) = %d, want 3 duplicate solutions preserved", len(resDup.Solutions))
		}
		for i, sol := range resDup.Solutions {
			if sol["s"].Value != alphaIRI {
				t.Fatalf("resDup.Solutions[%d] = %+v, want %s", i, sol, alphaIRI)
			}
		}

		queryDistinct := `
			PREFIX ex: <https://example.org/onto#>
			SELECT DISTINCT ?s WHERE {
				{ ?s ex:callsign "ALPHA-1" }
				UNION
				{ ?s ex:priority 10 }
				UNION
				{ ?s ex:callsign "ALPHA-1" }
			}
		`
		resDistinct, err := exec.ExecuteQuery(context.Background(), queryDistinct)
		if err != nil {
			t.Fatalf("ExecuteQuery UNION with DISTINCT: %v", err)
		}
		if len(resDistinct.Solutions) != 1 || resDistinct.Solutions[0]["s"].Value != alphaIRI {
			t.Fatalf("resDistinct.Solutions = %+v, want 1 deduplicated solution", resDistinct.Solutions)
		}
	})

	t.Run("FILTER built-ins BOUND REGEX STR LANG DATATYPE and 3-valued logic", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
			SELECT * WHERE {
				?s ex:callsign ?cs ;
				   ex:priority ?prio ;
				   ex:observedAt ?ts .
				OPTIONAL { ?s ex:active ?act }
				OPTIONAL { ?s ex:note ?note }
				FILTER(
					REGEX(?cs, "^(alpha|charlie)", "i") &&
					(+?prio >= 5 && -?prio <= -5) &&
					DATATYPE(?prio) = xsd:integer &&
					STR(?ts) >= "2026-10-15T08:00:00Z" &&
					(BOUND(?act) || !BOUND(?act)) &&
					(!BOUND(?note) || LANG(?note) = "en") &&
					(true || (?unboundVar > 100)) &&
					!(false && (?unboundVar > 100))
				)
			}
			ORDER BY DESC(?prio)
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery FILTER built-ins: %v", err)
		}
		// Matches ALPHA-1 (prio 10) and CHARLIE-3 (prio 5).
		if len(res.Solutions) != 2 {
			t.Fatalf("len(Solutions) = %d, want 2: %+v", len(res.Solutions), res.Solutions)
		}
		if res.Solutions[0]["cs"].Value != "ALPHA-1" || res.Solutions[1]["cs"].Value != "CHARLIE-3" {
			t.Fatalf("Solutions = %+v, want ALPHA-1 then CHARLIE-3", res.Solutions)
		}
	})

	t.Run("Blank node property list in WHERE BGP and SELECT *", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT * WHERE {
				?s ex:tracks [ ex:callsign "CHARLIE-3" ] .
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery blank-node property list: %v", err)
		}
		if !slices.Equal(res.Variables, []string{"s"}) {
			t.Fatalf("Variables = %v, want [s] (blank nodes excluded from SELECT *)", res.Variables)
		}
		if len(res.Solutions) != 1 || res.Solutions[0]["s"].Value != alphaIRI {
			t.Fatalf("Solutions = %+v, want [%s]", res.Solutions, alphaIRI)
		}
	})

	t.Run("ORDER BY and LIMIT/OFFSET deterministic slicing", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?cs WHERE {
				?s ex:callsign ?cs ;
				   ex:priority ?prio .
			}
			ORDER BY ASC(?prio)
			LIMIT 1
			OFFSET 1
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery slicing: %v", err)
		}
		// Sorted by prio ASC: CHARLIE-3 (5), ALPHA-1 (10), bravo-2 (20). Offset 1 Limit 1 -> ALPHA-1.
		if len(res.Solutions) != 1 || res.Solutions[0]["cs"].Value != "ALPHA-1" {
			t.Fatalf("Solutions = %+v, want [ALPHA-1]", res.Solutions)
		}
	})

	t.Run("ASK true and false", func(t *testing.T) {
		askTrue, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			ASK WHERE { ?s ex:callsign "bravo-2" }
		`)
		if err != nil {
			t.Fatalf("ASK true error: %v", err)
		}
		if !askTrue.AskResult {
			t.Fatal("AskResult = false, want true")
		}

		askFalse, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			ASK WHERE { ?s ex:callsign "nonexistent" }
		`)
		if err != nil {
			t.Fatalf("ASK false error: %v", err)
		}
		if askFalse.AskResult {
			t.Fatal("AskResult = true, want false")
		}
	})
}

func TestQueryLimits(t *testing.T) {
	defer registerTestVocab(t)()

	entities := make([]graph.EntityState, 0, 6)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("c360.ops.uas1.intel.platform.uav%d", i)
		entities = append(entities, graph.EntityState{
			ID: id,
			Triples: []message.Triple{
				{Subject: id, Predicate: "c360.intel.callsign", Object: fmt.Sprintf("UAV-%d", i)},
				{Subject: id, Predicate: "c360.intel.priority", Object: float64(i)},
			},
		})
	}
	mr := mustMemoryReader(t, entities...)
	mapper := irimap.NewMapper(testBaseIRI)

	t.Run("unconstrained scan stopped by scan budget", func(t *testing.T) {
		exec := NewExecutor(mr, Config{
			Mapper:          mapper,
			MaxScanEntities: 3,
		})
		_, err := exec.ExecuteQuery(context.Background(), `SELECT ?s ?p ?o WHERE { ?s ?p ?o }`)
		if !errors.Is(err, ErrScanBudgetExceeded) {
			t.Fatalf("err = %v, want errors.Is(..., ErrScanBudgetExceeded)", err)
		}
		var ce *errs.ClassifiedError
		if !errors.As(err, &ce) || ce.Code != ErrorCodeScanBudgetExceeded {
			t.Fatalf("ClassifiedError = %+v, want Code %q", ce, ErrorCodeScanBudgetExceeded)
		}
	})

	t.Run("result limit exceeded fails closed even when query has smaller LIMIT", func(t *testing.T) {
		exec := NewExecutor(mr, Config{
			Mapper:          mapper,
			MaxResults:      3,
			MaxScanEntities: 100,
		})
		// 6 entities match ex:callsign; MaxResults is 3; query LIMIT 2 is not consent to exceed MaxResults.
		_, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?cs WHERE { ?s ex:callsign ?cs }
			ORDER BY ?cs
			LIMIT 2
		`)
		if !errors.Is(err, ErrResultLimitExceeded) {
			t.Fatalf("err = %v, want errors.Is(..., ErrResultLimitExceeded)", err)
		}
		var ce *errs.ClassifiedError
		if !errors.As(err, &ce) || ce.Code != ErrorCodeResultLimitExceeded {
			t.Fatalf("ClassifiedError = %+v, want Code %q", ce, ErrorCodeResultLimitExceeded)
		}
	})

	t.Run("opt-in truncation on result limit returns partial result with metadata", func(t *testing.T) {
		exec := NewExecutor(mr, Config{
			Mapper:          mapper,
			MaxResults:      3,
			MaxScanEntities: 100,
		})
		q, err := Parse(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?cs WHERE { ?s ex:callsign ?cs }
			ORDER BY ?cs
		`)
		if err != nil {
			t.Fatalf("Parse: %v", err)
		}
		res, err := exec.Execute(context.Background(), q, WithAllowTruncation(true))
		if err != nil {
			t.Fatalf("Execute with AllowTruncation: %v", err)
		}
		if !res.Truncated || res.TruncationReason != TruncationReasonResultLimit {
			t.Fatalf("Truncated=%v Reason=%q, want true/%q", res.Truncated, res.TruncationReason, TruncationReasonResultLimit)
		}
		if len(res.Solutions) != 3 {
			t.Fatalf("len(Solutions) = %d, want 3", len(res.Solutions))
		}
	})

	t.Run("opt-in truncation on scan budget returns partial result with metadata", func(t *testing.T) {
		uav0IRI, _ := mapper.EntityIDToIRI("c360.ops.uas1.intel.platform.uav0")
		uav1IRI, _ := mapper.EntityIDToIRI("c360.ops.uas1.intel.platform.uav1")
		exec := NewExecutor(mr, Config{
			Mapper:          mapper,
			MaxResults:      100,
			MaxScanEntities: 1,
			AllowTruncation: true,
		})
		// Branch 1 reads 1 exact entity (within budget 1); Branch 2 attempts to read a 2nd entity and hits scan budget.
		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?cs WHERE {
				{ <%s> ex:callsign ?cs }
				UNION
				{ <%s> ex:callsign ?cs }
			}
		`, uav0IRI, uav1IRI))
		if err != nil {
			t.Fatalf("ExecuteQuery with AllowTruncation on scan budget: %v", err)
		}
		if !res.Truncated || res.TruncationReason != TruncationReasonScanBudget {
			t.Fatalf("Truncated=%v Reason=%q, want true/%q", res.Truncated, res.TruncationReason, TruncationReasonScanBudget)
		}
		if len(res.Solutions) != 1 || res.Solutions[0]["cs"].Value != "UAV-0" {
			t.Fatalf("Solutions = %+v, want partial [UAV-0]", res.Solutions)
		}
	})

	t.Run("query timeout fails closed even with AllowTruncation", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
		defer cancel()

		exec := NewExecutor(mr, Config{
			Mapper:          mapper,
			AllowTruncation: true,
		})
		_, err := exec.ExecuteQuery(ctx, `SELECT ?s ?p ?o WHERE { ?s ?p ?o }`)
		if !errors.Is(err, ErrQueryTimeout) {
			t.Fatalf("err = %v, want errors.Is(..., ErrQueryTimeout)", err)
		}
		var ce *errs.ClassifiedError
		if !errors.As(err, &ce) || ce.Code != ErrorCodeQueryTimeout {
			t.Fatalf("ClassifiedError = %+v, want Code %q", ce, ErrorCodeQueryTimeout)
		}
	})

	t.Run("nil context rejected", func(t *testing.T) {
		exec := NewExecutor(mr, Config{Mapper: mapper})
		var nilCtx context.Context
		_, err := exec.ExecuteQuery(nilCtx, `SELECT ?s WHERE { ?s ?p ?o }`)
		if !errors.Is(err, reader.ErrNilContext) {
			t.Fatalf("err = %v, want reader.ErrNilContext", err)
		}
	})
}

func TestPropSelectExecutionMatchesReferenceModel(t *testing.T) {
	defer registerTestVocab(t)()

	rapid.Check(t, func(rt *rapid.T) {
		nEntities := rapid.IntRange(1, 5).Draw(rt, "nEntities")
		entities := make([]graph.EntityState, 0, nEntities)
		type refRow struct {
			s        string
			callsign string
			prio     int
			hasAct   bool
			act      bool
		}
		mapper := irimap.NewMapper(testBaseIRI)
		refRows := make([]refRow, 0, nEntities)

		for i := 0; i < nEntities; i++ {
			id := fmt.Sprintf("c360.ops.uas1.intel.platform.node%d", i)
			cs := rapid.SampledFrom([]string{"ALPHA", "BRAVO", "CHARLIE"}).Draw(rt, fmt.Sprintf("cs_%d", i))
			prio := rapid.IntRange(1, 5).Draw(rt, fmt.Sprintf("prio_%d", i))
			hasAct := rapid.Bool().Draw(rt, fmt.Sprintf("hasAct_%d", i))
			act := rapid.Bool().Draw(rt, fmt.Sprintf("act_%d", i))

			triples := []message.Triple{
				{Subject: id, Predicate: "c360.intel.callsign", Object: cs},
				{Subject: id, Predicate: "c360.intel.priority", Object: float64(prio)},
			}
			if hasAct {
				triples = append(triples, message.Triple{
					Subject: id, Predicate: "c360.intel.active", Object: act,
				})
			}
			entities = append(entities, graph.EntityState{ID: id, Triples: triples})
			sIRI, err := mapper.EntityIDToIRI(id)
			if err != nil {
				rt.Fatalf("EntityIDToIRI: %v", err)
			}
			refRows = append(refRows, refRow{s: sIRI, callsign: cs, prio: prio, hasAct: hasAct, act: act})
		}

		mr, err := reader.NewMemoryReader(entities...)
		if err != nil {
			rt.Fatalf("NewMemoryReader: %v", err)
		}
		exec := NewExecutor(mr, Config{Mapper: mapper})

		distinct := rapid.Bool().Draw(rt, "distinct")
		minPrio := rapid.IntRange(1, 5).Draw(rt, "minPrio")
		descOrder := rapid.Bool().Draw(rt, "descOrder")

		distinctKw := ""
		if distinct {
			distinctKw = "DISTINCT "
		}
		orderKw := "ASC(?cs)"
		if descOrder {
			orderKw = "DESC(?cs)"
		}

		query := fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT %s?cs WHERE {
				?s ex:callsign ?cs ;
				   ex:priority ?prio .
				OPTIONAL { ?s ex:active ?act }
				FILTER(?prio >= %d)
			}
			ORDER BY %s
		`, distinctKw, minPrio, orderKw)

		res, err := exec.ExecuteQuery(context.Background(), query)
		if err != nil {
			rt.Fatalf("ExecuteQuery: %v\nquery:\n%s", err, query)
		}

		// Independent reference model over refRows.
		want := make([]string, 0)
		for _, r := range refRows {
			if r.prio >= minPrio {
				want = append(want, r.callsign)
			}
		}
		slices.SortFunc(want, func(a, b string) int {
			if descOrder {
				return -cmpStrings(a, b)
			}
			return cmpStrings(a, b)
		})
		if distinct {
			want = slices.Compact(want)
		}

		got := make([]string, 0, len(res.Solutions))
		for _, sol := range res.Solutions {
			got = append(got, sol["cs"].Value)
		}
		if !slices.Equal(got, want) {
			rt.Fatalf("got %v, want %v (minPrio=%d distinct=%v desc=%v)\nrows=%+v",
				got, want, minPrio, distinct, descOrder, strconv.Itoa(len(refRows)))
		}
	})
}

func cmpStrings(a, b string) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func TestPlannerAccessKindsAndEdgeCases(t *testing.T) {
	defer registerTestVocab(t)()

	// Verify accessKind.String() across all values.
	wantStrings := map[accessKind]string{
		accessEmpty:       "AccessEmpty",
		accessExactEntity: "AccessExactEntity",
		accessTypePattern: "AccessTypePattern",
		accessPredicate:   "AccessPredicate",
		accessFullScan:    "AccessFullScan",
		accessKind(99):    "AccessUnknown",
	}
	for k, want := range wantStrings {
		if got := k.String(); got != want {
			t.Fatalf("accessKind(%d).String() = %q, want %q", k, got, want)
		}
	}

	// LookupPredicateCandidates for empty and rdf:type.
	if got := LookupPredicateCandidates(""); got != nil {
		t.Fatalf("LookupPredicateCandidates(\"\") = %v, want nil", got)
	}
	typeCandidates := LookupPredicateCandidates(RDFTypeIRI)
	if !slices.Contains(typeCandidates, RDFTypeIRI) {
		t.Fatalf("LookupPredicateCandidates(RDFTypeIRI) = %v, want to contain %q", typeCandidates, RDFTypeIRI)
	}

	// planBGP with nil mapper uses default mapper; test literal subject, non-entity IRI subject,
	// blank-node/literal predicate, and property path classification.
	bgp := BGPPattern{
		Triples: []TriplePattern{
			{
				Subject:   Term{Kind: TermLiteral, Value: "lit"},
				Predicate: Term{Kind: TermIRI, Value: "https://example.org/onto#callsign"},
				Object:    Term{Kind: TermVariable, Value: "o"},
			},
			{
				Subject:   Term{Kind: TermIRI, Value: "https://example.org/not-an-entity"},
				Predicate: Term{Kind: TermIRI, Value: "https://example.org/onto#callsign"},
				Object:    Term{Kind: TermVariable, Value: "o"},
			},
			{
				Subject:   Term{Kind: TermVariable, Value: "s"},
				Predicate: Term{Kind: TermLiteral, Value: "bad-pred"},
				Object:    Term{Kind: TermVariable, Value: "o"},
			},
			{
				Subject:   Term{Kind: TermVariable, Value: "s"},
				Predicate: Term{Kind: TermIRI, Value: RDFTypeIRI},
				Path:      PathRDFTypeSubClassOfStar,
				Object:    Term{Kind: TermVariable, Value: "cls"},
			},
		},
	}
	plan := planBGP(bgp, nil)
	if len(plan.Steps) != 4 {
		t.Fatalf("len(plan.Steps) = %d, want 4", len(plan.Steps))
	}
	for i := 0; i < 3; i++ {
		if plan.Steps[i].Kind != accessEmpty {
			t.Fatalf("plan.Steps[%d].Kind = %v, want accessEmpty", i, plan.Steps[i].Kind)
		}
	}
	if plan.Steps[3].Kind != accessFullScan {
		t.Fatalf("plan.Steps[3].Kind = %v, want accessFullScan", plan.Steps[3].Kind)
	}
}

func TestFilterComparisonsEBVAndEscapes(t *testing.T) {
	defer registerTestVocab(t)()

	const (
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idBravo = "c360.ops.uas1.intel.platform.bravo"
	)

	mr := mustMemoryReader(t,
		graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "line1\n\"quote\"\t\\slash\r"},
				{Subject: idAlpha, Predicate: "c360.sensor.temp-c", Object: float64(22.5)},
				{Subject: idAlpha, Predicate: "c360.intel.priority", Object: float64(10)},
				{Subject: idAlpha, Predicate: "c360.intel.active", Object: true},
				{Subject: idAlpha, Predicate: "c360.intel.observed-at", Object: "2026-10-15T10:00:00Z"},
				{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: "_:b_target1"},
				{Subject: idAlpha, Predicate: "c360.intel.note", Object: "hello", Datatype: "@en"},
				{Subject: idAlpha, Predicate: RDFTypeIRI, Object: "https://example.org/onto#Drone"},
			},
		},
		graph.EntityState{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "c360.intel.callsign", Object: ""},
				{Subject: idBravo, Predicate: "c360.sensor.temp-c", Object: float64(0.0)},
				{Subject: idBravo, Predicate: "c360.intel.priority", Object: float64(0)},
				{Subject: idBravo, Predicate: "c360.intel.active", Object: false},
				{Subject: idBravo, Predicate: "c360.intel.observed-at", Object: "2026-10-15T09:00:00Z"},
				{Subject: idBravo, Predicate: "c360.intel.note", Object: "bonjour", Datatype: "@fr"},
			},
		},
	)

	mapper := irimap.NewMapper(testBaseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})
	alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
	bravoIRI, _ := mapper.EntityIDToIRI(idBravo)

	t.Run("escaped string literal round-trip and float literal matching", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?cs ?target ?type WHERE {
				{
					?s ex:temperatureCelsius 22.5 ;
					   ex:callsign ?cs ;
					   ex:tracks ?target ;
					   a ?type .
				}
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery: %v", err)
		}
		if len(res.Solutions) != 1 {
			t.Fatalf("len(Solutions) = %d, want 1: %+v", len(res.Solutions), res.Solutions)
		}
		wantCS := "line1\n\"quote\"\t\\slash\r"
		if res.Solutions[0]["cs"].Value != wantCS {
			t.Fatalf("cs = %q, want %q", res.Solutions[0]["cs"].Value, wantCS)
		}
		if res.Solutions[0]["target"].Kind != TermBlankNode || res.Solutions[0]["target"].Value != "_:b_target1" {
			t.Fatalf("target = %+v, want _:b_target1", res.Solutions[0]["target"])
		}
		if res.Solutions[0]["type"].Value != "https://example.org/onto#Drone" {
			t.Fatalf("type = %+v, want Drone IRI", res.Solutions[0]["type"])
		}
	})

	t.Run("all relational operators and EBV across types", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			PREFIX xsd: <http://www.w3.org/2001/XMLSchema#>
			SELECT ?s WHERE {
				?s ex:callsign ?cs ;
				   ex:temperatureCelsius ?temp ;
				   ex:priority ?prio ;
				   ex:active ?act ;
				   ex:observedAt ?ts ;
				   ex:note ?note .
				FILTER(
					?cs && ?temp && ?prio && ?act &&
					?prio > 5 && ?prio < 20 && ?prio <= 10 && ?prio >= 10 && ?prio != 0 &&
					-?temp < -20.0 && +?temp > 20.0 &&
					?act > false && ?act != false &&
					?ts > "2026-10-15T09:00:00Z"^^xsd:dateTime &&
					?ts != "2026-10-15T09:00:00Z"^^xsd:dateTime &&
					?note = "hello"@EN && ?note != "bonjour"@fr &&
					?s = <%s> && ?s != <%s> &&
					STR(?s) = "%s" &&
					DATATYPE(?note) = <http://www.w3.org/1999/02/22-rdf-syntax-ns#langString> &&
					REGEX(?cs, "quote", "")
				)
			}
		`, alphaIRI, bravoIRI, alphaIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery relational/EBV: %v", err)
		}
		if len(res.Solutions) != 1 || res.Solutions[0]["s"].Value != alphaIRI {
			t.Fatalf("Solutions = %+v, want [%s]", res.Solutions, alphaIRI)
		}
	})

	t.Run("falsy EBV values and type errors in FILTER rejected cleanly", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s WHERE {
				?s ex:callsign ?cs ;
				   ex:temperatureCelsius ?temp ;
				   ex:priority ?prio ;
				   ex:active ?act ;
				   ex:note ?note .
				FILTER(
					!?cs && !?temp && !?prio && !?act &&
					(true || (?s < ?cs)) &&
					(true || (?note < "z"@fr))
				)
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery falsy EBV: %v", err)
		}
		if len(res.Solutions) != 1 || res.Solutions[0]["s"].Value != bravoIRI {
			t.Fatalf("Solutions = %+v, want [%s]", res.Solutions, bravoIRI)
		}

		// In 3-valued logic, !(type error) is still a type error and rejected at top-level FILTER.
		resErr, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s WHERE {
				?s ex:callsign ?cs .
				FILTER(!(?s < ?cs))
			}
		`)
		if err != nil || len(resErr.Solutions) != 0 {
			t.Fatalf("expected negated type error to reject all solutions, got %+v (err=%v)", resErr, err)
		}
	})

	t.Run("ORDER BY across node kinds datetimes booleans and tie-break", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s WHERE {
				?s ex:observedAt ?ts ;
				   ex:active ?act .
				OPTIONAL { ?s ex:tracks ?trk }
			}
			ORDER BY ASC(?trk) ASC(?act) ASC(?ts)
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery ORDER BY node kinds: %v", err)
		}
		// Bravo has unbound ?trk (Unbound < BlankNode), so Bravo comes before Alpha.
		if len(res.Solutions) != 2 || res.Solutions[0]["s"].Value != bravoIRI || res.Solutions[1]["s"].Value != alphaIRI {
			t.Fatalf("Solutions = %+v, want [bravo, alpha]", res.Solutions)
		}
	})

	t.Run("missing entity in GetEntity and GetBatch plus LIMIT 0 and large OFFSET", func(t *testing.T) {
		missingIRI, _ := mapper.EntityIDToIRI("c360.ops.uas1.intel.platform.missing")

		// Single missing entity -> GetEntity ErrEntityNotFound -> 0 solutions, no error.
		resMissing, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?cs WHERE { <%s> ex:callsign ?cs }
		`, missingIRI))
		if err != nil || len(resMissing.Solutions) != 0 {
			t.Fatalf("single missing entity: res=%+v err=%v", resMissing, err)
		}

		// Batch with one existing and one missing entity via bound variable join.
		mrWithMissingLink := mustMemoryReader(t,
			graph.EntityState{
				ID: idAlpha,
				Triples: []message.Triple{
					{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: idBravo},
					{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: "c360.ops.uas1.intel.platform.missing"},
				},
			},
			graph.EntityState{
				ID: idBravo,
				Triples: []message.Triple{
					{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "BRAVO"},
				},
			},
		)
		execBatch := NewExecutor(mrWithMissingLink, Config{Mapper: mapper})
		resBatch, err := execBatch.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?target ?cs WHERE {
				<%s> ex:tracks ?target .
				?target ex:callsign ?cs .
			}
		`, alphaIRI))
		if err != nil || len(resBatch.Solutions) != 1 || resBatch.Solutions[0]["cs"].Value != "BRAVO" {
			t.Fatalf("batch with missing entity: res=%+v err=%v", resBatch, err)
		}

		// Batch truncation when MaxScanEntities allows only 1 of 2 batch entities.
		mrTwoTargets := mustMemoryReader(t,
			graph.EntityState{
				ID: idAlpha,
				Triples: []message.Triple{
					{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: idAlpha},
					{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: idBravo},
					{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA"},
				},
			},
			graph.EntityState{
				ID: idBravo,
				Triples: []message.Triple{
					{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "BRAVO"},
				},
			},
		)
		execBatchTrunc := NewExecutor(mrTwoTargets, Config{
			Mapper:          mapper,
			MaxScanEntities: 2, // 1 for alpha + 1 of {alpha, bravo} in GetBatch
			AllowTruncation: true,
		})
		resBatchTrunc, err := execBatchTrunc.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?target ?cs WHERE {
				<%s> ex:tracks ?target .
				?target ex:callsign ?cs .
			}
		`, alphaIRI))
		if err != nil || !resBatchTrunc.Truncated || resBatchTrunc.TruncationReason != TruncationReasonScanBudget {
			t.Fatalf("batch truncation: res=%+v err=%v", resBatchTrunc, err)
		}

		// OFFSET past end and LIMIT 0.
		resOffset, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s WHERE { ?s ex:callsign ?cs } OFFSET 99
		`)
		if err != nil || len(resOffset.Solutions) != 0 {
			t.Fatalf("OFFSET past end: res=%+v err=%v", resOffset, err)
		}
		resLimit0, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s WHERE { ?s ex:callsign ?cs } LIMIT 0
		`)
		if err != nil || len(resLimit0.Solutions) != 0 {
			t.Fatalf("LIMIT 0: res=%+v err=%v", resLimit0, err)
		}
	})

	t.Run("ASK with zero MaxResults and truncation", func(t *testing.T) {
		execZero := NewExecutor(mr, Config{
			Mapper:     mapper,
			MaxResults: -1,
		})
		if _, err := execZero.ExecuteQuery(context.Background(), `ASK WHERE { ?s ?p ?o }`); !errors.Is(err, ErrResultLimitExceeded) {
			t.Fatalf("ASK with 0 MaxResults err = %v, want ErrResultLimitExceeded", err)
		}
		qAsk, _ := Parse(`ASK WHERE { ?s ?p ?o }`)
		resAskTrunc, err := execZero.Execute(context.Background(), qAsk, WithAllowTruncation(true))
		if err != nil || !resAskTrunc.Truncated || resAskTrunc.TruncationReason != TruncationReasonResultLimit {
			t.Fatalf("ASK with 0 MaxResults + AllowTruncation: res=%+v err=%v", resAskTrunc, err)
		}
	})
}

type scanCountingReader struct {
	reader.GraphReader
	totalEntitiesRead int
	delayPerCall      time.Duration
}

func (r *scanCountingReader) sleepIfConfigured(ctx context.Context) error {
	if r.delayPerCall <= 0 {
		return nil
	}
	timer := time.NewTimer(r.delayPerCall)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *scanCountingReader) GetEntity(ctx context.Context, id string) (*graph.EntityState, error) {
	if err := r.sleepIfConfigured(ctx); err != nil {
		return nil, err
	}
	ent, err := r.GraphReader.GetEntity(ctx, id)
	if ent != nil {
		r.totalEntitiesRead++
	}
	return ent, err
}

func (r *scanCountingReader) GetBatch(ctx context.Context, ids []string) (*graph.EntityBatchResponse, error) {
	if err := r.sleepIfConfigured(ctx); err != nil {
		return nil, err
	}
	resp, err := r.GraphReader.GetBatch(ctx, ids)
	if resp != nil {
		r.totalEntitiesRead += len(resp.Entities)
	}
	return resp, err
}

func (r *scanCountingReader) ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error) {
	if err := r.sleepIfConfigured(ctx); err != nil {
		return nil, err
	}
	ents, err := r.GraphReader.ScanPattern(ctx, pattern, budget)
	r.totalEntitiesRead += len(ents)
	return ents, err
}

func (r *scanCountingReader) EntitiesByPredicate(ctx context.Context, predicate string, budget int) ([]graph.EntityState, error) {
	if err := r.sleepIfConfigured(ctx); err != nil {
		return nil, err
	}
	var scanned int
	subCtx := reader.WithScanCounter(ctx, &scanned)
	ents, err := r.GraphReader.EntitiesByPredicate(subCtx, predicate, budget)
	r.totalEntitiesRead += max(scanned, len(ents))
	return ents, err
}

func TestSubgroupAndUnionLexicalFilterScoping(t *testing.T) {
	defer registerTestVocab(t)()

	const idAlpha = "c360.ops.uas1.intel.platform.alpha"
	mr := mustMemoryReader(t, graph.EntityState{
		ID: idAlpha,
		Triples: []message.Triple{
			{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
			{Subject: idAlpha, Predicate: "c360.intel.priority", Object: float64(10)},
		},
	})
	mapper := irimap.NewMapper(testBaseIRI)
	exec := NewExecutor(mr, Config{Mapper: mapper})

	t.Run("nested group FILTER cannot reference variable bound only in outer group (B2)", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?pr ?x WHERE {
				?s ex:priority ?pr .
				{
					?s ex:priority ?x .
					FILTER(?pr > 2)
				}
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery subgroup out-of-scope FILTER: %v", err)
		}
		if len(res.Solutions) != 0 {
			t.Fatalf("len(Solutions) = %d, want 0 because ?pr is unbound inside nested group: %+v", len(res.Solutions), res.Solutions)
		}

		// When the FILTER is in the outer group (or references the inner variable ?x), the solution matches.
		resInScope, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?pr ?x WHERE {
				?s ex:priority ?pr .
				{
					?s ex:priority ?x .
					FILTER(?x > 2)
				}
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery subgroup in-scope FILTER: %v", err)
		}
		if len(resInScope.Solutions) != 1 {
			t.Fatalf("len(Solutions) = %d, want 1", len(resInScope.Solutions))
		}
	})

	t.Run("UNION branch FILTER cannot reference variable bound only in outer group (B2)", func(t *testing.T) {
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?pr WHERE {
				?s ex:priority ?pr .
				{
					?s ex:callsign ?cs .
					FILTER(?pr > 2)
				}
				UNION
				{
					?s ex:callsign ?cs .
					FILTER(?cs = "ALPHA-1")
				}
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery UNION lexical FILTER scope: %v", err)
		}
		// Branch 1 fails because ?pr is not bound inside branch 1; Branch 2 succeeds and joins with outer ?s/?pr.
		if len(res.Solutions) != 1 {
			t.Fatalf("len(Solutions) = %d, want 1 (only branch 2 matches): %+v", len(res.Solutions), res.Solutions)
		}
	})
}

func TestCumulativeScanBudgetAndMidEvalTimeoutAndEscapedLiterals(t *testing.T) {
	defer registerTestVocab(t)()

	entities := make([]graph.EntityState, 0, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("c360.ops.uas1.intel.platform.uav%d", i)
		entities = append(entities, graph.EntityState{
			ID: id,
			Triples: []message.Triple{
				{Subject: id, Predicate: "c360.intel.callsign", Object: fmt.Sprintf("UAV-%d", i)},
				{Subject: id, Predicate: "c360.intel.priority", Object: float64(i + 1)},
			},
		})
	}
	mr := mustMemoryReader(t, entities...)
	mapper := irimap.NewMapper(testBaseIRI)

	t.Run("multi-step query enforces cumulative MaxScanEntities across EntitiesByPredicate and joins (H1)", func(t *testing.T) {
		spy := &scanCountingReader{GraphReader: mr}
		exec := NewExecutor(spy, Config{
			Mapper:          mapper,
			MaxScanEntities: 4,
		})
		_, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?cs ?p WHERE {
				{ ?s ex:callsign ?cs }
				UNION
				{ ?s ex:priority ?p }
			}
		`)
		if !errors.Is(err, ErrScanBudgetExceeded) {
			t.Fatalf("err = %v, want ErrScanBudgetExceeded", err)
		}
		if spy.totalEntitiesRead > 4 {
			t.Fatalf("totalEntitiesRead = %d, want <= MaxScanEntities (4)", spy.totalEntitiesRead)
		}

		// With AllowTruncation, partial results are returned and totalEntitiesRead <= MaxScanEntities.
		spyTrunc := &scanCountingReader{GraphReader: mr}
		execTrunc := NewExecutor(spyTrunc, Config{
			Mapper:          mapper,
			MaxScanEntities: 4,
			AllowTruncation: true,
		})
		resTrunc, err := execTrunc.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?cs WHERE { ?s ex:callsign ?cs }
			ORDER BY ?s
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery with AllowTruncation: %v", err)
		}
		if !resTrunc.Truncated || resTrunc.TruncationReason != TruncationReasonScanBudget {
			t.Fatalf("Truncated=%v Reason=%q, want true/%q", resTrunc.Truncated, resTrunc.TruncationReason, TruncationReasonScanBudget)
		}
		if len(resTrunc.Solutions) != 4 || spyTrunc.totalEntitiesRead != 4 || resTrunc.EntitiesScanned != 4 {
			t.Fatalf("len(Solutions)=%d totalRead=%d EntitiesScanned=%d, want 4/4/4",
				len(resTrunc.Solutions), spyTrunc.totalEntitiesRead, resTrunc.EntitiesScanned)
		}
	})

	t.Run("context deadline expiring mid-evaluation returns ErrQueryTimeout (H5)", func(t *testing.T) {
		slowReader := &scanCountingReader{
			GraphReader:  mr,
			delayPerCall: 25 * time.Millisecond,
		}
		exec := NewExecutor(slowReader, Config{
			Mapper:  mapper,
			Timeout: 35 * time.Millisecond,
		})
		_, err := exec.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?s ?cs ?p WHERE {
				{ ?s ex:callsign ?cs }
				UNION
				{ ?s ex:priority ?p }
			}
		`)
		if !errors.Is(err, ErrQueryTimeout) {
			t.Fatalf("mid-eval timeout err = %v, want ErrQueryTimeout", err)
		}
	})

	t.Run("literals containing quote-at and quote-caret-caret sequences round-trip in SELECT and FILTER (H2)", func(t *testing.T) {
		const idTricky = "c360.ops.uas1.intel.platform.tricky"
		litAt := `say "@bob" now`
		litCaret := `value "^^<https://example.org/dt>" end`
		mrTricky := mustMemoryReader(t, graph.EntityState{
			ID: idTricky,
			Triples: []message.Triple{
				{Subject: idTricky, Predicate: "c360.intel.callsign", Object: litAt},
				{Subject: idTricky, Predicate: "c360.intel.note", Object: litCaret},
			},
		})
		execTricky := NewExecutor(mrTricky, Config{Mapper: mapper})
		res, err := execTricky.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT ?cs ?note WHERE {
				?s ex:callsign ?cs ;
				   ex:note ?note .
				FILTER(DATATYPE(?cs) = <http://www.w3.org/2001/XMLSchema#string> && LANG(?cs) = "")
			}
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery tricky literals: %v", err)
		}
		if len(res.Solutions) != 1 {
			t.Fatalf("len(Solutions) = %d, want 1: %+v", len(res.Solutions), res.Solutions)
		}
		if res.Solutions[0]["cs"].Value != litAt || res.Solutions[0]["cs"].Language != "" {
			t.Fatalf("?cs = %+v, want Value=%q Language=\"\"", res.Solutions[0]["cs"], litAt)
		}
		if res.Solutions[0]["note"].Value != litCaret || res.Solutions[0]["note"].Datatype != XSDStringIRI {
			t.Fatalf("?note = %+v, want Value=%q Datatype=xsd:string", res.Solutions[0]["note"], litCaret)
		}
	})

	t.Run("SELECT DISTINCT enforces MaxResults after deduplication and ASK short-circuits after first match", func(t *testing.T) {
		const (
			id1 = "c360.ops.uas1.intel.platform.n1"
			id2 = "c360.ops.uas1.intel.platform.n2"
			id3 = "c360.ops.uas1.intel.platform.n3"
		)
		mrDup := mustMemoryReader(t,
			graph.EntityState{ID: id1, Triples: []message.Triple{{Subject: id1, Predicate: "c360.intel.callsign", Object: "SAME"}}},
			graph.EntityState{ID: id2, Triples: []message.Triple{{Subject: id2, Predicate: "c360.intel.callsign", Object: "SAME"}}},
			graph.EntityState{ID: id3, Triples: []message.Triple{{Subject: id3, Predicate: "c360.intel.callsign", Object: "SAME"}}},
		)
		execDist := NewExecutor(mrDup, Config{
			Mapper:     mapper,
			MaxResults: 1,
		})
		// 3 entities bind ?cs="SAME", but SELECT DISTINCT ?cs produces 1 deduplicated row <= MaxResults(1).
		resDist, err := execDist.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			SELECT DISTINCT ?cs WHERE { ?s ex:callsign ?cs }
		`)
		if err != nil {
			t.Fatalf("SELECT DISTINCT within post-distinct MaxResults failed: %v", err)
		}
		if len(resDist.Solutions) != 1 || resDist.Solutions[0]["cs"].Value != "SAME" {
			t.Fatalf("SELECT DISTINCT solutions = %+v, want 1 row [SAME]", resDist.Solutions)
		}

		// ASK with MaxResults=1 succeeds on a multi-entity match without ErrResultLimitExceeded.
		resAsk, err := execDist.ExecuteQuery(context.Background(), `
			PREFIX ex: <https://example.org/onto#>
			ASK WHERE { ?s ex:callsign ?cs }
		`)
		if err != nil || !resAsk.AskResult {
			t.Fatalf("ASK with MaxResults=1: res=%+v err=%v, want true/nil", resAsk, err)
		}
	})

	t.Run("compareTermsTotalOrder is strictly transitive across mixed datatypes", func(t *testing.T) {
		terms := []Term{
			{Kind: TermBlankNode, Value: "_:b1"},
			{Kind: TermIRI, Value: "https://example.org/a"},
			{Kind: TermLiteral, Value: "10", Datatype: XSDStringIRI},
			{Kind: TermLiteral, Value: "2", Datatype: XSDStringIRI},
			{Kind: TermLiteral, Value: "hello", Language: "en", Datatype: RDFLangStringIRI},
			{Kind: TermLiteral, Value: "hello", Language: "fr", Datatype: RDFLangStringIRI},
			{Kind: TermLiteral, Value: "2", Datatype: XSDIntegerIRI},
			{Kind: TermLiteral, Value: "10", Datatype: XSDIntegerIRI},
			{Kind: TermLiteral, Value: "2.5", Datatype: XSDDoubleIRI},
			{Kind: TermLiteral, Value: "false", Datatype: XSDBooleanIRI},
			{Kind: TermLiteral, Value: "true", Datatype: XSDBooleanIRI},
			{Kind: TermLiteral, Value: "2026-10-15T08:00:00Z", Datatype: XSDDateTimeIRI},
			{Kind: TermLiteral, Value: "2026-10-15T12:00:00Z", Datatype: XSDDateTimeIRI},
			{Kind: TermLiteral, Value: `{"a":1}`, Datatype: "http://www.w3.org/1999/02/22-rdf-syntax-ns#JSON"},
		}
		for i := range terms {
			for j := range terms {
				cij := compareTermsTotalOrder(terms[i], true, terms[j], true)
				cji := compareTermsTotalOrder(terms[j], true, terms[i], true)
				if cij != -cji {
					t.Fatalf("antisymmetry violated for (%+v, %+v): %d vs %d", terms[i], terms[j], cij, cji)
				}
				for k := range terms {
					cjk := compareTermsTotalOrder(terms[j], true, terms[k], true)
					cik := compareTermsTotalOrder(terms[i], true, terms[k], true)
					if cij < 0 && cjk < 0 && cik >= 0 {
						t.Fatalf("transitivity violated for:\n  i=%+v\n  j=%+v\n  k=%+v\n  cij=%d cjk=%d cik=%d",
							terms[i], terms[j], terms[k], cij, cjk, cik)
					}
				}
			}
		}
	})
}
