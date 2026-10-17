package sparql

import (
	"slices"

	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/irimap"
)

// accessKind classifies the constrained GraphReader access strategy selected for
// a TriplePattern. Steps are ordered from most selective (lowest rank) to least
// selective (highest rank):
//
//	accessEmpty -> accessExactEntity -> accessTypePattern -> accessPredicate -> accessFullScan
type accessKind int

const (
	// accessEmpty indicates the pattern cannot match any triples in the virtual graph
	// (for example, a literal subject, a non-entity IRI subject on an ordinary triple,
	// or an ordinary predicate IRI with no registered match in vocabulary.LookupByIRI).
	accessEmpty accessKind = iota
	// accessExactEntity reads known canonical entity IDs via GraphReader.GetEntity or GetBatch.
	accessExactEntity
	// accessTypePattern scans a constrained six-part ADR-102 entity-type pattern via ScanPattern.
	accessTypePattern
	// accessPredicate scans candidate dotted predicates expanded from vocabulary.LookupByIRI via EntitiesByPredicate.
	accessPredicate
	// accessFullScan scans all entities under the query budget via ScanPattern(MatchAllPattern).
	accessFullScan
)

// String returns a human-readable name for the access strategy.
func (k accessKind) String() string {
	switch k {
	case accessEmpty:
		return "AccessEmpty"
	case accessExactEntity:
		return "AccessExactEntity"
	case accessTypePattern:
		return "AccessTypePattern"
	case accessPredicate:
		return "AccessPredicate"
	case accessFullScan:
		return "AccessFullScan"
	default:
		return "AccessUnknown"
	}
}

// planStep describes the planned access strategy for a single TriplePattern or
// a collapsed FoldedChain in a BGP.
type planStep struct {
	Pattern    TriplePattern
	Folded     *FoldedChain
	Kind       accessKind
	EntityID   string
	Predicates []string
	Patterns   []string
}

// bgpPlan is the ordered execution plan for a Basic Graph Pattern.
type bgpPlan struct {
	Steps []planStep
}

type bgpCandidate struct {
	isChain bool
	chain   FoldedChain
	triple  TriplePattern
}

// LookupPredicateCandidates expands a predicate IRI into all matching dotted
// predicates that can produce that IRI in the virtual unfolded graph:
//   - unprojected predicates registered in vocabulary.LookupByIRI(predicateIRI),
//   - projected predicates whose ProjectionSpec uses predicateIRI as a step
//     PredicateIRI or terminal ValuePredicateIRI, and
//   - for rdf:type, the literal rdf:type IRI plus all projected predicates with
//     intermediate NodeClassIRIs.
//
// An unknown ordinary predicate IRI returns nil.
func LookupPredicateCandidates(predicateIRI string) []string {
	if predicateIRI == "" {
		return nil
	}
	var matches []string
	for _, name := range vocabulary.LookupByIRI(predicateIRI) {
		meta := vocabulary.GetPredicateMetadata(name)
		if meta != nil && meta.Projection != nil && len(meta.Projection.Steps) > 0 {
			continue
		}
		matches = append(matches, name)
	}
	matches = append(matches, ProjectionPredicatesForStepIRI(predicateIRI)...)
	if predicateIRI == RDFTypeIRI {
		matches = append(matches, RDFTypeIRI)
		matches = append(matches, AllProjectedPredicates()...)
	}
	if len(matches) == 0 {
		return nil
	}
	slices.Sort(matches)
	return slices.Compact(matches)
}

// planBGP folds complete ProjectionSpec chains and orders the resulting steps
// of bgp using constrained-access priority (exact entity -> type pattern ->
// predicate selection -> bounded full scan), accounting for variables bound by
// earlier steps in the plan.
func planBGP(bgp BGPPattern, mapper *irimap.Mapper) bgpPlan {
	return planBGPWithScope(bgp, mapper, "", "")
}

// planBGPWithScope folds complete ProjectionSpec chains and orders the resulting
// steps of bgp using constrained-access priority, scoping bound-class pattern
// scans to `<org>.<platform>.*.<domain>.<type>.*` (substituting `*` when org or
// platform is empty).
func planBGPWithScope(bgp BGPPattern, mapper *irimap.Mapper, org, platform string, disallowedVars ...map[string]bool) bgpPlan {
	if mapper == nil {
		mapper = irimap.NewMapper("")
	}
	candidates := buildBGPCandidates(bgp, disallowedVars...)

	steps := make([]planStep, 0, len(candidates))
	boundVars := make(map[string]bool)

	for len(candidates) > 0 {
		bestIdx, bestStep := pickNextBGPCandidate(candidates, boundVars, boundVars, mapper, org, platform)
		steps = append(steps, bestStep)
		recordStepVars(bestStep, boundVars)
		candidates = slices.Delete(candidates, bestIdx, bestIdx+1)
	}

	return bgpPlan{Steps: steps}
}

