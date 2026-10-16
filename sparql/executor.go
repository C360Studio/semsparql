package sparql

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
)

// Operator defaults for query execution (owner rulings D.1a, D.1b).
const (
	DefaultQueryTimeout    = 10 * time.Second
	DefaultMaxResults      = 10000
	DefaultMaxScanEntities = 100000
)

// ADR-060 classified error codes and truncation reason constants (owner ruling D.1a).
const (
	ErrorCodeResultLimitExceeded = "result_limit_exceeded"
	ErrorCodeScanBudgetExceeded  = "scan_budget_exceeded"
	ErrorCodeQueryTimeout        = "query_timeout"

	TruncationReasonResultLimit = "result_limit"
	TruncationReasonScanBudget  = "scan_budget"
)

var (
	// ErrResultLimitExceeded is the classified ADR-060 sentinel returned when a
	// query exceeds MaxResults without explicit truncation consent.
	ErrResultLimitExceeded error = &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    ErrorCodeResultLimitExceeded,
		Message: "sparql: result limit exceeded",
	}

	// ErrScanBudgetExceeded is the classified ADR-060 sentinel returned when a
	// query exceeds MaxScanEntities without explicit truncation consent.
	ErrScanBudgetExceeded error = &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    ErrorCodeScanBudgetExceeded,
		Message: "sparql: scan budget exceeded",
	}

	// ErrQueryTimeout is the classified ADR-060 sentinel returned when a query
	// exceeds its configured timeout or context deadline.
	ErrQueryTimeout error = &errs.ClassifiedError{
		Class:   errs.ErrorTransient,
		Code:    ErrorCodeQueryTimeout,
		Message: "sparql: query timeout exceeded",
	}
)

// Solution maps bound variable names (without leading '?' or '$') to RDF Terms.
type Solution map[string]Term

// Clone returns a shallow copy of the solution map.
func (s Solution) Clone() Solution {
	out := make(Solution, len(s))
	for k, v := range s {
		out[k] = v
	}
	return out
}

// Result holds the evaluated output of a SPARQL query.
type Result struct {
	Form             QueryForm
	Variables        []string
	Solutions        []Solution
	AskResult        bool
	EntitiesScanned  int
	Truncated        bool
	TruncationReason string
}

// Config configures a SPARQL Executor. Zero values for Timeout, MaxResults, and
// MaxScanEntities use DefaultQueryTimeout (10s), DefaultMaxResults (10,000), and
// DefaultMaxScanEntities (100,000) respectively. Org and Platform optionally scope
// bound-class pattern scans (`<org>.<platform>.*.<domain>.<type>.*`), defaulting
// to `*` wildcards when empty.
type Config struct {
	Mapper          *irimap.Mapper
	Timeout         time.Duration
	MaxResults      int
	MaxScanEntities int
	AllowTruncation bool
	Org             string
	Platform        string
}

// EvalOption configures a single Execute / ExecuteQuery invocation.
type EvalOption func(*evalOptions)

type evalOptions struct {
	allowTruncation *bool
	org             *string
	platform        *string
}

// WithAllowTruncation overrides Config.AllowTruncation for a single query execution
// (for example when X-Semsparql-Allow-Truncation: true is present on an HTTP request).
func WithAllowTruncation(allow bool) EvalOption {
	return func(o *evalOptions) {
		o.allowTruncation = &allow
	}
}

// WithOrgPlatform overrides Config.Org and Config.Platform for a single query
// execution when scoping bound-class pattern scans.
func WithOrgPlatform(org, platform string) EvalOption {
	return func(o *evalOptions) {
		o.org = &org
		o.platform = &platform
	}
}

// Executor plans and evaluates SPARQL 1.1 queries against a read-only GraphReader.
type Executor struct {
	reader reader.GraphReader
	cfg    Config
}

// NewExecutor constructs an Executor over r with cfg.
func NewExecutor(r reader.GraphReader, cfg Config) *Executor {
	if cfg.Mapper == nil {
		cfg.Mapper = irimap.NewMapper("")
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultQueryTimeout
	}
	if cfg.MaxResults == 0 {
		cfg.MaxResults = DefaultMaxResults
	}
	if cfg.MaxScanEntities == 0 {
		cfg.MaxScanEntities = DefaultMaxScanEntities
	}
	return &Executor{
		reader: r,
		cfg:    cfg,
	}
}

// ExecuteQuery parses queryStr and evaluates the resulting AST against the Executor.
func (e *Executor) ExecuteQuery(ctx context.Context, queryStr string, opts ...EvalOption) (*Result, error) {
	if ctx == nil {
		return nil, reader.ErrNilContext
	}
	q, err := Parse(queryStr)
	if err != nil {
		return nil, err
	}
	return e.Execute(ctx, q, opts...)
}

// Execute evaluates a parsed Query against the Executor's GraphReader.
func (e *Executor) Execute(ctx context.Context, q *Query, opts ...EvalOption) (*Result, error) {
	if ctx == nil {
		return nil, reader.ErrNilContext
	}
	if e == nil || e.reader == nil {
		return nil, errors.New("sparql: nil GraphReader")
	}
	if q == nil {
		return nil, errors.New("sparql: nil Query")
	}

	var eo evalOptions
	for _, opt := range opts {
		if opt != nil {
			opt(&eo)
		}
	}
	allowTruncation := e.cfg.AllowTruncation
	if eo.allowTruncation != nil {
		allowTruncation = *eo.allowTruncation
	}
	org := e.cfg.Org
	if eo.org != nil {
		org = *eo.org
	}
	platform := e.cfg.Platform
	if eo.platform != nil {
		platform = *eo.platform
	}

	timeout := e.cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultQueryTimeout
	}
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxResults := e.cfg.MaxResults
	if maxResults < 0 {
		maxResults = 0
	}
	maxScan := e.cfg.MaxScanEntities
	if maxScan < 0 {
		maxScan = 0
	}

	state := &execState{
		ctx:             queryCtx,
		reader:          e.reader,
		mapper:          e.cfg.Mapper,
		maxResults:      maxResults,
		budgetRemaining: maxScan,
		allowTruncation: allowTruncation,
		org:             org,
		platform:        platform,
	}

	if err := state.checkContext(); err != nil {
		return nil, err
	}

	switch q.Form {
	case QuerySelect:
		return state.execSelect(q)
	case QueryAsk:
		return state.execAsk(q)
	default:
		return nil, fmt.Errorf("sparql: query form %v not supported in SELECT/ASK executor", q.Form)
	}
}

