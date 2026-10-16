package reader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c360studio/semengine/component"
	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/graph/inference"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/natsclient"
	"github.com/c360studio/semengine/payloadregistry"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/pkg/lifecycle"
	"github.com/c360studio/semengine/pkg/types"
	graphingest "github.com/c360studio/semengine/processor/graph-ingest"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"pgregory.net/rapid"
)

type stubClassifiedRequester struct {
	mu      sync.Mutex
	calls   []recordedRPCCall
	handler func(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error)
}

type recordedRPCCall struct {
	subject string
	data    []byte
	timeout time.Duration
}

func (s *stubClassifiedRequester) RequestClassified(ctx context.Context, subject string, data []byte, timeout time.Duration) ([]byte, error) {
	s.mu.Lock()
	s.calls = append(s.calls, recordedRPCCall{
		subject: subject,
		data:    slices.Clone(data),
		timeout: timeout,
	})
	fn := s.handler
	s.mu.Unlock()
	if fn == nil {
		return nil, errors.New("unexpected RPC call")
	}
	return fn(ctx, subject, data, timeout)
}

func (s *stubClassifiedRequester) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func TestNATSRPCReaderConstructorAndInputValidation(t *testing.T) {
	ctx := t.Context()

	if _, err := NewNATSRPCReader(nil, time.Second); err == nil {
		t.Fatal("NewNATSRPCReader(nil) succeeded, want error")
	}
	stub := &stubClassifiedRequester{}
	if _, err := NewNATSRPCReader(stub, -time.Second); err == nil {
		t.Fatal("NewNATSRPCReader(negative timeout) succeeded, want error")
	}
	if _, _, err := resolveDeclaredQuerySubjects(nil); err == nil {
		t.Fatal("resolveDeclaredQuerySubjects(nil) succeeded, want error")
	}

	r, err := NewNATSRPCReader(stub, 2*time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}

	// Nil receiver fails closed.
	var nilReader *NATSRPCReader
	if _, err := nilReader.GetEntity(ctx, "c360.ops.sys1.intel.platform.uav1"); err == nil {
		t.Fatal("nilReader.GetEntity succeeded, want error")
	}
	if _, err := nilReader.GetBatch(ctx, []string{"c360.ops.sys1.intel.platform.uav1"}); err == nil {
		t.Fatal("nilReader.GetBatch succeeded, want error")
	}
	if _, err := nilReader.ScanPrefix(ctx, "c360.ops", 5); err == nil {
		t.Fatal("nilReader.ScanPrefix succeeded, want error")
	}
	if _, err := nilReader.ScanPattern(ctx, MatchAllPattern, 5); err == nil {
		t.Fatal("nilReader.ScanPattern succeeded, want error")
	}
	if err := nilReader.VerifyPatternCapability(ctx); err == nil {
		t.Fatal("nilReader.VerifyPatternCapability succeeded, want error")
	}

	// Nil context is refused before any RPC.
	var nilCtx context.Context
	if _, err := r.GetEntity(nilCtx, "c360.ops.sys1.intel.platform.uav1"); !errors.Is(err, ErrNilContext) {
		t.Fatalf("GetEntity(nilCtx) = %v, want ErrNilContext", err)
	}
	if _, err := r.GetBatch(nilCtx, []string{"c360.ops.sys1.intel.platform.uav1"}); !errors.Is(err, ErrNilContext) {
		t.Fatalf("GetBatch(nilCtx) = %v, want ErrNilContext", err)
	}
	if _, err := r.ScanPrefix(nilCtx, "c360.ops", 5); !errors.Is(err, ErrNilContext) {
		t.Fatalf("ScanPrefix(nilCtx) = %v, want ErrNilContext", err)
	}
	if _, err := r.ScanPattern(nilCtx, MatchAllPattern, 5); !errors.Is(err, ErrNilContext) {
		t.Fatalf("ScanPattern(nilCtx) = %v, want ErrNilContext", err)
	}
	if _, err := r.EntitiesByPredicate(nilCtx, "intel.role.assigned", 5); !errors.Is(err, ErrNilContext) {
		t.Fatalf("EntitiesByPredicate(nilCtx) = %v, want ErrNilContext", err)
	}
	if err := r.VerifyPatternCapability(nilCtx); !errors.Is(err, ErrNilContext) {
		t.Fatalf("VerifyPatternCapability(nilCtx) = %v, want ErrNilContext", err)
	}

	// Invalid selectors are rejected at entry before any NATS RPC is sent.
	if _, err := r.GetEntity(ctx, "invalid-id"); err == nil {
		t.Fatal("GetEntity(invalid) succeeded")
	}
	if _, err := r.GetBatch(ctx, []string{"c360.ops.sys1.intel.platform.uav1", "bad"}); err == nil {
		t.Fatal("GetBatch(invalid) succeeded")
	}
	if _, err := r.ScanPrefix(ctx, "", 10); err == nil {
		t.Fatal("ScanPrefix(empty) succeeded")
	}
	if _, err := r.ScanPrefix(ctx, "c360.*", 10); err == nil {
		t.Fatal("ScanPrefix(wildcard) succeeded")
	}
	if _, err := r.ScanPattern(ctx, "c360.ops.*", 10); err == nil {
		t.Fatal("ScanPattern(invalid) succeeded")
	}
	if _, err := r.EntitiesByPredicate(ctx, "   ", 10); err == nil {
		t.Fatal("EntitiesByPredicate(blank) succeeded")
	}
	if _, err := r.ScanPrefix(ctx, "c360.ops", -1); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("ScanPrefix(negative budget) = %v, want ErrScanBudgetExceeded", err)
	}
	if _, err := r.ScanPattern(ctx, MatchAllPattern, -1); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("ScanPattern(negative budget) = %v, want ErrScanBudgetExceeded", err)
	}
	if _, err := r.EntitiesByPredicate(ctx, "intel.role.assigned", -1); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("EntitiesByPredicate(negative budget) = %v, want ErrScanBudgetExceeded", err)
	}

	// Empty batch returns empty response with zero RPCs.
	batchResp, err := r.GetBatch(ctx, nil)
	if err != nil || len(batchResp.Entities) != 0 || len(batchResp.Missing) != 0 {
		t.Fatalf("GetBatch(nil) = (%+v, %v), want empty response", batchResp, err)
	}
	if got := stub.callCount(); got != 0 {
		t.Fatalf("stub RPC call count = %d, want 0 after validation and empty batch", got)
	}
}

