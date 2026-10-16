package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/c360studio/semengine/component"
	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/graph/inference"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/natsclient"
	"github.com/c360studio/semengine/payloadregistry"
	"github.com/c360studio/semengine/pkg/lifecycle"
	graphingest "github.com/c360studio/semengine/processor/graph-ingest"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semsparql/server"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func TestRunConfigAndFlagValidation(t *testing.T) {
	ctx := t.Context()

	// 1. Nil and canceled contexts.
	var nilCtx context.Context
	if err := run(nilCtx, nil, nil, io.Discard); !errors.Is(err, reader.ErrNilContext) {
		t.Fatalf("run(nilCtx) = %v, want ErrNilContext", err)
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if err := run(canceledCtx, nil, nil, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("run(canceledCtx) = %v, want context.Canceled", err)
	}

	// 2. Flag validation errors before NATS connection.
	invalidFlags := [][]string{
		{"-unknown-flag"},
		{"unexpected-positional-arg"},
		{"-nats-url", "   "},
		{"-nats-timeout", "-1s"},
		{"-query-timeout", "0s"},
		{"-max-results", "0"},
		{"-max-scan-entities", "0"},
		{"-max-query-bytes", "0"},
		{"-max-export-bytes", "0"},
		{"-tls-cert", "only-cert.pem"},
		{"-tls-key", "only-key.pem"},
		{"-tls-cert", "nonexistent.pem", "-tls-key", "nonexistent.key"},
	}
	for _, args := range invalidFlags {
		if err := run(ctx, args, nil, nil); err == nil {
			t.Fatalf("run(%v) succeeded, want validation error", args)
		}
	}

	// 3. Non-local bind without bearer token or mTLS refuses before NATS connection.
	err := run(ctx, []string{"-bind", "0.0.0.0:0"}, func(string) string { return "" }, io.Discard)
	if !errors.Is(err, server.ErrUnsafeExposure) {
		t.Fatalf("run(-bind 0.0.0.0:0 without auth) = %v, want ErrUnsafeExposure", err)
	}
}

func TestBuildTLSConfigValidation(t *testing.T) {
	if cfg, err := buildTLSConfig("", "", ""); err != nil || cfg != nil {
		t.Fatalf("buildTLSConfig(empty) = (%v, %v), want (nil, nil)", cfg, err)
	}

	certPath, keyPath, caPath := writeSelfSignedTLSFiles(t)

	// Missing client CA file fails.
	if _, err := buildTLSConfig(certPath, keyPath, filepath.Join(t.TempDir(), "missing-ca.pem")); err == nil {
		t.Fatal("buildTLSConfig(missing client CA) succeeded, want error")
	}

	// Invalid client CA PEM fails.
	badCAPath := filepath.Join(t.TempDir(), "bad-ca.pem")
	if err := os.WriteFile(badCAPath, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := buildTLSConfig(certPath, keyPath, badCAPath); err == nil {
		t.Fatal("buildTLSConfig(invalid client CA) succeeded, want error")
	}

	// Valid server cert + key + client CA configures mTLS.
	tlsCfg, err := buildTLSConfig(certPath, keyPath, caPath)
	if err != nil {
		t.Fatalf("buildTLSConfig(valid mTLS): %v", err)
	}
	if tlsCfg == nil || tlsCfg.ClientCAs == nil || len(tlsCfg.Certificates) != 1 {
		t.Fatalf("buildTLSConfig returned incomplete tls.Config: %+v", tlsCfg)
	}
}

func TestRunRefusesIncompatibleLegacyNATSResponder(t *testing.T) {
	restore := vocabulary.SnapshotRegistry()
	defer restore()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	ns, err := natsserver.NewServer(&natsserver.Options{
		Host:   "127.0.0.1",
		Port:   -1,
		NoLog:  true,
		NoSigs: true,
	})
	if err != nil {
		t.Fatalf("natsserver.NewServer: %v", err)
	}
	ns.Start()
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS server not ready")
	}

	// Subscribe a legacy pre-#259 responder on the declared prefix subject that ignores
	// the "pattern" JSON field and returns {"entities":[]} with HTTP-style 200 OK.
	rawConn, err := nats.Connect(ns.ClientURL())
	if err != nil {
		t.Fatalf("nats.Connect: %v", err)
	}
	t.Cleanup(func() {
		rawConn.Close()
	})

	var prefixSub string
	for _, v := range graph.QueryVerbs() {
		if v.Name == "prefix" {
			prefixSub = v.Subject
		}
	}
	sub, err := rawConn.Subscribe(prefixSub, func(msg *nats.Msg) {
		_ = msg.Respond([]byte(`{"entities":[]}`))
	})
	if err != nil {
		t.Fatalf("Subscribe(%s): %v", prefixSub, err)
	}
	t.Cleanup(func() {
		_ = sub.Unsubscribe()
	})
	if err := rawConn.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	err = run(ctx, []string{
		"-bind", "127.0.0.1:0",
		"-nats-url", ns.ClientURL(),
		"-nats-timeout", "2s",
	}, nil, io.Discard)
	if !errors.Is(err, reader.ErrPatternCapabilityUnsupported) {
		t.Fatalf("run against legacy server = %v, want ErrPatternCapabilityUnsupported", err)
	}
}