type execState struct {
	ctx              context.Context
	reader           reader.GraphReader
	mapper           *irimap.Mapper
	maxResults       int
	budgetRemaining  int
	entitiesScanned  int
	allowTruncation  bool
	truncated        bool
	truncationReason string
	org              string
	platform         string
}

func (s *execState) markTruncated(reason string) {
	if !s.truncated {
		s.truncated = true
		s.truncationReason = reason
	}
}

func (s *execState) checkContext() error {
	if err := s.ctx.Err(); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return newTimeoutError(err)
		}
		return err
	}
	return nil
}

func (s *execState) translateReaderError(err error) (bool, error) {
	if errors.Is(err, context.DeadlineExceeded) {
		return false, newTimeoutError(err)
	}
	if s.ctx.Err() != nil && errors.Is(s.ctx.Err(), context.DeadlineExceeded) {
		return false, newTimeoutError(s.ctx.Err())
	}
	if errors.Is(err, reader.ErrScanBudgetExceeded) {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonScanBudget)
			return true, nil
		}
		return false, newScanBudgetError(err)
	}
	return false, err
}

func newResultLimitError(limit int) error {
	return errs.ClassifiedCode(
		errs.ErrorInvalid,
		ErrorCodeResultLimitExceeded,
		fmt.Errorf("sparql: query exceeded max_results limit of %d", limit),
	)
}

func newScanBudgetError(cause error) error {
	return errs.ClassifiedCode(
		errs.ErrorInvalid,
		ErrorCodeScanBudgetExceeded,
		fmt.Errorf("sparql: query exceeded max_scan_entities budget: %w", cause),
	)
}

func newTimeoutError(cause error) error {
	return errs.ClassifiedCode(
		errs.ErrorTransient,
		ErrorCodeQueryTimeout,
		fmt.Errorf("sparql: query timed out: %w", cause),
	)
}

func (s *execState) appendSolution(dst *[]Solution, sol Solution) (bool, error) {
	if len(*dst) >= s.maxResults {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonResultLimit)
			return true, nil
		}
		return false, newResultLimitError(s.maxResults)
	}
	*dst = append(*dst, sol)
	return false, nil
}

func (s *execState) execAsk(q *Query) (*Result, error) {
	if s.maxResults <= 0 {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonResultLimit)
			return &Result{
				Form:             QueryAsk,
				AskResult:        false,
				EntitiesScanned:  s.entitiesScanned,
				Truncated:        true,
				TruncationReason: s.truncationReason,
			}, nil
		}
		return nil, newResultLimitError(s.maxResults)
	}
	solutions, err := s.evalGroup(q.Where, []Solution{{}})
	if err != nil {
		return nil, err
	}
	return &Result{
		Form:             QueryAsk,
		AskResult:        len(solutions) > 0,
		EntitiesScanned:  s.entitiesScanned,
		Truncated:        s.truncated,
		TruncationReason: s.truncationReason,
	}, nil
}

func (s *execState) execSelect(q *Query) (*Result, error) {
	vars := selectVariables(q)
	solutions, err := s.evalGroup(q.Where, []Solution{{}})
	if err != nil {
		return nil, err
	}

	// Sort deterministically before projection/DISTINCT/LIMIT/OFFSET so ORDER BY
	// can reference non-projected variables and ties break deterministically.
	sortSolutions(solutions, q.OrderBy, vars)

	// Project only selected variables (stripping internal blank-node existential bindings).
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

	// Apply DISTINCT if requested.
	if q.Distinct {
		projected = deduplicateSolutions(projected, vars)
	}

	// Apply OFFSET and LIMIT slicing.
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
		Form:             QuerySelect,
		Variables:        vars,
		Solutions:        projected,
		EntitiesScanned:  s.entitiesScanned,
		Truncated:        s.truncated,
		TruncationReason: s.truncationReason,
	}, nil
}

func selectVariables(q *Query) []string {
	if !q.SelectAll {
		return slices.Clone(q.Variables)
	}
	var vars []string
	seen := make(map[string]bool)
	collectGroupVars(q.Where, &vars, seen)
	return vars
}

func collectGroupVars(g GroupPattern, vars *[]string, seen map[string]bool) {
	for _, el := range g.Elements {
		switch p := el.(type) {
		case BGPPattern:
			for _, tp := range p.Triples {
				addTermVar(tp.Subject, vars, seen)
				addTermVar(tp.Predicate, vars, seen)
				addTermVar(tp.Object, vars, seen)
			}
		case OptionalPattern:
			collectGroupVars(p.Group, vars, seen)
		case UnionPattern:
			for _, b := range p.Branches {
				collectGroupVars(b, vars, seen)
			}
		case SubGroupPattern:
			collectGroupVars(p.Group, vars, seen)
		}
	}
}

func addTermVar(t Term, vars *[]string, seen map[string]bool) {
	if t.Kind == TermVariable && !seen[t.Value] {
		seen[t.Value] = true
		*vars = append(*vars, t.Value)
	}
}

func (s *execState) evalGroup(g GroupPattern, current []Solution) ([]Solution, error) {
	solutions := current
	for _, el := range g.Elements {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		if len(solutions) == 0 {
			break
		}
		var err error
		switch p := el.(type) {
		case BGPPattern:
			solutions, err = s.evalBGP(p, solutions)
		case SubGroupPattern:
			solutions, err = s.evalGroup(p.Group, solutions)
		case OptionalPattern:
			solutions, err = s.evalOptional(p, solutions)
		case UnionPattern:
			solutions, err = s.evalUnion(p, solutions)
		}
		if err != nil {
			return nil, err
		}
	}

	if len(g.Filters) == 0 || len(solutions) == 0 {
		return solutions, nil
	}

	filtered := make([]Solution, 0, len(solutions))
	for _, sol := range solutions {
		if matchesAllFilters(g.Filters, sol) {
			filtered = append(filtered, sol)
		}
	}
	return filtered, nil
}

