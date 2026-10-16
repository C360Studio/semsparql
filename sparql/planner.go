package sparql

import (
	"slices"

	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/irimap"
)

// AccessKind classifies the constrained GraphReader access strategy selected for
// a TriplePattern. Steps are ordered from most selective (lowest rank) to least
// selective (highest rank):
//
//	AccessEmpty -> AccessExactEntity -> AccessTypePattern -> AccessPredicate -> AccessFullScan
type AccessKind int

const (
	// AccessEmpty indicates the pattern cannot match any triples in the virtual graph
	// (for example, a literal subject, a non-entity IRI subject on an ordinary triple,
	// or an ordinary predicate IRI with no registered match in vocabulary.LookupByIRI).
	AccessEmpty AccessKind = iota
	// AccessExactEntity reads known canonical entity IDs via GraphReader.GetEntity or GetBatch.
	AccessExactEntity
	// AccessTypePattern scans a constrained six-part ADR-102 entity-type pattern via ScanPattern.
	AccessTypePattern
	// AccessPredicate scans candidate dotted predicates expanded from vocabulary.LookupByIRI via EntitiesByPredicate.
	AccessPredicate
	// AccessFullScan scans all entities under the query budget via ScanPattern(MatchAllPattern).
	AccessFullScan
)

// String returns a human-readable name for the access strategy.
func (k AccessKind) String() string {
	switch k {
	case AccessEmpty:
		return "AccessEmpty"
	case AccessExactEntity:
		return "AccessExactEntity"
	case AccessTypePattern:
		return "AccessTypePattern"
	case AccessPredicate:
		return "AccessPredicate"
	case AccessFullScan:
		return "AccessFullScan"
	default:
		return "AccessUnknown"
	}
}

// PlanStep describes the planned access strategy for a single TriplePattern or
// a collapsed FoldedChain in a BGP.
type PlanStep struct {
	Pattern    TriplePattern
	Folded     *FoldedChain
	Kind       AccessKind
	EntityID   string
	Predicates []string
	Patterns   []string
}

