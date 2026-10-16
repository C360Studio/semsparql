package sparql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semvocab/packs/diekm"
	"pgregory.net/rapid"
)

type patternRecordingReader struct {
	reader.GraphReader
	mu           sync.Mutex
	scanPatterns []string
}

func (r *patternRecordingReader) ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error) {
	r.mu.Lock()
	r.scanPatterns = append(r.scanPatterns, pattern)
	r.mu.Unlock()
	return r.GraphReader.ScanPattern(ctx, pattern, budget)
}

func (r *patternRecordingReader) resetAndTake() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := slices.Clone(r.scanPatterns)
	r.scanPatterns = nil
	return out
}

// TestVirtualRDFTypeAndSubclassResolution proves Task 3.1's virtual rdf:type
// and rdfs:subClassOf* resolution, wildcard system pattern scans
// (`<org>.<platform>.*.<domain>.<type>.*`), unbound class queries, zero-length
// paths, standalone `rdfs:subClassOf*` over the registered ontology class
// closure (owner ruling D.1c), and explicit/virtual rdf:type graph-set merging.
func TestVirtualRDFTypeAndSubclassResolution(t *testing.T) {
	restore := vocabulary.SnapshotRegistry()
	defer restore()

	if err := diekm.Register(); err != nil {
		t.Fatalf("diekm.Register: %v", err)
	}
	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithIRI("https://example.org/onto#callsign"),
		vocabulary.WithDataType(vocabulary.DataTypeString))

	const (
		idAlpha  = "c360.ops.uas1.intel.platform.alpha"
		idBravo  = "c360.ops.uas2.intel.platform.bravo"
		idGamma  = "other.plat.radar9.intel.platform.gamma"
		idObs1   = "c360.ops.sensor1.intel.observation.obs1"
		idSensor = "c360.ops.uas1.telemetry.sensor.s1"

		diekmPlatformIRI    = "https://w3id.org/c360/diekm#IntelligencePlatform"
		diekmObservationIRI = "https://w3id.org/c360/diekm#IntelligenceObservation"
		ccoVehicleIRI       = "https://www.commoncoreontologies.org/ont00000713"
		bfoObjectIRI        = "http://purl.obolibrary.org/obo/BFO_0000030"
		bfoEntityIRI        = "http://purl.obolibrary.org/obo/BFO_0000001"
	)

	mapper := irimap.NewMapper(testBaseIRI)
	alphaIRI, _ := mapper.EntityIDToIRI(idAlpha)
	bravoIRI, _ := mapper.EntityIDToIRI(idBravo)
	gammaIRI, _ := mapper.EntityIDToIRI(idGamma)
	obs1IRI, _ := mapper.EntityIDToIRI(idObs1)

	mr, err := reader.NewMemoryReader(
		graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
			},
		},
		graph.EntityState{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "BRAVO-2"},
			},
		},
		graph.EntityState{
			ID: idGamma,
			Triples: []message.Triple{
				{Subject: idGamma, Predicate: "c360.intel.callsign", Object: "GAMMA-9"},
			},
		},
		graph.EntityState{
			ID: idObs1,
			Triples: []message.Triple{
				{Subject: idObs1, Predicate: "c360.intel.callsign", Object: "OBS-1"},
			},
		},
		graph.EntityState{
			ID: idSensor,
			Triples: []message.Triple{
				{Subject: idSensor, Predicate: "c360.intel.callsign", Object: "SENSOR-1"},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}
	rec := &patternRecordingReader{GraphReader: mr}

	t.Run("bound class and superclass queries use ScanPattern across wildcard systems without stored rdf:type", func(t *testing.T) {
		exec := NewExecutor(rec, Config{Mapper: mapper})

		// 1. Plain `?s a cco:ont00000713` (Vehicle superclass on intel.platform) with unconstrained org/platform.
		rec.resetAndTake()
		res, err := exec.ExecuteQuery(context.Background(), `
			PREFIX cco: <https://www.commoncoreontologies.org/>
			SELECT ?s WHERE { ?s a cco:ont00000713 }
			ORDER BY ?s
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(?s a cco:ont00000713): %v", err)
		}
		gotPatterns := rec.resetAndTake()
		wantPatterns := []string{"*.*.*.intel.platform.*"}
		if !slices.Equal(gotPatterns, wantPatterns) {
			t.Fatalf("ScanPattern calls = %v, want %v", gotPatterns, wantPatterns)
		}
		if res.EntitiesScanned != 3 {
			t.Fatalf("EntitiesScanned = %d, want 3 (only intel.platform entities, not obs1 or sensor)", res.EntitiesScanned)
		}
		gotIRIs := solutionStrings(res.Solutions, "s")
		wantIRIs := []string{alphaIRI, bravoIRI, gammaIRI}
		if !slices.Equal(gotIRIs, wantIRIs) {
			t.Fatalf("?s bindings = %v, want %v", gotIRIs, wantIRIs)
		}

		// 2. `?s rdf:type/rdfs:subClassOf* obo:BFO_0000030` scoped to org="c360", platform="ops".
		resScoped, err := exec.ExecuteQuery(
			context.Background(),
			`
				PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				SELECT ?s WHERE { ?s rdf:type/rdfs:subClassOf* obo:BFO_0000030 }
				ORDER BY ?s
			`,
			WithOrgPlatform("c360", "ops"),
		)
		if err != nil {
			t.Fatalf("ExecuteQuery(scoped BFO_0000030): %v", err)
		}
		gotScopedPatterns := rec.resetAndTake()
		wantScopedPatterns := []string{"c360.ops.*.intel.platform.*"}
		if !slices.Equal(gotScopedPatterns, wantScopedPatterns) {
			t.Fatalf("scoped ScanPattern calls = %v, want %v", gotScopedPatterns, wantScopedPatterns)
		}
		if resScoped.EntitiesScanned != 2 {
			t.Fatalf("scoped EntitiesScanned = %d, want 2", resScoped.EntitiesScanned)
		}
		gotScopedIRIs := solutionStrings(resScoped.Solutions, "s")
		wantScopedIRIs := []string{alphaIRI, bravoIRI}
		if !slices.Equal(gotScopedIRIs, wantScopedIRIs) {
			t.Fatalf("scoped ?s bindings = %v, want %v", gotScopedIRIs, wantScopedIRIs)
		}

		// 3. Zero-length `rdf:type/rdfs:subClassOf*` path on primary ClassIRI `diekm:IntelligencePlatform`.
		resZero, err := exec.ExecuteQuery(context.Background(), `
			PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?s WHERE { ?s rdf:type/rdfs:subClassOf* diekm:IntelligencePlatform }
			ORDER BY ?s
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(zero-length ClassIRI): %v", err)
		}
		gotZeroPatterns := rec.resetAndTake()
		if !slices.Equal(gotZeroPatterns, wantPatterns) {
			t.Fatalf("zero-length ScanPattern calls = %v, want %v", gotZeroPatterns, wantPatterns)
		}
		if !slices.Equal(solutionStrings(resZero.Solutions, "s"), wantIRIs) {
			t.Fatalf("zero-length ?s = %v, want %v", solutionStrings(resZero.Solutions, "s"), wantIRIs)
		}

		// 4. Shared ancestor `obo:BFO_0000001` (entity) scans both `intel.observation` and `intel.platform`.
		resBoth, err := exec.ExecuteQuery(
			context.Background(),
			`
				PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				SELECT ?s WHERE { ?s rdf:type/rdfs:subClassOf* obo:BFO_0000001 }
				ORDER BY ?s
			`,
			WithOrgPlatform("c360", "ops"),
		)
		if err != nil {
			t.Fatalf("ExecuteQuery(BFO_0000001): %v", err)
		}
		gotBothPatterns := rec.resetAndTake()
		wantBothPatterns := []string{
			"c360.ops.*.intel.observation.*",
			"c360.ops.*.intel.platform.*",
		}
		if !slices.Equal(gotBothPatterns, wantBothPatterns) {
			t.Fatalf("shared ancestor ScanPattern calls = %v, want %v", gotBothPatterns, wantBothPatterns)
		}
		if !slices.Equal(solutionStrings(resBoth.Solutions, "s"), []string{obs1IRI, alphaIRI, bravoIRI}) {
			t.Fatalf("shared ancestor ?s = %v, want [%s %s %s]", solutionStrings(resBoth.Solutions, "s"), obs1IRI, alphaIRI, bravoIRI)
		}
	})

	t.Run("unbound class supplies ClassIRI and registered superclasses", func(t *testing.T) {
		exec := NewExecutor(rec, Config{Mapper: mapper})
		rec.resetAndTake()

		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT ?cls WHERE { <%s> rdf:type/rdfs:subClassOf* ?cls }
			ORDER BY ?cls
		`, alphaIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery(unbound ?cls): %v", err)
		}
		if gotPats := rec.resetAndTake(); len(gotPats) != 0 {
			t.Fatalf("exact entity query should not call ScanPattern, got %v", gotPats)
		}
		if res.EntitiesScanned != 1 {
			t.Fatalf("EntitiesScanned = %d, want 1", res.EntitiesScanned)
		}
		gotClasses := solutionStrings(res.Solutions, "cls")
		wantClasses := []string{
			"http://purl.obolibrary.org/obo/BFO_0000001",
			"http://purl.obolibrary.org/obo/BFO_0000002",
			"http://purl.obolibrary.org/obo/BFO_0000004",
			"http://purl.obolibrary.org/obo/BFO_0000030",
			"http://purl.obolibrary.org/obo/BFO_0000040",
			"https://w3id.org/c360/diekm#IntelligencePlatform",
			"https://www.commoncoreontologies.org/ont00000713",
			"https://www.commoncoreontologies.org/ont00000995",
		}
		if !slices.Equal(gotClasses, wantClasses) {
			t.Fatalf("unbound ?cls = %v, want %v", gotClasses, wantClasses)
		}

		// Plain `a ?cls` emits the same RDFS entailment class set.
		resPlain, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			SELECT ?cls WHERE { <%s> a ?cls }
			ORDER BY ?cls
		`, alphaIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery(plain a ?cls): %v", err)
		}
		if !slices.Equal(solutionStrings(resPlain.Solutions, "cls"), wantClasses) {
			t.Fatalf("plain a ?cls = %v, want %v", solutionStrings(resPlain.Solutions, "cls"), wantClasses)
		}
	})

	t.Run("standalone rdfs:subClassOf* evaluates over registered ontology closure with reflexive (C, C) (D.1c)", func(t *testing.T) {
		exec := NewExecutor(rec, Config{Mapper: mapper})
		rec.resetAndTake()

		// 1. Bound superclass `?sub rdfs:subClassOf* obo:BFO_0000030`.
		resSub, err := exec.ExecuteQuery(context.Background(), `
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			SELECT ?sub WHERE { ?sub rdfs:subClassOf* obo:BFO_0000030 }
			ORDER BY ?sub
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(?sub rdfs:subClassOf* BFO_0000030): %v", err)
		}
		if resSub.EntitiesScanned != 0 || len(rec.resetAndTake()) != 0 {
			t.Fatalf("standalone rdfs:subClassOf* must not touch GraphReader, EntitiesScanned=%d", resSub.EntitiesScanned)
		}
		gotSubs := solutionStrings(resSub.Solutions, "sub")
		wantSubs := []string{bfoObjectIRI, diekmPlatformIRI}
		if !slices.Equal(gotSubs, wantSubs) {
			t.Fatalf("?sub = %v, want %v", gotSubs, wantSubs)
		}

		// 2. Bound subclass `diekm:IntelligencePlatform rdfs:subClassOf* ?sup`.
		resSup, err := exec.ExecuteQuery(context.Background(), `
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?sup WHERE { diekm:IntelligencePlatform rdfs:subClassOf* ?sup }
			ORDER BY ?sup
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(IntelligencePlatform rdfs:subClassOf* ?sup): %v", err)
		}
		if len(resSup.Solutions) != 8 {
			t.Fatalf("len(?sup) = %d, want 8 (reflexive ClassIRI + 7 superclasses)", len(resSup.Solutions))
		}

		// 3. Arbitrary bound unregistered IRI `C` satisfies reflexive `(C, C)`.
		resUnreg, err := exec.ExecuteQuery(context.Background(), `
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT ?sup WHERE { <https://example.org/onto#UnregisteredClass> rdfs:subClassOf* ?sup }
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(unregistered reflexive): %v", err)
		}
		if !slices.Equal(solutionStrings(resUnreg.Solutions, "sup"), []string{"https://example.org/onto#UnregisteredClass"}) {
			t.Fatalf("unregistered reflexive ?sup = %v", solutionStrings(resUnreg.Solutions, "sup"))
		}

		// 4. Joined standalone `rdfs:subClassOf*` + `?s a ?sub` upgrades `?s a ?sub` to AccessTypePattern!
		rec.resetAndTake()
		resJoin, err := exec.ExecuteQuery(
			context.Background(),
			`
				PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				SELECT ?s ?sub WHERE {
					?s a ?sub .
					?sub rdfs:subClassOf* obo:BFO_0000030 .
				}
				ORDER BY ?s ?sub
			`,
			WithOrgPlatform("c360", "ops"),
		)
		if err != nil {
			t.Fatalf("ExecuteQuery(join standalone + ?s a ?sub): %v", err)
		}
		gotJoinPats := rec.resetAndTake()
		if !slices.Equal(gotJoinPats, []string{"c360.ops.*.intel.platform.*"}) {
			t.Fatalf("join ScanPattern calls = %v, want [c360.ops.*.intel.platform.*]", gotJoinPats)
		}
		if len(resJoin.Solutions) != 4 {
			// alpha x (BFO_0000030, diekm:IntelligencePlatform) + bravo x (BFO_0000030, diekm:IntelligencePlatform) = 4
			t.Fatalf("len(resJoin.Solutions) = %d, want 4: %+v", len(resJoin.Solutions), resJoin.Solutions)
		}
	})

	t.Run("explicit and virtual rdf:type triples merge as a mathematical graph set", func(t *testing.T) {
		const (
			idMerged     = "c360.ops.uas1.intel.platform.merged"
			extraTypeIRI = "https://example.org/onto#FeaturedAsset"
		)
		mergedIRI, _ := mapper.EntityIDToIRI(idMerged)
		mrMerged, err := reader.NewMemoryReader(graph.EntityState{
			ID: idMerged,
			Triples: []message.Triple{
				// Duplicate of virtual ClassIRI:
				{Subject: idMerged, Predicate: RDFTypeIRI, Object: diekmPlatformIRI},
				// Duplicate of virtual superclass cco:Vehicle:
				{Subject: idMerged, Predicate: RDFTypeIRI, Object: ccoVehicleIRI},
				// Additional explicit rdf:type not in EntityTypeMetadata:
				{Subject: idMerged, Predicate: RDFTypeIRI, Object: extraTypeIRI},
			},
		})
		if err != nil {
			t.Fatalf("NewMemoryReader: %v", err)
		}
		exec := NewExecutor(mrMerged, Config{Mapper: mapper})

		// Without DISTINCT, `?s a cco:ont00000713` must return `merged` ONCE, not twice.
		resDup, err := exec.ExecuteQuery(context.Background(), `
			PREFIX cco: <https://www.commoncoreontologies.org/>
			SELECT ?s WHERE { ?s a cco:ont00000713 }
		`)
		if err != nil {
			t.Fatalf("ExecuteQuery(merged cco:Vehicle): %v", err)
		}
		if !slices.Equal(solutionStrings(resDup.Solutions, "s"), []string{mergedIRI}) {
			t.Fatalf("merged ?s = %v, want [%s] (single deduplicated graph triple)", solutionStrings(resDup.Solutions, "s"), mergedIRI)
		}

		// Querying all types of `merged` without DISTINCT returns 8 virtual types + 1 extra explicit type = 9 distinct types.
		resAll, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			SELECT ?cls WHERE { <%s> a ?cls }
			ORDER BY ?cls
		`, mergedIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery(<merged> a ?cls): %v", err)
		}
		gotAll := solutionStrings(resAll.Solutions, "cls")
		if len(gotAll) != 9 || !slices.Contains(gotAll, extraTypeIRI) {
			t.Fatalf("merged all types (len=%d) = %v, want 9 unique types containing %s", len(gotAll), gotAll, extraTypeIRI)
		}

		// Querying the unregistered explicit type `?s a <extraTypeIRI>` resolves `merged` via explicit rdf:type predicate scan.
		resExtra, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			SELECT ?s WHERE { ?s a <%s> }
		`, extraTypeIRI))
		if err != nil {
			t.Fatalf("ExecuteQuery(?s a extraTypeIRI): %v", err)
		}
		if !slices.Equal(solutionStrings(resExtra.Solutions, "s"), []string{mergedIRI}) {
			t.Fatalf("explicit type query ?s = %v, want [%s]", solutionStrings(resExtra.Solutions, "s"), mergedIRI)
		}
	})

	t.Run("type helper validation and scan budget enforcement on bound-class scans", func(t *testing.T) {
		if _, err := EntityTypePattern("invalid-key", "", ""); err == nil {
			t.Fatal("EntityTypePattern(invalid-key) = nil, want error")
		}
		if _, err := EntityTypePattern("intel.platform", "bad..org", "ops"); err == nil {
			t.Fatal("EntityTypePattern(bad..org) = nil, want error")
		}
		if pats, err := TypeScanPatternsForClass("", "", ""); err != nil || pats != nil {
			t.Fatalf("TypeScanPatternsForClass(\"\") = (%v, %v), want (nil, nil)", pats, err)
		}
		if iris := VirtualTypeIRIsForEntityID("not-an-entity-id"); iris != nil {
			t.Fatalf("VirtualTypeIRIsForEntityID(invalid) = %v, want nil", iris)
		}
		if supers := SuperclassesForClassIRI(""); supers != nil {
			t.Fatalf("SuperclassesForClassIRI(\"\") = %v, want nil", supers)
		}

		// Bound-class scan exceeding MaxScanEntities=1 fails closed by default and truncates on opt-in.
		execSmallBudget := NewExecutor(rec, Config{
			Mapper:          mapper,
			MaxScanEntities: 1,
		})
		_, err := execSmallBudget.ExecuteQuery(context.Background(), `
			PREFIX cco: <https://www.commoncoreontologies.org/>
			SELECT ?s WHERE { ?s a cco:ont00000713 }
		`)
		if !errors.Is(err, ErrScanBudgetExceeded) {
			t.Fatalf("bound-class scan over budget err = %v, want ErrScanBudgetExceeded", err)
		}
		resTrunc, err := execSmallBudget.ExecuteQuery(
			context.Background(),
			`
				PREFIX obo: <http://purl.obolibrary.org/obo/>
				SELECT ?s WHERE { ?s a obo:BFO_0000001 }
			`,
			WithAllowTruncation(true),
			WithOrgPlatform("c360", "ops"),
		)
		if err != nil {
			t.Fatalf("bound-class scan with AllowTruncation: %v", err)
		}
		if !resTrunc.Truncated || resTrunc.TruncationReason != TruncationReasonScanBudget {
			t.Fatalf("Truncated=%v Reason=%q, want true/%q", resTrunc.Truncated, resTrunc.TruncationReason, TruncationReasonScanBudget)
		}
	})
}

// TestSuperclassClosureStartupCheck proves Task 3.1's startup ancestor-closure
// validation (`ValidateSuperclassClosure`).
func TestSuperclassClosureStartupCheck(t *testing.T) {
	t.Run("released DIEKM pack passes startup superclass closure check", func(t *testing.T) {
		restore := vocabulary.SnapshotRegistry()
		defer restore()

		if err := diekm.Register(); err != nil {
			t.Fatalf("diekm.Register: %v", err)
		}
		if err := ValidateSuperclassClosure(); err != nil {
			t.Fatalf("ValidateSuperclassClosure on clean DIEKM pack = %v, want nil", err)
		}
	})

	t.Run("child entity type missing registered ancestor of superclass fails startup", func(t *testing.T) {
		restore := vocabulary.SnapshotRegistry()
		defer restore()

		// Register parent entity type `uxs.vehicle` with ClassIRI cco:Vehicle and superclass obo:BFO_0000030.
		vocabulary.RegisterEntityType("uxs.vehicle",
			vocabulary.WithClassIRI("https://www.commoncoreontologies.org/ont00000713"),
			vocabulary.WithSuperclass("https://www.commoncoreontologies.org/ont00000995"),
			vocabulary.WithSuperclass("http://purl.obolibrary.org/obo/BFO_0000030"),
		)
		// Register child entity type `intel.platform` listing cco:Vehicle as superclass,
		// but omitting obo:BFO_0000030 from its own Superclasses closure.
		vocabulary.RegisterEntityType("intel.platform",
			vocabulary.WithClassIRI("https://w3id.org/c360/diekm#IntelligencePlatform"),
			vocabulary.WithSuperclass("https://www.commoncoreontologies.org/ont00000713"),
			vocabulary.WithSuperclass("https://www.commoncoreontologies.org/ont00000995"),
		)

		err := ValidateSuperclassClosure()
		if !errors.Is(err, ErrIncompleteSuperclassClosure) {
			t.Fatalf("ValidateSuperclassClosure() = %v, want errors.Is(..., ErrIncompleteSuperclassClosure)", err)
		}
		var ce *errs.ClassifiedError
		if !errors.As(err, &ce) || ce.Code != ErrorCodeIncompleteSuperclassClosure {
			t.Fatalf("ClassifiedError = %+v, want Code %q", ce, ErrorCodeIncompleteSuperclassClosure)
		}
		for _, wantSubstr := range []string{
			"intel.platform",
			"https://www.commoncoreontologies.org/ont00000713",
			"http://purl.obolibrary.org/obo/BFO_0000030",
		} {
			if !strings.Contains(err.Error(), wantSubstr) {
				t.Fatalf("error %q does not identify %q", err.Error(), wantSubstr)
			}
		}
	})

	t.Run("multi-hop missing transitive ancestor fails startup", func(t *testing.T) {
		restore := vocabulary.SnapshotRegistry()
		defer restore()

		vocabulary.RegisterEntityType("ont.top",
			vocabulary.WithClassIRI("https://example.org/onto#Top"),
			vocabulary.WithSuperclass("https://example.org/onto#Root"),
		)
		vocabulary.RegisterEntityType("ont.mid",
			vocabulary.WithClassIRI("https://example.org/onto#Mid"),
			vocabulary.WithSuperclass("https://example.org/onto#Top"),
			vocabulary.WithSuperclass("https://example.org/onto#Root"),
		)
		vocabulary.RegisterEntityType("ont.leaf",
			vocabulary.WithClassIRI("https://example.org/onto#Leaf"),
			vocabulary.WithSuperclass("https://example.org/onto#Mid"),
			vocabulary.WithSuperclass("https://example.org/onto#Top"),
			// Missing https://example.org/onto#Root!
		)

		err := ValidateSuperclassClosure()
		if !errors.Is(err, ErrIncompleteSuperclassClosure) || !strings.Contains(err.Error(), "https://example.org/onto#Root") {
			t.Fatalf("ValidateSuperclassClosure() = %v, want ErrIncompleteSuperclassClosure naming Root", err)
		}
	})
}

// TestPropVirtualTypeAndSubClassClosure uses Rapid property generation to verify
// virtual rdf:type/subclass entailment and standalone rdfs:subClassOf* closure
// against an independent set-theoretic reference oracle.
func TestPropVirtualTypeAndSubClassClosure(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		restore := vocabulary.SnapshotRegistry()
		defer restore()

		// Generate a 3-tier ontology DAG: Leaf0 -> Mid0 -> Root0, Leaf1 -> Mid0 -> Root0.
		const (
			clsLeaf0 = "https://example.org/onto#Leaf0"
			clsLeaf1 = "https://example.org/onto#Leaf1"
			clsMid0  = "https://example.org/onto#Mid0"
			clsRoot0 = "https://example.org/onto#Root0"
		)
		leaf0HasMid := rapid.Bool().Draw(rt, "leaf0HasMid")
		midHasRoot := rapid.Bool().Draw(rt, "midHasRoot")

		var midSupers []vocabulary.EntityTypeOption
		midSupers = append(midSupers, vocabulary.WithClassIRI(clsMid0))
		if midHasRoot {
			midSupers = append(midSupers, vocabulary.WithSuperclass(clsRoot0))
		}
		vocabulary.RegisterEntityType("dom.mid", midSupers...)

		var leaf0Supers []vocabulary.EntityTypeOption
		leaf0Supers = append(leaf0Supers, vocabulary.WithClassIRI(clsLeaf0))
		if leaf0HasMid {
			leaf0Supers = append(leaf0Supers, vocabulary.WithSuperclass(clsMid0))
			if midHasRoot {
				leaf0Supers = append(leaf0Supers, vocabulary.WithSuperclass(clsRoot0))
			}
		}
		vocabulary.RegisterEntityType("dom.leafa", leaf0Supers...)

		vocabulary.RegisterEntityType("dom.leafb",
			vocabulary.WithClassIRI(clsLeaf1),
			vocabulary.WithSuperclass(clsRoot0),
		)

		if err := ValidateSuperclassClosure(); err != nil {
			rt.Fatalf("ValidateSuperclassClosure on complete DAG failed: %v", err)
		}

		// Independent reference map: domainType -> class set.
		refClasses := map[string][]string{
			"dom.mid":   {clsMid0},
			"dom.leafa": {clsLeaf0},
			"dom.leafb": {clsLeaf1, clsRoot0},
		}
		if midHasRoot {
			refClasses["dom.mid"] = append(refClasses["dom.mid"], clsRoot0)
		}
		if leaf0HasMid {
			refClasses["dom.leafa"] = append(refClasses["dom.leafa"], clsMid0)
			if midHasRoot {
				refClasses["dom.leafa"] = append(refClasses["dom.leafa"], clsRoot0)
			}
		}

		mapper := irimap.NewMapper(testBaseIRI)
		nEnts := rapid.IntRange(1, 5).Draw(rt, "nEnts")
		entities := make([]graph.EntityState, 0, nEnts)
		wantByClass := make(map[string][]string)

		for i := 0; i < nEnts; i++ {
			dt := rapid.SampledFrom([]string{"dom.mid", "dom.leafa", "dom.leafb"}).Draw(rt, fmt.Sprintf("dt_%d", i))
			sys := rapid.SampledFrom([]string{"sys1", "sys2"}).Draw(rt, fmt.Sprintf("sys_%d", i))
			id := fmt.Sprintf("c360.ops.%s.%s.e%d", sys, dt, i)
			entities = append(entities, graph.EntityState{ID: id})
			iri, err := mapper.EntityIDToIRI(id)
			if err != nil {
				rt.Fatalf("EntityIDToIRI: %v", err)
			}
			for _, c := range refClasses[dt] {
				wantByClass[c] = append(wantByClass[c], iri)
			}
		}

		mr, err := reader.NewMemoryReader(entities...)
		if err != nil {
			rt.Fatalf("NewMemoryReader: %v", err)
		}
		exec := NewExecutor(mr, Config{Mapper: mapper})

		targetClass := rapid.SampledFrom([]string{clsLeaf0, clsLeaf1, clsMid0, clsRoot0}).Draw(rt, "targetClass")
		res, err := exec.ExecuteQuery(context.Background(), fmt.Sprintf(`
			PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT ?s WHERE { ?s rdf:type/rdfs:subClassOf* <%s> }
			ORDER BY ?s
		`, targetClass))
		if err != nil {
			rt.Fatalf("ExecuteQuery: %v", err)
		}
		got := solutionStrings(res.Solutions, "s")
		want := slices.Clone(wantByClass[targetClass])
		slices.Sort(want)
		want = slices.Compact(want)
		if !slices.Equal(got, want) {
			rt.Fatalf("class %s: got %v, want %v", targetClass, got, want)
		}
	})
}

func solutionStrings(sols []Solution, varName string) []string {
	out := make([]string, 0, len(sols))
	for _, s := range sols {
		if t, ok := s[varName]; ok {
			out = append(out, t.Value)
		}
	}
	return out
}