func (s *execState) evalOptional(opt OptionalPattern, current []Solution) ([]Solution, error) {
	out := make([]Solution, 0, len(current))
	for _, sol := range current {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		ext, err := s.evalGroup(opt.Group, []Solution{sol.Clone()})
		if err != nil {
			return nil, err
		}
		if len(ext) == 0 {
			stop, err := s.appendSolution(&out, sol.Clone())
			if err != nil {
				return nil, err
			}
			if stop {
				break
			}
			continue
		}
		var stopped bool
		for _, e := range ext {
			stop, err := s.appendSolution(&out, e)
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
	return out, nil
}

func (s *execState) evalUnion(u UnionPattern, current []Solution) ([]Solution, error) {
	out := make([]Solution, 0)
	for _, branch := range u.Branches {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		seed := make([]Solution, len(current))
		for i := range current {
			seed[i] = current[i].Clone()
		}
		branchSols, err := s.evalGroup(branch, seed)
		if err != nil {
			return nil, err
		}
		var stopped bool
		for _, sol := range branchSols {
			stop, err := s.appendSolution(&out, sol)
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
	return out, nil
}

type rdfTriple struct {
	Subject   Term
	Predicate Term
	Object    Term
}

func (s *execState) evalBGP(bgp BGPPattern, current []Solution) ([]Solution, error) {
	if len(bgp.Triples) == 0 || len(current) == 0 {
		return current, nil
	}

	// Pre-check static emptiness before touching GraphReader.
	for _, tp := range bgp.Triples {
		step := classifySinglePatternWithScope(tp, s.mapper, s.org, s.platform)
		if step.Kind == AccessEmpty {
			return nil, nil
		}
	}

	remaining := slices.Clone(bgp.Triples)
	solutions := current

	for len(remaining) > 0 && len(solutions) > 0 {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		bestIdx := pickNextPatternIndex(remaining, solutions, s.mapper, s.org, s.platform)
		tp := remaining[bestIdx]
		remaining = slices.Delete(remaining, bestIdx, bestIdx+1)

		var err error
		solutions, err = s.evalTriplePattern(tp, solutions)
		if err != nil {
			return nil, err
		}
	}

	return solutions, nil
}

func pickNextPatternIndex(remaining []TriplePattern, solutions []Solution, mapper *irimap.Mapper, org, platform string) int {
	bestIdx := 0
	bestRank := dynamicPatternRank(remaining[0], solutions, mapper, org, platform)
	for i := 1; i < len(remaining); i++ {
		r := dynamicPatternRank(remaining[i], solutions, mapper, org, platform)
		if r < bestRank {
			bestIdx = i
			bestRank = r
		}
	}
	return bestIdx
}

func dynamicPatternRank(tp TriplePattern, solutions []Solution, mapper *irimap.Mapper, org, platform string) int {
	step := classifySinglePatternWithScope(tp, mapper, org, platform)
	if step.Kind == AccessEmpty {
		return 0
	}
	if tp.IsStandaloneSubClassPath() {
		return int(AccessExactEntity)*10 - 5
	}
	if step.Kind <= AccessExactEntity {
		return int(step.Kind) * 10
	}
	if allSubjectsBound(tp.Subject, solutions) {
		return int(AccessExactEntity)*10 + 5
	}
	if step.Kind > AccessTypePattern && (tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath()) && allObjectsBoundToIRIs(tp.Object, solutions) {
		return int(AccessTypePattern)*10 + 5
	}
	return int(step.Kind) * 10
}

func allSubjectsBound(subj Term, solutions []Solution) bool {
	key, ok := bindingKey(subj)
	if !ok {
		return true
	}
	if len(solutions) == 0 {
		return false
	}
	for _, sol := range solutions {
		if _, exists := sol[key]; !exists {
			return false
		}
	}
	return true
}

func allObjectsBoundToIRIs(obj Term, solutions []Solution) bool {
	if obj.Kind == TermIRI {
		return true
	}
	key, ok := bindingKey(obj)
	if !ok || len(solutions) == 0 {
		return false
	}
	for _, sol := range solutions {
		bound, exists := sol[key]
		if !exists || bound.Kind != TermIRI {
			return false
		}
	}
	return true
}

func (s *execState) evalTriplePattern(tp TriplePattern, solutions []Solution) ([]Solution, error) {
	if tp.IsStandaloneSubClassPath() {
		return s.evalStandaloneSubClassPath(tp, solutions)
	}

	step := classifySinglePatternWithScope(tp, s.mapper, s.org, s.platform)
	if step.Kind == AccessEmpty {
		return nil, nil
	}

	if allSubjectsBound(tp.Subject, solutions) {
		return s.evalBoundSubjectPattern(tp, step.Predicates, solutions)
	}

	if step.Kind == AccessFullScan && (tp.IsPlainRDFType() || tp.IsRDFTypeWithSubClassPath()) && allObjectsBoundToIRIs(tp.Object, solutions) {
		upgraded, ok := s.resolveBoundObjectTypePatterns(tp.Object, step, solutions)
		if !ok {
			return nil, nil
		}
		step = upgraded
	}

	var (
		entities []graph.EntityState
		err      error
	)
	switch step.Kind {
	case AccessTypePattern:
		entities, err = s.readEntitiesByPatterns(step.Patterns)
	case AccessPredicate:
		entities, err = s.readEntitiesByPredicates(step.Predicates)
	default:
		entities, err = s.readAllEntities()
	}
	if err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}

	allTriples := make([]rdfTriple, 0, len(entities)*2)
	for i := range entities {
		ets, err := s.entityToRDFTriples(&entities[i], step.Predicates)
		if err != nil {
			return nil, err
		}
		allTriples = append(allTriples, ets...)
	}

	next := make([]Solution, 0)
	for _, sol := range solutions {
		var stopped bool
		for _, rt := range allTriples {
			if ext, ok := matchTriplePattern(tp, rt, sol); ok {
				stop, err := s.appendSolution(&next, ext)
				if err != nil {
					return nil, err
				}
				if stop {
					stopped = true
					break
				}
			}
		}
		if stopped {
			break
		}
	}
	return next, nil
}

func (s *execState) resolveBoundObjectTypePatterns(obj Term, step PlanStep, solutions []Solution) (PlanStep, bool) {
	var patterns []string
	hasUnregistered := false
	for _, sol := range solutions {
		resolved := resolveTerm(obj, sol)
		if resolved.Kind != TermIRI {
			continue
		}
		pats, err := TypeScanPatternsForClass(resolved.Value, s.org, s.platform)
		if err != nil {
			return step, false
		}
		if len(pats) == 0 {
			hasUnregistered = true
		} else {
			patterns = append(patterns, pats...)
		}
	}
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	if len(patterns) > 0 && !hasUnregistered {
		step.Kind = AccessTypePattern
		step.Patterns = patterns
		return step, true
	}
	if len(patterns) == 0 && hasUnregistered {
		step.Kind = AccessPredicate
		return step, true
	}
	return step, true
}

func (s *execState) evalStandaloneSubClassPath(tp TriplePattern, solutions []Solution) ([]Solution, error) {
	if tp.Subject.Kind == TermLiteral || tp.Object.Kind == TermLiteral {
		return nil, nil
	}
	next := make([]Solution, 0)
	for _, sol := range solutions {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		subIRI, okSub := boundIRIFromPatternTerm(tp.Subject, sol)
		supIRI, okSup := boundIRIFromPatternTerm(tp.Object, sol)
		if !okSub || !okSup {
			continue
		}
		pairs := SubClassClosurePairs(subIRI, supIRI)
		var stopped bool
		for _, pair := range pairs {
			rt := rdfTriple{
				Subject:   Term{Kind: TermIRI, Value: pair[0]},
				Predicate: Term{Kind: TermIRI, Value: RDFSSubClassOfIRI},
				Object:    Term{Kind: TermIRI, Value: pair[1]},
			}
			if ext, ok := matchTriplePattern(tp, rt, sol); ok {
				stop, err := s.appendSolution(&next, ext)
				if err != nil {
					return nil, err
				}
				if stop {
					stopped = true
					break
				}
			}
		}
		if stopped {
			break
		}
	}
	return next, nil
}

func boundIRIFromPatternTerm(t Term, sol Solution) (string, bool) {
	if key, ok := bindingKey(t); ok {
		if bound, exists := sol[key]; exists {
			if bound.Kind != TermIRI {
				return "", false
			}
			return bound.Value, true
		}
		return "", true
	}
	if t.Kind == TermIRI {
		return t.Value, true
	}
	return "", false
}

func (s *execState) evalBoundSubjectPattern(tp TriplePattern, candidatePreds []string, solutions []Solution) ([]Solution, error) {
	idSet := make(map[string]struct{}, len(solutions))
	entityIDs := make([]string, 0, len(solutions))
	for _, sol := range solutions {
		subj := resolveTerm(tp.Subject, sol)
		if subj.Kind != TermIRI {
			continue
		}
		eid, err := s.mapper.IRIToEntityID(subj.Value)
		if err != nil {
			continue
		}
		if _, seen := idSet[eid]; !seen {
			idSet[eid] = struct{}{}
			entityIDs = append(entityIDs, eid)
		}
	}
	if len(entityIDs) == 0 {
		return nil, nil
	}

	entities, err := s.readEntitiesByIDs(entityIDs)
	if err != nil {
		return nil, err
	}
	if len(entities) == 0 {
		return nil, nil
	}

	triplesBySubj := make(map[string][]rdfTriple, len(entities))
	for i := range entities {
		subjIRI, err := s.mapper.EntityIDToIRI(entities[i].ID)
		if err != nil {
			return nil, err
		}
		ets, err := s.entityToRDFTriples(&entities[i], candidatePreds)
		if err != nil {
			return nil, err
		}
		triplesBySubj[subjIRI] = ets
	}

	next := make([]Solution, 0)
	for _, sol := range solutions {
		subj := resolveTerm(tp.Subject, sol)
		if subj.Kind != TermIRI {
			continue
		}
		candidates := triplesBySubj[subj.Value]
		var stopped bool
		for _, rt := range candidates {
			if ext, ok := matchTriplePattern(tp, rt, sol); ok {
				stop, err := s.appendSolution(&next, ext)
				if err != nil {
					return nil, err
				}
				if stop {
					stopped = true
					break
				}
			}
		}
		if stopped {
			break
		}
	}
	return next, nil
}

func (s *execState) readEntitiesByIDs(entityIDs []string) ([]graph.EntityState, error) {
	slices.Sort(entityIDs)
	entityIDs = slices.Compact(entityIDs)
	if len(entityIDs) == 0 {
		return nil, nil
	}
	if err := s.checkContext(); err != nil {
		return nil, err
	}
	if s.budgetRemaining <= 0 {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonScanBudget)
			return nil, nil
		}
		return nil, newScanBudgetError(reader.ErrScanBudgetExceeded)
	}

	if len(entityIDs) == 1 {
		ent, err := s.reader.GetEntity(s.ctx, entityIDs[0])
		if err != nil {
			if errors.Is(err, reader.ErrEntityNotFound) {
				return nil, nil
			}
			_, rerr := s.translateReaderError(err)
			return nil, rerr
		}
		s.budgetRemaining--
		s.entitiesScanned++
		return []graph.EntityState{*ent}, nil
	}

	resp, err := s.reader.GetBatch(s.ctx, entityIDs)
	if err != nil {
		_, rerr := s.translateReaderError(err)
		return nil, rerr
	}
	for _, m := range resp.Missing {
		if m.Reason != "" && m.Reason != graph.MissingNotFound {
			return nil, fmt.Errorf("sparql: batch entity %q failed with reason %q", m.ID, m.Reason)
		}
	}
	if len(resp.Entities) > s.budgetRemaining {
		if s.allowTruncation {
			kept := resp.Entities[:s.budgetRemaining]
			s.entitiesScanned += len(kept)
			s.budgetRemaining = 0
			s.markTruncated(TruncationReasonScanBudget)
			return kept, nil
		}
		return nil, newScanBudgetError(reader.ErrScanBudgetExceeded)
	}
	s.budgetRemaining -= len(resp.Entities)
	s.entitiesScanned += len(resp.Entities)
	return resp.Entities, nil
}

func (s *execState) readEntitiesByPredicates(predicates []string) ([]graph.EntityState, error) {
	byID := make(map[string]graph.EntityState)
	for _, pred := range predicates {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		if s.budgetRemaining <= 0 {
			if s.allowTruncation {
				s.markTruncated(TruncationReasonScanBudget)
				break
			}
			return nil, newScanBudgetError(reader.ErrScanBudgetExceeded)
		}
		ents, err := s.reader.EntitiesByPredicate(s.ctx, pred, s.budgetRemaining)
		if err != nil {
			stop, rerr := s.translateReaderError(err)
			if rerr != nil {
				return nil, rerr
			}
			if stop {
				break
			}
		}
		s.budgetRemaining -= len(ents)
		s.entitiesScanned += len(ents)
		for i := range ents {
			byID[ents[i].ID] = ents[i]
		}
	}
	if len(byID) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]graph.EntityState, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out, nil
}

