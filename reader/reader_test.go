package reader

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/pkg/types"
	"pgregory.net/rapid"
)

func TestMemoryReaderEmptySentinelDistinction(t *testing.T) {
	ctx := context.Background()
	r, err := NewMemoryReader(
		graph.EntityState{ID: "c360.ops.sys2.intel.platform.uav2"},
		graph.EntityState{ID: "c360.ops.sys1.intel.platform.uav1"},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader failed: %v", err)
	}

	// Empty string is NOT a valid literal prefix: ScanPrefix must reject it via
	// types.ValidateEntityIDPrefix rather than treating it as a stealth match-all.
	got, err := r.ScanPrefix(ctx, "", 10)
	if err == nil {
		t.Fatalf("ScanPrefix with empty prefix returned %d entities, want validation error", len(got))
	}
	var ce *errs.ClassifiedError
	if !errors.As(err, &ce) || ce.Class != errs.ErrorInvalid || ce.Code != types.ErrorCodeEntityIDPrefixInvalid {
		t.Fatalf("ScanPrefix empty error = %v (ce=%+v), want ErrorInvalid/%s", err, ce, types.ErrorCodeEntityIDPrefixInvalid)
	}
	if reason := ce.Detail[types.EntityIDDetailReason]; reason != types.EntityIDReasonEmpty {
		t.Fatalf("ScanPrefix empty error detail reason = %v, want %q", reason, types.EntityIDReasonEmpty)
	}

	// Intentional bounded match-all uses ScanPattern with MatchAllPattern ("*.*.*.*.*.*").
	all, err := r.ScanPattern(ctx, MatchAllPattern, 2)
	if err != nil {
		t.Fatalf("ScanPattern(MatchAllPattern, budget=2) failed: %v", err)
	}
	if len(all) != 2 || all[0].ID != "c360.ops.sys1.intel.platform.uav1" || all[1].ID != "c360.ops.sys2.intel.platform.uav2" {
		t.Fatalf("ScanPattern(MatchAllPattern) = %+v, want 2 sorted entities", all)
	}

	// MatchAllPattern enforces the scan budget and returns ErrScanBudgetExceeded with the bounded partial slice.
	if partial, err := r.ScanPattern(ctx, MatchAllPattern, 1); !errors.Is(err, ErrScanBudgetExceeded) || len(partial) != 1 {
		t.Fatalf("ScanPattern(MatchAllPattern, budget=1) = (%v, %v), want (len=1, ErrScanBudgetExceeded)", partial, err)
	}
}

