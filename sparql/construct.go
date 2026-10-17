package sparql

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semsparql/irimap"
)

// ErrorCodeInvalidOutputTriple is the ADR-060 classified error code returned
// when an output RDF triple has an empty subject/predicate, nil object, or
// NaN/Inf float value that semengine/vocabulary/export would silently omit.
const ErrorCodeInvalidOutputTriple = "invalid_output_triple"

// ErrInvalidOutputTriple is the classified ADR-060 sentinel matching any
// invalid output triple detected before or during RDF graph serialization via
// errors.Is(err, ErrInvalidOutputTriple).
var ErrInvalidOutputTriple error = &errs.ClassifiedError{
	Class:   errs.ErrorInvalid,
	Code:    ErrorCodeInvalidOutputTriple,
	Message: "sparql: invalid output triple omitted by exporter",
}

func newInvalidOutputTripleError(detail string) error {
	return errs.ClassifiedCode(
		errs.ErrorInvalid,
		ErrorCodeInvalidOutputTriple,
		fmt.Errorf("sparql: invalid output triple: %s", detail),
	)
}

// ExportOptions returns the semengine/vocabulary/export Option slice for
// serializing unfolded or constructed graphs under baseIRI. Canonical 6-part
// entity IDs delegate to export.SubjectIRI(subject, baseIRI), blank nodes
// (_:label) are preserved by the exporter, and already-expanded http(s)/urn
// subject IRIs from arbitrary CONSTRUCT templates pass through unchanged.
func ExportOptions(baseIRI string) []export.Option {
	if strings.TrimSpace(baseIRI) == "" {
		baseIRI = vocabulary.SemEngineBase
	}
	return []export.Option{
		export.WithBaseIRI(baseIRI),
		export.WithSubjectIRIFunc(func(subject string) string {
			if isAbsoluteSubjectIRI(subject) {
				return subject
			}
			return export.SubjectIRI(subject, baseIRI)
		}),
	}
}

func isAbsoluteSubjectIRI(s string) bool {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") && !strings.HasPrefix(s, "urn:") {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || strings.ContainsRune("<>\"{}|^`\\", r) {
			return false
		}
	}
	return true
}

// validateTripleSerializable verifies that tr is not silently omitted by
// semengine/vocabulary/export (D.2 TestProbeSerializerInvalidTripleOmissionSeam)
// and does not emit a relative/pragmatic datatype IRI (such as ^^<float>).
func validateTripleSerializable(tr message.Triple, baseIRI ...string) error {
	if strings.TrimSpace(tr.Subject) == "" {
		return newInvalidOutputTripleError("triple has empty subject")
	}
	if strings.TrimSpace(tr.Predicate) == "" {
		return newInvalidOutputTripleError("triple has empty predicate")
	}
	if tr.Object == nil {
		return newInvalidOutputTripleError(fmt.Sprintf("triple (%q, %q) has nil object", tr.Subject, tr.Predicate))
	}
	base := resolveOptionalBaseIRI(baseIRI)
	out, err := export.SerializeToString([]message.Triple{tr}, export.NTriples, ExportOptions(base)...)
	if err != nil {
		return newInvalidOutputTripleError(err.Error())
	}
	if strings.TrimSpace(out) == "" {
		return newInvalidOutputTripleError(fmt.Sprintf(
			"exporter silently omitted triple with subject %q predicate %q object %#v",
			tr.Subject, tr.Predicate, tr.Object,
		))
	}
	if idx := strings.LastIndex(out, `"^^<`); idx >= 0 && strings.HasSuffix(out, "> .\n") {
		dtIRI := out[idx+4 : len(out)-4]
		if !strings.Contains(dtIRI, ":") {
			return newInvalidOutputTripleError(fmt.Sprintf("invalid non-IRI datatype %q in N-Triples output %q", dtIRI, out))
		}
	}
	return nil
}

