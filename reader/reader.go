package reader

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/pkg/types"
)

// MatchAllPattern is the explicit six-position wildcard pattern used for
// bounded full-graph scans (such as unconstrained ?s ?p ?o or
// EntitiesByPredicate). It is distinct from a literal prefix: ScanPrefix
// rejects an empty string via types.ValidateEntityIDPrefix.
const MatchAllPattern = "*.*.*.*.*.*"

var (
	// ErrNilContext indicates a nil context.Context was passed to a reader operation.
	ErrNilContext = errors.New("reader: nil context")

	// ErrEntityNotFound is the classified ADR-060 sentinel matching any
	// *errs.ClassifiedError with Code == graph.ErrorCodeEntityNotFound via
	// errors.Is(err, ErrEntityNotFound).
	ErrEntityNotFound error = &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    graph.ErrorCodeEntityNotFound,
		Message: "reader: entity not found",
	}

	// ErrScanBudgetExceeded indicates a scan or predicate lookup exceeded the
	// caller's entity-read budget. Readers return the bounded prefix of up to
	// budget entities alongside ErrScanBudgetExceeded so callers without
	// explicit truncation consent fail closed while callers with
	// X-Semsparql-Allow-Truncation: true can emit the bounded partial set.
	ErrScanBudgetExceeded = errors.New("reader: scan budget exceeded")
)

// GraphReader is the read-only graph access contract for SemSparql query
// planning and evaluation. Implementations never write ENTITY_STATES or publish
// graph.mutation.* subjects.
type GraphReader interface {
	// GetEntity reads a single entity by its canonical six-part entity ID.
	// Invalid entity IDs are rejected before transport; a missing entity
	// returns an error matching errors.Is(err, ErrEntityNotFound).
	GetEntity(ctx context.Context, entityID string) (*graph.EntityState, error)

	// GetBatch reads a set of entities by their canonical six-part entity IDs.
	// Every requested ID is validated at entry before transport. Found entities
	// are returned in deterministic sorted ID order, and any absent ID is
	// recorded explicitly in EntityBatchResponse.Missing with MissingNotFound.
	GetBatch(ctx context.Context, entityIDs []string) (*graph.EntityBatchResponse, error)

	// ScanPrefix reads all entities matching a non-empty one-to-six-part literal
	// entity-ID prefix in deterministic sorted ID order, bounded by budget. An
	// empty string is rejected by types.ValidateEntityIDPrefix; callers that
	// intend a bounded full-graph scan must call ScanPattern with MatchAllPattern.
	ScanPrefix(ctx context.Context, prefix string, budget int) ([]graph.EntityState, error)

	// ScanPattern reads all entities matching an exact six-position ADR-102
	// pattern (where any position may be "*") in deterministic sorted ID order,
	// bounded by budget.
	ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error)

	// EntitiesByPredicate returns all entities holding at least one triple for
	// predicate in deterministic sorted ID order. Until a graph-index predicate
	// verb is declared in graph.QueryVerbs(), it executes a bounded reader scan
	// charging every scanned entity against budget.
	EntitiesByPredicate(ctx context.Context, predicate string, budget int) ([]graph.EntityState, error)
}

func validateContext(ctx context.Context) error {
	if ctx == nil {
		return ErrNilContext
	}
	return ctx.Err()
}

func validateEntityID(entityID string) error {
	if err := types.ValidateEntityID(entityID); err != nil {
		return fmt.Errorf("reader: invalid entity ID %q: %w", entityID, err)
	}
	return nil
}

// validateAndNormalizeBatchIDs validates every requested entity ID before
// transport and returns a deduplicated, lexicographically sorted copy.
func validateAndNormalizeBatchIDs(entityIDs []string) ([]string, error) {
	for _, id := range entityIDs {
		if err := types.ValidateEntityID(id); err != nil {
			return nil, fmt.Errorf("reader: invalid batch entity ID %q: %w", id, err)
		}
	}
	if len(entityIDs) == 0 {
		return nil, nil
	}
	sorted := slices.Clone(entityIDs)
	slices.Sort(sorted)
	return slices.Compact(sorted), nil
}

func validatePrefix(prefix string) error {
	if err := types.ValidateEntityIDPrefix(prefix); err != nil {
		return fmt.Errorf("reader: invalid entity ID prefix %q: %w", prefix, err)
	}
	return nil
}

func validatePattern(pattern string) error {
	if err := types.ValidateEntityIDPattern(pattern); err != nil {
		return fmt.Errorf("reader: invalid entity ID pattern %q: %w", pattern, err)
	}
	return nil
}

func validatePredicate(predicate string) error {
	if strings.TrimSpace(predicate) == "" || strings.ContainsRune(predicate, '\x00') {
		return errs.ClassifiedCode(
			errs.ErrorInvalid,
			graph.ErrorCodeInvalidRequest,
			fmt.Errorf("reader: invalid predicate %q", predicate),
		)
	}
	return nil
}

func validateBudget(budget int) error {
	if budget < 0 {
		return fmt.Errorf("%w: negative budget %d", ErrScanBudgetExceeded, budget)
	}
	return nil
}

func filterEntitiesByPredicate(scanned []graph.EntityState, predicate string) []graph.EntityState {
	matched := make([]graph.EntityState, 0)
	for i := range scanned {
		if entityHasPredicate(&scanned[i], predicate) {
			matched = append(matched, scanned[i])
		}
	}
	return matched
}

func entityHasPredicate(entity *graph.EntityState, predicate string) bool {
	for i := range entity.Triples {
		if entity.Triples[i].Predicate == predicate {
			return true
		}
	}
	return false
}

type scanCountKey struct{}

// WithScanCounter returns a derived context that accumulates the number of
// entities scanned by ScanPrefix, ScanPattern, or EntitiesByPredicate into *count.
func WithScanCounter(ctx context.Context, count *int) context.Context {
	if ctx == nil || count == nil {
		return ctx
	}
	return context.WithValue(ctx, scanCountKey{}, count)
}

func recordScannedEntity(ctx context.Context, n int) {
	if n <= 0 || ctx == nil {
		return
	}
	if count, ok := ctx.Value(scanCountKey{}).(*int); ok && count != nil {
		*count += n
	}
}