func TestMemoryReaderInvalidInput(t *testing.T) {
	ctx := context.Background()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := NewMemoryReader(graph.EntityState{ID: "bad-id"}); err == nil {
		t.Fatal("NewMemoryReader accepted invalid entity ID")
	}

	r, err := NewMemoryReader(
		graph.EntityState{
			ID: "c360.ops.sys1.intel.platform.uav1",
			Triples: []message.Triple{
				{
					Subject:   "c360.ops.sys1.intel.platform.uav1",
					Predicate: "intel.role.assigned",
					Object:    "ISR",
				},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader failed: %v", err)
	}

	// Nil context is refused across all five GraphReader operations.
	var nilCtx context.Context
	if _, err := r.GetEntity(nilCtx, "c360.ops.sys1.intel.platform.uav1"); !errors.Is(err, ErrNilContext) {
		t.Fatalf("GetEntity(nilCtx) err = %v, want ErrNilContext", err)
	}
	if _, err := r.GetBatch(nilCtx, []string{"c360.ops.sys1.intel.platform.uav1"}); !errors.Is(err, ErrNilContext) {
		t.Fatalf("GetBatch(nilCtx) err = %v, want ErrNilContext", err)
	}
	if _, err := r.ScanPrefix(nilCtx, "c360.ops", 10); !errors.Is(err, ErrNilContext) {
		t.Fatalf("ScanPrefix(nilCtx) err = %v, want ErrNilContext", err)
	}
	if _, err := r.ScanPattern(nilCtx, MatchAllPattern, 10); !errors.Is(err, ErrNilContext) {
		t.Fatalf("ScanPattern(nilCtx) err = %v, want ErrNilContext", err)
	}
	if _, err := r.EntitiesByPredicate(nilCtx, "intel.role.assigned", 10); !errors.Is(err, ErrNilContext) {
		t.Fatalf("EntitiesByPredicate(nilCtx) err = %v, want ErrNilContext", err)
	}

	// Canceled context is refused across all five GraphReader operations.
	if _, err := r.GetEntity(canceledCtx, "c360.ops.sys1.intel.platform.uav1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetEntity(canceledCtx) err = %v, want context.Canceled", err)
	}
	if _, err := r.GetBatch(canceledCtx, []string{"c360.ops.sys1.intel.platform.uav1"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetBatch(canceledCtx) err = %v, want context.Canceled", err)
	}
	if _, err := r.ScanPrefix(canceledCtx, "c360.ops", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanPrefix(canceledCtx) err = %v, want context.Canceled", err)
	}
	if _, err := r.ScanPattern(canceledCtx, MatchAllPattern, 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("ScanPattern(canceledCtx) err = %v, want context.Canceled", err)
	}
	if _, err := r.EntitiesByPredicate(canceledCtx, "intel.role.assigned", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("EntitiesByPredicate(canceledCtx) err = %v, want context.Canceled", err)
	}

	// Invalid entity IDs at GetEntity and GetBatch entry.
	invalidIDs := []string{
		"",
		"c360.ops.sys1.intel.platform",
		"c360.ops.sys1.intel.platform.uav1.extra",
		"-c360.ops.sys1.intel.platform.uav1",
		"c360.ops.*.intel.platform.uav1",
		"c360.ops.sys1.intel.platform." + strings.Repeat("a", types.MaxEntityIDBytes),
	}
	for _, badID := range invalidIDs {
		if _, err := r.GetEntity(ctx, badID); err == nil {
			t.Fatalf("GetEntity(%q) succeeded, want validation error", badID)
		} else {
			var ce *errs.ClassifiedError
			if !errors.As(err, &ce) || ce.Code != types.ErrorCodeEntityIDInvalid {
				t.Fatalf("GetEntity(%q) err = %v, want Code %s", badID, err, types.ErrorCodeEntityIDInvalid)
			}
		}
		if _, err := r.GetBatch(ctx, []string{"c360.ops.sys1.intel.platform.uav1", badID}); err == nil {
			t.Fatalf("GetBatch([valid, %q]) succeeded, want validation error", badID)
		} else {
			var ce *errs.ClassifiedError
			if !errors.As(err, &ce) || ce.Code != types.ErrorCodeEntityIDInvalid {
				t.Fatalf("GetBatch([valid, %q]) err = %v, want Code %s", badID, err, types.ErrorCodeEntityIDInvalid)
			}
		}
	}

	// Invalid literal prefixes at ScanPrefix entry.
	invalidPrefixes := []string{
		"",
		"c360.*",
		"c360.ops.",
		"c360..ops",
		"a.b.c.d.e.f.g",
		strings.Repeat("a", types.MaxEntityIDBytes+1),
	}
	for _, badPrefix := range invalidPrefixes {
		if _, err := r.ScanPrefix(ctx, badPrefix, 10); err == nil {
			t.Fatalf("ScanPrefix(%q) succeeded, want validation error", badPrefix)
		} else {
			var ce *errs.ClassifiedError
			if !errors.As(err, &ce) || ce.Code != types.ErrorCodeEntityIDPrefixInvalid {
				t.Fatalf("ScanPrefix(%q) err = %v, want Code %s", badPrefix, err, types.ErrorCodeEntityIDPrefixInvalid)
			}
		}
	}

	// Invalid six-position patterns at ScanPattern entry.
	invalidPatterns := []string{
		"",
		"c360.ops.*.*.*",
		"c360.ops.*.*.*.*.*",
		"c360.ops.*.intel.platform.uav*",
		"c360..*.intel.platform.*",
		"c360.ops.*.intel.platform." + strings.Repeat("a", types.MaxEntityIDBytes),
	}
	for _, badPattern := range invalidPatterns {
		if _, err := r.ScanPattern(ctx, badPattern, 10); err == nil {
			t.Fatalf("ScanPattern(%q) succeeded, want validation error", badPattern)
		} else {
			var ce *errs.ClassifiedError
			if !errors.As(err, &ce) || ce.Code != types.ErrorCodeEntityIDPatternInvalid {
				t.Fatalf("ScanPattern(%q) err = %v, want Code %s", badPattern, err, types.ErrorCodeEntityIDPatternInvalid)
			}
		}
	}

	// Invalid predicates at EntitiesByPredicate entry.
	for _, badPred := range []string{"", "   ", "intel.role\x00assigned"} {
		if _, err := r.EntitiesByPredicate(ctx, badPred, 10); err == nil {
			t.Fatalf("EntitiesByPredicate(%q) succeeded, want validation error", badPred)
		} else {
			var ce *errs.ClassifiedError
			if !errors.As(err, &ce) || ce.Code != graph.ErrorCodeInvalidRequest {
				t.Fatalf("EntitiesByPredicate(%q) err = %v, want Code %s", badPred, err, graph.ErrorCodeInvalidRequest)
			}
		}
	}

	// Negative budget is rejected across ScanPrefix, ScanPattern, and EntitiesByPredicate.
	if _, err := r.ScanPrefix(ctx, "c360", -1); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("ScanPrefix negative budget err = %v, want ErrScanBudgetExceeded", err)
	}
	if _, err := r.ScanPattern(ctx, MatchAllPattern, -1); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("ScanPattern negative budget err = %v, want ErrScanBudgetExceeded", err)
	}
	if _, err := r.EntitiesByPredicate(ctx, "intel.role.assigned", -1); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("EntitiesByPredicate negative budget err = %v, want ErrScanBudgetExceeded", err)
	}
}

