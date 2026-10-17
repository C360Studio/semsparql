package sparql

import (
	"slices"

	"github.com/c360studio/semengine/vocabulary"
)

// FoldedChain represents a complete ProjectionSpec reification chain in a BGP
// that has been collapsed into a single planned lookup over its matching dotted
// predicates while preserving deterministic blank-node bindings for all
// intermediate variables.
type FoldedChain struct {
	// Subject is the root entity term of the chain (step 0 subject).
	Subject Term
	// Object is the terminal value term of the chain (final step ValuePredicateIRI object).
	Object Term
	// IntermediateNodes holds the variable or blank-node term for each step i in Spec.Steps.
	IntermediateNodes []Term
	// Spec is the matched ontology ProjectionSpec.
	Spec vocabulary.ProjectionSpec
	// Predicates holds all registered dotted predicates whose ProjectionSpec matches
	// this chain, sorted in lexicographical order.
	Predicates []string
	// SourcePatterns holds the original TriplePatterns collapsed into this FoldedChain.
	SourcePatterns []TriplePattern
}

// FoldedBGP holds the result of running FoldProjections over a BGPPattern:
// the collapsed complete chains and the remaining unfolded TriplePatterns.
type FoldedBGP struct {
	Chains           []FoldedChain
	RemainingTriples []TriplePattern
}

type registeredProjection struct {
	name string
	spec vocabulary.ProjectionSpec
}

// listRegisteredProjections returns all registered dotted predicates that carry
// a non-empty ProjectionSpec, sorted by predicate name, along with the set of
// StandardIRIs belonging to unprojected predicates.
func listRegisteredProjections() ([]registeredProjection, map[string]bool) {
	names := vocabulary.ListRegisteredPredicates()
	slices.Sort(names)

	var projs []registeredProjection
	unprojectedIRIs := make(map[string]bool)
	for _, name := range names {
		meta := vocabulary.GetPredicateMetadata(name)
		if meta == nil {
			continue
		}
		if meta.Projection != nil && len(meta.Projection.Steps) > 0 {
			projs = append(projs, registeredProjection{
				name: name,
				spec: *meta.Projection,
			})
			continue
		}
		if meta.StandardIRI != "" {
			unprojectedIRIs[meta.StandardIRI] = true
		}
	}
	return projs, unprojectedIRIs
}

// ProjectionPredicatesForStepIRI returns all registered dotted predicates whose
// ProjectionSpec uses iri as a step PredicateIRI or terminal ValuePredicateIRI,
// sorted and deduplicated.
func ProjectionPredicatesForStepIRI(iri string) []string {
	if iri == "" {
		return nil
	}
	projs, _ := listRegisteredProjections()
	var matches []string
	for _, p := range projs {
		for _, step := range p.spec.Steps {
			if step.PredicateIRI == iri || step.ValuePredicateIRI == iri {
				matches = append(matches, p.name)
				break
			}
		}
	}
	if len(matches) == 0 {
		return nil
	}
	slices.Sort(matches)
	return slices.Compact(matches)
}

// ProjectionPredicatesForNodeClass returns all registered dotted predicates
// whose ProjectionSpec declares a step NodeClassIRI matching classIRI (either
// directly or via registered superclass closure), sorted and deduplicated.
func ProjectionPredicatesForNodeClass(classIRI string) []string {
	if classIRI == "" {
		return nil
	}
	projs, _ := listRegisteredProjections()
	var matches []string
	for _, p := range projs {
		for _, step := range p.spec.Steps {
			if step.NodeClassIRI != "" && classMatchesNodeClass(classIRI, step.NodeClassIRI) {
				matches = append(matches, p.name)
				break
			}
		}
	}
	if len(matches) == 0 {
		return nil
	}
	slices.Sort(matches)
	return slices.Compact(matches)
}

// AllProjectedPredicates returns all registered dotted predicates that carry a
// non-empty ProjectionSpec, sorted in lexicographical order.
func AllProjectedPredicates() []string {
	projs, _ := listRegisteredProjections()
	if len(projs) == 0 {
		return nil
	}
	out := make([]string, len(projs))
	for i, p := range projs {
		out[i] = p.name
	}
	return out
}

