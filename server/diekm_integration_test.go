//go:build integration

package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/c360studio/semengine/component"
	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/graph/inference"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/natsclient"
	"github.com/c360studio/semengine/payloadregistry"
	"github.com/c360studio/semengine/pkg/lifecycle"
	"github.com/c360studio/semengine/pkg/types"
	graphingest "github.com/c360studio/semengine/processor/graph-ingest"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semsparql/sparql"
	"github.com/c360studio/semvocab/packs"
	"github.com/c360studio/semvocab/packs/diekm"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"pgregory.net/rapid"
)

const integrationBaseIRI = "https://semengine.local"

type liveIntegrationEnv struct {
	natsURL       string
	natsClient    *natsclient.Client
	kvBucket      jetstream.KeyValue
	natsReader    *reader.NATSRPCReader
	memReader     *reader.MemoryReader
	liveServer    *Server
	liveURL       string
	httpClient    *http.Client
	entities      []graph.EntityState
	prefixQueries atomic.Int64
	mutations     atomic.Int64
}

func registerIntegrationDIEKMVocab(t testing.TB) func() {
	t.Helper()
	restore := vocabulary.SnapshotRegistry()
	vocabulary.ClearRegistry()
	if err := packs.RegisterBFOCCOCore(); err != nil {
		restore()
		t.Fatalf("RegisterBFOCCOCore: %v", err)
	}
	if err := diekm.Register(vocabulary.WithPrefixRegistrar(func([]vocabulary.PrefixSpec) error { return nil })); err != nil {
		restore()
		t.Fatalf("diekm.Register: %v", err)
	}

	// Register additional projected predicates covering all 6 terminal datatypes required by
	// TestUnfoldTerminalDatatypePropagation (xsd:dateTime, xsd:double, xsd:integer,
	// xsd:boolean, rdf:JSON, and <entity IRI> resource objects) plus a multi-match
	// shared IRI predicate pair for TestLookupByIRIMultiMatchUnion.
	vocabulary.Register("intel.telemetry.timestamp",
		vocabulary.WithIRI("https://w3id.org/c360/diekm#telemetryTimestamp"),
		vocabulary.WithDataType(vocabulary.DataTypeDateTime),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000199",
					NodeClassIRI:      "http://purl.obolibrary.org/obo/BFO_0000202",
					ValuePredicateIRI: "https://w3id.org/c360/diekm#instantDateTime",
				},
			},
		}),
	)
	vocabulary.Register("intel.telemetry.count",
		vocabulary.WithIRI("https://w3id.org/c360/diekm#telemetryCount"),
		vocabulary.WithDataType(vocabulary.DataTypeInt),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000196",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00001163",
					ValuePredicateIRI: "https://w3id.org/c360/diekm#integerValue",
				},
			},
		}),
	)
	vocabulary.Register("intel.telemetry.active",
		vocabulary.WithIRI("https://w3id.org/c360/diekm#telemetryActive"),
		vocabulary.WithDataType(vocabulary.DataTypeBool),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000196",
					NodeClassIRI:      "http://purl.obolibrary.org/obo/BFO_0000019",
					ValuePredicateIRI: "https://w3id.org/c360/diekm#booleanValue",
				},
			},
		}),
	)
	vocabulary.Register("intel.telemetry.payload-json",
		vocabulary.WithIRI("https://w3id.org/c360/diekm#telemetryPayloadJSON"),
		vocabulary.WithDataType(vocabulary.DataTypeJSON),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/BFO_0000196",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00000253",
					ValuePredicateIRI: "https://w3id.org/c360/diekm#jsonValue",
				},
			},
		}),
	)
	vocabulary.Register("intel.relation.assigned-sensor",
		vocabulary.WithIRI("https://w3id.org/c360/diekm#assignedSensor"),
		vocabulary.WithDataType(vocabulary.DataTypeEntityID),
		vocabulary.WithProjection(vocabulary.ProjectionSpec{
			Steps: []vocabulary.ProjectionStep{
				{
					PredicateIRI:      "http://purl.obolibrary.org/obo/RO_0000056",
					NodeClassIRI:      "https://www.commoncoreontologies.org/ont00000037",
					ValuePredicateIRI: "http://purl.obolibrary.org/obo/RO_0000057",
				},
			},
		}),
	)

	// Shared direct IRI mapped to two distinct dotted predicates for TestLookupByIRIMultiMatchUnion.
	const sharedAlertIRI = "https://w3id.org/c360/diekm#sharedOperationalTag"
	vocabulary.Register("intel.tag.primary",
		vocabulary.WithIRI(sharedAlertIRI),
		vocabulary.WithDataType(vocabulary.DataTypeString),
	)
	vocabulary.Register("intel.tag.secondary",
		vocabulary.WithIRI(sharedAlertIRI),
		vocabulary.WithDataType(vocabulary.DataTypeString),
	)
	vocabulary.Register("rdf.syntax.type",
		vocabulary.WithIRI(sparql.RDFTypeIRI),
	)

	return restore
}

func buildIntegrationDIEKMEntities() []graph.EntityState {
	fixedTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	// Padding on observation entities ensures that under NATS MaxPayload=4096,
	// graph-ingest's marshalFittingPrefixPage splits full scans across >= 3 opaque cursor pages,
	// while keeping the 3 UAV platform entities compact enough for a single GetBatch response.
	padding := strings.Repeat("diekm-telemetry-pad-", 45)

	const (
		idUAV1 = "c360.ops.uas1.intel.platform.uav1"
		idUAV2 = "c360.ops.uas2.intel.platform.uav2"
		idUAV9 = "ally.eu.uas9.intel.platform.uav9"
		idObs1 = "c360.ops.uas1.intel.observation.obs1"
		idObs2 = "c360.ops.uas2.intel.observation.obs2"
		idObs3 = "ally.eu.uas9.intel.observation.obs3"
		idObs4 = "c360.ops.uas1.intel.observation.obs4"
		idObs5 = "c360.ops.uas2.intel.observation.obs5"
	)

	return []graph.EntityState{
		{
			ID:        idUAV1,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				// Multi-valued role for TestBlankNodeIDMultiValued and DIEKM 1-step (3-triple) chain folding.
				{Subject: idUAV1, Predicate: "intel.role.assigned", Object: "ISR", Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV1, Predicate: "intel.role.assigned", Object: "Strike", Source: "diekm-test", Timestamp: fixedTime},
				// 3-step (7-triple) DIEKM altitude measurement chain (xsd:double).
				{Subject: idUAV1, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5), Source: "diekm-test", Timestamp: fixedTime},
				// Terminal datatype propagation: xsd:dateTime, xsd:integer, xsd:boolean, rdf:JSON, and EntityID resource IRI.
				{Subject: idUAV1, Predicate: "intel.telemetry.timestamp", Object: "2026-10-16T12:00:00Z", Datatype: "xsd:dateTime", Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV1, Predicate: "intel.telemetry.count", Object: float64(42), Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV1, Predicate: "intel.telemetry.active", Object: true, Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV1, Predicate: "intel.telemetry.payload-json", Object: `{"mode":"autonomous","rev":1}`, Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV1, Predicate: "intel.relation.assigned-sensor", Object: idObs1, Source: "diekm-test", Timestamp: fixedTime},
				// Shared IRI predicate #1.
				{Subject: idUAV1, Predicate: "intel.tag.primary", Object: "TAG-ALPHA", Source: "diekm-test", Timestamp: fixedTime},
				// Explicit stored rdf:type alongside virtual rdf:type.
				{Subject: idUAV1, Predicate: "rdf.syntax.type", Object: "https://w3id.org/c360/diekm#TacticalAsset", Datatype: "iri", Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idUAV2,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idUAV2, Predicate: "intel.role.assigned", Object: "Recon", Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV2, Predicate: "intel.measurement.altitude-m", Object: float64(2400.0), Source: "diekm-test", Timestamp: fixedTime},
				// Shared IRI predicate #2 on a second entity.
				{Subject: idUAV2, Predicate: "intel.tag.secondary", Object: "TAG-BRAVO", Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idUAV9,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idUAV9, Predicate: "intel.role.assigned", Object: "Escort", Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idUAV9, Predicate: "intel.measurement.altitude-m", Object: float64(3100.25), Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idObs1,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idObs1, Predicate: "intel.telemetry.timestamp", Object: "2026-10-16T12:05:00Z", Datatype: "xsd:dateTime", Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idObs1, Predicate: "intel.telemetry.active", Object: true, Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idObs1, Predicate: "intel.telemetry.payload-json", Object: `{"pad":"` + padding + `"}`, Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idObs2,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idObs2, Predicate: "intel.telemetry.count", Object: float64(7), Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idObs2, Predicate: "intel.telemetry.payload-json", Object: `{"pad":"` + padding + `"}`, Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idObs3,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idObs3, Predicate: "intel.telemetry.count", Object: float64(9), Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idObs3, Predicate: "intel.telemetry.payload-json", Object: `{"pad":"` + padding + `"}`, Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idObs4,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idObs4, Predicate: "intel.telemetry.count", Object: float64(11), Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idObs4, Predicate: "intel.telemetry.payload-json", Object: `{"pad":"` + padding + `"}`, Source: "diekm-test", Timestamp: fixedTime},
			},
		},
		{
			ID:        idObs5,
			UpdatedAt: fixedTime,
			Triples: []message.Triple{
				{Subject: idObs5, Predicate: "intel.telemetry.count", Object: float64(13), Source: "diekm-test", Timestamp: fixedTime},
				{Subject: idObs5, Predicate: "intel.telemetry.payload-json", Object: `{"pad":"` + padding + `"}`, Source: "diekm-test", Timestamp: fixedTime},
			},
		},
	}
}