func TestNATSRPCReaderNoLiteralQuerySubjectsInSource(t *testing.T) {
	// Enforce query-pattern skill rule: never spell a graph.ingest.query.* subject
	// as a literal in reader/ or cmd/semsparql/ production files.
	forbidden := "graph.ingest." + "query."
	for _, rel := range []string{"nats.go", "reader.go", "memory.go", "../cmd/semsparql/main.go"} {
		raw, err := os.ReadFile(filepath.Clean(rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if strings.Contains(string(raw), `"`+forbidden) {
			t.Fatalf("%s contains forbidden literal subject prefix %q; must read from graph.QueryVerbs()", rel, forbidden)
		}
	}
}

func TestNATSRPCReaderShortPagesContinuationAndFailClosed(t *testing.T) {
	ctx := t.Context()
	id1 := "c360.ops.sys1.intel.platform.uav1"
	id2 := "c360.ops.sys2.intel.platform.uav2"
	id3 := "c360.ops.sys3.intel.platform.uav3"

	fixedNow := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	mkEntity := func(id, role string) graph.EntityState {
		return graph.EntityState{
			ID: id,
			Triples: []message.Triple{
				{
					Subject:   id,
					Predicate: "intel.role.assigned",
					Object:    role,
					Source:    "test",
					Timestamp: fixedNow,
				},
			},
			UpdatedAt: fixedNow,
		}
	}

	// 1. Short pages with continuation cursor (including a 1-entity page and a 0-entity
	// page with a valid advancing NextCursor when req.Limit > 1) must continue until
	// NextCursor == "" and sort/deduplicate the complete set.
	pageStep := 0
	stub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, data []byte, _ time.Duration) ([]byte, error) {
			var req graph.PrefixQueryRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, err
			}
			pageStep++
			switch pageStep {
			case 1:
				if req.Cursor != "" {
					return nil, fmt.Errorf("page 1 expected empty cursor, got %q", req.Cursor)
				}
				// Short page of 1 entity (even though req.Limit == 4) in unsorted order.
				return json.Marshal(graph.PrefixQueryResponse{
					Entities:   []graph.EntityState{mkEntity(id3, "Strike")},
					NextCursor: "cursor-page-1",
				})
			case 2:
				if req.Cursor != "cursor-page-1" {
					return nil, fmt.Errorf("page 2 expected cursor-page-1, got %q", req.Cursor)
				}
				// Empty short page with advancing continuation cursor (e.g. concurrent delete of page key).
				return json.Marshal(graph.PrefixQueryResponse{
					Entities:   []graph.EntityState{},
					NextCursor: "cursor-page-2",
				})
			case 3:
				if req.Cursor != "cursor-page-2" {
					return nil, fmt.Errorf("page 3 expected cursor-page-2, got %q", req.Cursor)
				}
				return json.Marshal(graph.PrefixQueryResponse{
					Entities:   []graph.EntityState{mkEntity(id2, "Recon"), mkEntity(id1, "ISR")},
					NextCursor: "",
				})
			default:
				return nil, fmt.Errorf("unexpected page %d", pageStep)
			}
		},
	}

	r, err := NewNATSRPCReader(stub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	got, err := r.ScanPattern(ctx, "c360.ops.*.intel.platform.*", 3)
	if err != nil {
		t.Fatalf("ScanPattern across short pages failed: %v", err)
	}
	if pageStep != 3 {
		t.Fatalf("ScanPattern made %d page calls, want 3 (must not stop on short page)", pageStep)
	}
	if len(got) != 3 || got[0].ID != id1 || got[1].ID != id2 || got[2].ID != id3 {
		t.Fatalf("ScanPattern entities = %+v, want sorted [%s, %s, %s]", got, id1, id2, id3)
	}

	// 2. Non-advancing cursor fails closed with a fatal ClassifiedError instead of looping forever.
	stuckStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
			return json.Marshal(graph.PrefixQueryResponse{
				Entities:   []graph.EntityState{mkEntity(id1, "ISR")},
				NextCursor: "stuck-cursor",
			})
		},
	}
	stuckReader, err := NewNATSRPCReader(stuckStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if _, err := stuckReader.ScanPrefix(ctx, "c360.ops", 10); !errs.IsFatal(err) {
		t.Fatalf("ScanPrefix with non-advancing cursor err = %v, want fatal ClassifiedError", err)
	}

	// 3. Non-matching entity returned by an incompatible server fails closed with ErrPatternCapabilityUnsupported
	// rather than silently filtering on the client.
	mismatchStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
			return json.Marshal(graph.PrefixQueryResponse{
				Entities: []graph.EntityState{mkEntity("c360.ops.sys1.intel.sensor.radar1", "Radar")},
			})
		},
	}
	mismatchReader, err := NewNATSRPCReader(mismatchStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if _, err := mismatchReader.ScanPattern(ctx, "c360.ops.*.intel.platform.*", 10); !errors.Is(err, ErrPatternCapabilityUnsupported) {
		t.Fatalf("ScanPattern with non-matching server entity err = %v, want ErrPatternCapabilityUnsupported", err)
	}
	if _, err := mismatchReader.ScanPrefix(ctx, "c360.ops.sys1.intel.platform", 10); !errors.Is(err, ErrPatternCapabilityUnsupported) {
		t.Fatalf("ScanPrefix with non-matching server entity err = %v, want ErrPatternCapabilityUnsupported", err)
	}

	// 4. Unrequested entity in GetBatch fails closed as a state contract error.
	unrequestedBatchStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
			return json.Marshal(graph.EntityBatchResponse{
				Entities: []graph.EntityState{mkEntity(id2, "Recon")},
			})
		},
	}
	unreqReader, err := NewNATSRPCReader(unrequestedBatchStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if _, err := unreqReader.GetBatch(ctx, []string{id1}); !errs.IsFatal(err) {
		t.Fatalf("GetBatch with unrequested entity err = %v, want fatal state contract error", err)
	}

	// 5. Batch response omitting a requested ID from both Entities and Missing still records it as MissingNotFound.
	silentDropBatchStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
			return json.Marshal(graph.EntityBatchResponse{
				Entities: []graph.EntityState{mkEntity(id2, "Recon")},
				// id1 omitted from Missing by a pre-ADR-084 responder
			})
		},
	}
	dropReader, err := NewNATSRPCReader(silentDropBatchStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	batchOut, err := dropReader.GetBatch(ctx, []string{id2, id1})
	if err != nil {
		t.Fatalf("GetBatch: %v", err)
	}
	if len(batchOut.Entities) != 1 || batchOut.Entities[0].ID != id2 {
		t.Fatalf("batchOut.Entities = %+v, want [%s]", batchOut.Entities, id2)
	}
	if len(batchOut.Missing) != 1 || batchOut.Missing[0].ID != id1 || batchOut.Missing[0].Reason != graph.MissingNotFound {
		t.Fatalf("batchOut.Missing = %+v, want [{ID:%s Reason:not_found}]", batchOut.Missing, id1)
	}
}