func (s *execState) readEntitiesByPatterns(patterns []string) ([]graph.EntityState, error) {
	byID := make(map[string]graph.EntityState)
	for _, pattern := range patterns {
		if err := s.checkContext(); err != nil {
			return nil, err
		}
		if s.budgetRemaining <= 0 {
			if s.allowTruncation {
				s.markTruncated(TruncationReasonScanBudget)
				break
			}
			return nil, newScanBudgetError(reader.ErrScanBudgetExceeded)
		}
		ents, err := s.reader.ScanPattern(s.ctx, pattern, s.budgetRemaining)
		if err != nil {
			stop, rerr := s.translateReaderError(err)
			if rerr != nil {
				return nil, rerr
			}
			if stop {
				break
			}
		}
		s.budgetRemaining -= len(ents)
		s.entitiesScanned += len(ents)
		for i := range ents {
			byID[ents[i].ID] = ents[i]
		}
	}
	if len(byID) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	out := make([]graph.EntityState, 0, len(ids))
	for _, id := range ids {
		out = append(out, byID[id])
	}
	return out, nil
}

func (s *execState) readAllEntities() ([]graph.EntityState, error) {
	if err := s.checkContext(); err != nil {
		return nil, err
	}
	if s.budgetRemaining <= 0 {
		if s.allowTruncation {
			s.markTruncated(TruncationReasonScanBudget)
			return nil, nil
		}
		return nil, newScanBudgetError(reader.ErrScanBudgetExceeded)
	}
	ents, err := s.reader.ScanPattern(s.ctx, reader.MatchAllPattern, s.budgetRemaining)
	if err != nil {
		stop, rerr := s.translateReaderError(err)
		if rerr != nil {
			return nil, rerr
		}
		if stop {
			return nil, nil
		}
	}
	s.budgetRemaining -= len(ents)
	s.entitiesScanned += len(ents)
	return ents, nil
}