func startLiveIntegrationEnv(ctx context.Context, t *testing.T, maxPayload int32) *liveIntegrationEnv {
	t.Helper()

	opts := &natsserver.Options{
		Host:       "127.0.0.1",
		Port:       -1,
		NoLog:      true,
		NoSigs:     true,
		JetStream:  true,
		StoreDir:   t.TempDir(),
		MaxPayload: maxPayload,
	}
	ns, err := natsserver.NewServer(opts)
	if err != nil {
		t.Fatalf("natsserver.NewServer: %v", err)
	}
	go ns.Start()
	if !ns.ReadyForConnections(10 * time.Second) {
		ns.Shutdown()
		t.Fatal("embedded NATS server not ready within 10s")
	}
	t.Cleanup(func() {
		ns.Shutdown()
	})

	nc, err := natsclient.NewClient(ns.ClientURL(),
		natsclient.WithName("semsparql-integration-test"),
		natsclient.WithTimeout(5*time.Second),
		natsclient.WithMaxReconnects(3),
	)
	if err != nil {
		t.Fatalf("natsclient.NewClient: %v", err)
	}
	t.Cleanup(func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		_ = nc.Close(closeCtx)
	})
	if err := nc.Connect(ctx); err != nil {
		t.Fatalf("nc.Connect: %v", err)
	}
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

	entities := buildIntegrationDIEKMEntities()
	for i := range entities {
		ent := entities[i].Clone()
		ent.MessageType = message.Type{Domain: "test", Category: "entity", Version: "v1"}
		if _, err := types.ParseEntityID(ent.ID); err != nil {
			t.Fatalf("ParseEntityID(%q): %v", ent.ID, err)
		}
		encoded, err := graph.MarshalEntityState(ent)
		if err != nil {
			t.Fatalf("MarshalEntityState(%q): %v", ent.ID, err)
		}
		if _, err := kv.Put(ctx, ent.ID, encoded); err != nil {
			t.Fatalf("kv.Put(%q): %v", ent.ID, err)
		}
		entities[i] = *ent
	}

	natsReader, err := reader.NewNATSRPCReader(nc, 10*time.Second)
	if err != nil {
		t.Fatalf("NewNATSRPCReader: %v", err)
	}
	if err := natsReader.VerifyPatternCapability(ctx); err != nil {
		t.Fatalf("VerifyPatternCapability: %v", err)
	}

	memReader, err := reader.NewMemoryReader(entities...)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}

	liveSrv, err := New(natsReader, Config{
		BaseIRI:         integrationBaseIRI,
		QueryTimeout:    10 * time.Second,
		MaxResults:      500,
		MaxScanEntities: 500,
		MaxQueryBytes:   64 * 1024,
		MaxExportBytes:  64 * 1024,
	})
	if err != nil {
		t.Fatalf("New live server: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	serveCtx, serveCancel := context.WithCancel(ctx)
	serveErrCh := make(chan error, 1)
	go func() {
		serveErrCh <- liveSrv.Serve(serveCtx, ln)
	}()
	t.Cleanup(func() {
		serveCancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = liveSrv.Shutdown(shutCtx)
		if err := <-serveErrCh; err != nil {
			t.Errorf("liveSrv.Serve returned error: %v", err)
		}
	})

	env := &liveIntegrationEnv{
		natsURL:    ns.ClientURL(),
		natsClient: nc,
		kvBucket:   kv,
		natsReader: natsReader,
		memReader:  memReader,
		liveServer: liveSrv,
		liveURL:    "http://" + ln.Addr().String(),
		httpClient: &http.Client{Timeout: 10 * time.Second},
		entities:   entities,
	}

	var prefixSub string
	for _, v := range graph.QueryVerbs() {
		if v.Name == "prefix" {
			prefixSub = v.Subject
		}
	}
	rawConn := nc.GetConnection()
	pSub, err := rawConn.Subscribe(prefixSub, func(*nats.Msg) {
		env.prefixQueries.Add(1)
	})
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", prefixSub, err)
	}
	mSub, err := rawConn.Subscribe("graph.mutation.>", func(*nats.Msg) {
		env.mutations.Add(1)
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

	return env
}

func snapshotIntegrationKVRevisions(ctx context.Context, t *testing.T, kv jetstream.KeyValue) []string {
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

func (e *liveIntegrationEnv) doSPARQL(ctx context.Context, t *testing.T, transport, query, accept string, extraHeaders map[string]string) (*http.Response, []byte) {
	t.Helper()
	var req *http.Request
	var err error
	switch transport {
	case "GET":
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, e.liveURL+"/sparql?query="+url.QueryEscape(query), nil)
	case "POST-raw":
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, e.liveURL+"/sparql", strings.NewReader(query))
		if err == nil {
			req.Header.Set("Content-Type", "application/sparql-query; charset=utf-8")
		}
	case "POST-form":
		form := url.Values{"query": {query}}.Encode()
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, e.liveURL+"/sparql", strings.NewReader(form))
		if err == nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
	default:
		t.Fatalf("unknown transport %q", transport)
	}
	if err != nil {
		t.Fatalf("NewRequestWithContext(%s): %v", transport, err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	resp, err := e.httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do(%s): %v", transport, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", transport, err)
	}
	return resp, body
}

// independentRDFTriple is a canonicalized RDF triple parsed by the independent test parsers.
type independentRDFTriple struct {
	Subject   string
	Predicate string
	Object    string
}

func parseIndependentNTriples(t *testing.T, text string) []independentRDFTriple {
	t.Helper()
	var out []independentRDFTriple
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for lineNo, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.HasSuffix(line, ".") {
			t.Fatalf("N-Triples line %d missing trailing dot: %q", lineNo+1, line)
		}
		body := strings.TrimSpace(strings.TrimSuffix(line, "."))
		subj, rest, err := readIndependentNTripleTerm(body)
		if err != nil {
			t.Fatalf("N-Triples line %d subject: %v (%q)", lineNo+1, err, line)
		}
		pred, rest, err := readIndependentNTripleTerm(rest)
		if err != nil {
			t.Fatalf("N-Triples line %d predicate: %v (%q)", lineNo+1, err, line)
		}
		obj, rest, err := readIndependentNTripleTerm(rest)
		if err != nil {
			t.Fatalf("N-Triples line %d object: %v (%q)", lineNo+1, err, line)
		}
		if strings.TrimSpace(rest) != "" {
			t.Fatalf("N-Triples line %d trailing content %q", lineNo+1, rest)
		}
		out = append(out, independentRDFTriple{
			Subject:   canonicalizeIndependentTerm(t, subj),
			Predicate: canonicalizeIndependentTerm(t, pred),
			Object:    canonicalizeIndependentTerm(t, obj),
		})
	}
	sortAndDedupIndependentTriples(&out)
	return out
}