func buildBGPCandidates(bgp BGPPattern, disallowedVars ...map[string]bool) []bgpCandidate {
	folded := FoldProjections(bgp, disallowedVars...)
	candidates := make([]bgpCandidate, 0, len(folded.Chains)+len(folded.RemainingTriples))
	for _, ch := range folded.Chains {
		candidates = append(candidates, bgpCandidate{isChain: true, chain: ch})
	}
	for _, tp := range folded.RemainingTriples {
		candidates = append(candidates, bgpCandidate{triple: tp})
	}
	return candidates
}

// pickNextBGPCandidate selects the next highest-selectivity BGP candidate given
// the currently bound subject variables (boundSubjects) and object IRI variables
// (boundObjectIRIs). Both planBGPWithScope and Executor.evalBGP delegate to this
// function so static planning and runtime execution share one ordering engine.
func pickNextBGPCandidate(
	candidates []bgpCandidate,
	boundSubjects map[string]bool,
	boundObjectIRIs map[string]bool,
	mapper *irimap.Mapper,
	org, platform string,
) (int, planStep) {
	bestIdx := 0
	bestStep, bestRank := classifyCandidate(candidates[0], boundSubjects, boundObjectIRIs, mapper, org, platform)
	for i := 1; i < len(candidates); i++ {
		step, rank := classifyCandidate(candidates[i], boundSubjects, boundObjectIRIs, mapper, org, platform)
		if rank < bestRank {
			bestIdx = i
			bestStep = step
			bestRank = rank
		}
	}
	return bestIdx, bestStep
}

func classifyCandidate(
	cand bgpCandidate,
	boundSubjects map[string]bool,
	boundObjectIRIs map[string]bool,
	mapper *irimap.Mapper,
	org, platform string,
) (planStep, int) {
	if cand.isChain {
		return classifyStaticFoldedChain(cand.chain, boundSubjects, mapper)
	}
	return classifyStaticPattern(cand.triple, boundSubjects, boundObjectIRIs, mapper, org, platform)
}

func classifyStaticFoldedChain(ch FoldedChain, boundSubjects map[string]bool, mapper *irimap.Mapper) (planStep, int) {
	step := classifySingleFoldedChain(ch, mapper)
	if step.Kind == accessEmpty {
		return step, 0
	}
	rank := int(step.Kind) * 10
	if step.Kind > accessExactEntity {
		if key, ok := bindingKey(ch.Subject); ok && boundSubjects[key] {
			rank = int(accessExactEntity)*10 + 5
		}
	}
	return step, rank
}

func classifySingleFoldedChain(ch FoldedChain, mapper *irimap.Mapper) planStep {
	chainCopy := ch
	pred := ""
	if len(ch.Predicates) > 0 {
		pred = ch.Predicates[0]
	}
	step := planStep{
		Pattern: TriplePattern{
			Subject:   ch.Subject,
			Predicate: Term{Kind: TermIRI, Value: pred},
			Object:    ch.Object,
		},
		Folded:     &chainCopy,
		Predicates: slices.Clone(ch.Predicates),
	}
	if len(ch.Predicates) == 0 {
		step.Kind = accessEmpty
		return step
	}
	switch ch.Subject.Kind {
	case TermLiteral:
		step.Kind = accessEmpty
	case TermIRI:
		eid, err := mapper.IRIToEntityID(ch.Subject.Value)
		if err != nil {
			step.Kind = accessEmpty
			return step
		}
		step.Kind = accessExactEntity
		step.EntityID = eid
	case TermVariable, TermBlankNode:
		step.Kind = accessPredicate
	default:
		step.Kind = accessEmpty
	}
	return step
}

func classifyStaticPattern(
	tp TriplePattern,
	boundSubjects map[string]bool,
	boundObjectIRIs map[string]bool,
	mapper *irimap.Mapper,
	org, platform string,
) (planStep, int) {
	step := classifySinglePatternWithScope(tp, mapper, org, platform)
	if step.Kind == accessEmpty {
		return step, 0
	}
	// Standalone rdfs:subClassOf* evaluates over the finite in-memory vocabulary
	// ontology graph without touching GraphReader, so run it before reader scans.
	if tp.IsStandaloneSubClassPath() {
		return step, int(accessExactEntity)*10 - 5
	}
	rank := int(step.Kind) * 10
	// If the subject is a variable/blank node already bound by an earlier step,
	// it will be resolved via exact-entity lookup at execution time, so prefer it
	// ahead of unbound predicate or full scans.
	if step.Kind > accessExactEntity {
		if key, ok := bindingKey(tp.Subject); ok && boundSubjects[key] {
			rank = int(accessExactEntity)*10 + 5
		} else if step.Kind > accessTypePattern && (tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath()) {
			if objKey, ok := bindingKey(tp.Object); ok && boundObjectIRIs[objKey] {
				rank = int(accessTypePattern)*10 + 5
			}
		}
	}
	return step, rank
}