// BGPPlan is the ordered execution plan for a Basic Graph Pattern.
type BGPPlan struct {
	Steps []PlanStep
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

// PlanBGP folds complete ProjectionSpec chains and orders the resulting steps
// of bgp using constrained-access priority (exact entity -> type pattern ->
// predicate selection -> bounded full scan), accounting for variables bound by
// earlier steps in the plan.
func PlanBGP(bgp BGPPattern, mapper *irimap.Mapper) BGPPlan {
	return PlanBGPWithScope(bgp, mapper, "", "")
}

// PlanBGPWithScope folds complete ProjectionSpec chains and orders the resulting
// steps of bgp using constrained-access priority, scoping bound-class pattern
// scans to `<org>.<platform>.*.<domain>.<type>.*` (substituting `*` when org or
// platform is empty).
func PlanBGPWithScope(bgp BGPPattern, mapper *irimap.Mapper, org, platform string, disallowedVars ...map[string]bool) BGPPlan {
	if mapper == nil {
		mapper = irimap.NewMapper("")
	}
	folded := FoldProjections(bgp, disallowedVars...)

	type planCandidate struct {
		isChain bool
		chain   FoldedChain
		triple  TriplePattern
	}
	candidates := make([]planCandidate, 0, len(folded.Chains)+len(folded.RemainingTriples))
	for _, ch := range folded.Chains {
		candidates = append(candidates, planCandidate{isChain: true, chain: ch})
	}
	for _, tp := range folded.RemainingTriples {
		candidates = append(candidates, planCandidate{triple: tp})
	}

	steps := make([]PlanStep, 0, len(candidates))
	boundVars := make(map[string]bool)

	for len(candidates) > 0 {
		bestIdx := 0
		bestStep, bestRank := classifyCandidate(candidates[0].isChain, candidates[0].chain, candidates[0].triple, boundVars, mapper, org, platform)
		for i := 1; i < len(candidates); i++ {
			step, rank := classifyCandidate(candidates[i].isChain, candidates[i].chain, candidates[i].triple, boundVars, mapper, org, platform)
			if rank < bestRank {
				bestIdx = i
				bestStep = step
				bestRank = rank
			}
		}
		steps = append(steps, bestStep)
		recordStepVars(bestStep, boundVars)
		candidates = slices.Delete(candidates, bestIdx, bestIdx+1)
	}

	return BGPPlan{Steps: steps}
}

func classifyCandidate(
	isChain bool,
	ch FoldedChain,
	tp TriplePattern,
	boundVars map[string]bool,
	mapper *irimap.Mapper,
	org, platform string,
) (PlanStep, int) {
	if isChain {
		return classifyStaticFoldedChain(ch, boundVars, mapper)
	}
	return classifyStaticPattern(tp, boundVars, mapper, org, platform)
}

func classifyStaticFoldedChain(ch FoldedChain, boundVars map[string]bool, mapper *irimap.Mapper) (PlanStep, int) {
	step := classifySingleFoldedChain(ch, mapper)
	if step.Kind == AccessEmpty {
		return step, 0
	}
	rank := int(step.Kind) * 10
	if step.Kind > AccessExactEntity {
		if key, ok := bindingKey(ch.Subject); ok && boundVars[key] {
			rank = int(AccessExactEntity)*10 + 5
		}
	}
	return step, rank
}

func classifySingleFoldedChain(ch FoldedChain, mapper *irimap.Mapper) PlanStep {
	chainCopy := ch
	pred := ""
	if len(ch.Predicates) > 0 {
		pred = ch.Predicates[0]
	}
	step := PlanStep{
		Pattern: TriplePattern{
			Subject:   ch.Subject,
			Predicate: Term{Kind: TermIRI, Value: pred},
			Object:    ch.Object,
		},
		Folded:     &chainCopy,
		Predicates: slices.Clone(ch.Predicates),
	}
	if len(ch.Predicates) == 0 {
		step.Kind = AccessEmpty
		return step
	}
	switch ch.Subject.Kind {
	case TermLiteral:
		step.Kind = AccessEmpty
	case TermIRI:
		eid, err := mapper.IRIToEntityID(ch.Subject.Value)
		if err != nil {
			step.Kind = AccessEmpty
			return step
		}
		step.Kind = AccessExactEntity
		step.EntityID = eid
	case TermVariable, TermBlankNode:
		step.Kind = AccessPredicate
	default:
		step.Kind = AccessEmpty
	}
	return step
}

func classifyStaticPattern(tp TriplePattern, boundVars map[string]bool, mapper *irimap.Mapper, org, platform string) (PlanStep, int) {
	step := classifySinglePatternWithScope(tp, mapper, org, platform)
	if step.Kind == AccessEmpty {
		return step, 0
	}
	// Standalone rdfs:subClassOf* evaluates over the finite in-memory vocabulary
	// ontology graph without touching GraphReader, so run it before reader scans.
	if tp.IsStandaloneSubClassPath() {
		return step, int(AccessExactEntity)*10 - 5
	}
	rank := int(step.Kind) * 10
	// If the subject is a variable/blank node already bound by an earlier step,
	// it will be resolved via exact-entity lookup at execution time, so prefer it
	// ahead of unbound predicate or full scans.
	if step.Kind > AccessExactEntity {
		if key, ok := bindingKey(tp.Subject); ok && boundVars[key] {
			rank = int(AccessExactEntity)*10 + 5
		} else if step.Kind > AccessTypePattern && (tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath()) {
			if objKey, ok := bindingKey(tp.Object); ok && boundVars[objKey] {
				rank = int(AccessTypePattern)*10 + 5
			}
		}
	}
	return step, rank
}

func classifySinglePattern(tp TriplePattern, mapper *irimap.Mapper) PlanStep {
	return classifySinglePatternWithScope(tp, mapper, "", "")
}

func classifySinglePatternWithScope(tp TriplePattern, mapper *irimap.Mapper, org, platform string) PlanStep {
	step := PlanStep{Pattern: tp}

	// Standalone `?sub rdfs:subClassOf* ?sup` (owner ruling D.1c) evaluates over
	// the finite registered ontology class graph in vocabulary.
	if tp.IsStandaloneSubClassPath() {
		if tp.Subject.Kind == TermLiteral || tp.Object.Kind == TermLiteral {
			step.Kind = AccessEmpty
			return step
		}
		step.Kind = AccessTypePattern
		return step
	}

	// Virtual and explicit `rdf:type` and `rdf:type/rdfs:subClassOf*` patterns.
	if tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath() {
		if tp.Subject.Kind == TermLiteral || tp.Object.Kind == TermLiteral {
			step.Kind = AccessEmpty
			return step
		}
		step.Predicates = LookupPredicateCandidates(RDFTypeIRI)
		switch tp.Subject.Kind {
		case TermIRI:
			eid, err := mapper.IRIToEntityID(tp.Subject.Value)
			if err != nil {
				step.Kind = AccessEmpty
				return step
			}
			step.Kind = AccessExactEntity
			step.EntityID = eid
			return step
		case TermVariable, TermBlankNode:
			if tp.Object.Kind == TermIRI {
				patterns, err := TypeScanPatternsForClass(tp.Object.Value, org, platform)
				if err != nil {
					step.Kind = AccessEmpty
					return step
				}
				preds := append([]string{RDFTypeIRI}, ProjectionPredicatesForNodeClass(tp.Object.Value)...)
				slices.Sort(preds)
				step.Predicates = slices.Compact(preds)
				if len(patterns) > 0 {
					step.Kind = AccessTypePattern
					step.Patterns = patterns
					return step
				}
				// Class IRI matches no registered EntityTypeMetadata; only explicit
				// stored rdf:type triples or intermediate projection node classes can match.
				step.Kind = AccessPredicate
				return step
			}
			step.Kind = AccessFullScan
			return step
		default:
			step.Kind = AccessEmpty
			return step
		}
	}

	if tp.Path != PathNone {
		step.Kind = AccessEmpty
		return step
	}

	// Validate predicate term first: a concrete predicate that cannot match anything
	// makes the entire pattern empty without touching GraphReader.
	switch tp.Predicate.Kind {
	case TermLiteral, TermBlankNode:
		step.Kind = AccessEmpty
		return step
	case TermIRI:
		preds := LookupPredicateCandidates(tp.Predicate.Value)
		if len(preds) == 0 {
			step.Kind = AccessEmpty
			return step
		}
		step.Predicates = preds
	}

	// Classify subject term.
	switch tp.Subject.Kind {
	case TermLiteral:
		step.Kind = AccessEmpty
		return step
	case TermIRI:
		eid, err := mapper.IRIToEntityID(tp.Subject.Value)
		if err != nil {
			step.Kind = AccessEmpty
			return step
		}
		step.Kind = AccessExactEntity
		step.EntityID = eid
		return step
	case TermVariable, TermBlankNode:
		if len(step.Predicates) > 0 {
			step.Kind = AccessPredicate
			return step
		}
		step.Kind = AccessFullScan
		return step
	default:
		step.Kind = AccessEmpty
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

func recordStepVars(step PlanStep, boundVars map[string]bool) {
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