func (s *execState) entityToRDFTriples(entity *graph.EntityState, filterPredicates []string) ([]rdfTriple, error) {
	subjIRI, err := s.mapper.EntityIDToIRI(entity.ID)
	if err != nil {
		return nil, err
	}
	defaultSubj := Term{Kind: TermIRI, Value: subjIRI}
	baseIRI := s.mapper.BaseIRI()

	seen := make(map[rdfTriple]struct{}, len(entity.Triples)+4)
	out := make([]rdfTriple, 0, len(entity.Triples)+4)
	addTriple := func(rt rdfTriple) {
		if _, dup := seen[rt]; !dup {
			seen[rt] = struct{}{}
			out = append(out, rt)
		}
	}

	includeRDFType := len(filterPredicates) == 0 || slices.Contains(filterPredicates, RDFTypeIRI)
	if includeRDFType {
		rdfTypeTerm := Term{Kind: TermIRI, Value: RDFTypeIRI}
		for _, clsIRI := range VirtualTypeIRIsForEntityID(entity.ID) {
			addTriple(rdfTriple{
				Subject:   defaultSubj,
				Predicate: rdfTypeTerm,
				Object:    Term{Kind: TermIRI, Value: clsIRI},
			})
		}
	}

	for _, tr := range entity.Triples {
		if len(filterPredicates) > 0 && !slices.Contains(filterPredicates, tr.Predicate) {
			continue
		}
		subjTerm := defaultSubj
		if tr.Subject != "" && tr.Subject != entity.ID {
			if strings.HasPrefix(tr.Subject, "_:") {
				subjTerm = Term{Kind: TermBlankNode, Value: tr.Subject}
			} else {
				subjTerm = Term{Kind: TermIRI, Value: export.SubjectIRI(tr.Subject, baseIRI)}
			}
		}

		predTerm, objTerm, ok, err := resolveTriplePredicateAndObject(tr, baseIRI)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		addTriple(rdfTriple{
			Subject:   subjTerm,
			Predicate: predTerm,
			Object:    objTerm,
		})
		if predTerm.Kind == TermIRI && predTerm.Value == RDFTypeIRI && objTerm.Kind == TermIRI {
			for _, supIRI := range SuperclassesForClassIRI(objTerm.Value) {
				addTriple(rdfTriple{
					Subject:   subjTerm,
					Predicate: predTerm,
					Object:    Term{Kind: TermIRI, Value: supIRI},
				})
			}
		}
	}
	return out, nil
}

// resolveTriplePredicateAndObject delegates predicate IRI resolution and object
// RDF classification to semengine/vocabulary/export via single-triple N-Triples
// serialization, avoiding any local copy of exporter classification rules.
func resolveTriplePredicateAndObject(tr message.Triple, baseIRI string) (predTerm, objTerm Term, ok bool, err error) {
	if strings.TrimSpace(tr.Predicate) == "" || tr.Object == nil {
		return Term{}, Term{}, false, nil
	}
	probe := message.Triple{
		Subject:   "_:s",
		Predicate: tr.Predicate,
		Object:    tr.Object,
		Datatype:  tr.Datatype,
	}
	line, err := export.SerializeToString([]message.Triple{probe}, export.NTriples, export.WithBaseIRI(baseIRI))
	if err != nil {
		return Term{}, Term{}, false, err
	}
	if line == "" {
		return Term{}, Term{}, false, nil
	}
	const prefix = "_:s <"
	idx := strings.Index(line, "> ")
	if !strings.HasPrefix(line, prefix) || idx < len(prefix) || !strings.HasSuffix(line, " .\n") {
		return Term{}, Term{}, false, fmt.Errorf("sparql: unexpected N-Triples probe line %q", line)
	}
	predIRI := line[len(prefix):idx]
	ntObj := line[idx+2 : len(line)-3]
	objTerm, err = parseNTriplesObjectTerm(ntObj)
	if err != nil {
		return Term{}, Term{}, false, err
	}
	return Term{Kind: TermIRI, Value: predIRI}, objTerm, true, nil
}

func parseNTriplesObjectTerm(ntObj string) (Term, error) {
	switch {
	case strings.HasPrefix(ntObj, "<") && strings.HasSuffix(ntObj, ">"):
		return Term{Kind: TermIRI, Value: ntObj[1 : len(ntObj)-1]}, nil
	case strings.HasPrefix(ntObj, "_:"):
		return Term{Kind: TermBlankNode, Value: ntObj}, nil
	case strings.HasPrefix(ntObj, `"`):
		var dtIRI, lang string
		rest := ntObj
		if strings.HasSuffix(rest, ">") {
			sep := strings.LastIndex(rest, `"^^<`)
			if sep < 1 {
				return Term{}, fmt.Errorf("sparql: malformed typed literal %q", ntObj)
			}
			dtIRI = rest[sep+4 : len(rest)-1]
			rest = rest[:sep+1]
			if strings.HasPrefix(dtIRI, "@") && len(dtIRI) > 1 {
				lang = strings.ToLower(dtIRI[1:])
				dtIRI = RDFLangStringIRI
			}
		} else if sep := strings.LastIndex(rest, `"@`); sep >= 1 {
			lang = strings.ToLower(rest[sep+2:])
			dtIRI = RDFLangStringIRI
			rest = rest[:sep+1]
		}
		if len(rest) < 2 || rest[0] != '"' || rest[len(rest)-1] != '"' {
			return Term{}, fmt.Errorf("sparql: malformed literal %q", ntObj)
		}
		lex, err := unescapeNTriplesLiteral(rest[1 : len(rest)-1])
		if err != nil {
			return Term{}, err
		}
		if dtIRI == "" {
			dtIRI = XSDStringIRI
		}
		return Term{
			Kind:     TermLiteral,
			Value:    lex,
			Datatype: dtIRI,
			Language: lang,
		}, nil
	default:
		return Term{}, fmt.Errorf("sparql: unrecognized N-Triples object token %q", ntObj)
	}
}