func TestRunEndToEndWithLiveNATSAndGraphIngest(t *testing.T) {
	restore := vocabulary.SnapshotRegistry()
	defer restore()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	natsURL, ingest := startCLIEmbeddedGraphIngest(ctx, t)

	fixedTime := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	entityID := "c360.ops.uas1.intel.platform.alpha"
	if err := ingest.CreateEntity(ctx, &graph.EntityState{
		ID:          entityID,
		MessageType: message.Type{Domain: "test", Category: "entity", Version: "v1"},
		UpdatedAt:   fixedTime,
		Triples: []message.Triple{
			{
				Subject:   entityID,
				Predicate: "intel.role.assigned",
				Object:    "ISR",
				Source:    "test-fixture",
				Timestamp: fixedTime,
			},
			{
				Subject:   entityID,
				Predicate: "intel.measurement.altitude-m",
				Object:    float64(1500.5),
				Source:    "test-fixture",
				Timestamp: fixedTime,
			},
		},
	}); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	runCtx, stopRun := context.WithCancel(ctx)
	defer stopRun()

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()

	errCh := make(chan error, 1)
	go func() {
		defer func() { _ = pw.Close() }()
		errCh <- run(
			runCtx,
			[]string{
				"-bind", "127.0.0.1:0",
				"-nats-url", natsURL,
				"-base-iri", "https://example.org",
			},
			func(k string) string {
				if k == envBearerToken {
					return "cli-secret-token"
				}
				return ""
			},
			pw,
		)
	}()

	lineCh := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		if sc.Scan() {
			lineCh <- sc.Text()
		}
	}()

	var listenAddr string
	select {
	case line := <-lineCh:
		const prefix = "semsparql listening on "
		if !strings.HasPrefix(line, prefix) {
			t.Fatalf("unexpected startup line %q", line)
		}
		listenAddr = strings.TrimSpace(strings.TrimPrefix(line, prefix))
	case err := <-errCh:
		t.Fatalf("run exited before listening: %v", err)
	case <-ctx.Done():
		t.Fatalf("timed out waiting for semsparql listener: %v", ctx.Err())
	}

	// 1. Query SELECT with DIEKM virtual rdf:type and folded role chain over HTTP.
	sparqlQuery := `
PREFIX rdf: <http://www.w3.org/1999/02/22-rdf-syntax-ns#>
PREFIX rdfs: <http://www.w3.org/2000/01/rdf-schema#>
PREFIX obo: <http://purl.obolibrary.org/obo/>
PREFIX diekm: <https://w3id.org/c360/diekm#>
SELECT ?s ?label WHERE {
  ?s a diekm:IntelligencePlatform ;
     obo:BFO_0000196 ?role .
  ?role a obo:BFO_0000023 ;
        rdfs:label ?label .
}`
	reqURL := fmt.Sprintf("http://%s/sparql?query=%s", listenAddr, url.QueryEscape(sparqlQuery))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	httpReq.Header.Set("Authorization", "Bearer cli-secret-token")
	httpReq.Header.Set("Accept", "application/sparql-results+json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("GET /sparql: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /sparql status = %d, body = %s", resp.StatusCode, string(body))
	}
	if !bytes.Contains(body, []byte("https://example.org/entities/c360/ops/uas1/intel/platform/alpha")) || !bytes.Contains(body, []byte("ISR")) {
		t.Fatalf("GET /sparql body missing expected binding: %s", string(body))
	}

	// 2. Clean shutdown on context cancellation.
	stopRun()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("run returned error on graceful shutdown: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for run shutdown: %v", ctx.Err())
	}
}

func startCLIEmbeddedGraphIngest(ctx context.Context, t *testing.T) (string, *graphingest.Component) {
	t.Helper()
	ns, err := natsserver.NewServer(&natsserver.Options{
		Host:      "127.0.0.1",
		Port:      -1,
		NoLog:     true,
		NoSigs:    true,
		JetStream: true,
		StoreDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("natsserver.NewServer: %v", err)
	}
	ns.Start()
	t.Cleanup(func() {
		ns.Shutdown()
		ns.WaitForShutdown()
	})
	if !ns.ReadyForConnections(10 * time.Second) {
		t.Fatal("embedded NATS server not ready")
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
		Description: "test entity",
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

	return ns.ClientURL(), ingest
}

func writeSelfSignedTLSFiles(t *testing.T) (certPath, keyPath, caPath string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("CreateCertificate: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalECPrivateKey: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	dir := t.TempDir()
	certPath = filepath.Join(dir, "server.crt")
	keyPath = filepath.Join(dir, "server.key")
	caPath = filepath.Join(dir, "ca.crt")
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(cert): %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(key): %v", err)
	}
	if err := os.WriteFile(caPath, certPEM, 0o600); err != nil {
		t.Fatalf("WriteFile(ca): %v", err)
	}
	return certPath, keyPath, caPath
}