// ValidateGraphTriples validates that every triple in triples renders to a
// non-empty, well-formed N-Triples statement via semengine/vocabulary/export.
func ValidateGraphTriples(triples []message.Triple, baseIRI ...string) error {
	base := resolveOptionalBaseIRI(baseIRI)
	for i := range triples {
		if err := validateTripleSerializable(triples[i], base); err != nil {
			return err
		}
	}
	return nil
}

func resolveOptionalBaseIRI(baseIRI []string) string {
	if len(baseIRI) > 0 && strings.TrimSpace(baseIRI[0]) != "" {
		return baseIRI[0]
	}
	return vocabulary.SemEngineBase
}

// UnfoldEntity expands an EntityState into its complete RDF graph representation
// for CONSTRUCT, DESCRIBE, GET /entities/{id}.jsonld, and POST /export:
//  1. Virtual rdf:type triples (ClassIRI and registered Superclasses) for entity.ID.
//  2. Stored triples whose predicate declares a ProjectionSpec unfolded into
//     deterministic blank-node chains (irimap.BlankNodeID) with the source
//     predicate's RDF datatype or entity-reference resource semantics preserved
//     on the terminal ValuePredicateIRI triple.
//  3. Remaining flat triples (plus registered superclasses for explicit rdf:type
//     triples), deduplicated as an RDF graph set.
//
// Any stored triple that would be silently omitted by export.Serialize returns
// a classified ErrInvalidOutputTriple error.
func UnfoldEntity(entity *graph.EntityState, baseIRI ...string) ([]message.Triple, error) {
	if entity == nil {
		return nil, newInvalidOutputTripleError("nil entity")
	}
	base := resolveOptionalBaseIRI(baseIRI)
	mapper := irimap.NewMapper(base)
	if _, err := mapper.EntityIDToIRI(entity.ID); err != nil {
		return nil, fmt.Errorf("sparql: invalid entity ID %q in UnfoldEntity: %w", entity.ID, err)
	}

	collector := newGraphTripleCollector(mapper, 0, false)

	// 1. Virtual rdf:type triples (ClassIRI + registered Superclasses).
	for _, clsIRI := range VirtualTypeIRIsForEntityID(entity.ID) {
		if _, err := collector.add(message.Triple{
			Subject:   entity.ID,
			Predicate: RDFTypeIRI,
			Object:    clsIRI,
		}); err != nil {
			return nil, err
		}
	}

	// 2. Stored triples (unfolding ProjectionSpec chains and validating serializability).
	for _, tr := range entity.Triples {
		if err := unfoldAndCollectStoredTriple(collector, entity.ID, tr); err != nil {
			return nil, err
		}
	}

	return collector.triples, nil
}