func unescapeNTriplesLiteral(s string) (string, error) {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("sparql: trailing backslash in literal %q", s)
		}
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		default:
			return "", fmt.Errorf("sparql: unsupported escape \\%c in literal %q", s[i], s)
		}
	}
	return b.String(), nil
}

func resolveTerm(t Term, sol Solution) Term {
	if key, ok := bindingKey(t); ok {
		if bound, exists := sol[key]; exists {
			return bound
		}
	}
	return t
}

func matchTriplePattern(tp TriplePattern, rt rdfTriple, sol Solution) (Solution, bool) {
	ext := sol.Clone()
	if !bindOrMatchTerm(tp.Subject, rt.Subject, ext) {
		return nil, false
	}
	switch tp.Path {
	case PathRDFTypeSubClassOfStar:
		if rt.Predicate.Kind != TermIRI || rt.Predicate.Value != RDFTypeIRI {
			return nil, false
		}
	case PathSubClassOfStar:
		if rt.Predicate.Kind != TermIRI || rt.Predicate.Value != RDFSSubClassOfIRI {
			return nil, false
		}
	default:
		if !bindOrMatchTerm(tp.Predicate, rt.Predicate, ext) {
			return nil, false
		}
	}
	if !bindOrMatchTerm(tp.Object, rt.Object, ext) {
		return nil, false
	}
	return ext, true
}

func bindOrMatchTerm(patternTerm, graphTerm Term, sol Solution) bool {
	if key, ok := bindingKey(patternTerm); ok {
		if existing, exists := sol[key]; exists {
			return termsMatch(existing, graphTerm)
		}
		sol[key] = graphTerm
		return true
	}
	return termsMatch(patternTerm, graphTerm)
}

func termsMatch(a, b Term) bool {
	if a.Kind != b.Kind {
		return false
	}
	if a.Kind != TermLiteral {
		return a.Value == b.Value
	}
	if a.Language != "" || b.Language != "" {
		return strings.EqualFold(a.Language, b.Language) && a.Value == b.Value
	}
	dtA := normalizeDatatypeIRI(a.Datatype)
	dtB := normalizeDatatypeIRI(b.Datatype)
	if dtA == dtB && a.Value == b.Value {
		return true
	}
	if isFloatDatatype(dtA) && isFloatDatatype(dtB) {
		fa, errA := strconv.ParseFloat(a.Value, 64)
		fb, errB := strconv.ParseFloat(b.Value, 64)
		return errA == nil && errB == nil && fa == fb
	}
	if dtA == XSDIntegerIRI && dtB == XSDIntegerIRI {
		ia, errA := strconv.ParseInt(a.Value, 10, 64)
		ib, errB := strconv.ParseInt(b.Value, 10, 64)
		return errA == nil && errB == nil && ia == ib
	}
	return false
}

func normalizeDatatypeIRI(dt string) string {
	if dt == "" {
		return XSDStringIRI
	}
	return dt
}

func isFloatDatatype(dt string) bool {
	return dt == XSDDoubleIRI || dt == XSDDecimalIRI
}

func isNumericDatatype(dt string) bool {
	return dt == XSDIntegerIRI || dt == XSDDecimalIRI || dt == XSDDoubleIRI
}

// --- FILTER evaluation (SPARQL 1.1 §17 with 3-valued logic) ---

func matchesAllFilters(filters []FilterExpr, sol Solution) bool {
	for _, f := range filters {
		b, ok := evalFilterEBV(f, sol)
		if !ok || !b {
			return false
		}
	}
	return true
}

func evalFilterEBV(expr FilterExpr, sol Solution) (bool, bool) {
	switch e := expr.(type) {
	case UnaryExpr:
		if e.Op == UnaryNot {
			b, ok := evalFilterEBV(e.Operand, sol)
			if !ok {
				return false, false
			}
			return !b, true
		}
	case BinaryExpr:
		switch e.Op {
		case BinaryAnd:
			lb, lok := evalFilterEBV(e.Left, sol)
			rb, rok := evalFilterEBV(e.Right, sol)
			if (lok && !lb) || (rok && !rb) {
				return false, true
			}
			if lok && rok && lb && rb {
				return true, true
			}
			return false, false
		case BinaryOr:
			lb, lok := evalFilterEBV(e.Left, sol)
			rb, rok := evalFilterEBV(e.Right, sol)
			if (lok && lb) || (rok && rb) {
				return true, true
			}
			if lok && rok && !lb && !rb {
				return false, true
			}
			return false, false
		}
	}

	val, ok := evalFilterValue(expr, sol)
	if !ok {
		return false, false
	}
	return termEBV(val)
}

func termEBV(t Term) (bool, bool) {
	if t.Kind != TermLiteral {
		return false, false
	}
	dt := normalizeDatatypeIRI(t.Datatype)
	switch dt {
	case XSDBooleanIRI:
		switch t.Value {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		default:
			return false, true
		}
	case XSDStringIRI, RDFLangStringIRI:
		return len(t.Value) > 0, true
	case XSDIntegerIRI:
		n, err := strconv.ParseInt(t.Value, 10, 64)
		if err != nil {
			return false, true
		}
		return n != 0, true
	case XSDDecimalIRI, XSDDoubleIRI:
		f, err := strconv.ParseFloat(t.Value, 64)
		if err != nil || math.IsNaN(f) {
			return false, true
		}
		return f != 0, true
	default:
		return false, false
	}
}

func boolTerm(b bool) Term {
	if b {
		return Term{Kind: TermLiteral, Value: "true", Datatype: XSDBooleanIRI}
	}
	return Term{Kind: TermLiteral, Value: "false", Datatype: XSDBooleanIRI}
}

