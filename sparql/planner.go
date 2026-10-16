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

// PlanStep describes the planned access strategy for a single TriplePattern in a BGP.
type PlanStep struct {
	Pattern    TriplePattern
	Kind       AccessKind
	EntityID   string
	Predicates []string
}

// BGPPlan is the ordered execution plan for a Basic Graph Pattern.
type BGPPlan struct {
	Steps []PlanStep
}

// LookupPredicateCandidates expands a standard predicate IRI into all matching
// dotted predicates registered in vocabulary.LookupByIRI. For rdf:type, it also
// includes the literal rdf:type IRI so explicit stored rdf:type triples match.
// An unknown ordinary predicate IRI returns nil.
func LookupPredicateCandidates(predicateIRI string) []string {
	if predicateIRI == "" {
		return nil
	}
	matches := slices.Clone(vocabulary.LookupByIRI(predicateIRI))
	if predicateIRI == RDFTypeIRI {
		if !slices.Contains(matches, RDFTypeIRI) {
			matches = append(matches, RDFTypeIRI)
			slices.Sort(matches)
		}
	}
	return matches
}

// PlanBGP orders the triple patterns of bgp using constrained-access priority
// (exact entity -> type pattern -> predicate selection -> bounded full scan),
// accounting for variables bound by earlier steps in the plan.
func PlanBGP(bgp BGPPattern, mapper *irimap.Mapper) BGPPlan {
	if mapper == nil {
		mapper = irimap.NewMapper("")
	}
	remaining := slices.Clone(bgp.Triples)
	steps := make([]PlanStep, 0, len(remaining))
	boundVars := make(map[string]bool)

	for len(remaining) > 0 {
		bestIdx := 0
		bestStep, bestRank := classifyStaticPattern(remaining[0], boundVars, mapper)
		for i := 1; i < len(remaining); i++ {
			step, rank := classifyStaticPattern(remaining[i], boundVars, mapper)
			if rank < bestRank {
				bestIdx = i
				bestStep = step
				bestRank = rank
			}
		}
		steps = append(steps, bestStep)
		recordPatternVars(bestStep.Pattern, boundVars)
		remaining = slices.Delete(remaining, bestIdx, bestIdx+1)
	}

	return BGPPlan{Steps: steps}
}

func classifyStaticPattern(tp TriplePattern, boundVars map[string]bool, mapper *irimap.Mapper) (PlanStep, int) {
	step := classifySinglePattern(tp, mapper)
	rank := int(step.Kind) * 10
	// If the subject is a variable/blank node already bound by an earlier step,
	// it will be resolved via exact-entity lookup at execution time, so prefer it
	// ahead of unbound predicate or full scans.
	if step.Kind > AccessExactEntity {
		if key, ok := bindingKey(tp.Subject); ok && boundVars[key] {
			rank = int(AccessExactEntity)*10 + 5
		}
	}
	return step, rank
}

func classifySinglePattern(tp TriplePattern, mapper *irimap.Mapper) PlanStep {
	step := PlanStep{Pattern: tp}

	// Property paths (Task 3.1) are not ordinary flat triples.
	if tp.Path != PathNone {
		step.Kind = AccessFullScan
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
		if len(step.Predicates) > 0 && tp.Predicate.Value != RDFTypeIRI {
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