func classifySinglePatternWithScope(tp TriplePattern, mapper *irimap.Mapper, org, platform string) planStep {
	step := planStep{Pattern: tp}

	// Standalone `?sub rdfs:subClassOf* ?sup` (owner ruling D.1c) evaluates over
	// the finite registered ontology class graph in vocabulary.
	if tp.IsStandaloneSubClassPath() {
		if tp.Subject.Kind == TermLiteral || tp.Object.Kind == TermLiteral {
			step.Kind = accessEmpty
			return step
		}
		step.Kind = accessTypePattern
		return step
	}

	// Virtual and explicit `rdf:type` and `rdf:type/rdfs:subClassOf*` patterns.
	if tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath() {
		if tp.Subject.Kind == TermLiteral || tp.Object.Kind == TermLiteral {
			step.Kind = accessEmpty
			return step
		}
		step.Predicates = LookupPredicateCandidates(RDFTypeIRI)
		switch tp.Subject.Kind {
		case TermIRI:
			eid, err := mapper.IRIToEntityID(tp.Subject.Value)
			if err != nil {
				step.Kind = accessEmpty
				return step
			}
			step.Kind = accessExactEntity
			step.EntityID = eid
			return step
		case TermVariable, TermBlankNode:
			if tp.Object.Kind == TermIRI {
				patterns, err := TypeScanPatternsForClass(tp.Object.Value, org, platform)
				if err != nil {
					step.Kind = accessEmpty
					return step
				}
				preds := append([]string{RDFTypeIRI}, ProjectionPredicatesForNodeClass(tp.Object.Value)...)
				slices.Sort(preds)
				step.Predicates = slices.Compact(preds)
				if len(patterns) > 0 {
					// Per owner ruling on Issue #1 (#issuecomment-6224587243) and §2.2.3 #4,
					// bound registered-class `?s a <C>` / `?s rdf:type/rdfs:subClassOf* <C>`
					// intentionally uses selective `<org>.<platform>.*.<domain>.<type>.*`
					// pattern scans (plus any ProjectionPredicatesForNodeClass(C) for
					// intermediate blank-node classes) rather than a full-graph explicit
					// rdf:type predicate scan so non-matching entities never cross NATS.
					step.Kind = accessTypePattern
					step.Patterns = patterns
					return step
				}
				// Class IRI matches no registered EntityTypeMetadata; only explicit
				// stored rdf:type triples or intermediate projection node classes can match.
				step.Kind = accessPredicate
				return step
			}
			step.Kind = accessFullScan
			return step
		default:
			step.Kind = accessEmpty
			return step
		}
	}

	if tp.Path != PathNone {
		step.Kind = accessEmpty
		return step
	}

	// Validate predicate term first: a concrete predicate that cannot match anything
	// makes the entire pattern empty without touching GraphReader.
	switch tp.Predicate.Kind {
	case TermLiteral, TermBlankNode:
		step.Kind = accessEmpty
		return step
	case TermIRI:
		preds := LookupPredicateCandidates(tp.Predicate.Value)
		if len(preds) == 0 {
			step.Kind = accessEmpty
			return step
		}
		step.Predicates = preds
	}

	// Classify subject term.
	switch tp.Subject.Kind {
	case TermLiteral:
		step.Kind = accessEmpty
		return step
	case TermIRI:
		eid, err := mapper.IRIToEntityID(tp.Subject.Value)
		if err != nil {
			step.Kind = accessEmpty
			return step
		}
		step.Kind = accessExactEntity
		step.EntityID = eid
		return step
	case TermVariable, TermBlankNode:
		if len(step.Predicates) > 0 {
			step.Kind = accessPredicate
			return step
		}
		step.Kind = accessFullScan
		return step
	default:
		step.Kind = accessEmpty
		return step
	}
}

func bindingKey(t Term) (string, bool) {
	switch t.Kind {
	case TermVariable:
		return t.Value, true
	case TermBlankNode:
		return "\x00bnode:" + t.Value, true
	default:
		return "", false
	}
}

func isInternalBindingKey(k string) bool {
	return len(k) > 0 && k[0] == '\x00'
}

func recordStepVars(step planStep, boundVars map[string]bool) {
	if step.Folded != nil {
		if k, ok := bindingKey(step.Folded.Subject); ok {
			boundVars[k] = true
		}
		for _, inter := range step.Folded.IntermediateNodes {
			if k, ok := bindingKey(inter); ok {
				boundVars[k] = true
			}
		}
		if k, ok := bindingKey(step.Folded.Object); ok {
			boundVars[k] = true
		}
		return
	}
	recordPatternVars(step.Pattern, boundVars)
}

func recordPatternVars(tp TriplePattern, boundVars map[string]bool) {
	if k, ok := bindingKey(tp.Subject); ok {
		boundVars[k] = true
	}
	if k, ok := bindingKey(tp.Predicate); ok {
		boundVars[k] = true
	}
	if k, ok := bindingKey(tp.Object); ok {
		boundVars[k] = true
	}
}