func evalFilterValue(expr FilterExpr, sol Solution) (Term, bool) {
	switch e := expr.(type) {
	case TermExpr:
		if e.Term.Kind == TermVariable {
			val, exists := sol[e.Term.Value]
			return val, exists
		}
		return e.Term, true
	case UnaryExpr:
		switch e.Op {
		case UnaryNot:
			b, ok := evalFilterEBV(e, sol)
			if !ok {
				return Term{}, false
			}
			return boolTerm(b), true
		case UnaryPlus:
			val, ok := evalFilterValue(e.Operand, sol)
			if !ok || val.Kind != TermLiteral || !isNumericDatatype(normalizeDatatypeIRI(val.Datatype)) {
				return Term{}, false
			}
			return val, true
		case UnaryMinus:
			val, ok := evalFilterValue(e.Operand, sol)
			if !ok || val.Kind != TermLiteral {
				return Term{}, false
			}
			dt := normalizeDatatypeIRI(val.Datatype)
			switch dt {
			case XSDIntegerIRI:
				n, err := strconv.ParseInt(val.Value, 10, 64)
				if err != nil {
					return Term{}, false
				}
				return Term{Kind: TermLiteral, Value: strconv.FormatInt(-n, 10), Datatype: XSDIntegerIRI}, true
			case XSDDecimalIRI, XSDDoubleIRI:
				f, err := strconv.ParseFloat(val.Value, 64)
				if err != nil {
					return Term{}, false
				}
				return Term{Kind: TermLiteral, Value: strconv.FormatFloat(-f, 'g', -1, 64), Datatype: dt}, true
			default:
				return Term{}, false
			}
		}
	case BinaryExpr:
		switch e.Op {
		case BinaryAnd, BinaryOr:
			b, ok := evalFilterEBV(e, sol)
			if !ok {
				return Term{}, false
			}
			return boolTerm(b), true
		default:
			lv, lok := evalFilterValue(e.Left, sol)
			rv, rok := evalFilterValue(e.Right, sol)
			if !lok || !rok {
				return Term{}, false
			}
			b, ok := compareTermsForFilter(e.Op, lv, rv)
			if !ok {
				return Term{}, false
			}
			return boolTerm(b), true
		}
	case CallExpr:
		return evalCallExpr(e, sol)
	}
	return Term{}, false
}

func evalCallExpr(call CallExpr, sol Solution) (Term, bool) {
	switch call.Func {
	case FuncBound:
		if len(call.Args) != 1 {
			return Term{}, false
		}
		te, ok := call.Args[0].(TermExpr)
		if !ok || te.Term.Kind != TermVariable {
			return Term{}, false
		}
		_, exists := sol[te.Term.Value]
		return boolTerm(exists), true

	case FuncStr:
		if len(call.Args) != 1 {
			return Term{}, false
		}
		val, ok := evalFilterValue(call.Args[0], sol)
		if !ok || (val.Kind != TermIRI && val.Kind != TermLiteral) {
			return Term{}, false
		}
		return Term{Kind: TermLiteral, Value: val.Value, Datatype: XSDStringIRI}, true

	case FuncLang:
		if len(call.Args) != 1 {
			return Term{}, false
		}
		val, ok := evalFilterValue(call.Args[0], sol)
		if !ok || val.Kind != TermLiteral {
			return Term{}, false
		}
		return Term{Kind: TermLiteral, Value: val.Language, Datatype: XSDStringIRI}, true

	case FuncDatatype:
		if len(call.Args) != 1 {
			return Term{}, false
		}
		val, ok := evalFilterValue(call.Args[0], sol)
		if !ok || val.Kind != TermLiteral {
			return Term{}, false
		}
		if val.Language != "" {
			return Term{Kind: TermIRI, Value: RDFLangStringIRI}, true
		}
		return Term{Kind: TermIRI, Value: normalizeDatatypeIRI(val.Datatype)}, true

	case FuncRegex:
		if len(call.Args) < 2 || len(call.Args) > 3 {
			return Term{}, false
		}
		textTerm, ok := evalFilterValue(call.Args[0], sol)
		if !ok || !isStringLikeLiteral(textTerm) {
			return Term{}, false
		}
		patTerm, ok := evalFilterValue(call.Args[1], sol)
		if !ok || !isSimpleStringLiteral(patTerm) {
			return Term{}, false
		}
		pattern := patTerm.Value
		if len(call.Args) == 3 {
			flagTerm, ok := evalFilterValue(call.Args[2], sol)
			if !ok || !isSimpleStringLiteral(flagTerm) {
				return Term{}, false
			}
			prefix, ok := buildRegexFlagPrefix(flagTerm.Value)
			if !ok {
				return Term{}, false
			}
			pattern = prefix + pattern
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return Term{}, false
		}
		return boolTerm(re.MatchString(textTerm.Value)), true
	}
	return Term{}, false
}

func isStringLikeLiteral(t Term) bool {
	if t.Kind != TermLiteral {
		return false
	}
	dt := normalizeDatatypeIRI(t.Datatype)
	return dt == XSDStringIRI || dt == RDFLangStringIRI
}

func isSimpleStringLiteral(t Term) bool {
	return t.Kind == TermLiteral && t.Language == "" && normalizeDatatypeIRI(t.Datatype) == XSDStringIRI
}

func buildRegexFlagPrefix(flags string) (string, bool) {
	if flags == "" {
		return "", true
	}
	var b strings.Builder
	b.WriteString("(?")
	for _, r := range flags {
		switch r {
		case 'i', 'm', 's':
			b.WriteRune(r)
		default:
			return "", false
		}
	}
	b.WriteByte(')')
	return b.String(), true
}