func TestMemoryReaderMissingEntitiesAndImmutability(t *testing.T) {
	ctx := context.Background()
	id1 := "c360.ops.sys1.intel.platform.uav1"
	id2 := "c360.ops.sys2.intel.platform.uav2"
	missing1 := "c360.ops.sys1.intel.platform.ghost1"
	missing2 := "c360.ops.sys2.intel.platform.ghost2"

	inputTriples := []message.Triple{
		{
			Subject:   id1,
			Predicate: "intel.role.assigned",
			Object:    "ISR",
		},
	}
	r, err := NewMemoryReader(
		graph.EntityState{
			ID:      id2,
			Triples: []message.Triple{{Subject: id2, Predicate: "intel.role.assigned", Object: "Strike"}},
		},
		graph.EntityState{
			ID:      id1,
			Triples: inputTriples,
			StorageRef: &message.StorageReference{
				StorageInstance: "obj1",
				Key:             "k1",
			},
			UpdatedAt: time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader failed: %v", err)
	}

	// Mutating caller's input slice after NewMemoryReader must not affect stored state.
	inputTriples[0].Object = "MUTATED_INPUT"

	got1, err := r.GetEntity(ctx, id1)
	if err != nil {
		t.Fatalf("GetEntity(%q) failed: %v", id1, err)
	}
	if got1.ID != id1 || len(got1.Triples) != 1 || got1.Triples[0].Object != "ISR" {
		t.Fatalf("GetEntity(%q) = %+v, want untouched ISR triple", id1, got1)
	}

	// Mutating returned EntityState must not affect subsequent reads.
	got1.Triples[0].Object = "MUTATED_OUTPUT"
	got1Again, err := r.GetEntity(ctx, id1)
	if err != nil {
		t.Fatalf("GetEntity(%q) second call failed: %v", id1, err)
	}
	if got1Again.Triples[0].Object != "ISR" {
		t.Fatalf("GetEntity(%q) was mutated by caller: got %v, want ISR", id1, got1Again.Triples[0].Object)
	}

	// Missing entity returns ErrEntityNotFound (and ADR-060 Code entity_not_found).
	if gotMissing, err := r.GetEntity(ctx, missing1); !errors.Is(err, ErrEntityNotFound) || gotMissing != nil {
		t.Fatalf("GetEntity(%q) = (%v, %v), want (nil, ErrEntityNotFound)", missing1, gotMissing, err)
	} else {
		var ce *errs.ClassifiedError
		if !errors.As(err, &ce) || ce.Code != graph.ErrorCodeEntityNotFound {
			t.Fatalf("GetEntity(%q) classified error = %+v, want Code %s", missing1, ce, graph.ErrorCodeEntityNotFound)
		}
	}

	// GetBatch with empty slice returns empty Entities and nil Missing.
	emptyBatch, err := r.GetBatch(ctx, nil)
	if err != nil {
		t.Fatalf("GetBatch(nil) failed: %v", err)
	}
	if len(emptyBatch.Entities) != 0 || len(emptyBatch.Missing) != 0 {
		t.Fatalf("GetBatch(nil) = %+v, want empty Entities and Missing", emptyBatch)
	}

	// GetBatch with out-of-order + duplicate + missing IDs returns sorted deduplicated
	// Entities and explicit sorted Missing entries (no zero-valued EntityState).
	batchResp, err := r.GetBatch(ctx, []string{missing2, id2, id1, missing1, id1, missing2})
	if err != nil {
		t.Fatalf("GetBatch failed: %v", err)
	}
	if len(batchResp.Entities) != 2 || batchResp.Entities[0].ID != id1 || batchResp.Entities[1].ID != id2 {
		t.Fatalf("GetBatch.Entities = %+v, want [%s, %s]", batchResp.Entities, id1, id2)
	}
	wantMissing := []graph.MissingEntity{
		{ID: missing1, Reason: graph.MissingNotFound},
		{ID: missing2, Reason: graph.MissingNotFound},
	}
	if !slices.Equal(batchResp.Missing, wantMissing) {
		t.Fatalf("GetBatch.Missing = %+v, want %+v", batchResp.Missing, wantMissing)
	}

	// Nil *MemoryReader behaves as an empty reader without panicking.
	var nilReader *MemoryReader
	if _, err := nilReader.GetEntity(ctx, id1); !errors.Is(err, ErrEntityNotFound) {
		t.Fatalf("nilReader.GetEntity err = %v, want ErrEntityNotFound", err)
	}
	nilBatch, err := nilReader.GetBatch(ctx, []string{id1})
	if err != nil || len(nilBatch.Entities) != 0 || len(nilBatch.Missing) != 1 || nilBatch.Missing[0].ID != id1 {
		t.Fatalf("nilReader.GetBatch = (%+v, %v), want 1 missing entity", nilBatch, err)
	}
	if got, err := nilReader.ScanPrefix(ctx, "c360", 0); err != nil || len(got) != 0 {
		t.Fatalf("nilReader.ScanPrefix = (%v, %v), want empty slice", got, err)
	}
	if got, err := nilReader.ScanPattern(ctx, MatchAllPattern, 0); err != nil || len(got) != 0 {
		t.Fatalf("nilReader.ScanPattern = (%v, %v), want empty slice", got, err)
	}
	if got, err := nilReader.EntitiesByPredicate(ctx, "intel.role.assigned", 0); err != nil || len(got) != 0 {
		t.Fatalf("nilReader.EntitiesByPredicate = (%v, %v), want empty slice", got, err)
	}
}

func TestMemoryReaderDeterministicIDSets(t *testing.T) {
	ctx := context.Background()

	e1 := graph.EntityState{
		ID: "c360.ops.sys1.intel.platform.uav1",
		Triples: []message.Triple{
			{Subject: "c360.ops.sys1.intel.platform.uav1", Predicate: "intel.role.assigned", Object: "old"},
		},
	}
	e1Updated := graph.EntityState{
		ID: "c360.ops.sys1.intel.platform.uav1",
		Triples: []message.Triple{
			{Subject: "c360.ops.sys1.intel.platform.uav1", Predicate: "intel.role.assigned", Object: "ISR"},
		},
	}
	e2 := graph.EntityState{
		ID: "c360.ops.sys2.intel.platform.uav2",
		Triples: []message.Triple{
			{Subject: "c360.ops.sys2.intel.platform.uav2", Predicate: "intel.role.assigned", Object: "Strike"},
		},
	}
	e3 := graph.EntityState{
		ID: "c360.ops.sys1.intel.observation.obs1",
		Triples: []message.Triple{
			{Subject: "c360.ops.sys1.intel.observation.obs1", Predicate: "intel.measurement.altitude-m", Object: 1500.0},
		},
	}
	// Sister deployment "c360.ops2" tests that ScanPrefix("c360.ops") never false-matches "c360.ops2.*".
	e4 := graph.EntityState{
		ID: "c360.ops2.sys1.intel.platform.uav3",
		Triples: []message.Triple{
			{Subject: "c360.ops2.sys1.intel.platform.uav3", Predicate: "intel.role.assigned", Object: "Recon"},
		},
	}

	rA, err := NewMemoryReader(e4, e2, e1, e3, e1Updated)
	if err != nil {
		t.Fatalf("NewMemoryReader(rA) failed: %v", err)
	}
	rB, err := NewMemoryReader(e1, e1Updated, e3, e2, e4)
	if err != nil {
		t.Fatalf("NewMemoryReader(rB) failed: %v", err)
	}

	idsOf := func(entities []graph.EntityState) []string {
		out := make([]string, len(entities))
		for i := range entities {
			out[i] = entities[i].ID
		}
		return out
	}

	// Duplicate entity ID keeps the last supplied state ("ISR", not "old").
	gotE1, err := rA.GetEntity(ctx, e1.ID)
	if err != nil || gotE1.Triples[0].Object != "ISR" {
		t.Fatalf("GetEntity duplicate overwrite = (%+v, %v), want Object=ISR", gotE1, err)
	}

	// ScanPrefix("c360.ops") matches the three "c360.ops.*" entities in sorted order,
	// excluding "c360.ops2.sys1.intel.platform.uav3".
	wantOpsIDs := []string{
		"c360.ops.sys1.intel.observation.obs1",
		"c360.ops.sys1.intel.platform.uav1",
		"c360.ops.sys2.intel.platform.uav2",
	}
	for _, r := range []*MemoryReader{rA, rB} {
		got, err := r.ScanPrefix(ctx, "c360.ops", 10)
		if err != nil {
			t.Fatalf("ScanPrefix(c360.ops) failed: %v", err)
		}
		if !slices.Equal(idsOf(got), wantOpsIDs) {
			t.Fatalf("ScanPrefix(c360.ops) IDs = %v, want %v", idsOf(got), wantOpsIDs)
		}
	}

	// Six-part exact prefix in ScanPrefix matches that single entity (graph-ingest parity).
	exactPrefixGot, err := rA.ScanPrefix(ctx, e2.ID, 1)
	if err != nil || !slices.Equal(idsOf(exactPrefixGot), []string{e2.ID}) {
		t.Fatalf("ScanPrefix(6-part %q) = (%v, %v), want [%s]", e2.ID, idsOf(exactPrefixGot), err, e2.ID)
	}

	// ScanPattern with wildcard system "c360.ops.*.intel.platform.*" matches uav1 and uav2 across systems.
	wantPlatformOps := []string{e1.ID, e2.ID}
	for _, r := range []*MemoryReader{rA, rB} {
		got, err := r.ScanPattern(ctx, "c360.ops.*.intel.platform.*", 10)
		if err != nil {
			t.Fatalf("ScanPattern(c360.ops.*.intel.platform.*) failed: %v", err)
		}
		if !slices.Equal(idsOf(got), wantPlatformOps) {
			t.Fatalf("ScanPattern(c360.ops.*.intel.platform.*) = %v, want %v", idsOf(got), wantPlatformOps)
		}
	}

	// ScanPattern with unconstrained org/platform "*.*.*.intel.platform.*" matches uav1, uav2, and uav3.
	wantAllPlatforms := []string{e1.ID, e2.ID, e4.ID}
	gotAllPlatforms, err := rA.ScanPattern(ctx, "*.*.*.intel.platform.*", 10)
	if err != nil || !slices.Equal(idsOf(gotAllPlatforms), wantAllPlatforms) {
		t.Fatalf("ScanPattern(*.*.*.intel.platform.*) = (%v, %v), want %v", idsOf(gotAllPlatforms), err, wantAllPlatforms)
	}

	// EntitiesByPredicate("intel.role.assigned") returns uav1, uav2, and uav3 in sorted ID order.
	for _, r := range []*MemoryReader{rA, rB} {
		got, err := r.EntitiesByPredicate(ctx, "intel.role.assigned", 10)
		if err != nil {
			t.Fatalf("EntitiesByPredicate(intel.role.assigned) failed: %v", err)
		}
		if !slices.Equal(idsOf(got), wantAllPlatforms) {
			t.Fatalf("EntitiesByPredicate(intel.role.assigned) = %v, want %v", idsOf(got), wantAllPlatforms)
		}
	}

	// Unknown predicate yields an empty slice, not an error.
	unknownGot, err := rA.EntitiesByPredicate(ctx, "intel.unknown.predicate", 10)
	if err != nil || len(unknownGot) != 0 {
		t.Fatalf("EntitiesByPredicate(unknown) = (%v, %v), want empty slice", unknownGot, err)
	}
}

func TestMemoryReaderBudgetAwareScans(t *testing.T) {
	ctx := context.Background()
	r, err := NewMemoryReader(
		graph.EntityState{
			ID: "c360.ops.sys1.intel.platform.uav1",
			Triples: []message.Triple{
				{Subject: "c360.ops.sys1.intel.platform.uav1", Predicate: "intel.role.assigned", Object: "ISR"},
			},
		},
		graph.EntityState{
			ID: "c360.ops.sys1.intel.platform.uav2",
			Triples: []message.Triple{
				{Subject: "c360.ops.sys1.intel.platform.uav2", Predicate: "intel.platform.callsign", Object: "HAWK2"},
			},
		},
		graph.EntityState{
			ID: "c360.ops.sys2.intel.platform.uav3",
			Triples: []message.Triple{
				{Subject: "c360.ops.sys2.intel.platform.uav3", Predicate: "intel.platform.callsign", Object: "HAWK3"},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader failed: %v", err)
	}

	// ScanPrefix matches 3 entities under "c360.ops": budget=3 succeeds, budget=2 returns ErrScanBudgetExceeded with 2 bounded entities.
	if got, err := r.ScanPrefix(ctx, "c360.ops", 3); err != nil || len(got) != 3 {
		t.Fatalf("ScanPrefix(budget=3) = (%d, %v), want (3, nil)", len(got), err)
	}
	if got, err := r.ScanPrefix(ctx, "c360.ops", 2); !errors.Is(err, ErrScanBudgetExceeded) || len(got) != 2 {
		t.Fatalf("ScanPrefix(budget=2) = (len=%d, %v), want (len=2, ErrScanBudgetExceeded)", len(got), err)
	}
	if got, err := r.ScanPrefix(ctx, "c360.ops", 0); !errors.Is(err, ErrScanBudgetExceeded) || len(got) != 0 {
		t.Fatalf("ScanPrefix(budget=0 on non-empty match) = (len=%d, %v), want (len=0, ErrScanBudgetExceeded)", len(got), err)
	}
	// Non-matching prefix reads 0 entities, so budget=0 succeeds with 0 entities.
	if got, err := r.ScanPrefix(ctx, "other.ops", 0); err != nil || len(got) != 0 {
		t.Fatalf("ScanPrefix(non-matching, budget=0) = (%v, %v), want ([], nil)", got, err)
	}

	// ScanPattern matches 2 entities under "c360.ops.sys1.intel.platform.*": budget=2 succeeds, budget=1 returns ErrScanBudgetExceeded with 1 bounded entity.
	if got, err := r.ScanPattern(ctx, "c360.ops.sys1.intel.platform.*", 2); err != nil || len(got) != 2 {
		t.Fatalf("ScanPattern(budget=2) = (%d, %v), want (2, nil)", len(got), err)
	}
	if got, err := r.ScanPattern(ctx, "c360.ops.sys1.intel.platform.*", 1); !errors.Is(err, ErrScanBudgetExceeded) || len(got) != 1 {
		t.Fatalf("ScanPattern(budget=1) = (len=%d, %v), want (len=1, ErrScanBudgetExceeded)", len(got), err)
	}

	// EntitiesByPredicate("intel.role.assigned") returns 1 entity, but must scan all 3 entities
	// in the store. A budget of 2 must return ErrScanBudgetExceeded rather than nil error!
	if got, err := r.EntitiesByPredicate(ctx, "intel.role.assigned", 2); !errors.Is(err, ErrScanBudgetExceeded) || len(got) != 1 {
		t.Fatalf("EntitiesByPredicate(budget=2 for 3-entity store) = (len=%d, %v), want (len=1, ErrScanBudgetExceeded)", len(got), err)
	}
	if got, err := r.EntitiesByPredicate(ctx, "intel.role.assigned", 3); err != nil || len(got) != 1 || got[0].ID != "c360.ops.sys1.intel.platform.uav1" {
		t.Fatalf("EntitiesByPredicate(budget=3) = (%+v, %v), want [uav1]", got, err)
	}
}

func TestPropMemoryReaderPatternAndPrefix(t *testing.T) {
	ctx := context.Background()
	segGen := rapid.SampledFrom([]string{"c360", "acme", "ops", "dev", "sys1", "sys2", "intel", "nav", "platform", "sensor", "uav1", "uav2", "s1"})

	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(rt, "num_entities")
		entities := make([]graph.EntityState, n)
		uniqueIDs := make(map[string]struct{}, n)
		for i := 0; i < n; i++ {
			parts := make([]string, 6)
			for p := range parts {
				parts[p] = segGen.Draw(rt, "ent_"+strconv.Itoa(i)+"_p_"+strconv.Itoa(p))
			}
			id := strings.Join(parts, ".")
			entities[i] = graph.EntityState{ID: id}
			uniqueIDs[id] = struct{}{}
		}

		r, err := NewMemoryReader(entities...)
		if err != nil {
			rt.Fatalf("NewMemoryReader failed: %v", err)
		}

		// Generate a 6-position pattern where each position is either a drawn segment or "*".
		patParts := make([]string, 6)
		for p := range patParts {
			if rapid.Bool().Draw(rt, "wild_"+strconv.Itoa(p)) {
				patParts[p] = "*"
			} else {
				patParts[p] = segGen.Draw(rt, "pat_p_"+strconv.Itoa(p))
			}
		}
		pattern := strings.Join(patParts, ".")

		var wantPatternIDs []string
		for id := range uniqueIDs {
			ok, err := types.MatchEntityIDPattern(pattern, id)
			if err != nil {
				rt.Fatalf("MatchEntityIDPattern(%q, %q) failed: %v", pattern, id, err)
			}
			if ok {
				wantPatternIDs = append(wantPatternIDs, id)
			}
		}
		slices.Sort(wantPatternIDs)

		gotPattern, err := r.ScanPattern(ctx, pattern, len(wantPatternIDs))
		if err != nil {
			rt.Fatalf("ScanPattern(%q, budget=%d) failed: %v", pattern, len(wantPatternIDs), err)
		}
		gotPatternIDs := make([]string, len(gotPattern))
		for i := range gotPattern {
			gotPatternIDs[i] = gotPattern[i].ID
		}
		if !slices.Equal(gotPatternIDs, wantPatternIDs) {
			rt.Fatalf("ScanPattern(%q) = %v, want oracle %v", pattern, gotPatternIDs, wantPatternIDs)
		}
		if len(wantPatternIDs) > 0 {
			if _, err := r.ScanPattern(ctx, pattern, len(wantPatternIDs)-1); !errors.Is(err, ErrScanBudgetExceeded) {
				rt.Fatalf("ScanPattern(%q, budget=%d) err = %v, want ErrScanBudgetExceeded", pattern, len(wantPatternIDs)-1, err)
			}
		}

		// Generate a 1-to-6-part literal prefix and compare against independent segment-prefix oracle.
		prefixLen := rapid.IntRange(1, 6).Draw(rt, "prefix_len")
		prefParts := make([]string, prefixLen)
		for p := range prefParts {
			prefParts[p] = segGen.Draw(rt, "pref_p_"+strconv.Itoa(p))
		}
		prefix := strings.Join(prefParts, ".")

		var wantPrefixIDs []string
		for id := range uniqueIDs {
			idParts := strings.Split(id, ".")
			if slices.Equal(idParts[:prefixLen], prefParts) {
				wantPrefixIDs = append(wantPrefixIDs, id)
			}
		}
		slices.Sort(wantPrefixIDs)

		gotPrefix, err := r.ScanPrefix(ctx, prefix, len(wantPrefixIDs))
		if err != nil {
			rt.Fatalf("ScanPrefix(%q, budget=%d) failed: %v", prefix, len(wantPrefixIDs), err)
		}
		gotPrefixIDs := make([]string, len(gotPrefix))
		for i := range gotPrefix {
			gotPrefixIDs[i] = gotPrefix[i].ID
		}
		if !slices.Equal(gotPrefixIDs, wantPrefixIDs) {
			rt.Fatalf("ScanPrefix(%q) = %v, want oracle %v", prefix, gotPrefixIDs, wantPrefixIDs)
		}
	})
}

func FuzzMemoryReaderSelectors(f *testing.F) {
	seeds := []struct {
		selector string
		budget   int
	}{
		{"c360.ops.sys1.intel.platform.uav1", 5},
		{"c360.ops", 5},
		{"c360.ops.*.intel.platform.*", 5},
		{MatchAllPattern, 5},
		{MatchAllPattern, 1},
		{"", 5},
		{"c360.*", 5},
		{"c360.ops.", 5},
		{"*.*.*.*.*", 5},
		{"c360.ops.sys1.intel.platform.uav1", -1},
		{"\x00", 5},
	}
	for _, s := range seeds {
		f.Add(s.selector, s.budget)
	}

	r, err := NewMemoryReader(
		graph.EntityState{
			ID: "c360.ops.sys1.intel.platform.uav1",
			Triples: []message.Triple{
				{Subject: "c360.ops.sys1.intel.platform.uav1", Predicate: "intel.role.assigned", Object: "ISR"},
			},
		},
		graph.EntityState{
			ID: "c360.ops.sys2.intel.platform.uav2",
			Triples: []message.Triple{
				{Subject: "c360.ops.sys2.intel.platform.uav2", Predicate: "intel.role.assigned", Object: "Strike"},
			},
		},
	)
	if err != nil {
		f.Fatalf("NewMemoryReader failed: %v", err)
	}
	ctx := context.Background()

	f.Fuzz(func(t *testing.T, selector string, budget int) {
		validID := types.ValidateEntityID(selector) == nil
		_, err := r.GetEntity(ctx, selector)
		if !validID && err == nil {
			t.Fatalf("GetEntity(%q) succeeded on invalid entity ID", selector)
		}

		_, batchErr := r.GetBatch(ctx, []string{selector})
		if !validID && batchErr == nil {
			t.Fatalf("GetBatch([%q]) succeeded on invalid entity ID", selector)
		}

		validPrefix := types.ValidateEntityIDPrefix(selector) == nil
		prefEntities, prefErr := r.ScanPrefix(ctx, selector, budget)
		if !validPrefix && prefErr == nil {
			t.Fatalf("ScanPrefix(%q) succeeded on invalid prefix", selector)
		}
		if budget < 0 && validPrefix && !errors.Is(prefErr, ErrScanBudgetExceeded) {
			t.Fatalf("ScanPrefix(%q, %d) err = %v, want ErrScanBudgetExceeded", selector, budget, prefErr)
		}
		if prefErr == nil && len(prefEntities) > budget {
			t.Fatalf("ScanPrefix(%q, %d) returned %d entities > budget", selector, budget, len(prefEntities))
		}

		validPattern := types.ValidateEntityIDPattern(selector) == nil
		patEntities, patErr := r.ScanPattern(ctx, selector, budget)
		if !validPattern && patErr == nil {
			t.Fatalf("ScanPattern(%q) succeeded on invalid pattern", selector)
		}
		if budget < 0 && validPattern && !errors.Is(patErr, ErrScanBudgetExceeded) {
			t.Fatalf("ScanPattern(%q, %d) err = %v, want ErrScanBudgetExceeded", selector, budget, patErr)
		}
		if patErr == nil && len(patEntities) > budget {
			t.Fatalf("ScanPattern(%q, %d) returned %d entities > budget", selector, budget, len(patEntities))
		}

		predEntities, predErr := r.EntitiesByPredicate(ctx, selector, budget)
		if (strings.TrimSpace(selector) == "" || strings.ContainsRune(selector, '\x00')) && predErr == nil {
			t.Fatalf("EntitiesByPredicate(%q) succeeded on invalid predicate", selector)
		}
		if predErr == nil && len(predEntities) > budget {
			t.Fatalf("EntitiesByPredicate(%q, %d) returned %d entities > budget", selector, budget, len(predEntities))
		}
	})
}