func classMatchesNodeClass(queriedClassIRI, nodeClassIRI string) bool {
	if queriedClassIRI == "" || nodeClassIRI == "" {
		return false
	}
	if queriedClassIRI == nodeClassIRI {
		return true
	}
	return slices.Contains(SuperclassesForClassIRI(nodeClassIRI), queriedClassIRI)
}

// FoldProjections inspects bgp and collapses complete ProjectionSpec chains into
// FoldedChain entries when:
//  1. Every step's PredicateIRI, declared NodeClassIRI type pattern, and terminal
//     ValuePredicateIRI pattern are present in bgp.
//  2. Every intermediate node is a variable or blank node used only as a join key
//     within the chain (no extra triple patterns in bgp and no presence in any
//     optional disallowedVars set from enclosing FILTERs or sibling patterns).
//  3. No registered ProjectionSpec or unprojected predicate can partially match
//     the chain in the virtual unfolded graph without being a complete match.
//
// Partial chains, chains with missing NodeClassIRI constraints, chains with extra
// constraints or FILTERs on intermediate nodes, and variable-predicate patterns
// remain in FoldedBGP.RemainingTriples for evaluation over the virtual unfolded graph.
func FoldProjections(bgp BGPPattern, disallowedVars ...map[string]bool) FoldedBGP {
	if len(bgp.Triples) == 0 {
		return FoldedBGP{}
	}
	projs, unprojectedIRIs := listRegisteredProjections()
	if len(projs) == 0 {
		return FoldedBGP{RemainingTriples: slices.Clone(bgp.Triples)}
	}

	disallowed := make(map[string]bool)
	for _, m := range disallowedVars {
		for k, v := range m {
			if v {
				disallowed[k] = true
			}
		}
	}

	maxSteps := 0
	step0Preds := make(map[string]bool, len(projs))
	for _, p := range projs {
		if len(p.spec.Steps) > maxSteps {
			maxSteps = len(p.spec.Steps)
		}
		step0Preds[p.spec.Steps[0].PredicateIRI] = true
	}

	consumed := make([]bool, len(bgp.Triples))
	var chains []FoldedChain

	for idx0 := 0; idx0 < len(bgp.Triples); idx0++ {
		if consumed[idx0] {
			continue
		}
		tp0 := bgp.Triples[idx0]
		if tp0.Path != PathNone || tp0.Predicate.Kind != TermIRI || !step0Preds[tp0.Predicate.Value] {
			continue
		}
		if tp0.Subject.Kind == TermLiteral {
			continue
		}

		chain, indices, ok := tryTraceFoldedChain(bgp.Triples, idx0, consumed, disallowed, maxSteps, projs, unprojectedIRIs)
		if !ok {
			continue
		}
		for _, idx := range indices {
			consumed[idx] = true
		}
		chains = append(chains, chain)
	}

	remaining := make([]TriplePattern, 0, len(bgp.Triples))
	for i, tp := range bgp.Triples {
		if !consumed[i] {
			remaining = append(remaining, tp)
		}
	}
	return FoldedBGP{
		Chains:           chains,
		RemainingTriples: remaining,
	}
}