func compareTermsForFilter(op BinaryOp, a, b Term) (bool, bool) {
	// Non-literal terms (IRIs and blank nodes) support = and != in FILTER;
	// relational ordering (<, <=, >, >=) on non-literals is a SPARQL type error.
	if a.Kind != TermLiteral || b.Kind != TermLiteral {
		switch op {
		case BinaryEqual:
			return a.Kind == b.Kind && a.Value == b.Value, true
		case BinaryNotEqual:
			return a.Kind != b.Kind || a.Value != b.Value, true
		default:
			return false, false
		}
	}

	dtA := normalizeDatatypeIRI(a.Datatype)
	dtB := normalizeDatatypeIRI(b.Datatype)

	// Numeric comparison.
	if isNumericDatatype(dtA) && isNumericDatatype(dtB) {
		if dtA == XSDIntegerIRI && dtB == XSDIntegerIRI {
			ia, errA := strconv.ParseInt(a.Value, 10, 64)
			ib, errB := strconv.ParseInt(b.Value, 10, 64)
			if errA == nil && errB == nil {
				return applyOrderOp(op, cmp.Compare(ia, ib)), true
			}
		}
		fa, errA := strconv.ParseFloat(a.Value, 64)
		fb, errB := strconv.ParseFloat(b.Value, 64)
		if errA != nil || errB != nil || math.IsNaN(fa) || math.IsNaN(fb) {
			return false, false
		}
		return applyOrderOp(op, cmp.Compare(fa, fb)), true
	}

	// Simple string comparison.
	if dtA == XSDStringIRI && dtB == XSDStringIRI && a.Language == "" && b.Language == "" {
		return applyOrderOp(op, cmp.Compare(a.Value, b.Value)), true
	}

	// Boolean comparison.
	if dtA == XSDBooleanIRI && dtB == XSDBooleanIRI {
		ba, oka := termEBV(a)
		bb, okb := termEBV(b)
		if !oka || !okb {
			return false, false
		}
		return applyOrderOp(op, compareBools(ba, bb)), true
	}

	// DateTime comparison.
	if dtA == XSDDateTimeIRI && dtB == XSDDateTimeIRI {
		ta, errA := time.Parse(time.RFC3339Nano, a.Value)
		tb, errB := time.Parse(time.RFC3339Nano, b.Value)
		if errA != nil || errB != nil {
			return false, false
		}
		var c int
		switch {
		case ta.Before(tb):
			c = -1
		case ta.After(tb):
			c = 1
		default:
			c = 0
		}
		return applyOrderOp(op, c), true
	}

	// Language-tagged strings support = and !=.
	if dtA == RDFLangStringIRI && dtB == RDFLangStringIRI {
		eq := strings.EqualFold(a.Language, b.Language) && a.Value == b.Value
		switch op {
		case BinaryEqual:
			return eq, true
		case BinaryNotEqual:
			return !eq, true
		default:
			return false, false
		}
	}

	// Same unknown datatype supports = and != by lexical equality.
	if dtA == dtB && op == BinaryEqual {
		return a.Value == b.Value, true
	}
	if dtA == dtB && op == BinaryNotEqual {
		return a.Value != b.Value, true
	}

	return false, false
}

func applyOrderOp(op BinaryOp, c int) bool {
	switch op {
	case BinaryEqual:
		return c == 0
	case BinaryNotEqual:
		return c != 0
	case BinaryLess:
		return c < 0
	case BinaryLessEqual:
		return c <= 0
	case BinaryGreater:
		return c > 0
	case BinaryGreaterEqual:
		return c >= 0
	default:
		return false
	}
}

func compareBools(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a && b:
		return -1
	default:
		return 1
	}
}

// --- Deterministic ORDER BY and DISTINCT ---

func sortSolutions(solutions []Solution, orderBy []OrderCondition, projectedVars []string) {
	if len(solutions) <= 1 {
		return
	}
	tieVars := collectTieBreakVars(solutions, projectedVars)
	slices.SortStableFunc(solutions, func(a, b Solution) int {
		for _, cond := range orderBy {
			va, oka := evalFilterValue(cond.Expr, a)
			vb, okb := evalFilterValue(cond.Expr, b)
			c := compareTermsTotalOrder(va, oka, vb, okb)
			if c != 0 {
				if cond.Direction == OrderDesc {
					return -c
				}
				return c
			}
		}
		for _, v := range tieVars {
			va, oka := a[v]
			vb, okb := b[v]
			if c := compareTermsTotalOrder(va, oka, vb, okb); c != 0 {
				return c
			}
		}
		return 0
	})
}

func collectTieBreakVars(solutions []Solution, projectedVars []string) []string {
	seen := make(map[string]bool, len(projectedVars))
	out := make([]string, 0, len(projectedVars))
	for _, v := range projectedVars {
		seen[v] = true
		out = append(out, v)
	}
	var extra []string
	for _, sol := range solutions {
		for k := range sol {
			if !isInternalBindingKey(k) && !seen[k] {
				seen[k] = true
				extra = append(extra, k)
			}
		}
	}
	slices.Sort(extra)
	return append(out, extra...)
}

// compareTermsTotalOrder implements the SPARQL 1.1 §15.1 total order:
// Unbound < BlankNode < IRI < Literal.
func compareTermsTotalOrder(a Term, oka bool, b Term, okb bool) int {
	if !oka && !okb {
		return 0
	}
	if !oka {
		return -1
	}
	if !okb {
		return 1
	}
	rankA := termOrderRank(a.Kind)
	rankB := termOrderRank(b.Kind)
	if rankA != rankB {
		return cmp.Compare(rankA, rankB)
	}
	if a.Kind != TermLiteral {
		return cmp.Compare(a.Value, b.Value)
	}

	dtA := normalizeDatatypeIRI(a.Datatype)
	dtB := normalizeDatatypeIRI(b.Datatype)
	if isNumericDatatype(dtA) && isNumericDatatype(dtB) {
		fa, errA := strconv.ParseFloat(a.Value, 64)
		fb, errB := strconv.ParseFloat(b.Value, 64)
		if errA == nil && errB == nil {
			if c := cmp.Compare(fa, fb); c != 0 {
				return c
			}
		}
	}
	if dtA == XSDBooleanIRI && dtB == XSDBooleanIRI {
		ba, oka := termEBV(a)
		bb, okb := termEBV(b)
		if oka && okb {
			if c := compareBools(ba, bb); c != 0 {
				return c
			}
		}
	}
	if dtA == XSDDateTimeIRI && dtB == XSDDateTimeIRI {
		ta, errA := time.Parse(time.RFC3339Nano, a.Value)
		tb, errB := time.Parse(time.RFC3339Nano, b.Value)
		if errA == nil && errB == nil {
			if ta.Before(tb) {
				return -1
			}
			if ta.After(tb) {
				return 1
			}
		}
	}
	if c := cmp.Compare(a.Value, b.Value); c != 0 {
		return c
	}
	if c := cmp.Compare(a.Language, b.Language); c != 0 {
		return c
	}
	return cmp.Compare(dtA, dtB)
}

func termOrderRank(k TermKind) int {
	switch k {
	case TermBlankNode:
		return 1
	case TermIRI:
		return 2
	case TermLiteral:
		return 3
	default:
		return 0
	}
}

func deduplicateSolutions(solutions []Solution, vars []string) []Solution {
	out := make([]Solution, 0, len(solutions))
	for _, sol := range solutions {
		dup := false
		for _, existing := range out {
			if solutionsEqualOnVars(existing, sol, vars) {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, sol)
		}
	}
	return out
}

func solutionsEqualOnVars(a, b Solution, vars []string) bool {
	for _, v := range vars {
		va, oka := a[v]
		vb, okb := b[v]
		if oka != okb {
			return false
		}
		if oka && !termsMatch(va, vb) {
			return false
		}
	}
	return true
}