func unfoldAndCollectStoredTriple(collector *graphTripleCollector, defaultEntityID string, tr message.Triple) error {
	if strings.TrimSpace(tr.Subject) == "" {
		return newInvalidOutputTripleError("triple has empty subject")
	}
	if strings.TrimSpace(tr.Predicate) == "" {
		return newInvalidOutputTripleError("triple has empty predicate")
	}
	if tr.Object == nil {
		return newInvalidOutputTripleError(fmt.Sprintf("triple (%q, %q) has nil object", tr.Subject, tr.Predicate))
	}

	base := collector.mapper.BaseIRI()
	normTr := normalizeSourceTripleDatatype(tr)
	predTerm, objTerm, canonObj, ok, err := resolveTripleTerms(normTr, base)
	if err != nil {
		return err
	}
	if !ok {
		return newInvalidOutputTripleError(fmt.Sprintf(
			"exporter silently omitted triple with subject %q predicate %q object %#v",
			tr.Subject, tr.Predicate, tr.Object,
		))
	}

	subj := tr.Subject
	subjEntityID := defaultEntityID
	if !strings.HasPrefix(subj, "_:") {
		if id, idErr := collector.mapper.IRIToEntityID(subj); idErr == nil {
			subj = id
			subjEntityID = id
		} else if subj != defaultEntityID {
			subjEntityID = subj
		}
	}

	meta := vocabulary.GetPredicateMetadata(tr.Predicate)
	if meta != nil && meta.Projection != nil && len(meta.Projection.Steps) > 0 && !strings.HasPrefix(subj, "_:") {
		return emitProjectedChainTriples(collector, subj, subjEntityID, tr.Predicate, normTr, canonObj, meta.Projection)
	}

	outTr := normTr
	outTr.Subject = subj
	if strings.HasPrefix(outTr.Datatype, "@") && outTr.Datatype != message.EntityReferenceDatatype {
		if s, ok := outTr.Object.(string); ok && requiresExplicitStringDatatype(s) {
			outTr.Datatype = XSDStringIRI
		} else {
			outTr.Datatype = ""
		}
	}
	if _, err := collector.add(outTr); err != nil {
		return err
	}
	if predTerm.Kind == TermIRI && predTerm.Value == RDFTypeIRI && objTerm.Kind == TermIRI {
		for _, supIRI := range SuperclassesForClassIRI(objTerm.Value) {
			if _, err := collector.add(message.Triple{
				Subject:   subj,
				Predicate: RDFTypeIRI,
				Object:    supIRI,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func emitProjectedChainTriples(
	collector *graphTripleCollector,
	subj string,
	subjEntityID string,
	dottedPred string,
	normSrc message.Triple,
	canonObj string,
	proj *vocabulary.ProjectionSpec,
) error {
	curSubj := subj
	steps := proj.Steps
	for stepIdx, step := range steps {
		bnodeID, err := irimap.BlankNodeID(subjEntityID, dottedPred, canonObj, stepIdx)
		if err != nil {
			return err
		}
		if stop, err := collector.add(message.Triple{
			Subject:   curSubj,
			Predicate: step.PredicateIRI,
			Object:    bnodeID,
		}); err != nil || stop {
			return err
		}
		if step.NodeClassIRI != "" {
			if stop, err := collector.add(message.Triple{
				Subject:   bnodeID,
				Predicate: RDFTypeIRI,
				Object:    step.NodeClassIRI,
			}); err != nil || stop {
				return err
			}
			for _, supIRI := range SuperclassesForClassIRI(step.NodeClassIRI) {
				if stop, err := collector.add(message.Triple{
					Subject:   bnodeID,
					Predicate: RDFTypeIRI,
					Object:    supIRI,
				}); err != nil || stop {
					return err
				}
			}
		}
		if stepIdx == len(steps)-1 {
			termTriple, err := propagateTerminalTriple(normSrc, canonObj, bnodeID, step.ValuePredicateIRI)
			if err != nil {
				return err
			}
			if stop, err := collector.add(termTriple); err != nil || stop {
				return err
			}
		}
		curSubj = bnodeID
	}
	return nil
}

// propagateTerminalTriple maps a normalized source triple and its exporter-owned
// canonical N-Triples object token onto the terminal step of an unfolded
// ProjectionSpec chain (whose Predicate is a full ValuePredicateIRI).
func propagateTerminalTriple(normSrc message.Triple, canonObj, terminalSubj, valuePredicateIRI string) (message.Triple, error) {
	term := message.Triple{
		Subject:   terminalSubj,
		Predicate: valuePredicateIRI,
	}
	switch {
	case strings.HasPrefix(canonObj, "<") && strings.HasSuffix(canonObj, ">"):
		if s, ok := normSrc.Object.(string); ok && message.IsValidEntityID(s) {
			term.Object = s
			term.Datatype = message.EntityReferenceDatatype
		} else {
			term.Object = canonObj[1 : len(canonObj)-1]
		}
	case strings.HasPrefix(canonObj, "_:"):
		term.Object = canonObj
	case strings.HasPrefix(canonObj, `"`):
		objTerm, err := parseNTriplesObjectTerm(canonObj)
		if err != nil {
			return message.Triple{}, err
		}
		term.Object, term.Datatype = literalTermToMessageObject(objTerm, normSrc.Datatype != "")
	default:
		return message.Triple{}, newInvalidOutputTripleError(fmt.Sprintf("unrecognized N-Triples object token %q", canonObj))
	}
	return term, nil
}

func literalTermToMessageObject(lit Term, forceExplicitDatatype bool) (any, string) {
	if lit.Language != "" {
		if requiresExplicitStringDatatype(lit.Value) {
			return lit.Value, XSDStringIRI
		}
		return lit.Value, ""
	}
	dtIRI := lit.Datatype
	if !forceExplicitDatatype {
		switch dtIRI {
		case XSDIntegerIRI:
			if n, err := strconv.ParseInt(lit.Value, 10, 64); err == nil && strconv.FormatInt(n, 10) == lit.Value {
				return n, ""
			}
		case XSDDoubleIRI:
			if f, err := strconv.ParseFloat(lit.Value, 64); err == nil {
				return f, ""
			}
		case XSDBooleanIRI:
			switch lit.Value {
			case "true":
				return true, ""
			case "false":
				return false, ""
			}
		case "", XSDStringIRI:
			if requiresExplicitStringDatatype(lit.Value) {
				return lit.Value, XSDStringIRI
			}
			return lit.Value, ""
		}
	}
	if dtIRI == "" || dtIRI == XSDStringIRI {
		if requiresExplicitStringDatatype(lit.Value) {
			return lit.Value, XSDStringIRI
		}
		return lit.Value, ""
	}
	return lit.Value, dtIRI
}

func requiresExplicitStringDatatype(val string) bool {
	return strings.HasPrefix(val, "_:") ||
		strings.HasPrefix(val, "http://") ||
		strings.HasPrefix(val, "https://") ||
		strings.HasPrefix(val, "urn:") ||
		message.IsValidEntityID(val)
}

type graphTripleKey struct {
	subj     string
	pred     string
	canonObj string
}

type graphTripleCollector struct {
	mapper          *irimap.Mapper
	maxResults      int
	allowTruncation bool
	truncated       bool
	seen            map[graphTripleKey]struct{}
	triples         []message.Triple
}

func newGraphTripleCollector(mapper *irimap.Mapper, maxResults int, allowTruncation bool) *graphTripleCollector {
	if mapper == nil {
		mapper = irimap.NewMapper("")
	}
	return &graphTripleCollector{
		mapper:          mapper,
		maxResults:      maxResults,
		allowTruncation: allowTruncation,
		seen:            make(map[graphTripleKey]struct{}),
		triples:         make([]message.Triple, 0),
	}
}

func (c *graphTripleCollector) add(tr message.Triple) (stopped bool, err error) {
	base := c.mapper.BaseIRI()
	if err := validateTripleSerializable(tr, base); err != nil {
		return false, err
	}
	predTerm, _, canonObj, ok, err := resolveTripleTerms(tr, base)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, newInvalidOutputTripleError(fmt.Sprintf(
			"exporter silently omitted triple (%q, %q, %#v)",
			tr.Subject, tr.Predicate, tr.Object,
		))
	}

	subjKey := tr.Subject
	if !strings.HasPrefix(subjKey, "_:") && !isAbsoluteSubjectIRI(subjKey) {
		subjKey = export.SubjectIRI(subjKey, base)
	}
	key := graphTripleKey{
		subj:     subjKey,
		pred:     predTerm.Value,
		canonObj: canonObj,
	}
	if _, exists := c.seen[key]; exists {
		return false, nil
	}

	if c.maxResults > 0 && len(c.triples) >= c.maxResults {
		if c.allowTruncation {
			c.truncated = true
			return true, nil
		}
		return false, newResultLimitError(c.maxResults)
	}
	c.seen[key] = struct{}{}
	c.triples = append(c.triples, tr)
	return false, nil
}

func (s *execState) execConstruct(ctx context.Context, q *Query) (*Result, error) {
	if s.maxResults <= 0 {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonResultLimit)
			return &Result{
				Form:             QueryConstruct,
				Triples:          []message.Triple{},
				EntitiesScanned:  s.entitiesScanned,
				Truncated:        true,
				TruncationReason: s.truncationReason,
			}, nil
		}
		return nil, newResultLimitError(s.maxResults)
	}

	// Enforce MaxResults on deduplicated output triples rather than intermediate WHERE rows.
	s.enforceSolutionLimit = false
	solutions, err := s.evalGroup(ctx, q.Where, []Solution{{}})
	if err != nil {
		return nil, err
	}
	if err := s.checkContext(ctx); err != nil {
		return nil, err
	}

	vars := selectVariables(q)
	sortSolutions(solutions, q.OrderBy, vars)
	if q.Distinct {
		if err := s.checkContext(ctx); err != nil {
			return nil, err
		}
		solutions = deduplicateSolutions(solutions, vars)
	}
	if q.Offset > 0 {
		if q.Offset >= len(solutions) {
			solutions = nil
		} else {
			solutions = solutions[q.Offset:]
		}
	}
	if q.Limit != nil {
		if *q.Limit <= 0 {
			solutions = nil
		} else if *q.Limit < len(solutions) {
			solutions = solutions[:*q.Limit]
		}
	}

	collector := newGraphTripleCollector(s.mapper, s.maxResults, s.allowTruncation)
	for rowIdx, sol := range solutions {
		if err := s.checkContext(ctx); err != nil {
			return nil, err
		}
		stopped, err := s.instantiateConstructRow(collector, q.Construct, sol, rowIdx)
		if err != nil {
			return nil, err
		}
		if stopped {
			break
		}
	}
	if err := s.checkContext(ctx); err != nil {
		return nil, err
	}
	if collector.truncated {
		s.markTruncated(TruncationReasonResultLimit)
	}

	return &Result{
		Form:             QueryConstruct,
		Triples:          collector.triples,
		EntitiesScanned:  s.entitiesScanned,
		Truncated:        s.truncated,
		TruncationReason: s.truncationReason,
	}, nil
}

func (s *execState) instantiateConstructRow(
	collector *graphTripleCollector,
	template []TriplePattern,
	sol Solution,
	rowIdx int,
) (bool, error) {
	base := s.mapper.BaseIRI()
	for _, tp := range template {
		subjTerm, ok := instantiateConstructTerm(tp.Subject, sol, rowIdx)
		if !ok || (subjTerm.Kind != TermIRI && subjTerm.Kind != TermBlankNode) {
			continue
		}
		predTerm, ok := instantiateConstructTerm(tp.Predicate, sol, rowIdx)
		if !ok || predTerm.Kind != TermIRI {
			continue
		}
		objTerm, ok := instantiateConstructTerm(tp.Object, sol, rowIdx)
		if !ok {
			continue
		}

		subj, subjEntityID := s.termToTripleSubject(subjTerm)

		// If the CONSTRUCT template explicitly uses the StandardIRI of a projected
		// predicate (e.g. diekm:altitudeMeters or diekm:assignedRole) on an entity
		// subject, unfold it into its ProjectionSpec blank-node chain and emit the
		// entity's virtual rdf:type triples so the constructed graph is SHACL-ready.
		if subjEntityID != "" {
			if projPred, projMeta := lookupSingleProjectedPredicateByStandardIRI(predTerm.Value); projMeta != nil {
				for _, clsIRI := range VirtualTypeIRIsForEntityID(subjEntityID) {
					stop, err := collector.add(message.Triple{
						Subject:   subj,
						Predicate: RDFTypeIRI,
						Object:    clsIRI,
					})
					if err != nil || stop {
						return stop, err
					}
				}
				srcTr := rdfTermToSourceTriple(subj, projPred, objTerm, s.mapper)
				normSrc := normalizeSourceTripleDatatype(srcTr)
				_, _, canonObj, ok, err := resolveTripleTerms(normSrc, base)
				if err != nil {
					return false, err
				}
				if ok {
					if err := emitProjectedChainTriples(collector, subj, subjEntityID, projPred, normSrc, canonObj, projMeta.Projection); err != nil {
						return false, err
					}
					if collector.truncated {
						return true, nil
					}
					continue
				}
			}
		}

		tr := rdfTripleToMessageTriple(subj, predTerm.Value, objTerm, s.mapper)
		stop, err := collector.add(tr)
		if err != nil || stop {
			return stop, err
		}

		// When a wildcard predicate pattern in CONSTRUCT (e.g. CONSTRUCT { ?s ?p ?o })
		// emits a link to an unfolded reification blank node (_:b_...), include the
		// unfolded blank-node chain triples rooted at that blank node so the output
		// graph never leaves dangling reification blank nodes.
		if tp.Predicate.Kind == TermVariable && objTerm.Kind == TermBlankNode {
			if stop, err := s.expandConstructBlankNodeChain(collector, objTerm); err != nil || stop {
				return stop, err
			}
		}
	}
	return false, nil
}

func (s *execState) expandConstructBlankNodeChain(collector *graphTripleCollector, bnode Term) (bool, error) {
	queue := []Term{bnode}
	visited := make(map[Term]bool)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if visited[cur] {
			continue
		}
		visited[cur] = true
		for _, rt := range s.unfoldedBySubj[cur] {
			tr := rdfTripleToMessageTriple(cur.Value, rt.Predicate.Value, rt.Object, s.mapper)
			stop, err := collector.add(tr)
			if err != nil || stop {
				return stop, err
			}
			if rt.Object.Kind == TermBlankNode && !visited[rt.Object] {
				queue = append(queue, rt.Object)
			}
		}
	}
	return false, nil
}

func lookupSingleProjectedPredicateByStandardIRI(iri string) (string, *vocabulary.PredicateMetadata) {
	preds := vocabulary.LookupByIRI(iri)
	if len(preds) != 1 {
		return "", nil
	}
	meta := vocabulary.GetPredicateMetadata(preds[0])
	if meta == nil || meta.Projection == nil || len(meta.Projection.Steps) == 0 {
		return "", nil
	}
	// Do not re-unfold if the IRI is already one of the projection's step or value IRIs.
	for _, st := range meta.Projection.Steps {
		if st.PredicateIRI == iri || st.ValuePredicateIRI == iri {
			return "", nil
		}
	}
	return preds[0], meta
}

func instantiateConstructTerm(t Term, sol Solution, rowIdx int) (Term, bool) {
	switch t.Kind {
	case TermVariable:
		bound, ok := sol[t.Value]
		return bound, ok
	case TermBlankNode:
		if bound, ok := sol[t.Value]; ok {
			return bound, true
		}
		label := strings.TrimPrefix(t.Value, "_:")
		return Term{
			Kind:  TermBlankNode,
			Value: fmt.Sprintf("_:c_%d_%s", rowIdx, sanitizeBNodeLabel(label)),
		}, true
	default:
		return t, true
	}
}

func sanitizeBNodeLabel(label string) string {
	var b strings.Builder
	for i := 0; i < len(label); i++ {
		ch := label[i]
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			b.WriteByte(ch)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "b"
	}
	return b.String()
}

func (s *execState) termToTripleSubject(subjTerm Term) (subj, entityID string) {
	if subjTerm.Kind == TermBlankNode {
		return subjTerm.Value, ""
	}
	if id, err := s.mapper.IRIToEntityID(subjTerm.Value); err == nil {
		return id, id
	}
	return subjTerm.Value, ""
}

func rdfTermToSourceTriple(subj, pred string, objTerm Term, mapper *irimap.Mapper) message.Triple {
	tr := rdfTripleToMessageTriple(subj, pred, objTerm, mapper)
	tr.Predicate = pred
	return tr
}

func rdfTripleToMessageTriple(subj, pred string, objTerm Term, mapper *irimap.Mapper) message.Triple {
	tr := message.Triple{
		Subject:   subj,
		Predicate: pred,
	}
	switch objTerm.Kind {
	case TermBlankNode:
		tr.Object = objTerm.Value
	case TermIRI:
		if mapper != nil {
			if id, err := mapper.IRIToEntityID(objTerm.Value); err == nil {
				tr.Object = id
				tr.Datatype = message.EntityReferenceDatatype
				return tr
			}
		}
		tr.Object = objTerm.Value
	case TermLiteral:
		tr.Object, tr.Datatype = literalTermToMessageObject(objTerm, false)
	}
	return tr
}

func (s *execState) execDescribe(ctx context.Context, q *Query) (*Result, error) {
	if s.maxResults <= 0 {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonResultLimit)
			return &Result{
				Form:             QueryDescribe,
				Triples:          []message.Triple{},
				EntitiesScanned:  s.entitiesScanned,
				Truncated:        true,
				TruncationReason: s.truncationReason,
			}, nil
		}
		return nil, newResultLimitError(s.maxResults)
	}

	targetIRIs := make([]string, 0, len(q.DescribeIRIs))
	targetIRIs = append(targetIRIs, q.DescribeIRIs...)

	if q.HasWhere {
		// Enforce MaxResults on deduplicated output triples rather than intermediate WHERE rows.
		s.enforceSolutionLimit = false
		solutions, err := s.evalGroup(ctx, q.Where, []Solution{{}})
		if err != nil {
			return nil, err
		}
		if err := s.checkContext(ctx); err != nil {
			return nil, err
		}
		vars := describeVariables(q)
		sortSolutions(solutions, q.OrderBy, vars)
		if q.Distinct {
			if err := s.checkContext(ctx); err != nil {
				return nil, err
			}
			solutions = deduplicateSolutions(solutions, vars)
		}
		if q.Offset > 0 {
			if q.Offset >= len(solutions) {
				solutions = nil
			} else {
				solutions = solutions[q.Offset:]
			}
		}
		if q.Limit != nil {
			if *q.Limit <= 0 {
				solutions = nil
			} else if *q.Limit < len(solutions) {
				solutions = solutions[:*q.Limit]
			}
		}
		for _, sol := range solutions {
			for _, v := range vars {
				if term, ok := sol[v]; ok && term.Kind == TermIRI {
					targetIRIs = append(targetIRIs, term.Value)
				}
			}
		}
	}

	entityIDs := make([]string, 0, len(targetIRIs))
	for _, iri := range targetIRIs {
		id, err := s.mapper.IRIToEntityID(iri)
		if err != nil {
			// Per owner ruling D.1d, non-entity IRIs contribute 0 triples without failing.
			continue
		}
		entityIDs = append(entityIDs, id)
	}
	slices.Sort(entityIDs)
	entityIDs = slices.Compact(entityIDs)

	var (
		entities []graph.EntityState
		err      error
	)
	if len(entityIDs) > 0 {
		entities, err = s.readEntitiesByIDs(ctx, entityIDs)
		if err != nil {
			return nil, err
		}
	}

	collector := newGraphTripleCollector(s.mapper, s.maxResults, s.allowTruncation)
	for i := range entities {
		if err := s.checkContext(ctx); err != nil {
			return nil, err
		}
		unfolded, err := UnfoldEntity(&entities[i], s.mapper.BaseIRI())
		if err != nil {
			return nil, err
		}
		var stopped bool
		for _, tr := range unfolded {
			stop, err := collector.add(tr)
			if err != nil {
				return nil, err
			}
			if stop {
				stopped = true
				break
			}
		}
		if stopped {
			break
		}
	}
	if err := s.checkContext(ctx); err != nil {
		return nil, err
	}
	if collector.truncated {
		s.markTruncated(TruncationReasonResultLimit)
	}

	return &Result{
		Form:             QueryDescribe,
		Triples:          collector.triples,
		EntitiesScanned:  s.entitiesScanned,
		Truncated:        s.truncated,
		TruncationReason: s.truncationReason,
	}, nil
}

func describeVariables(q *Query) []string {
	if !q.DescribeAll {
		return slices.Clone(q.Variables)
	}
	var vars []string
	seen := make(map[string]bool)
	collectGroupVars(q.Where, &vars, seen)
	return vars
}