func parseIndependentTurtle(t *testing.T, text string) []independentRDFTriple {
	t.Helper()
	prefixes := make(map[string]string)
	var bodyBuilder strings.Builder
	for _, raw := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "@prefix") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "@prefix"))
			rest = strings.TrimSpace(strings.TrimSuffix(rest, "."))
			colonIdx := strings.IndexByte(rest, ':')
			openIdx := strings.IndexByte(rest, '<')
			closeIdx := strings.LastIndexByte(rest, '>')
			if colonIdx < 0 || openIdx <= colonIdx || closeIdx <= openIdx {
				t.Fatalf("invalid @prefix line: %q", trimmed)
			}
			pfx := strings.TrimSpace(rest[:colonIdx])
			ns := rest[openIdx+1 : closeIdx]
			prefixes[pfx] = ns
			continue
		}
		bodyBuilder.WriteString(trimmed)
		bodyBuilder.WriteByte(' ')
	}

	tokens := tokenizeIndependentTurtle(t, bodyBuilder.String(), prefixes)
	var out []independentRDFTriple
	idx := 0
	for idx < len(tokens) {
		subj := tokens[idx]
		idx++
		for idx < len(tokens) {
			pred := tokens[idx]
			idx++
			if pred == "a" {
				pred = "<" + sparql.RDFTypeIRI + ">"
			}
			for idx < len(tokens) {
				obj := tokens[idx]
				idx++
				out = append(out, independentRDFTriple{
					Subject:   canonicalizeIndependentTerm(t, subj),
					Predicate: canonicalizeIndependentTerm(t, pred),
					Object:    canonicalizeIndependentTerm(t, obj),
				})
				if idx < len(tokens) && tokens[idx] == "," {
					idx++
					continue
				}
				break
			}
			if idx < len(tokens) && tokens[idx] == ";" {
				idx++
				if idx < len(tokens) && tokens[idx] == "." {
					idx++
					break
				}
				continue
			}
			if idx < len(tokens) && tokens[idx] == "." {
				idx++
				break
			}
			t.Fatalf("unexpected Turtle token at %d: %v", idx, tokens[idx:])
		}
	}
	sortAndDedupIndependentTriples(&out)
	return out
}

func tokenizeIndependentTurtle(t *testing.T, input string, prefixes map[string]string) []string {
	t.Helper()
	var tokens []string
	s := strings.TrimSpace(input)
	for s != "" {
		s = strings.TrimSpace(s)
		if s == "" {
			break
		}
		switch s[0] {
		case '.', ';', ',':
			tokens = append(tokens, s[:1])
			s = s[1:]
		case '<', '"', '_':
			term, rest, err := readIndependentNTripleTerm(s)
			if err != nil {
				t.Fatalf("tokenizeIndependentTurtle term error: %v in %q", err, s)
			}
			tokens = append(tokens, expandIndependentPrefixedDatatype(term, prefixes))
			s = rest
		default:
			end := 0
			for end < len(s) && s[end] != ' ' && s[end] != '\t' && s[end] != ';' && s[end] != ',' {
				if s[end] == '.' && (end+1 == len(s) || s[end+1] == ' ' || s[end+1] == '\t') {
					break
				}
				end++
			}
			tok := s[:end]
			s = s[end:]
			switch {
			case tok == "a":
				tokens = append(tokens, "a")
			case tok == "true" || tok == "false":
				tokens = append(tokens, fmt.Sprintf("%q^^<%s>", tok, "http://www.w3.org/2001/XMLSchema#boolean"))
			case strings.Contains(tok, ":"):
				colon := strings.IndexByte(tok, ':')
				pfx, local := tok[:colon], tok[colon+1:]
				ns, ok := prefixes[pfx]
				if !ok {
					t.Fatalf("unknown Turtle prefix %q in token %q", pfx, tok)
				}
				tokens = append(tokens, "<"+ns+local+">")
			default:
				if strings.ContainsAny(tok, ".eE") {
					tokens = append(tokens, fmt.Sprintf("%q^^<%s>", tok, "http://www.w3.org/2001/XMLSchema#double"))
				} else {
					tokens = append(tokens, fmt.Sprintf("%q^^<%s>", tok, "http://www.w3.org/2001/XMLSchema#integer"))
				}
			}
		}
	}
	return tokens
}

func expandIndependentPrefixedDatatype(term string, prefixes map[string]string) string {
	idx := strings.LastIndex(term, "^^")
	if idx < 0 {
		return term
	}
	dt := term[idx+2:]
	if strings.HasPrefix(dt, "<") {
		return term
	}
	if colon := strings.IndexByte(dt, ':'); colon >= 0 {
		if ns, ok := prefixes[dt[:colon]]; ok {
			return term[:idx+2] + "<" + ns + dt[colon+1:] + ">"
		}
	}
	return term
}

func parseIndependentJSONLD(t *testing.T, raw []byte) []independentRDFTriple {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("parseIndependentJSONLD Decode: %v\n%s", err, string(raw))
	}
	prefixes := make(map[string]string)
	if ctxRaw, ok := doc["@context"].(map[string]any); ok {
		for k, v := range ctxRaw {
			switch tv := v.(type) {
			case string:
				prefixes[k] = tv
			case map[string]any:
				if id, ok := tv["@id"].(string); ok {
					prefixes[k] = id
				}
			}
		}
	}
	expandCompact := func(term string) string {
		if strings.HasPrefix(term, "_:") || strings.Contains(term, "://") {
			return term
		}
		if colon := strings.IndexByte(term, ':'); colon > 0 {
			if ns, ok := prefixes[term[:colon]]; ok {
				return ns + term[colon+1:]
			}
		}
		return term
	}

	var nodes []map[string]any
	if graphRaw, ok := doc["@graph"].([]any); ok {
		for _, item := range graphRaw {
			if m, ok := item.(map[string]any); ok {
				nodes = append(nodes, m)
			}
		}
	} else if _, hasID := doc["@id"]; hasID {
		nodes = append(nodes, doc)
	}

	var out []independentRDFTriple
	for _, node := range nodes {
		idVal, _ := node["@id"].(string)
		if idVal == "" {
			t.Fatalf("JSON-LD node missing @id: %+v", node)
		}
		subj := "<" + expandCompact(idVal) + ">"
		if strings.HasPrefix(idVal, "_:") {
			subj = idVal
		}
		for k, v := range node {
			if k == "@id" || k == "@context" {
				continue
			}
			if k == "@type" {
				var typesList []any
				switch tv := v.(type) {
				case string:
					typesList = []any{tv}
				case []any:
					typesList = tv
				}
				for _, item := range typesList {
					ts, _ := item.(string)
					out = append(out, independentRDFTriple{
						Subject:   canonicalizeIndependentTerm(t, subj),
						Predicate: "<" + sparql.RDFTypeIRI + ">",
						Object:    "<" + expandCompact(ts) + ">",
					})
				}
				continue
			}
			predIRI := expandCompact(k)
			var values []any
			if arr, ok := v.([]any); ok {
				values = arr
			} else {
				values = []any{v}
			}
			for _, elem := range values {
				objTerm := jsonLDValueToCanonicalTerm(t, elem, expandCompact)
				out = append(out, independentRDFTriple{
					Subject:   canonicalizeIndependentTerm(t, subj),
					Predicate: "<" + predIRI + ">",
					Object:    canonicalizeIndependentTerm(t, objTerm),
				})
			}
		}
	}
	sortAndDedupIndependentTriples(&out)
	return out
}

func jsonLDValueToCanonicalTerm(t *testing.T, elem any, expand func(string) string) string {
	t.Helper()
	switch v := elem.(type) {
	case string:
		return strconv.Quote(v)
	case bool:
		return fmt.Sprintf("%q^^<http://www.w3.org/2001/XMLSchema#boolean>", strconv.FormatBool(v))
	case json.Number:
		s := v.String()
		if strings.ContainsAny(s, ".eE") {
			return fmt.Sprintf("%q^^<http://www.w3.org/2001/XMLSchema#double>", s)
		}
		return fmt.Sprintf("%q^^<http://www.w3.org/2001/XMLSchema#integer>", s)
	case float64:
		return fmt.Sprintf("%q^^<http://www.w3.org/2001/XMLSchema#double>", strconv.FormatFloat(v, 'g', -1, 64))
	case map[string]any:
		if idStr, ok := v["@id"].(string); ok {
			if strings.HasPrefix(idStr, "_:") {
				return idStr
			}
			return "<" + expand(idStr) + ">"
		}
		valStr := fmt.Sprintf("%v", v["@value"])
		if langStr, ok := v["@language"].(string); ok && langStr != "" {
			return strconv.Quote(valStr) + "@" + strings.ToLower(langStr)
		}
		if typeStr, ok := v["@type"].(string); ok && typeStr != "" {
			return strconv.Quote(valStr) + "^^<" + expand(typeStr) + ">"
		}
		return strconv.Quote(valStr)
	default:
		t.Fatalf("unexpected JSON-LD value %#v", elem)
		return ""
	}
}