func TestNATSRPCReaderVerifyPatternCapabilityRefusesIncompatibleServer(t *testing.T) {
	ctx := t.Context()

	// 1. Legacy pre-#259 responder ignores the unknown "pattern" field and returns 200 OK empty response.
	legacyStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
			return []byte(`{"entities":[]}`), nil
		},
	}
	legacyReader, err := NewNATSRPCReader(legacyStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if err := legacyReader.VerifyPatternCapability(ctx); !errors.Is(err, ErrPatternCapabilityUnsupported) {
		t.Fatalf("VerifyPatternCapability(legacy server) = %v, want ErrPatternCapabilityUnsupported", err)
	}

	// 2. Responder returning a transient/unexpected error on negative probe is refused.
	transientStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, error) {
			return nil, errs.ClassifiedCode(errs.ErrorTransient, graph.ErrorCodeInternal, errors.New("broker busy"))
		},
	}
	transientReader, err := NewNATSRPCReader(transientStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if err := transientReader.VerifyPatternCapability(ctx); !errors.Is(err, ErrPatternCapabilityUnsupported) || !errs.IsTransient(err) {
		t.Fatalf("VerifyPatternCapability(transient error) = %v, want ErrPatternCapabilityUnsupported wrapping transient error", err)
	}

	// 3. Capable responder rejects invalid pattern with ErrorCodeEntityIDPatternInvalid and accepts MatchAllPattern.
	capableStub := &stubClassifiedRequester{
		handler: func(_ context.Context, _ string, data []byte, _ time.Duration) ([]byte, error) {
			var req graph.PrefixQueryRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, errs.Classified(errs.ErrorInvalid, err)
			}
			if req.Pattern != "" {
				if err := types.ValidateEntityIDPattern(req.Pattern); err != nil {
					return nil, err
				}
			}
			return []byte(`{"entities":[]}`), nil
		},
	}
	capableReader, err := NewNATSRPCReader(capableStub, time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if err := capableReader.VerifyPatternCapability(ctx); err != nil {
		t.Fatalf("VerifyPatternCapability(capable server) = %v, want nil", err)
	}
}