func tryTraceFoldedChain(
	triples []TriplePattern,
	idx0 int,
	consumed []bool,
	disallowed map[string]bool,
	maxSteps int,
	projs []registeredProjection,
	unprojectedIRIs map[string]bool,
) (FoldedChain, []int, bool) {
	tp0 := triples[idx0]
	rootKey, rootIsVar := bindingKey(tp0.Subject)

	interTerms := make([]Term, 0, maxSteps)
	interKeys := make([]string, 0, maxSteps)
	linkPreds := make([]string, 0, maxSteps)
	typeClassIRIs := make([]string, 0, maxSteps)
	stepIndices := make([]int, 0, maxSteps*2+1)

	curLinkIdx := idx0
	for stepIdx := 0; stepIdx < maxSteps; stepIdx++ {
		curLink := triples[curLinkIdx]
		nodeTerm := curLink.Object
		nodeKey, ok := bindingKey(nodeTerm)
		if !ok || disallowed[nodeKey] {
			return FoldedChain{}, nil, false
		}
		if rootIsVar && nodeKey == rootKey {
			return FoldedChain{}, nil, false
		}
		if slices.Contains(interKeys, nodeKey) {
			return FoldedChain{}, nil, false
		}

		// Inspect every triple in the BGP that references nodeKey.
		typeIdx := -1
		var typeClassIRI string
		outIdx := -1
		validNode := true

		for j, tp := range triples {
			subjMatch := hasBindingKey(tp.Subject, nodeKey)
			predMatch := hasBindingKey(tp.Predicate, nodeKey)
			objMatch := hasBindingKey(tp.Object, nodeKey)
			if !subjMatch && !predMatch && !objMatch {
				continue
			}
			if consumed[j] || predMatch {
				validNode = false
				break
			}
			if j == curLinkIdx {
				// curLink has nodeKey as Object; it must not also have nodeKey as Subject.
				if subjMatch {
					validNode = false
					break
				}
				continue
			}
			// No other triple may have nodeKey as Object.
			if objMatch || !subjMatch {
				validNode = false
				break
			}
			// Classify outgoing triple from nodeKey.
			if (tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath()) && tp.Object.Kind == TermIRI {
				if typeIdx != -1 {
					// Multiple rdf:type patterns on the same intermediate node: extra constraint.
					validNode = false
					break
				}
				typeIdx = j
				typeClassIRI = tp.Object.Value
				continue
			}
			if outIdx != -1 {
				// Multiple non-type outgoing triples on the same intermediate node: extra constraint.
				validNode = false
				break
			}
			outIdx = j
		}

		if !validNode || outIdx == -1 {
			return FoldedChain{}, nil, false
		}
		outTriple := triples[outIdx]
		if outTriple.Path != PathNone || outTriple.Predicate.Kind != TermIRI || outTriple.Predicate.Value == RDFTypeIRI {
			return FoldedChain{}, nil, false
		}

		interTerms = append(interTerms, nodeTerm)
		interKeys = append(interKeys, nodeKey)
		linkPreds = append(linkPreds, curLink.Predicate.Value)
		typeClassIRIs = append(typeClassIRIs, typeClassIRI)
		stepIndices = append(stepIndices, curLinkIdx)
		if typeIdx != -1 {
			stepIndices = append(stepIndices, typeIdx)
		}

		// Check if terminating at stepIdx with outTriple as ValuePredicateIRI forms a complete chain.
		if valKey, ok := bindingKey(outTriple.Object); ok && slices.Contains(interKeys, valKey) {
			return FoldedChain{}, nil, false
		}

		preds, spec, matched := matchCompleteProjectionSpecs(linkPreds, typeClassIRIs, outTriple.Predicate.Value, projs, unprojectedIRIs)
		if matched {
			allIndices := append(slices.Clone(stepIndices), outIdx)
			sourcePatterns := make([]TriplePattern, len(allIndices))
			for i, idx := range allIndices {
				sourcePatterns[i] = triples[idx]
			}
			return FoldedChain{
				Subject:           tp0.Subject,
				Object:            outTriple.Object,
				IntermediateNodes: slices.Clone(interTerms),
				Spec:              spec,
				Predicates:        preds,
				SourcePatterns:    sourcePatterns,
			}, allIndices, true
		}

		// Otherwise continue tracing to stepIdx + 1 along outIdx.
		curLinkIdx = outIdx
	}

	return FoldedChain{}, nil, false
}

func hasBindingKey(t Term, wantKey string) bool {
	k, ok := bindingKey(t)
	return ok && k == wantKey
}