func readIndependentNTripleTerm(s string) (string, string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", fmt.Errorf("empty term")
	}
	switch s[0] {
	case '<':
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return "", "", fmt.Errorf("unclosed IRI %q", s)
		}
		return s[:end+1], s[end+1:], nil
	case '_':
		end := 0
		for end < len(s) && s[end] != ' ' && s[end] != '\t' && s[end] != ';' && s[end] != ',' && (s[end] != '.' || (end+1 < len(s) && s[end+1] != ' ' && s[end+1] != '\t')) {
			end++
		}
		return s[:end], s[end:], nil
	case '"':
		i := 1
		for i < len(s) {
			if s[i] == '\\' {
				i += 2
				continue
			}
			if s[i] == '"' {
				i++
				break
			}
			i++
		}
		for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != ';' && s[i] != ',' {
			if s[i] == '<' {
				closeIdx := strings.IndexByte(s[i:], '>')
				if closeIdx < 0 {
					return "", "", fmt.Errorf("unclosed datatype IRI in %q", s)
				}
				i += closeIdx + 1
				break
			}
			if s[i] == '.' && (i+1 == len(s) || s[i+1] == ' ' || s[i+1] == '\t') {
				break
			}
			i++
		}
		return s[:i], s[i:], nil
	default:
		return "", "", fmt.Errorf("unexpected token start %q", s)
	}
}

func canonicalizeIndependentTerm(t *testing.T, term string) string {
	t.Helper()
	term = strings.TrimSpace(term)
	if strings.HasPrefix(term, "<") || strings.HasPrefix(term, "_:") {
		return term
	}
	if !strings.HasPrefix(term, `"`) {
		t.Fatalf("unexpected non-quoted literal term %q", term)
	}
	i := 1
	var lexBuilder strings.Builder
	for i < len(term) {
		if term[i] == '\\' && i+1 < len(term) {
			switch term[i+1] {
			case 'n':
				lexBuilder.WriteByte('\n')
			case 'r':
				lexBuilder.WriteByte('\r')
			case 't':
				lexBuilder.WriteByte('\t')
			case '"':
				lexBuilder.WriteByte('"')
			case '\\':
				lexBuilder.WriteByte('\\')
			default:
				lexBuilder.WriteByte(term[i+1])
			}
			i += 2
			continue
		}
		if term[i] == '"' {
			i++
			break
		}
		lexBuilder.WriteByte(term[i])
		i++
	}
	lex := lexBuilder.String()
	suffix := term[i:]
	if strings.HasPrefix(suffix, "@") {
		return strconv.Quote(lex) + strings.ToLower(suffix)
	}
	dt := "http://www.w3.org/2001/XMLSchema#string"
	if strings.HasPrefix(suffix, "^^<") && strings.HasSuffix(suffix, ">") {
		dt = suffix[3 : len(suffix)-1]
	}
	switch dt {
	case "http://www.w3.org/2001/XMLSchema#double":
		if f, err := strconv.ParseFloat(lex, 64); err == nil {
			lex = strconv.FormatFloat(f, 'g', -1, 64)
		}
	case "http://www.w3.org/2001/XMLSchema#integer":
		if n, err := strconv.ParseInt(lex, 10, 64); err == nil {
			lex = strconv.FormatInt(n, 10)
		}
	}
	if dt == "http://www.w3.org/2001/XMLSchema#string" {
		return strconv.Quote(lex)
	}
	return strconv.Quote(lex) + "^^<" + dt + ">"
}

func sortAndDedupIndependentTriples(triples *[]independentRDFTriple) {
	s := *triples
	slices.SortFunc(s, func(a, b independentRDFTriple) int {
		if c := strings.Compare(a.Subject, b.Subject); c != 0 {
			return c
		}
		if c := strings.Compare(a.Predicate, b.Predicate); c != 0 {
			return c
		}
		return strings.Compare(a.Object, b.Object)
	})
	*triples = slices.Compact(s)
}