type liveGraphIngestFixture struct {
	natsURL       string
	client        *natsclient.Client
	ingest        *graphingest.Component
	kvBucket      jetstream.KeyValue
	prefixQueries atomic.Int64
	mutations     atomic.Int64
}

func startLiveGraphIngestFixture(ctx context.Context, t *testing.T, maxPayload int32) *liveGraphIngestFixture {
	t.Helper()

	opts := &natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  t.TempDir(),
	}
	if maxPayload > 0 {
		opts.MaxPayload = maxPayload
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("natsserver.NewServer: %v", err)
	}
	ns.Start()
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS server not ready for connections")
	}

	nc, err := natsclient.NewClient(
		ns.ClientURL(),
		natsclient.WithTimeout(10*time.Second),
		natsclient.WithMaxReconnects(0),
		natsclient.WithHealthInterval(0),
	)
	if err != nil {
		t.Fatalf("natsclient.NewClient: %v", err)
	}
	if err := nc.Connect(ctx); err != nil {
		t.Fatalf("nc.Connect: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = nc.Close(closeCtx)
	})
	if err := nc.WaitForConnection(ctx); err != nil {
		t.Fatalf("nc.WaitForConnection: %v", err)
	}

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("nc.JetStream: %v", err)
	}
	if _, err := js.CreateStream(ctx, jetstream.StreamConfig{
		Name:     "ENTITY",
		Subjects: []string{"entity.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   time.Hour,
		MaxBytes: 64 << 20,
		Discard:  jetstream.DiscardOld,
	}); err != nil {
		t.Fatalf("CreateStream(ENTITY): %v", err)
	}

	reg := payloadregistry.New()
	for _, fn := range []func(*payloadregistry.Registry) error{
		message.RegisterPayloads,
		inference.RegisterPayloads,
		lifecycle.RegisterPayloads,
	} {
		if err := fn(reg); err != nil {
			t.Fatalf("register framework payloads: %v", err)
		}
	}
	if err := reg.Register(&payloadregistry.Registration{
		Domain:      "test",
		Category:    "entity",
		Version:     "v1",
		Description: "test entity type",
		Factory:     func() any { return &struct{}{} },
	}); err != nil {
		t.Fatalf("register test.entity.v1: %v", err)
	}

	cfg := graphingest.DefaultConfig()
	cfg.EnableHierarchy = false
	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal(DefaultConfig): %v", err)
	}
	comp, err := graphingest.CreateGraphIngest(cfgJSON, component.Dependencies{
		NATSClient:      nc,
		PayloadRegistry: reg,
		Platform:        component.PlatformMeta{Org: "c360", Platform: "ops"},
	})
	if err != nil {
		t.Fatalf("CreateGraphIngest: %v", err)
	}
	ingest := comp.(*graphingest.Component)
	if err := ingest.Initialize(); err != nil {
		t.Fatalf("ingest.Initialize: %v", err)
	}
	if err := ingest.Start(ctx); err != nil {
		t.Fatalf("ingest.Start: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = ingest.Stop(stopCtx)
	})

	kv, err := js.KeyValue(ctx, "ENTITY_STATES")
	if err != nil {
		t.Fatalf("KeyValue(ENTITY_STATES): %v", err)
	}

	fix := &liveGraphIngestFixture{
		natsURL:  ns.ClientURL(),
		client:   nc,
		ingest:   ingest,
		kvBucket: kv,
	}

	// Observe prefix query count and any forbidden graph.mutation.> publishes on the real broker.
	var prefixSub string
	for _, v := range graph.QueryVerbs() {
		if v.Name == "prefix" {
			prefixSub = v.Subject
		}
	}
	rawConn := nc.GetConnection()
	pSub, err := rawConn.Subscribe(prefixSub, func(*nats.Msg) {
		fix.prefixQueries.Add(1)
	})
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", prefixSub, err)
	}
	mSub, err := rawConn.Subscribe("graph.mutation.>", func(*nats.Msg) {
		fix.mutations.Add(1)
	})
	if err != nil {
		t.Fatalf("Subscribe(graph.mutation.>): %v", err)
	}
	if err := rawConn.Flush(); err != nil {
		t.Fatalf("Flush subscriptions: %v", err)
	}
	t.Cleanup(func() {
		_ = pSub.Unsubscribe()
		_ = mSub.Unsubscribe()
	})

	return fix
}

