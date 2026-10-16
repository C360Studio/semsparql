package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/pkg/types"
)

// ErrPatternCapabilityUnsupported indicates the remote graph-ingest responder
// does not support PrefixQueryRequest.Pattern (semengine#259) or returned an
// entity that violated the requested prefix/pattern filter. SemSparql fails
// closed rather than falling back to client-side full-prefix filtering.
var ErrPatternCapabilityUnsupported = errors.New("reader: remote graph-ingest server does not support PrefixQueryRequest.Pattern (semengine#259 required)")

// ClassifiedRequester is the NATS classified request/reply seam satisfied by
// *natsclient.Client and accepted by graph.NewExactEntityReader.
type ClassifiedRequester interface {
	RequestClassified(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

// NATSRPCReader implements GraphReader over SemEngine's declared NATS
// request/reply query verbs (graph.QueryVerbs()) using classified RPC.
// It never writes ENTITY_STATES or publishes graph.mutation.* subjects.
type NATSRPCReader struct {
	requester     ClassifiedRequester
	exact         graph.ExactEntityReader
	batchSubject  string
	prefixSubject string
	timeout       time.Duration
}

var _ GraphReader = (*NATSRPCReader)(nil)

// NewNATSRPCReader constructs a read-only NATSRPCReader over requester using
// the query subjects declared in graph.QueryVerbs(). A zero timeout uses the
// requester's default timeout; a negative timeout is refused.
func NewNATSRPCReader(requester ClassifiedRequester, timeout time.Duration) (*NATSRPCReader, error) {
	if requester == nil {
		return nil, errors.New("reader: nil ClassifiedRequester")
	}
	exact, err := graph.NewExactEntityReader(requester, timeout)
	if err != nil {
		return nil, fmt.Errorf("reader: %w", err)
	}
	batchSubject, prefixSubject, err := resolveDeclaredQuerySubjects(graph.QueryVerbs())
	if err != nil {
		return nil, err
	}
	return &NATSRPCReader{
		requester:     requester,
		exact:         exact,
		batchSubject:  batchSubject,
		prefixSubject: prefixSubject,
		timeout:       timeout,
	}, nil
}

func resolveDeclaredQuerySubjects(verbs []graph.QueryVerb) (batchSubject, prefixSubject string, err error) {
	for _, v := range verbs {
		switch v.Name {
		case "batch":
			batchSubject = v.Subject
		case "prefix":
			prefixSubject = v.Subject
		}
	}
	if batchSubject == "" || prefixSubject == "" {
		return "", "", errors.New("reader: graph.QueryVerbs() missing required batch or prefix verb")
	}
	return batchSubject, prefixSubject, nil
}

func (r *NATSRPCReader) validateAvailable() error {
	if r == nil || r.requester == nil || r.exact == nil || r.batchSubject == "" || r.prefixSubject == "" {
		return errors.New("reader: NATS RPC reader is unavailable")
	}
	return nil
}

// VerifyPatternCapability probes the remote graph-ingest prefix responder using
// the D.2 TestProbeServerPatternCapabilitySeam check:
//  1. A negative probe (PrefixQueryRequest{Pattern: "invalid", Limit: 1}) must
//     be rejected with classified errs.ErrorInvalid and code
//     types.ErrorCodeEntityIDPatternInvalid before bucket I/O. A legacy pre-#259
//     server ignores the unknown "pattern" field and returns nil, which is
//     refused with ErrPatternCapabilityUnsupported.
//  2. A positive probe (PrefixQueryRequest{Pattern: MatchAllPattern, Limit: 1})
//     must succeed and return a valid PrefixQueryResponse.
func (r *NATSRPCReader) VerifyPatternCapability(ctx context.Context) error {
	if err := validateContext(ctx); err != nil {
		return err
	}
	if err := r.validateAvailable(); err != nil {
		return err
	}

	negProbe, err := json.Marshal(graph.PrefixQueryRequest{
		Pattern: "invalid",
		Limit:   1,
	})
	if err != nil {
		return fmt.Errorf("reader: marshal negative pattern probe: %w", err)
	}
	_, negErr := r.requester.RequestClassified(ctx, r.prefixSubject, negProbe, r.timeout)
	if negErr == nil {
		return fmt.Errorf("%w: server accepted invalid pattern probe without error", ErrPatternCapabilityUnsupported)
	}
	var classified *errs.ClassifiedError
	if !errs.IsInvalid(negErr) || !errors.As(negErr, &classified) || classified.Code != types.ErrorCodeEntityIDPatternInvalid {
		return fmt.Errorf("%w: negative pattern probe returned unexpected error: %w", ErrPatternCapabilityUnsupported, negErr)
	}

	posProbe, err := json.Marshal(graph.PrefixQueryRequest{
		Pattern: MatchAllPattern,
		Limit:   1,
	})
	if err != nil {
		return fmt.Errorf("reader: marshal positive pattern probe: %w", err)
	}
	raw, err := r.requester.RequestClassified(ctx, r.prefixSubject, posProbe, r.timeout)
	if err != nil {
		return fmt.Errorf("reader: positive pattern capability probe failed: %w", err)
	}
	var resp graph.PrefixQueryResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return errs.ClassifiedCode(
			errs.ErrorFatal,
			graph.ErrorCodeInternal,
			fmt.Errorf("reader: decode positive pattern probe response: %w", err),
		)
	}
	if err := graph.ValidateDecodedEntityStates(resp.Entities); err != nil {
		return graph.ClassifyStateContractError(err)
	}
	return nil
}

// GetEntity reads a single entity by its canonical six-part entity ID through
// graph.ExactEntityReader. Invalid entity IDs are rejected before transport; a
// missing entity returns a classified error matching errors.Is(err, ErrEntityNotFound).
func (r *NATSRPCReader) GetEntity(ctx context.Context, entityID string) (*graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validateEntityID(entityID); err != nil {
		return nil, err
	}
	if err := r.validateAvailable(); err != nil {
		return nil, err
	}
	exact, err := r.exact.ReadExactEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	return exact.Entity.Clone(), nil
}

// GetBatch reads a set of entities by their canonical six-part entity IDs over
// the declared batch verb. Every requested ID is validated at entry before
// transport. Found entities are returned in deterministic sorted ID order, and
// any absent ID is recorded explicitly in EntityBatchResponse.Missing.
func (r *NATSRPCReader) GetBatch(ctx context.Context, entityIDs []string) (*graph.EntityBatchResponse, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	sortedIDs, err := validateAndNormalizeBatchIDs(entityIDs)
	if err != nil {
		return nil, err
	}
	if err := r.validateAvailable(); err != nil {
		return nil, err
	}
	if len(sortedIDs) == 0 {
		return &graph.EntityBatchResponse{
			Entities: []graph.EntityState{},
		}, nil
	}

	reqBytes, err := json.Marshal(struct {
		IDs []string `json:"ids"`
	}{IDs: sortedIDs})
	if err != nil {
		return nil, fmt.Errorf("reader: marshal batch request: %w", err)
	}

	respBytes, err := r.requester.RequestClassified(ctx, r.batchSubject, reqBytes, r.timeout)
	if err != nil {
		return nil, err
	}

	var wireResp graph.EntityBatchResponse
	if err := json.Unmarshal(respBytes, &wireResp); err != nil {
		return nil, errs.ClassifiedCode(
			errs.ErrorFatal,
			graph.ErrorCodeInternal,
			fmt.Errorf("reader: decode batch response: %w", err),
		)
	}
	if err := graph.ValidateDecodedEntityStates(wireResp.Entities); err != nil {
		return nil, graph.ClassifyStateContractError(err)
	}

	foundByID := make(map[string]graph.EntityState, len(wireResp.Entities))
	for i := range wireResp.Entities {
		ent := &wireResp.Entities[i]
		if _, requested := slices.BinarySearch(sortedIDs, ent.ID); !requested {
			return nil, graph.ClassifyStateContractError(&graph.StateContractError{
				Reason:   graph.GraphStateReasonNoncanonicalEntityID,
				EntityID: ent.ID,
				Err:      fmt.Errorf("batch response contains unrequested entity %q", ent.ID),
			})
		}
		foundByID[ent.ID] = *ent.Clone()
	}

	missingReasonByID := make(map[string]graph.MissingReason, len(wireResp.Missing))
	for _, m := range wireResp.Missing {
		reason := m.Reason
		if reason == "" {
			reason = graph.MissingNotFound
		}
		missingReasonByID[m.ID] = reason
	}

	out := &graph.EntityBatchResponse{
		Entities: make([]graph.EntityState, 0, len(foundByID)),
	}
	for _, id := range sortedIDs {
		if ent, ok := foundByID[id]; ok {
			out.Entities = append(out.Entities, ent)
			continue
		}
		reason, ok := missingReasonByID[id]
		if !ok {
			reason = graph.MissingNotFound
		}
		out.Missing = append(out.Missing, graph.MissingEntity{
			ID:     id,
			Reason: reason,
		})
	}
	return out, nil
}

// ScanPrefix reads all entities matching a non-empty one-to-six-part literal
// entity-ID prefix in deterministic sorted ID order over the declared prefix
// verb, following opaque continuation cursors and charging every matching
// entity against budget.
func (r *NATSRPCReader) ScanPrefix(ctx context.Context, prefix string, budget int) ([]graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validatePrefix(prefix); err != nil {
		return nil, err
	}
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	if err := r.validateAvailable(); err != nil {
		return nil, err
	}
	prefixDot := prefix + "."
	return r.scanPages(
		ctx,
		graph.PrefixQueryRequest{Prefix: prefix},
		fmt.Sprintf("prefix %q", prefix),
		budget,
		func(id string) (bool, error) {
			return id == prefix || strings.HasPrefix(id, prefixDot), nil
		},
	)
}

// ScanPattern reads all entities matching an exact six-position ADR-102 pattern
// in deterministic sorted ID order over the declared prefix verb, following
// opaque continuation cursors and charging every matching entity against budget.
func (r *NATSRPCReader) ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error) {
	if err := validateContext(ctx); err != nil {
		return nil, err
	}
	if err := validatePattern(pattern); err != nil {
		return nil, err
	}
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	if err := r.validateAvailable(); err != nil {
		return nil, err
	}
	return r.scanPages(
		ctx,
		graph.PrefixQueryRequest{Pattern: pattern},
		fmt.Sprintf("pattern %q", pattern),
		budget,
		func(id string) (bool, error) {
			return types.MatchEntityIDPattern(pattern, id)
		},
	)
}