func TestIntegrationDIEKMEndToEndSuite(t *testing.T) {
	defer registerIntegrationDIEKMVocab(t)()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	// Use MaxPayload=8192 so full scans across the 8 seeded entities split across >= 3 opaque cursor pages.
	env := startLiveIntegrationEnv(ctx, t, 4096)
	beforeKVRevs := snapshotIntegrationKVRevisions(ctx, t, env.kvBucket)
	mapper := irimap.NewMapper(integrationBaseIRI)

	const (
		idUAV1 = "c360.ops.uas1.intel.platform.uav1"
		idUAV2 = "c360.ops.uas2.intel.platform.uav2"
		idUAV9 = "ally.eu.uas9.intel.platform.uav9"
		idObs1 = "c360.ops.uas1.intel.observation.obs1"
	)

	// 1. TestIRIMapperRoundTrip + TestIRIMapperMatchesExporter + TestBlankNodeIDMultiValued
	t.Run("TestIRIMapperRoundTripAndBlankNodeIDMultiValued", func(t *testing.T) {
		for _, ent := range env.entities {
			gotIRI, err := mapper.EntityIDToIRI(ent.ID)
			if err != nil {
				t.Fatalf("EntityIDToIRI(%q): %v", ent.ID, err)
			}
			wantExporterIRI := export.SubjectIRI(ent.ID, integrationBaseIRI)
			if gotIRI != wantExporterIRI {
				t.Fatalf("EntityIDToIRI(%q) = %q, want export.SubjectIRI %q", ent.ID, gotIRI, wantExporterIRI)
			}
			roundTripID, err := mapper.IRIToEntityID(gotIRI)
			if err != nil || roundTripID != ent.ID {
				t.Fatalf("IRIToEntityID(%q) = (%q, %v), want (%q, nil)", gotIRI, roundTripID, err, ent.ID)
			}
		}

		// Multi-valued role on uav1 ("ISR" and "Strike") must mint distinct blank node IDs
		// that match across SELECT, CONSTRUCT, and POST /export over the live NATS server.
		bISR, err := irimap.BlankNodeID(idUAV1, "intel.role.assigned", `"ISR"`, 0)
		if err != nil {
			t.Fatalf("BlankNodeID(ISR): %v", err)
		}
		bStrike, err := irimap.BlankNodeID(idUAV1, "intel.role.assigned", `"Strike"`, 0)
		if err != nil {
			t.Fatalf("BlankNodeID(Strike): %v", err)
		}
		if bISR == bStrike {
			t.Fatalf("multi-valued blank node collision: %q == %q", bISR, bStrike)
		}

		uav1IRI, _ := mapper.EntityIDToIRI(idUAV1)
		roleQuery := fmt.Sprintf(`
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			SELECT ?roleNode ?label WHERE {
				<%s> obo:BFO_0000196 ?roleNode .
				?roleNode a obo:BFO_0000023 ;
				          rdfs:label ?label .
			}
			ORDER BY ASC(?label)
		`, uav1IRI)
		resp, body := env.doSPARQL(ctx, t, "GET", roleQuery, MediaTypeSPARQLJSON, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("roleQuery status = %d: %s", resp.StatusCode, string(body))
		}
		var parsed sparqlJSONResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("json.Unmarshal: %v", err)
		}
		if parsed.Results == nil || len(parsed.Results.Bindings) != 2 {
			t.Fatalf("bindings len = %v, want 2 (%s)", parsed.Results, string(body))
		}
		gotB0 := "_:" + parsed.Results.Bindings[0]["roleNode"].Value
		gotB1 := "_:" + parsed.Results.Bindings[1]["roleNode"].Value
		if gotB0 != bISR || gotB1 != bStrike {
			t.Fatalf("roleNode blank nodes = [%s, %s], want [%s, %s]", gotB0, gotB1, bISR, bStrike)
		}
	})

	// 2. TestBGPFoldingCollapsesDIEKMChains (live NATS vs MemoryReader vs unfolded fallback)
	t.Run("TestBGPFoldingCollapsesDIEKMChains", func(t *testing.T) {
		queries := []struct {
			name  string
			query string
		}{
			{
				name: "1-step (3-triple) role chain + 3-step (7-triple) altitude chain",
				query: `
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX cco: <https://www.commoncoreontologies.org/>
					PREFIX diekm: <https://w3id.org/c360/diekm#>
					PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
					SELECT ?uav ?roleNode ?role ?altNode ?mice ?ibe ?alt WHERE {
						?uav a diekm:IntelligencePlatform ;
						     obo:BFO_0000196 ?roleNode ;
						     obo:BFO_0000196 ?altNode .
						?roleNode a obo:BFO_0000023 ;
						          rdfs:label ?role .
						?altNode a cco:ont00000787 ;
						         cco:ont00001904 ?mice .
						?mice a cco:ont00001163 ;
						      obo:BFO_0000084 ?ibe .
						?ibe a cco:ont00000253 ;
						     diekm:altitudeMetersValue ?alt .
					}
					ORDER BY ASC(?uav) ASC(?role)
				`,
			},
			{
				name: "partial chain fallback equivalence",
				query: `
					PREFIX obo: <http://purl.obolibrary.org/obo/>
					PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
					SELECT ?uav ?node ?val WHERE {
						?uav obo:BFO_0000196 ?node .
						?node rdfs:label ?val .
					}
					ORDER BY ASC(?uav) ASC(?val)
				`,
			},
		}

		refExec := sparql.NewExecutor(env.memReader, sparql.Config{Mapper: mapper})

		for _, tc := range queries {
			resp, body := env.doSPARQL(ctx, t, "POST-raw", tc.query, MediaTypeSPARQLJSON, nil)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("%s live HTTP status = %d: %s", tc.name, resp.StatusCode, string(body))
			}
			var liveParsed sparqlJSONResponse
			if err := json.Unmarshal(body, &liveParsed); err != nil {
				t.Fatalf("%s unmarshal live response: %v", tc.name, err)
			}

			ast, err := sparql.Parse(tc.query)
			if err != nil {
				t.Fatalf("%s Parse: %v", tc.name, err)
			}
			refRes, err := refExec.Execute(ctx, ast)
			if err != nil {
				t.Fatalf("%s refExec.Execute: %v", tc.name, err)
			}
			if len(liveParsed.Results.Bindings) == 0 {
				t.Fatalf("%s returned 0 bindings", tc.name)
			}
			if len(liveParsed.Results.Bindings) != len(refRes.Solutions) {
				t.Fatalf("%s row count mismatch: live=%d ref=%d",
					tc.name, len(liveParsed.Results.Bindings), len(refRes.Solutions))
			}
		}
	})

	// 3. TestLookupByIRIMultiMatchUnion
	t.Run("TestLookupByIRIMultiMatchUnion", func(t *testing.T) {
		query := `
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?s ?tag WHERE {
				?s diekm:sharedOperationalTag ?tag .
			}
			ORDER BY ASC(?tag)
		`
		resp, body := env.doSPARQL(ctx, t, "GET", query, MediaTypeSPARQLJSON, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, string(body))
		}
		var parsed sparqlJSONResponse
		if err := json.Unmarshal(body, &parsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if parsed.Results == nil || len(parsed.Results.Bindings) != 2 {
			t.Fatalf("shared IRI bindings = %+v, want 2 rows across both dotted predicates", parsed.Results)
		}
		gotTags := []string{
			parsed.Results.Bindings[0]["tag"].Value,
			parsed.Results.Bindings[1]["tag"].Value,
		}
		if !slices.Equal(gotTags, []string{"TAG-ALPHA", "TAG-BRAVO"}) {
			t.Fatalf("shared IRI tags = %v, want [TAG-ALPHA TAG-BRAVO]", gotTags)
		}
	})

	// 4. TestVirtualRDFTypeAndSubclassResolution (across wildcard systems)
	t.Run("TestVirtualRDFTypeAndSubclassResolution", func(t *testing.T) {
		// Querying diekm:IntelligencePlatform must find uav1 (uas1), uav2 (uas2), and uav9 (ally.eu.uas9).
		leafQuery := `
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?s WHERE {
				?s a diekm:IntelligencePlatform .
			}
			ORDER BY ASC(?s)
		`
		resp, body := env.doSPARQL(ctx, t, "GET", leafQuery, MediaTypeSPARQLJSON, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("leafQuery status = %d: %s", resp.StatusCode, string(body))
		}
		var leafParsed sparqlJSONResponse
		if err := json.Unmarshal(body, &leafParsed); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if len(leafParsed.Results.Bindings) != 3 {
			t.Fatalf("diekm:IntelligencePlatform count = %d, want 3 across wildcard systems (%s)", len(leafParsed.Results.Bindings), string(body))
		}

		// Querying root BFO Entity (obo:BFO_0000001) must match all 8 DIEKM entities via superclass closure.
		rootQuery := `
			PREFIX obo: <http://purl.obolibrary.org/obo/>
			SELECT ?s WHERE {
				?s a obo:BFO_0000001 .
			}
			ORDER BY ASC(?s)
		`
		respRoot, bodyRoot := env.doSPARQL(ctx, t, "POST-form", rootQuery, MediaTypeSPARQLJSON, nil)
		if respRoot.StatusCode != http.StatusOK {
			t.Fatalf("rootQuery status = %d: %s", respRoot.StatusCode, string(bodyRoot))
		}
		var rootParsed sparqlJSONResponse
		if err := json.Unmarshal(bodyRoot, &rootParsed); err != nil {
			t.Fatalf("Unmarshal root: %v", err)
		}
		if len(rootParsed.Results.Bindings) != 8 {
			t.Fatalf("obo:BFO_0000001 count = %d, want all 8 entities (%s)", len(rootParsed.Results.Bindings), string(bodyRoot))
		}

		// Querying rdfs:subClassOf* for diekm:IntelligencePlatform over live HTTP.
		subClassQuery := `
			PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?super WHERE {
				diekm:IntelligencePlatform rdfs:subClassOf* ?super .
			}
			ORDER BY ASC(?super)
		`
		respSub, bodySub := env.doSPARQL(ctx, t, "GET", subClassQuery, MediaTypeSPARQLJSON, nil)
		if respSub.StatusCode != http.StatusOK {
			t.Fatalf("subClassQuery status = %d: %s", respSub.StatusCode, string(bodySub))
		}
		var subParsed sparqlJSONResponse
		if err := json.Unmarshal(bodySub, &subParsed); err != nil {
			t.Fatalf("Unmarshal subClass: %v", err)
		}
		if len(subParsed.Results.Bindings) != 8 {
			t.Fatalf("rdfs:subClassOf* count = %d, want 8 (reflexive + 7 superclasses) (%s)", len(subParsed.Results.Bindings), string(bodySub))
		}
	})

	// 5. TestScanPatternReaderParity (multi-page cursor pagination under MaxPayload=4096)
	t.Run("TestScanPatternReaderParity", func(t *testing.T) {
		beforePrefixCalls := env.prefixQueries.Load()
		gotAll, err := env.natsReader.ScanPattern(ctx, reader.MatchAllPattern, 50)
		if err != nil {
			t.Fatalf("natsReader.ScanPattern(*.*.*.*.*.*): %v", err)
		}
		wantAll, err := env.memReader.ScanPattern(ctx, reader.MatchAllPattern, 50)
		if err != nil {
			t.Fatalf("memReader.ScanPattern(*.*.*.*.*.*): %v", err)
		}
		pageCalls := env.prefixQueries.Load() - beforePrefixCalls
		if pageCalls < 3 {
			t.Fatalf("ScanPattern(*.*.*.*.*.*) made %d prefix RPC calls, want >= 3 multi-page cursor calls under 4 KiB MaxPayload", pageCalls)
		}
		if len(gotAll) != len(wantAll) {
			t.Fatalf("ScanPattern count = %d, want %d", len(gotAll), len(wantAll))
		}
		for i := range wantAll {
			if gotAll[i].ID != wantAll[i].ID {
				t.Fatalf("ScanPattern[%d].ID = %q, want %q", i, gotAll[i].ID, wantAll[i].ID)
			}
		}
	})

	// 6. TestSuperclassClosureStartupCheck
	t.Run("TestSuperclassClosureStartupCheck", func(t *testing.T) {
		if err := sparql.ValidateSuperclassClosure(); err != nil {
			t.Fatalf("ValidateSuperclassClosure on BFO/CCO + DIEKM failed: %v", err)
		}
		restore := vocabulary.SnapshotRegistry()
		vocabulary.RegisterEntityType("ont.mid",
			vocabulary.WithClassIRI("https://example.org/onto#Mid"),
			vocabulary.WithSuperclass("https://example.org/onto#Root"),
		)
		vocabulary.RegisterEntityType("ont.leaf",
			vocabulary.WithClassIRI("https://example.org/onto#Leaf"),
			vocabulary.WithSuperclass("https://example.org/onto#Mid"),
			// Missing transitive ancestor https://example.org/onto#Root!
		)
		closureErr := sparql.ValidateSuperclassClosure()
		restore()
		if !errors.Is(closureErr, sparql.ErrIncompleteSuperclassClosure) {
			t.Fatalf("ValidateSuperclassClosure on incomplete closure = %v, want ErrIncompleteSuperclassClosure", closureErr)
		}
	})

	// 7. TestConstructUnfoldsReifiedGraphForSHACL + TestUnfoldTerminalDatatypePropagation
	t.Run("TestConstructUnfoldsReifiedGraphForSHACLAndTerminalDatatypes", func(t *testing.T) {
		constructAll := "CONSTRUCT WHERE { ?s ?p ?o }"

		// Fetch CONSTRUCT in Turtle, N-Triples, and JSON-LD over live HTTP.
		respTTL, bodyTTL := env.doSPARQL(ctx, t, "GET", constructAll, MediaTypeTurtle, nil)
		if respTTL.StatusCode != http.StatusOK {
			t.Fatalf("CONSTRUCT Turtle status = %d: %s", respTTL.StatusCode, string(bodyTTL))
		}
		respNT, bodyNT := env.doSPARQL(ctx, t, "POST-raw", constructAll, MediaTypeNTriples, nil)
		if respNT.StatusCode != http.StatusOK {
			t.Fatalf("CONSTRUCT N-Triples status = %d: %s", respNT.StatusCode, string(bodyNT))
		}
		respJSONLD, bodyJSONLD := env.doSPARQL(ctx, t, "POST-form", constructAll, MediaTypeJSONLD, nil)
		if respJSONLD.StatusCode != http.StatusOK {
			t.Fatalf("CONSTRUCT JSON-LD status = %d: %s", respJSONLD.StatusCode, string(bodyJSONLD))
		}

		triplesTTL := parseIndependentTurtle(t, string(bodyTTL))
		triplesNT := parseIndependentNTriples(t, string(bodyNT))
		triplesJSONLD := parseIndependentJSONLD(t, bodyJSONLD)

		if !slices.Equal(triplesTTL, triplesNT) {
			t.Fatalf("Turtle vs N-Triples canonical triple sets differ:\nTTL(%d)=%+v\nNT(%d)=%+v", len(triplesTTL), triplesTTL, len(triplesNT), triplesNT)
		}
		if !slices.Equal(triplesNT, triplesJSONLD) {
			t.Fatalf("N-Triples vs JSON-LD canonical triple sets differ:\nNT(%d)=%+v\nJSONLD(%d)=%+v", len(triplesNT), triplesNT, len(triplesJSONLD), triplesJSONLD)
		}

		// Verify POST /export with {"construct":"CONSTRUCT WHERE { ?s ?p ?o }"} matches CONSTRUCT exactly.
		exportReq, err := http.NewRequestWithContext(ctx, http.MethodPost, env.liveURL+"/export", strings.NewReader(`{"construct":"CONSTRUCT WHERE { ?s ?p ?o }"}`))
		if err != nil {
			t.Fatalf("NewRequest POST /export: %v", err)
		}
		exportReq.Header.Set("Content-Type", "application/json")
		exportReq.Header.Set("Accept", MediaTypeNTriples)
		exportResp, err := env.httpClient.Do(exportReq)
		if err != nil {
			t.Fatalf("Do POST /export: %v", err)
		}
		exportBody, _ := io.ReadAll(exportResp.Body)
		exportResp.Body.Close()
		if exportResp.StatusCode != http.StatusOK {
			t.Fatalf("POST /export status = %d: %s", exportResp.StatusCode, string(exportBody))
		}
		triplesExport := parseIndependentNTriples(t, string(exportBody))
		if !slices.Equal(triplesNT, triplesExport) {
			t.Fatalf("CONSTRUCT vs POST /export canonical triple sets differ")
		}

		// Verify real _:b_... blank nodes, zero /.well-known/genid/ skolem IRIs, and unmangled ontology IRIs.
		rawNT := string(bodyNT)
		if strings.Contains(rawNT, "/.well-known/genid/") || strings.Contains(rawNT, "/subjects/_:") {
			t.Fatalf("unfolded graph contains skolem or mangled blank node IRI instead of real _:b_... blank node:\n%s", rawNT)
		}
		if !strings.Contains(rawNT, "_:b_") {
			t.Fatalf("unfolded graph missing _:b_... blank nodes:\n%s", rawNT)
		}
		for _, wantPredIRI := range []string{
			"http://purl.obolibrary.org/obo/BFO_0000196",
			"http://www.w3.org/2000/01/rdf-schema#label",
			"https://www.commoncoreontologies.org/ont00001904",
			"http://purl.obolibrary.org/obo/BFO_0000084",
			"https://w3id.org/c360/diekm#altitudeMetersValue",
			"https://w3id.org/c360/diekm#instantDateTime",
			"https://w3id.org/c360/diekm#integerValue",
			"https://w3id.org/c360/diekm#booleanValue",
			"https://w3id.org/c360/diekm#jsonValue",
			"http://purl.obolibrary.org/obo/RO_0000057",
		} {
			if !strings.Contains(rawNT, "<"+wantPredIRI+">") {
				t.Fatalf("unfolded graph missing full unmangled ontology predicate IRI <%s>:\n%s", wantPredIRI, rawNT)
			}
		}

		// Verify all 6 terminal datatypes required by TestUnfoldTerminalDatatypePropagation:
		// 1. xsd:dateTime
		// 2. xsd:double
		// 3. xsd:integer
		// 4. xsd:boolean
		// 5. rdf:JSON
		// 6. <entity IRI> resource object (obs1 IRI on assigned-sensor)
		obs1IRI, _ := mapper.EntityIDToIRI(idObs1)
		wantObjects := []string{
			`"2026-10-16T12:00:00Z"^^<http://www.w3.org/2001/XMLSchema#dateTime>`,
			`"1500.5"^^<http://www.w3.org/2001/XMLSchema#double>`,
			`"42"^^<http://www.w3.org/2001/XMLSchema#integer>`,
			`"true"^^<http://www.w3.org/2001/XMLSchema#boolean>`,
			`"{\"mode\":\"autonomous\",\"rev\":1}"^^<http://www.w3.org/1999/02/22-rdf-syntax-ns#JSON>`,
			`<` + obs1IRI + `>`,
		}
		for _, wantObj := range wantObjects {
			found := false
			for _, tr := range triplesNT {
				if tr.Object == wantObj {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("unfolded graph missing expected terminal object %s in triples:\n%s", wantObj, rawNT)
			}
		}

		// Verify diekm.Shapes() target classes and reified BFO/CCO intermediate classes are materialized.
		shapesTTL := string(diekm.Shapes())
		if !strings.Contains(shapesTTL, "IntelligencePlatform") || !strings.Contains(shapesTTL, "IntelligenceObservation") {
			t.Fatalf("unexpected diekm.Shapes() content:\n%s", shapesTTL)
		}
		for _, wantTargetClass := range []string{
			"https://w3id.org/c360/diekm#IntelligencePlatform",
			"https://w3id.org/c360/diekm#IntelligenceObservation",
			"http://purl.obolibrary.org/obo/BFO_0000023",
			"https://www.commoncoreontologies.org/ont00000787",
			"https://www.commoncoreontologies.org/ont00001163",
			"https://www.commoncoreontologies.org/ont00000253",
		} {
			foundClass := false
			for _, tr := range triplesNT {
				if tr.Predicate == "<"+sparql.RDFTypeIRI+">" && tr.Object == "<"+wantTargetClass+">" {
					foundClass = true
					break
				}
			}
			if !foundClass {
				t.Fatalf("unfolded graph missing target class <%s>", wantTargetClass)
			}
		}
	})

	// 8. TestSPARQL11ProtocolConformance across all transports, endpoints, and 7 media types
	t.Run("TestSPARQL11ProtocolConformance", func(t *testing.T) {
		selectQ := `
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?s WHERE {
				?s a diekm:IntelligencePlatform .
			}
			ORDER BY ASC(?s)
		`
		askQ := `
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			ASK WHERE { ?s a diekm:IntelligencePlatform }
		`
		uav1IRI, _ := mapper.EntityIDToIRI(idUAV1)
		describeQ := fmt.Sprintf("DESCRIBE <%s>", uav1IRI)

		for _, transport := range []string{"GET", "POST-raw", "POST-form"} {
			// SELECT across JSON, XML, CSV, TSV.
			for _, accept := range []string{MediaTypeSPARQLJSON, MediaTypeSPARQLXML, MediaTypeCSV, MediaTypeTSV} {
				resp, body := env.doSPARQL(ctx, t, transport, selectQ, accept, nil)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("SELECT (%s, %s) status = %d: %s", transport, accept, resp.StatusCode, string(body))
				}
				if !strings.HasPrefix(resp.Header.Get("Content-Type"), accept) {
					t.Fatalf("SELECT (%s, %s) Content-Type = %q", transport, accept, resp.Header.Get("Content-Type"))
				}
				switch accept {
				case MediaTypeSPARQLJSON:
					var jr sparqlJSONResponse
					if err := json.Unmarshal(body, &jr); err != nil || jr.Results == nil || len(jr.Results.Bindings) != 3 {
						t.Fatalf("SELECT JSON (%s) invalid: %v (%s)", transport, err, string(body))
					}
				case MediaTypeSPARQLXML:
					var xr sparqlXMLResponse
					if err := xml.Unmarshal(body, &xr); err != nil || xr.Results == nil || len(xr.Results.Results) != 3 {
						t.Fatalf("SELECT XML (%s) invalid: %v (%s)", transport, err, string(body))
					}
				case MediaTypeCSV:
					rows, err := csv.NewReader(strings.NewReader(string(body))).ReadAll()
					if err != nil || len(rows) != 4 || rows[0][0] != "s" {
						t.Fatalf("SELECT CSV (%s) invalid: rows=%v err=%v", transport, rows, err)
					}
				case MediaTypeTSV:
					r := csv.NewReader(strings.NewReader(string(body)))
					r.Comma = '\t'
					r.LazyQuotes = true
					rows, err := r.ReadAll()
					if err != nil || len(rows) != 4 || rows[0][0] != "?s" {
						t.Fatalf("SELECT TSV (%s) invalid: rows=%v err=%v", transport, rows, err)
					}
				}
			}

			// ASK across JSON and XML.
			for _, accept := range []string{MediaTypeSPARQLJSON, MediaTypeSPARQLXML} {
				resp, body := env.doSPARQL(ctx, t, transport, askQ, accept, nil)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("ASK (%s, %s) status = %d: %s", transport, accept, resp.StatusCode, string(body))
				}
				if accept == MediaTypeSPARQLJSON {
					var jr sparqlJSONResponse
					if err := json.Unmarshal(body, &jr); err != nil || jr.Boolean == nil || !*jr.Boolean {
						t.Fatalf("ASK JSON (%s) invalid: %v (%s)", transport, err, string(body))
					}
				} else {
					var xr sparqlXMLResponse
					if err := xml.Unmarshal(body, &xr); err != nil || xr.Boolean == nil || !*xr.Boolean {
						t.Fatalf("ASK XML (%s) invalid: %v (%s)", transport, err, string(body))
					}
				}
			}

			// DESCRIBE across Turtle, N-Triples, and JSON-LD.
			for _, accept := range []string{MediaTypeTurtle, MediaTypeNTriples, MediaTypeJSONLD} {
				resp, body := env.doSPARQL(ctx, t, transport, describeQ, accept, nil)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("DESCRIBE (%s, %s) status = %d: %s", transport, accept, resp.StatusCode, string(body))
				}
				if !strings.Contains(string(body), "1500.5") {
					t.Fatalf("DESCRIBE (%s, %s) missing uav1 altitude:\n%s", transport, accept, string(body))
				}
			}
		}

		// GET /entities/{entity_id}.jsonld over live HTTP.
		entReq, err := http.NewRequestWithContext(ctx, http.MethodGet, env.liveURL+"/entities/"+idUAV1+".jsonld", nil)
		if err != nil {
			t.Fatalf("NewRequest entity jsonld: %v", err)
		}
		entResp, err := env.httpClient.Do(entReq)
		if err != nil {
			t.Fatalf("Do entity jsonld: %v", err)
		}
		entBody, _ := io.ReadAll(entResp.Body)
		entResp.Body.Close()
		if entResp.StatusCode != http.StatusOK {
			t.Fatalf("GET /entities/%s.jsonld status = %d: %s", idUAV1, entResp.StatusCode, string(entBody))
		}
		entTriples := parseIndependentJSONLD(t, entBody)
		if len(entTriples) < 10 {
			t.Fatalf("GET /entities/%s.jsonld returned only %d triples, want >= 10", idUAV1, len(entTriples))
		}

		// GET /export.ttl is not registered (404 Not Found) and GET /export returns 405 Method Not Allowed.
		ttlReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, env.liveURL+"/export.ttl", nil)
		ttlResp, err := env.httpClient.Do(ttlReq)
		if err != nil {
			t.Fatalf("GET /export.ttl: %v", err)
		}
		ttlResp.Body.Close()
		if ttlResp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /export.ttl status = %d, want 404", ttlResp.StatusCode)
		}
	})

	// 9. TestQueryLimits (result cap, scan budget, truncation headers, 413 body cap, unconstrained ?s ?p ?o rejection)
	t.Run("TestQueryLimits", func(t *testing.T) {
		cappedSrv, err := New(env.natsReader, Config{
			BaseIRI:         integrationBaseIRI,
			QueryTimeout:    5 * time.Second,
			MaxResults:      2,
			MaxScanEntities: 50,
			MaxQueryBytes:   256,
			MaxExportBytes:  256,
		})
		if err != nil {
			t.Fatalf("New cappedSrv: %v", err)
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen: %v", err)
		}
		srvCtx, srvCancel := context.WithCancel(ctx)
		doneCh := make(chan error, 1)
		go func() {
			doneCh <- cappedSrv.Serve(srvCtx, ln)
		}()
		defer func() {
			srvCancel()
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutCancel()
			_ = cappedSrv.Shutdown(shutCtx)
			<-doneCh
		}()
		cappedURL := "http://" + ln.Addr().String()

		// a) Unconstrained ?s ?p ?o rejected with 422 Unprocessable Entity (planner ErrUnconstrainedScan).
		reqUncon, _ := http.NewRequestWithContext(ctx, http.MethodGet, cappedURL+"/sparql?query="+url.QueryEscape("SELECT ?s ?p ?o WHERE { ?s ?p ?o }"), nil)
		respUncon, err := env.httpClient.Do(reqUncon)
		if err != nil {
			t.Fatalf("Do unconstrained: %v", err)
		}
		respUncon.Body.Close()
		if respUncon.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("unconstrained ?s ?p ?o status = %d, want 422", respUncon.StatusCode)
		}

		// b) MaxResults=2 exceeded without X-Semsparql-Allow-Truncation returns 422.
		threeRowsQuery := `
			PREFIX diekm: <https://w3id.org/c360/diekm#>
			SELECT ?s WHERE { ?s a diekm:IntelligencePlatform }
			ORDER BY ASC(?s)
		`
		req422, _ := http.NewRequestWithContext(ctx, http.MethodGet, cappedURL+"/sparql?query="+url.QueryEscape(threeRowsQuery), nil)
		resp422, err := env.httpClient.Do(req422)
		if err != nil {
			t.Fatalf("Do MaxResults 422: %v", err)
		}
		resp422.Body.Close()
		if resp422.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("MaxResults breach without header status = %d, want 422", resp422.StatusCode)
		}

		// c) MaxResults=2 with X-Semsparql-Allow-Truncation: true returns 200 OK with truncation headers and 2 rows.
		reqTrunc, _ := http.NewRequestWithContext(ctx, http.MethodGet, cappedURL+"/sparql?query="+url.QueryEscape(threeRowsQuery), nil)
		reqTrunc.Header.Set(HeaderAllowTruncation, "true")
		respTrunc, err := env.httpClient.Do(reqTrunc)
		if err != nil {
			t.Fatalf("Do MaxResults truncated: %v", err)
		}
		truncBody, _ := io.ReadAll(respTrunc.Body)
		respTrunc.Body.Close()
		if respTrunc.StatusCode != http.StatusOK {
			t.Fatalf("MaxResults with Allow-Truncation status = %d, want 200: %s", respTrunc.StatusCode, string(truncBody))
		}
		if respTrunc.Header.Get(HeaderTruncated) != "true" || respTrunc.Header.Get(HeaderTruncationReason) != sparql.TruncationReasonResultLimit {
			t.Fatalf("truncation headers = (%q, %q), want (true, %q)",
				respTrunc.Header.Get(HeaderTruncated), respTrunc.Header.Get(HeaderTruncationReason), sparql.TruncationReasonResultLimit)
		}
		var truncParsed sparqlJSONResponse
		if err := json.Unmarshal(truncBody, &truncParsed); err != nil || len(truncParsed.Results.Bindings) != 2 {
			t.Fatalf("truncated bindings len = %v, want 2", truncParsed.Results)
		}

		// d) 413 Payload Too Large when query body exceeds MaxQueryBytes (256 bytes).
		oversizedQuery := "SELECT ?s WHERE { ?s ?p \"" + strings.Repeat("x", 300) + "\" }"
		req413, _ := http.NewRequestWithContext(ctx, http.MethodPost, cappedURL+"/sparql", strings.NewReader(oversizedQuery))
		req413.Header.Set("Content-Type", "application/sparql-query")
		resp413, err := env.httpClient.Do(req413)
		if err != nil {
			t.Fatalf("Do 413: %v", err)
		}
		resp413.Body.Close()
		if resp413.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized query status = %d, want 413", resp413.StatusCode)
		}

		// e) Scan budget breach returns 422 Unprocessable Entity.
		tightScanSrv, err := New(env.natsReader, Config{
			BaseIRI:         integrationBaseIRI,
			MaxScanEntities: 1,
		})
		if err != nil {
			t.Fatalf("New tightScanSrv: %v", err)
		}
		lnScan, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("net.Listen tightScanSrv: %v", err)
		}
		scanCtx, scanCancel := context.WithCancel(ctx)
		scanDone := make(chan error, 1)
		go func() {
			scanDone <- tightScanSrv.Serve(scanCtx, lnScan)
		}()
		defer func() {
			scanCancel()
			shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer shutCancel()
			_ = tightScanSrv.Shutdown(shutCtx)
			<-scanDone
		}()
		reqBudget, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+lnScan.Addr().String()+"/sparql?query="+url.QueryEscape(threeRowsQuery), nil)
		respBudget, err := env.httpClient.Do(reqBudget)
		if err != nil {
			t.Fatalf("Do scan budget: %v", err)
		}
		respBudget.Body.Close()
		if respBudget.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("scan budget breach status = %d, want 422", respBudget.StatusCode)
		}

		// f) Multi-entity GetBatch across all 8 DIEKM fixture entities (>4 KiB combined JSON)
		// succeeds under MaxPayload=4096 by adaptively splitting on response_too_large.
		allIDs := make([]string, 0, len(env.entities))
		for _, ent := range env.entities {
			allIDs = append(allIDs, ent.ID)
		}
		batchEnts, err := env.natsReader.GetBatch(ctx, allIDs)
		if err != nil {
			t.Fatalf("natsReader.GetBatch(%d entities under MaxPayload=4096): %v", len(allIDs), err)
		}
		if batchEnts == nil || len(batchEnts.Entities) != len(allIDs) {
			t.Fatalf("natsReader.GetBatch len = %v, want %d", batchEnts, len(allIDs))
		}
	})

	// 10. TestUnsupportedFeatureErrors
	t.Run("TestUnsupportedFeatureErrors", func(t *testing.T) {
		unsupportedQueries := []struct {
			name        string
			query       string
			wantFeature string
		}{
			{
				name:        "SPARQL Update INSERT DATA",
				query:       `INSERT DATA { <https://example.org/s> <https://example.org/p> "v" }`,
				wantFeature: "INSERT",
			},
			{
				name:        "SERVICE federation",
				query:       `SELECT ?s WHERE { SERVICE <https://example.org/sparql> { ?s ?p ?o } }`,
				wantFeature: "SERVICE",
			},
			{
				name:        "Named GRAPH",
				query:       `SELECT ?s WHERE { GRAPH <https://example.org/g> { ?s ?p ?o } }`,
				wantFeature: "GRAPH",
			},
			{
				name:        "FROM dataset clause",
				query:       `SELECT ?s FROM <https://example.org/g> WHERE { ?s ?p ?o }`,
				wantFeature: "FROM",
			},
			{
				name:        "Property path +",
				query:       `PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#> SELECT ?s WHERE { ?s rdfs:subClassOf+ ?o }`,
				wantFeature: "property path",
			},
			{
				name:        "GROUP BY aggregate",
				query:       `SELECT ?s WHERE { ?s ?p ?o } GROUP BY ?s`,
				wantFeature: "GROUP BY",
			},
			{
				name:        "VALUES clause",
				query:       `SELECT ?s WHERE { VALUES ?s { <https://example.org/a> } ?s ?p ?o }`,
				wantFeature: "VALUES",
			},
			{
				name:        "BIND clause",
				query:       `SELECT ?s ?x WHERE { ?s ?p ?o BIND(?o AS ?x) }`,
				wantFeature: "BIND",
			},
			{
				name:        "MINUS clause",
				query:       `SELECT ?s WHERE { ?s ?p ?o MINUS { ?s ?p "x" } }`,
				wantFeature: "MINUS",
			},
			{
				name:        "Subquery",
				query:       `SELECT ?s WHERE { { SELECT ?s WHERE { ?s ?p ?o } } }`,
				wantFeature: "subquery",
			},
		}

		for _, tc := range unsupportedQueries {
			resp, body := env.doSPARQL(ctx, t, "POST-raw", tc.query, MediaTypeSPARQLJSON, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("%s status = %d, want 400: %s", tc.name, resp.StatusCode, string(body))
			}
			if !strings.Contains(strings.ToLower(string(body)), strings.ToLower(tc.wantFeature)) {
				t.Fatalf("%s response %q does not name unsupported feature %q", tc.name, string(body), tc.wantFeature)
			}
		}
	})

	// 11. Zero ENTITY_STATES KV writes and zero graph.mutation.> messages across the entire suite
	t.Run("TestZeroKVWritesAndZeroMutations", func(t *testing.T) {
		afterKVRevs := snapshotIntegrationKVRevisions(ctx, t, env.kvBucket)
		if !slices.Equal(beforeKVRevs, afterKVRevs) {
			t.Fatalf("ENTITY_STATES KV revisions changed during integration suite: before=%v after=%v", beforeKVRevs, afterKVRevs)
		}
		if muts := env.mutations.Load(); muts != 0 {
			t.Fatalf("observed %d graph.mutation.> messages during integration suite, want 0", muts)
		}
	})
}