func matchCompleteProjectionSpecs(
	linkPreds []string,
	typeClassIRIs []string,
	valPredIRI string,
	projs []registeredProjection,
	unprojectedIRIs map[string]bool,
) ([]string, vocabulary.ProjectionSpec, bool) {
	k := len(linkPreds)
	if k == 0 || valPredIRI == "" {
		return nil, vocabulary.ProjectionSpec{}, false
	}

	// If any predicate in the chain is also an unprojected flat predicate's StandardIRI,
	// do not fold so flat triples with that predicate are not missed.
	for _, lp := range linkPreds {
		if unprojectedIRIs[lp] {
			return nil, vocabulary.ProjectionSpec{}, false
		}
	}
	if unprojectedIRIs[valPredIRI] {
		return nil, vocabulary.ProjectionSpec{}, false
	}

	// Build the query's non-type predicate sequence Q of length k + 1.
	qPreds := make([]string, k+1)
	copy(qPreds, linkPreds)
	qPreds[k] = valPredIRI

	var matchedPreds []string
	var repSpec vocabulary.ProjectionSpec

	for _, p := range projs {
		m := len(p.spec.Steps)
		isComplete := false
		if m == k && isCompleteSpecMatch(p.spec, linkPreds, typeClassIRIs, valPredIRI) {
			isComplete = true
			if len(matchedPreds) == 0 {
				repSpec = p.spec
			}
			matchedPreds = append(matchedPreds, p.name)
		}

		// Guard against partial/sub-chain overlap: if p.spec can match (qPreds, typeClassIRIs)
		// at any offset in the unfolded graph without being a complete match, folding is unsafe.
		if !isComplete && canSubChainMatchInUnfoldedGraph(p.spec, qPreds, typeClassIRIs) {
			return nil, vocabulary.ProjectionSpec{}, false
		}
	}

	if len(matchedPreds) == 0 {
		return nil, vocabulary.ProjectionSpec{}, false
	}
	slices.Sort(matchedPreds)
	matchedPreds = slices.Compact(matchedPreds)
	return matchedPreds, repSpec, true
}

func isCompleteSpecMatch(
	spec vocabulary.ProjectionSpec,
	linkPreds []string,
	typeClassIRIs []string,
	valPredIRI string,
) bool {
	if len(spec.Steps) != len(linkPreds) {
		return false
	}
	last := len(spec.Steps) - 1
	if spec.Steps[last].ValuePredicateIRI != valPredIRI {
		return false
	}
	for i, step := range spec.Steps {
		if step.PredicateIRI != linkPreds[i] {
			return false
		}
		if step.NodeClassIRI != "" {
			if typeClassIRIs[i] == "" || !classMatchesNodeClass(typeClassIRIs[i], step.NodeClassIRI) {
				return false
			}
		} else if typeClassIRIs[i] != "" {
			return false
		}
	}
	return true
}

func canSubChainMatchInUnfoldedGraph(
	spec vocabulary.ProjectionSpec,
	qPreds []string,
	typeClassIRIs []string,
) bool {
	m := len(spec.Steps)
	k := len(typeClassIRIs)
	if m < k || len(qPreds) != k+1 {
		return false
	}
	// Construct the unfolded non-type predicate sequence W(spec) of length m + 1.
	wPreds := make([]string, m+1)
	for i, step := range spec.Steps {
		wPreds[i] = step.PredicateIRI
	}
	wPreds[m] = spec.Steps[m-1].ValuePredicateIRI

	for off := 0; off <= m-k; off++ {
		predMatch := true
		for j := 0; j <= k; j++ {
			if wPreds[off+j] != qPreds[j] {
				predMatch = false
				break
			}
		}
		if !predMatch {
			continue
		}
		typeMatch := true
		for j := 0; j < k; j++ {
			if typeClassIRIs[j] != "" {
				stepClass := spec.Steps[off+j].NodeClassIRI
				if stepClass == "" || !classMatchesNodeClass(typeClassIRIs[j], stepClass) {
					typeMatch = false
					break
				}
			}
		}
		if typeMatch {
			return true
		}
	}
	return false
}