func (r *NATSRPCReader) scanPages(
	ctx context.Context,
	reqTemplate graph.PrefixQueryRequest,
	selectorDesc string,
	budget int,
	matchesSelector func(string) (bool, error),
) ([]graph.EntityState, error) {
	byID := make(map[string]graph.EntityState)
	var cursor string

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		remaining := budget - len(byID)
		if remaining < 0 {
			remaining = 0
		}
		limit := remaining + 1
		if limit > graph.MaxPrefixQueryLimit {
			limit = graph.MaxPrefixQueryLimit
		}

		req := reqTemplate
		req.Limit = limit
		req.Cursor = cursor

		reqBytes, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("reader: marshal prefix query request: %w", err)
		}

		respBytes, err := r.requester.RequestClassified(ctx, r.prefixSubject, reqBytes, r.timeout)
		if err != nil {
			return nil, err
		}

		var resp graph.PrefixQueryResponse
		if err := json.Unmarshal(respBytes, &resp); err != nil {
			return nil, errs.ClassifiedCode(
				errs.ErrorFatal,
				graph.ErrorCodeInternal,
				fmt.Errorf("reader: decode prefix query response: %w", err),
			)
		}
		if err := graph.ValidateDecodedEntityStates(resp.Entities); err != nil {
			return nil, graph.ClassifyStateContractError(err)
		}

		for i := range resp.Entities {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			ent := &resp.Entities[i]
			matched, err := matchesSelector(ent.ID)
			if err != nil {
				return nil, err
			}
			if !matched {
				return nil, errs.ClassifiedCode(
					errs.ErrorFatal,
					graph.ErrorCodeInternal,
					fmt.Errorf("%w: %s returned non-matching entity %q", ErrPatternCapabilityUnsupported, selectorDesc, ent.ID),
				)
			}
			if _, exists := byID[ent.ID]; !exists {
				if len(byID)+1 > budget {
					return nil, fmt.Errorf("%w: %s exceeded budget of %d entities", ErrScanBudgetExceeded, selectorDesc, budget)
				}
			}
			byID[ent.ID] = *ent.Clone()
		}

		if resp.NextCursor == "" {
			break
		}
		if resp.NextCursor == cursor {
			return nil, errs.ClassifiedCode(
				errs.ErrorFatal,
				graph.ErrorCodeInternal,
				fmt.Errorf("reader: %s pagination cursor %q did not advance", selectorDesc, cursor),
			)
		}
		cursor = resp.NextCursor
	}

	if len(byID) == 0 {
		return []graph.EntityState{}, nil
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

// EntitiesByPredicate scans the graph under budget using MatchAllPattern over
// the declared prefix verb and filters to entities holding at least one triple
// with Predicate == predicate in deterministic sorted ID order.
func (r *NATSRPCReader) EntitiesByPredicate(ctx context.Context, predicate string, budget int) ([]graph.EntityState, error) {
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