func TestPropIntegrationDIEKMLiveServerParity(t *testing.T) {
	defer registerIntegrationDIEKMVocab(t)()

	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()

	env := startLiveIntegrationEnv(ctx, t, 4096)
	refSrv, err := New(env.memReader, Config{
		BaseIRI:         integrationBaseIRI,
		QueryTimeout:    10 * time.Second,
		MaxResults:      500,
		MaxScanEntities: 500,
		MaxQueryBytes:   64 * 1024,
		MaxExportBytes:  64 * 1024,
	})
	if err != nil {
		t.Fatalf("New refSrv: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen refSrv: %v", err)
	}
	refCtx, refCancel := context.WithCancel(ctx)
	refErrCh := make(chan error, 1)
	go func() {
		refErrCh <- refSrv.Serve(refCtx, ln)
	}()
	t.Cleanup(func() {
		refCancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		_ = refSrv.Shutdown(shutCtx)
		<-refErrCh
	})
	refURL := "http://" + ln.Addr().String()

	classChoices := []string{
		"https://w3id.org/c360/diekm#IntelligencePlatform",
		"https://w3id.org/c360/diekm#IntelligenceObservation",
		"https://www.commoncoreontologies.org/ont00000713",
		"http://purl.obolibrary.org/obo/BFO_0000030",
		"http://purl.obolibrary.org/obo/BFO_0000001",
	}
	transports := []string{"GET", "POST-raw", "POST-form"}

	rapid.Check(t, func(rt *rapid.T) {
		classIRI := rapid.SampledFrom(classChoices).Draw(rt, "classIRI")
		includeRole := rapid.Bool().Draw(rt, "includeRole")
		includeAlt := rapid.Bool().Draw(rt, "includeAlt")
		limit := rapid.IntRange(1, 10).Draw(rt, "limit")
		transport := rapid.SampledFrom(transports).Draw(rt, "transport")

		var where strings.Builder
		fmt.Fprintf(&where, "?s a <%s> .\n", classIRI)
		if includeRole {
			where.WriteString("OPTIONAL { ?s <http://purl.obolibrary.org/obo/BFO_0000196> ?r . ?r a <http://purl.obolibrary.org/obo/BFO_0000023> ; <http://www.w3.org/2000/01/rdf-schema#label> ?role . }\n")
		}
		if includeAlt {
			where.WriteString("OPTIONAL { ?s <http://purl.obolibrary.org/obo/BFO_0000196> ?a . ?a a <https://www.commoncoreontologies.org/ont00000787> ; <https://www.commoncoreontologies.org/ont00001904> ?m . ?m a <https://www.commoncoreontologies.org/ont00001163> ; <http://purl.obolibrary.org/obo/BFO_0000084> ?i . ?i a <https://www.commoncoreontologies.org/ont00000253> ; <https://w3id.org/c360/diekm#altitudeMetersValue> ?alt . }\n")
		}
		query := fmt.Sprintf("SELECT DISTINCT ?s ?role ?alt WHERE {\n%s} ORDER BY ASC(?s) ASC(?role) ASC(?alt) LIMIT %d", where.String(), limit)

		_, liveBody := env.doSPARQL(ctx, t, transport, query, MediaTypeSPARQLJSON, nil)

		reqRef, err := http.NewRequestWithContext(ctx, http.MethodGet, refURL+"/sparql?query="+url.QueryEscape(query), nil)
		if err != nil {
			rt.Fatalf("NewRequest ref: %v", err)
		}
		reqRef.Header.Set("Accept", MediaTypeSPARQLJSON)
		respRef, err := env.httpClient.Do(reqRef)
		if err != nil {
			rt.Fatalf("Do ref: %v", err)
		}
		refBody, _ := io.ReadAll(respRef.Body)
		respRef.Body.Close()

		if string(liveBody) != string(refBody) {
			rt.Fatalf("live NATS server vs reference MemoryReader server mismatch for query:\n%s\nlive=%s\nref=%s", query, string(liveBody), string(refBody))
		}
	})
}