func (f *liveGraphIngestFixture) seedEntities(t *testing.T, entities ...graph.EntityState) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()

	for i := range entities {
		ent := entities[i].Clone()
		if ent.MessageType.Domain == "" {
			ent.MessageType = message.Type{Domain: "test", Category: "entity", Version: "v1"}
		}
		if ent.UpdatedAt.IsZero() {
			ent.UpdatedAt = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
		}
		for j := range ent.Triples {
			if ent.Triples[j].Source == "" {
				ent.Triples[j].Source = "test-fixture"
			}
			if ent.Triples[j].Timestamp.IsZero() {
				ent.Triples[j].Timestamp = ent.UpdatedAt
			}
		}
		if _, err := types.ParseEntityID(ent.ID); err != nil {
			t.Fatalf("ParseEntityID(%q): %v", ent.ID, err)
		}
		encoded, err := graph.MarshalEntityState(ent)
		if err != nil {
			t.Fatalf("MarshalEntityState(%q): %v", ent.ID, err)
		}
		if _, err := f.kvBucket.Put(ctx, ent.ID, encoded); err != nil {
			t.Fatalf("kvBucket.Put(%q): %v", ent.ID, err)
		}
	}
}

func TestScanPatternReaderParity(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	// Use a 4096-byte broker max_payload so graph-ingest's marshalFittingPrefixPage
	// splits the seeded entities across multiple opaque cursor pages on the real NATS wire.
	fix := startLiveGraphIngestFixture(ctx, t, 4096)

	fixedTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	padding := strings.Repeat("telemetry-payload-", 45) // ~810 bytes per entity so each page holds ~2 entities under 4 KiB
	fixtureEntities := []graph.EntityState{
		{
			ID:        "c360.ops.sys3.intel.platform.uav3",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "c360.ops.sys3.intel.platform.uav3", Predicate: "intel.role.assigned", Object: "Strike-" + padding, Source: "test-fixture", Timestamp: fixedTime},
				{Subject: "c360.ops.sys3.intel.platform.uav3", Predicate: "intel.measurement.altitude-m", Object: float64(3200.5), Source: "test-fixture", Timestamp: fixedTime},
			},
		},
		{
			ID:        "c360.ops.sys1.intel.platform.uav1",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "c360.ops.sys1.intel.platform.uav1", Predicate: "intel.role.assigned", Object: "ISR-" + padding, Source: "test-fixture", Timestamp: fixedTime},
				{Subject: "c360.ops.sys1.intel.platform.uav1", Predicate: "intel.measurement.altitude-m", Object: float64(1500.25), Source: "test-fixture", Timestamp: fixedTime},
			},
		},
		{
			ID:        "c360.ops.sys2.intel.platform.uav2",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "c360.ops.sys2.intel.platform.uav2", Predicate: "intel.role.assigned", Object: "Recon-" + padding, Source: "test-fixture", Timestamp: fixedTime},
			},
		},
		{
			ID:        "c360.ops.sys1.intel.sensor.radar1",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "c360.ops.sys1.intel.sensor.radar1", Predicate: "intel.sensor.band", Object: "X-Band-" + padding, Source: "test-fixture", Timestamp: fixedTime},
			},
		},
		{
			ID:        "c360.ops.sys2.intel.sensor.eo1",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "c360.ops.sys2.intel.sensor.eo1", Predicate: "intel.sensor.band", Object: "EO-IR-" + padding, Source: "test-fixture", Timestamp: fixedTime},
			},
		},
		{
			ID:        "c360.ops.sys4.intel.platform.uav4",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "c360.ops.sys4.intel.platform.uav4", Predicate: "intel.role.assigned", Object: "Relay-" + padding, Source: "test-fixture", Timestamp: fixedTime},
			},
		},
		{
			ID:        "ally.eu.sys2.intel.platform.uav9",
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: "ally.eu.sys2.intel.platform.uav9", Predicate: "intel.role.assigned", Object: "Coalition-" + padding, Source: "test-fixture", Timestamp: fixedTime},
			},
		},
	}
	fix.seedEntities(t, fixtureEntities...)

	memReader, err := NewMemoryReader(fixtureEntities...)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}
	natsReader, err := NewNATSRPCReader(fix.client, 10*time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}

	// Verify startup pattern capability against real graph-ingest.
	if err := natsReader.VerifyPatternCapability(ctx); err != nil {
		t.Fatalf("VerifyPatternCapability against live graph-ingest failed: %v", err)
	}

	// Record KV revisions before reader calls to prove zero ENTITY_STATES writes.
	beforeRevs := snapshotKVRevisions(ctx, t, fix.kvBucket)

	// 1. Pattern parity across wildcard systems, wildcard org/platform, and opaque cursor pages.
	patterns := []string{
		"c360.ops.*.intel.platform.*",
		"c360.ops.sys1.intel.*.*",
		"*.*.sys2.intel.platform.*",
		"*.*.*.intel.sensor.*",
		"c360.ops.*.intel.nonexistent.*",
		MatchAllPattern,
	}
	for _, pat := range patterns {
		beforePrefixCalls := fix.prefixQueries.Load()
		want, wantErr := memReader.ScanPattern(ctx, pat, 20)
		got, gotErr := natsReader.ScanPattern(ctx, pat, 20)
		if (wantErr != nil) != (gotErr != nil) {
			t.Fatalf("ScanPattern(%q) err = %v, want %v", pat, gotErr, wantErr)
		}
		assertEntityIDsAndTriplesMatch(t, fmt.Sprintf("ScanPattern(%q)", pat), got, want)
		if pat == MatchAllPattern {
			pageCalls := fix.prefixQueries.Load() - beforePrefixCalls
			if pageCalls < 3 {
				t.Fatalf("ScanPattern(%q) made %d prefix RPC calls, want >= 3 multi-page cursor calls under 4 KiB max_payload", pat, pageCalls)
			}
		}
	}

	// 2. ScanPattern budget parity (exact budget succeeds; budget-1 fails with ErrScanBudgetExceeded).
	for _, budget := range []int{0, 1, 3, 4} {
		want, wantErr := memReader.ScanPattern(ctx, "c360.ops.*.intel.platform.*", budget)
		got, gotErr := natsReader.ScanPattern(ctx, "c360.ops.*.intel.platform.*", budget)
		if errors.Is(wantErr, ErrScanBudgetExceeded) != errors.Is(gotErr, ErrScanBudgetExceeded) {
			t.Fatalf("ScanPattern budget=%d err = %v, want %v", budget, gotErr, wantErr)
		}
		if wantErr == nil {
			assertEntityIDsAndTriplesMatch(t, fmt.Sprintf("ScanPattern(budget=%d)", budget), got, want)
		}
	}

	// 3. ScanPrefix parity across multi-page cursors and exact 6-part prefix.
	for _, prefix := range []string{
		"c360",
		"c360.ops",
		"c360.ops.sys1",
		"c360.ops.sys1.intel.platform.uav1",
		"c360.ops.sys9",
	} {
		want, wantErr := memReader.ScanPrefix(ctx, prefix, 20)
		got, gotErr := natsReader.ScanPrefix(ctx, prefix, 20)
		if (wantErr != nil) != (gotErr != nil) {
			t.Fatalf("ScanPrefix(%q) err = %v, want %v", prefix, gotErr, wantErr)
		}
		assertEntityIDsAndTriplesMatch(t, fmt.Sprintf("ScanPrefix(%q)", prefix), got, want)
	}
	if _, err := natsReader.ScanPrefix(ctx, "c360.ops", 2); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("ScanPrefix(budget=2) = %v, want ErrScanBudgetExceeded", err)
	}

	// 4. GetEntity parity (found and missing).
	for _, id := range []string{
		"c360.ops.sys1.intel.platform.uav1",
		"ally.eu.sys2.intel.platform.uav9",
		"c360.ops.sys1.intel.platform.missing",
	} {
		want, wantErr := memReader.GetEntity(ctx, id)
		got, gotErr := natsReader.GetEntity(ctx, id)
		if errors.Is(wantErr, ErrEntityNotFound) != errors.Is(gotErr, ErrEntityNotFound) {
			t.Fatalf("GetEntity(%q) err = %v, want %v", id, gotErr, wantErr)
		}
		if wantErr == nil {
			assertEntityIDsAndTriplesMatch(t, fmt.Sprintf("GetEntity(%q)", id), []graph.EntityState{*got}, []graph.EntityState{*want})
		}
	}

	// 5. GetBatch parity (found, missing, duplicates, unsorted order).
	batchReq := []string{
		"c360.ops.sys3.intel.platform.uav3",
		"c360.ops.sys1.intel.platform.ghost1",
		"c360.ops.sys1.intel.platform.uav1",
		"c360.ops.sys3.intel.platform.uav3",
		"ally.eu.sys2.intel.platform.uav9",
		"c360.ops.sys9.intel.platform.ghost2",
	}
	wantBatch, err := memReader.GetBatch(ctx, batchReq)
	if err != nil {
		t.Fatalf("memReader.GetBatch: %v", err)
	}
	gotBatch, err := natsReader.GetBatch(ctx, batchReq)
	if err != nil {
		t.Fatalf("natsReader.GetBatch: %v", err)
	}
	assertEntityIDsAndTriplesMatch(t, "GetBatch.Entities", gotBatch.Entities, wantBatch.Entities)
	if !slices.Equal(gotBatch.Missing, wantBatch.Missing) {
		t.Fatalf("GetBatch.Missing = %+v, want %+v", gotBatch.Missing, wantBatch.Missing)
	}

	// 6. EntitiesByPredicate parity (matched, unmatched, and budget exceeded).
	for _, pred := range []string{"intel.role.assigned", "intel.sensor.band", "intel.unknown.pred"} {
		want, err := memReader.EntitiesByPredicate(ctx, pred, 20)
		if err != nil {
			t.Fatalf("memReader.EntitiesByPredicate(%q): %v", pred, err)
		}
		got, err := natsReader.EntitiesByPredicate(ctx, pred, 20)
		if err != nil {
			t.Fatalf("natsReader.EntitiesByPredicate(%q): %v", pred, err)
		}
		assertEntityIDsAndTriplesMatch(t, fmt.Sprintf("EntitiesByPredicate(%q)", pred), got, want)
	}
	if _, err := natsReader.EntitiesByPredicate(ctx, "intel.role.assigned", 3); !errors.Is(err, ErrScanBudgetExceeded) {
		t.Fatalf("EntitiesByPredicate(budget=3) = %v, want ErrScanBudgetExceeded", err)
	}

	// 7. Rapid property check: generated 6-slot patterns and budgets over the live NATS fixture
	// agree 100% between MemoryReader and NATSRPCReader.
	rapid.Check(t, func(rt *rapid.T) {
		orgSlot := rapid.SampledFrom([]string{"*", "c360", "ally", "other"}).Draw(rt, "org")
		platSlot := rapid.SampledFrom([]string{"*", "ops", "eu", "other"}).Draw(rt, "platform")
		sysSlot := rapid.SampledFrom([]string{"*", "sys1", "sys2", "sys3", "sys4", "sys9"}).Draw(rt, "system")
		domSlot := rapid.SampledFrom([]string{"*", "intel", "other"}).Draw(rt, "domain")
		typeSlot := rapid.SampledFrom([]string{"*", "platform", "sensor", "other"}).Draw(rt, "type")
		instSlot := rapid.SampledFrom([]string{"*", "uav1", "uav2", "uav3", "uav9", "radar1"}).Draw(rt, "instance")
		pattern := strings.Join([]string{orgSlot, platSlot, sysSlot, domSlot, typeSlot, instSlot}, ".")
		budget := rapid.IntRange(0, len(fixtureEntities)+1).Draw(rt, "budget")

		want, wantErr := memReader.ScanPattern(ctx, pattern, budget)
		got, gotErr := natsReader.ScanPattern(ctx, pattern, budget)
		if errors.Is(wantErr, ErrScanBudgetExceeded) != errors.Is(gotErr, ErrScanBudgetExceeded) {
			rt.Fatalf("ScanPattern(%q, budget=%d) err = %v, want %v", pattern, budget, gotErr, wantErr)
		}
		if (wantErr != nil) != (gotErr != nil) {
			rt.Fatalf("ScanPattern(%q, budget=%d) err = %v, want %v", pattern, budget, gotErr, wantErr)
		}
		if wantErr == nil {
			assertEntityIDsAndTriplesMatch(t, fmt.Sprintf("Rapid ScanPattern(%q, budget=%d)", pattern, budget), got, want)
		}
	})

	// 8. Prove zero writes to ENTITY_STATES and zero graph.mutation.* publishes.
	afterRevs := snapshotKVRevisions(ctx, t, fix.kvBucket)
	if !slices.Equal(beforeRevs, afterRevs) {
		t.Fatalf("ENTITY_STATES KV revisions changed during reader operations: before=%v after=%v", beforeRevs, afterRevs)
	}
	if muts := fix.mutations.Load(); muts != 0 {
		t.Fatalf("observed %d graph.mutation.> messages during reader operations, want 0", muts)
	}
}

