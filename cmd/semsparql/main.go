// Command semsparql runs the read-only SPARQL 1.1 Protocol and JSON-LD query/export sidecar for SemEngine.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/c360studio/semengine/natsclient"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semsparql/server"
	"github.com/c360studio/semsparql/sparql"
	"github.com/c360studio/semvocab/packs"
	"github.com/c360studio/semvocab/packs/diekm"
)

const (
	defaultNATSURL       = "nats://127.0.0.1:4222"
	defaultNATSTimeout   = 10 * time.Second
	defaultNATSLossGrace = 30 * time.Second
	envBearerToken       = "SEMSPARQL_BEARER_TOKEN"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Getenv, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "semsparql: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) error {
	if ctx == nil {
		return reader.ErrNilContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if stderr == nil {
		stderr = io.Discard
	}

	fs := flag.NewFlagSet("semsparql", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var (
		bindAddr        = fs.String("bind", server.DefaultBindAddress, "HTTP listen address (non-local binds require -bearer-token or mTLS)")
		natsURL         = fs.String("nats-url", defaultNATSURL, "NATS server URL")
		natsTimeout     = fs.Duration("nats-timeout", defaultNATSTimeout, "Per-request NATS RPC timeout")
		natsLossGrace   = fs.Duration("nats-loss-grace", defaultNATSLossGrace, "Grace period after NATS disconnect before failing closed (0 disables)")
		baseIRI         = fs.String("base-iri", vocabulary.SemEngineBase, "Base IRI for canonical entity IRIs")
		bearerToken     = fs.String("bearer-token", "", "Bearer token for HTTP authentication (prefer SEMSPARQL_BEARER_TOKEN to avoid process-list exposure)")
		tlsCertPath     = fs.String("tls-cert", "", "Path to server TLS certificate PEM file")
		tlsKeyPath      = fs.String("tls-key", "", "Path to server TLS private key PEM file")
		tlsClientCAPath = fs.String("tls-client-ca", "", "Path to client CA bundle PEM file for mTLS verification")
		queryTimeout    = fs.Duration("query-timeout", sparql.DefaultQueryTimeout, "Per-query execution timeout")
		maxResults      = fs.Int("max-results", sparql.DefaultMaxResults, "Maximum results (bindings or RDF triples) per request")
		maxScanEntities = fs.Int("max-scan-entities", sparql.DefaultMaxScanEntities, "Maximum entities scanned per request")
		maxQueryBytes   = fs.Int("max-query-bytes", server.DefaultMaxQueryBytes, "Maximum SPARQL query size in bytes")
		maxExportBytes  = fs.Int("max-export-bytes", server.DefaultMaxExportBytes, "Maximum POST /export body size in bytes")
	)

	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("parse flags: %w", err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %v", fs.Args())
	}

	if strings.TrimSpace(*natsURL) == "" {
		return errors.New("nats-url must not be empty")
	}
	if *natsTimeout < 0 {
		return fmt.Errorf("nats-timeout must not be negative, got %s", *natsTimeout)
	}
	if *natsLossGrace < 0 {
		return fmt.Errorf("nats-loss-grace must not be negative, got %s", *natsLossGrace)
	}
	if *queryTimeout <= 0 {
		return fmt.Errorf("query-timeout must be positive, got %s", *queryTimeout)
	}
	if *maxResults <= 0 {
		return fmt.Errorf("max-results must be positive, got %d", *maxResults)
	}
	if *maxScanEntities <= 0 {
		return fmt.Errorf("max-scan-entities must be positive, got %d", *maxScanEntities)
	}
	if *maxQueryBytes <= 0 {
		return fmt.Errorf("max-query-bytes must be positive, got %d", *maxQueryBytes)
	}
	if *maxExportBytes <= 0 {
		return fmt.Errorf("max-export-bytes must be positive, got %d", *maxExportBytes)
	}

	token := strings.TrimSpace(*bearerToken)
	if token == "" && getenv != nil {
		token = strings.TrimSpace(getenv(envBearerToken))
	}

	tlsCfg, err := buildTLSConfig(*tlsCertPath, *tlsKeyPath, *tlsClientCAPath)
	if err != nil {
		return err
	}

	srvCfg := server.Config{
		BindAddress:     *bindAddr,
		BaseIRI:         *baseIRI,
		BearerToken:     token,
		TLSConfig:       tlsCfg,
		QueryTimeout:    *queryTimeout,
		MaxResults:      *maxResults,
		MaxScanEntities: *maxScanEntities,
		MaxQueryBytes:   *maxQueryBytes,
		MaxExportBytes:  *maxExportBytes,
	}
	if err := server.ValidateConfig(srvCfg); err != nil {
		return err
	}

	if err := registerDefaultVocabularyPacks(); err != nil {
		return err
	}
	if err := sparql.ValidateSuperclassClosure(); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	nc, err := natsclient.NewClient(
		*natsURL,
		natsclient.WithName("semsparql"),
		natsclient.WithTimeout(*natsTimeout),
		natsclient.WithHealthInterval(0),
		natsclient.WithConnectionLossTimeout(*natsLossGrace),
		natsclient.WithConnectionLostCallback(func(connErr error) {
			if connErr == nil {
				connErr = errors.New("NATS connection permanently lost")
			}
			cancelRun(fmt.Errorf("nats connection lost: %w", connErr))
		}),
	)
	if err != nil {
		return fmt.Errorf("create NATS client: %w", err)
	}
	if err := nc.Connect(runCtx); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		return errors.Join(fmt.Errorf("connect NATS client: %w", err), nc.Close(closeCtx))
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = nc.Close(closeCtx)
	}()
	if err := nc.WaitForConnection(runCtx); err != nil {
		return fmt.Errorf("wait for NATS connection: %w", err)
	}

	natsReader, err := reader.NewNATSRPCReader(nc, *natsTimeout)
	if err != nil {
		return err
	}
	if err := natsReader.VerifyPatternCapability(runCtx); err != nil {
		return err
	}

	srv, err := server.New(natsReader, srvCfg)
	if err != nil {
		return err
	}

	ln, err := (&net.ListenConfig{}).Listen(runCtx, "tcp", srvCfg.BindAddress)
	if err != nil {
		return fmt.Errorf("listen %s: %w", srvCfg.BindAddress, err)
	}
	_, _ = fmt.Fprintf(stderr, "semsparql listening on %s\n", ln.Addr().String())

	serveErr := srv.Serve(runCtx, ln)
	if cause := context.Cause(runCtx); cause != nil && ctx.Err() == nil {
		return errors.Join(cause, serveErr)
	}
	return serveErr
}

func registerDefaultVocabularyPacks() error {
	if err := packs.RegisterBFOCCOCore(); err != nil {
		return fmt.Errorf("register bfo-cco-core pack: %w", err)
	}
	if err := diekm.Register(); err != nil {
		return fmt.Errorf("register diekm pack: %w", err)
	}
	return nil
}

func buildTLSConfig(certPath, keyPath, clientCAPath string) (*tls.Config, error) {
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	clientCAPath = strings.TrimSpace(clientCAPath)

	if certPath == "" && keyPath == "" && clientCAPath == "" {
		return nil, nil
	}
	if certPath == "" || keyPath == "" {
		return nil, errors.New("tls configuration requires both -tls-cert and -tls-key")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	cfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if clientCAPath != "" {
		caPEM, err := os.ReadFile(clientCAPath)
		if err != nil {
			return nil, fmt.Errorf("read TLS client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("TLS client CA file contains no valid certificates")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}
