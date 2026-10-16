package reader

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/pkg/types"
)

// MemoryReader is a deterministic, immutable in-memory GraphReader for unit
// and property tests. It validates all selectors with SemEngine's shared
// validators and evaluates wildcard patterns with types.MatchEntityIDPattern.
type MemoryReader struct {
	entities  map[string]*graph.EntityState
	sortedIDs []string
}

var _ GraphReader = (*MemoryReader)(nil)

// NewMemoryReader constructs an immutable MemoryReader from entities. Every
// supplied entity ID is validated with types.ValidateEntityID at construction
// time; duplicate entity IDs keep the last entry (matching KV Put semantics),
// and stored EntityState values are deep-cloned.
func NewMemoryReader(entities ...graph.EntityState) (*MemoryReader, error) {
	byID := make(map[string]*graph.EntityState, len(entities))
	for i := range entities {
		id := entities[i].ID
		if err := validateEntityID(id); err != nil {
			return nil, fmt.Errorf("reader: fixture entity[%d]: %w", i, err)
		}
		byID[id] = entities[i].Clone()
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return &MemoryReader{
		entities:  byID,
		sortedIDs: ids,
	}, nil
}

// GetEntity returns a deep clone of the entity for entityID, or an error
// matching ErrEntityNotFound when absent.
func (r *MemoryReader) GetEntity(ctx context.Context, entityID string) (*graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validateEntityID(entityID); err != nil {
		return nil, err
	}
	if r != nil {
		if entity, ok := r.entities[entityID]; ok {
			return entity.Clone(), nil
		}
	}
	return nil, errs.ClassifiedCode(
		errs.ErrorInvalid,
		graph.ErrorCodeEntityNotFound,
		fmt.Errorf("reader: entity not found: %s", entityID),
	)
}

// GetBatch returns found entities in deterministic sorted ID order and reports
// any absent requested IDs in EntityBatchResponse.Missing with MissingNotFound.
func (r *MemoryReader) GetBatch(ctx context.Context, entityIDs []string) (*graph.EntityBatchResponse, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	sortedIDs, err := validateAndNormalizeBatchIDs(entityIDs)
	if err != nil {
		return nil, err
	}
	resp := &graph.EntityBatchResponse{
		Entities: make([]graph.EntityState, 0, len(sortedIDs)),
	}
	for _, id := range sortedIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if r != nil {
			if entity, ok := r.entities[id]; ok {
				resp.Entities = append(resp.Entities, *entity.Clone())
				continue
			}
		}
		resp.Missing = append(resp.Missing, graph.MissingEntity{
			ID:     id,
			Reason: graph.MissingNotFound,
		})
	}
	return resp, nil
}

// ScanPrefix returns all entities matching the non-empty one-to-six-part
// literal prefix in sorted ID order, charging every matching entity against
// budget.
func (r *MemoryReader) ScanPrefix(ctx context.Context, prefix string, budget int) ([]graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	if r == nil || len(r.sortedIDs) == 0 {
		return []graph.EntityState{}, nil
	}
	prefixDot := prefix + "."
	out := make([]graph.EntityState, 0)
	for _, id := range r.sortedIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if id != prefix && !strings.HasPrefix(id, prefixDot) {
			continue
		}
		if len(out)+1 > budget {
			return nil, fmt.Errorf("%w: prefix %q exceeded budget of %d entities", ErrScanBudgetExceeded, prefix, budget)
		}
		out = append(out, *r.entities[id].Clone())
	}
	return out, nil
}

// ScanPattern returns all entities matching the six-position ADR-102 pattern in
// sorted ID order using types.MatchEntityIDPattern, charging every matching
// entity against budget.
func (r *MemoryReader) ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validatePattern(pattern); err != nil {
		return nil, err
	}
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	if r == nil || len(r.sortedIDs) == 0 {
		return []graph.EntityState{}, nil
	}
	out := make([]graph.EntityState, 0)
	for _, id := range r.sortedIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		matched, err := types.MatchEntityIDPattern(pattern, id)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		if len(out)+1 > budget {
			return nil, fmt.Errorf("%w: pattern %q exceeded budget of %d entities", ErrScanBudgetExceeded, pattern, budget)
		}
		out = append(out, *r.entities[id].Clone())
	}
	return out, nil
}

// EntitiesByPredicate scans the graph under budget using MatchAllPattern and
// filters to entities holding at least one triple with Predicate == predicate
// in sorted ID order.
func (r *MemoryReader) EntitiesByPredicate(ctx context.Context, predicate string, budget int) ([]graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validatePredicate(predicate); err != nil {
		return nil, err
	}
	scanned, err := r.ScanPattern(ctx, MatchAllPattern, budget)
	if err != nil {
		return nil, err
	}
	return filterEntitiesByPredicate(scanned, predicate), nil
}