func snapshotKVRevisions(ctx context.Context, t *testing.T, kv jetstream.KeyValue) []string {
	t.Helper()
	keys, err := kv.Keys(ctx)
	if err != nil {
		t.Fatalf("kv.Keys: %v", err)
	}
	slices.Sort(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		entry, err := kv.Get(ctx, k)
		if err != nil {
			t.Fatalf("kv.Get(%q): %v", k, err)
		}
		out = append(out, fmt.Sprintf("%s@%d", k, entry.Revision()))
	}
	return out
}

func assertEntityIDsAndTriplesMatch(t *testing.T, label string, got, want []graph.EntityState) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: len(got)=%d, len(want)=%d (got=%+v, want=%+v)", label, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("%s[%d].ID = %q, want %q", label, i, got[i].ID, want[i].ID)
		}
		if len(got[i].Triples) != len(want[i].Triples) {
			t.Fatalf("%s[%d] (%s) triples count = %d, want %d", label, i, got[i].ID, len(got[i].Triples), len(want[i].Triples))
		}
		for j := range want[i].Triples {
			gTr := got[i].Triples[j]
			wTr := want[i].Triples[j]
			if gTr.Subject != wTr.Subject || gTr.Predicate != wTr.Predicate || gTr.Object != wTr.Object || gTr.Datatype != wTr.Datatype {
				t.Fatalf("%s[%d].Triples[%d] = %+v, want %+v", label, i, j, gTr, wTr)
			}
		}
	}
}
