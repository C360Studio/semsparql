package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/natsclient"
	"github.com/c360studio/semengine/pkg/errs"
	semtypes "github.com/c360studio/semengine/pkg/types"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semsparql/sparql"
)

// Operator defaults and protocol header names (owner rulings D.1a, D.1b).
const (
	DefaultBindAddress    = "127.0.0.1:8080"
	DefaultMaxQueryBytes  = 64 * 1024  // 64 KiB
	DefaultMaxExportBytes = 256 * 1024 // 256 KiB

	HeaderAllowTruncation  = "X-Semsparql-Allow-Truncation"
	HeaderTruncated        = "X-Semsparql-Truncated"
	HeaderTruncationReason = "X-Semsparql-Truncation-Reason"
)

var (
	// ErrUnsafeExposure indicates a non-local bind address was configured without
	// bearer-token or mTLS client certificate authentication.
	ErrUnsafeExposure = errors.New("server: non-local bind address requires BearerToken or mTLS client authentication")

	// ErrQueryTooLarge indicates a SPARQL query exceeded MaxQueryBytes (HTTP 413 Content Too Large, D.1b).
	ErrQueryTooLarge = errors.New("server: SPARQL query exceeds maximum byte limit")

	// ErrExportBodyTooLarge indicates a POST /export request body exceeded MaxExportBytes (HTTP 413 Content Too Large, D.1b).
	ErrExportBodyTooLarge = errors.New("server: export request body exceeds maximum byte limit")
)

// Config configures the SPARQL 1.1 HTTP Server.
type Config struct {
	BindAddress     string
	BaseIRI         string
	BearerToken     string
	TLSConfig       *tls.Config
	QueryTimeout    time.Duration
	MaxResults      int
	MaxScanEntities int
	MaxQueryBytes   int
	MaxExportBytes  int
}

// ValidateConfig verifies that the bind address is well-formed and that any
// non-local bind is protected by a bearer token or mTLS client verification.
func ValidateConfig(cfg Config) error {
	bind := cfg.BindAddress
	if bind == "" {
		bind = DefaultBindAddress
	}
	host, _, err := net.SplitHostPort(bind)
	if err != nil {
		return fmt.Errorf("server: invalid bind address %q: %w", bind, err)
	}
	if !isLoopbackHost(host) && !hasAuthConfigured(cfg) {
		return fmt.Errorf("%w: bind address %q", ErrUnsafeExposure, bind)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func hasAuthConfigured(cfg Config) bool {
	if strings.TrimSpace(cfg.BearerToken) != "" {
		return true
	}
	if cfg.TLSConfig != nil &&
		cfg.TLSConfig.ClientAuth >= tls.RequireAndVerifyClientCert &&
		cfg.TLSConfig.ClientCAs != nil {
		return true
	}
	return false
}

// Server serves the read-only SPARQL 1.1 Protocol and RDF export endpoints.
type Server struct {
	cfg      Config
	reader   reader.GraphReader
	mapper   *irimap.Mapper
	executor *sparql.Executor
	mux      *http.ServeMux

	mu      sync.Mutex
	httpSrv *http.Server
}

// New validates cfg and constructs a Server backed by r.
func New(r reader.GraphReader, cfg Config) (*Server, error) {
	if r == nil {
		return nil, errors.New("server: nil GraphReader")
	}
	if cfg.BindAddress == "" {
		cfg.BindAddress = DefaultBindAddress
	}
	if cfg.MaxQueryBytes <= 0 {
		cfg.MaxQueryBytes = DefaultMaxQueryBytes
	}
	if cfg.MaxExportBytes <= 0 {
		cfg.MaxExportBytes = DefaultMaxExportBytes
	}
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if err := sparql.ValidateSuperclassClosure(); err != nil {
		return nil, err
	}

	mapper := irimap.NewMapper(cfg.BaseIRI)
	exec := sparql.NewExecutor(r, sparql.Config{
		Mapper:          mapper,
		Timeout:         cfg.QueryTimeout,
		MaxResults:      cfg.MaxResults,
		MaxScanEntities: cfg.MaxScanEntities,
	})

	s := &Server{
		cfg:      cfg,
		reader:   r,
		mapper:   mapper,
		executor: exec,
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("/sparql", s.handleSPARQL)
	s.mux.HandleFunc("/entities/", s.handleEntityJSONLD)
	s.mux.HandleFunc("/export", s.handleExport)
	return s, nil
}

// Handler returns the Server's HTTP handler (including optional bearer-token
// authentication middleware).
func (s *Server) Handler() http.Handler {
	if strings.TrimSpace(s.cfg.BearerToken) == "" {
		return s.mux
	}
	return s.requireBearerAuth(s.mux)
}

// Serve serves HTTP requests on ln until ctx is cancelled or Shutdown is called.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	if ctx == nil {
		return reader.ErrNilContext
	}
	if ln == nil {
		return errors.New("server: nil net.Listener")
	}

	httpSrv := &http.Server{
		Handler:   s.Handler(),
		TLSConfig: s.cfg.TLSConfig,
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	s.mu.Lock()
	s.httpSrv = httpSrv
	s.mu.Unlock()

	serveLn := ln
	if s.cfg.TLSConfig != nil && (len(s.cfg.TLSConfig.Certificates) > 0 || s.cfg.TLSConfig.GetCertificate != nil) {
		serveLn = tls.NewListener(ln, s.cfg.TLSConfig)
	}

	errCh := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(serveLn)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		return <-errCh
	}
}

// Shutdown gracefully stops the running HTTP server under ctx. A nil context is
// refused with reader.ErrNilContext.
func (s *Server) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return reader.ErrNilContext
	}
	s.mu.Lock()
	httpSrv := s.httpSrv
	s.mu.Unlock()
	if httpSrv == nil {
		return nil
	}
	return httpSrv.Shutdown(ctx)
}

func (s *Server) requireBearerAuth(next http.Handler) http.Handler {
	expected := "Bearer " + strings.TrimSpace(s.cfg.BearerToken)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimSpace(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="semsparql"`)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleSPARQL(w http.ResponseWriter, r *http.Request) {
	queryStr, status, code, err := s.extractSPARQLQuery(r)
	if err != nil {
		writeJSONError(w, status, code, err.Error())
		return
	}

	q, err := sparql.Parse(queryStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, classifyErrorCode(err, sparql.ErrorCodeSyntax), err.Error())
		return
	}

	format, err := NegotiateAccept(q.Form, r.Header.Get("Accept"))
	if err != nil {
		writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", err.Error())
		return
	}

	allowTruncation := strings.EqualFold(strings.TrimSpace(r.Header.Get(HeaderAllowTruncation)), "true")
	res, err := s.executor.Execute(r.Context(), q, sparql.WithAllowTruncation(allowTruncation))
	if err != nil {
		s.writeExecutionError(w, err)
		return
	}

	// Buffer the serialized response before writing HTTP 200 headers so any
	// serialization failure fails closed rather than emitting a partial body.
	var buf bytes.Buffer
	switch res.Form {
	case sparql.QueryConstruct, sparql.QueryDescribe:
		if err := WriteGraphResult(&buf, res.Triples, format, s.mapper.BaseIRI()); err != nil {
			writeJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "serialization_error"), err.Error())
			return
		}
	default:
		if err := WriteSelectAskResult(&buf, res, format); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "serialization_error", err.Error())
			return
		}
	}

	w.Header().Set("Content-Type", format.ContentType())
	if res.Truncated {
		w.Header().Set(HeaderTruncated, "true")
		if res.TruncationReason != "" {
			w.Header().Set(HeaderTruncationReason, res.TruncationReason)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleEntityJSONLD(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", fmt.Sprintf("method %s not allowed on %s", r.Method, r.URL.Path))
		return
	}

	const prefix = "/entities/"
	const suffix = ".jsonld"
	if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, suffix) {
		writeJSONError(w, http.StatusBadRequest, "invalid_entity_id", "path must match /entities/{entity_id}.jsonld")
		return
	}
	entityID := r.URL.Path[len(prefix) : len(r.URL.Path)-len(suffix)]
	if entityID == "" || strings.Contains(entityID, "/") {
		writeJSONError(w, http.StatusBadRequest, "invalid_entity_id", fmt.Sprintf("invalid entity ID %q", entityID))
		return
	}
	if _, err := semtypes.ParseEntityID(entityID); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_entity_id", err.Error())
		return
	}

	if accept := strings.TrimSpace(r.Header.Get("Accept")); accept != "" {
		if !acceptsJSONLD(accept) {
			writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", fmt.Sprintf("Accept %q is incompatible with application/ld+json", accept))
			return
		}
	}

	timeout := s.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = sparql.DefaultQueryTimeout
	}
	queryCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	ent, err := s.reader.GetEntity(queryCtx, entityID)
	if err != nil {
		if errors.Is(err, reader.ErrEntityNotFound) {
			writeJSONError(w, http.StatusNotFound, "entity_not_found", err.Error())
			return
		}
		s.writeExecutionError(w, err)
		return
	}

	triples, err := sparql.UnfoldEntity(ent, s.mapper.BaseIRI())
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "serialization_error"), err.Error())
		return
	}

	allowTruncation := strings.EqualFold(strings.TrimSpace(r.Header.Get(HeaderAllowTruncation)), "true")
	maxResults := s.cfg.MaxResults
	if maxResults <= 0 {
		maxResults = sparql.DefaultMaxResults
	}
	truncated := false
	truncReason := ""
	if len(triples) > maxResults {
		if !allowTruncation {
			writeJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeResultLimitExceeded,
				fmt.Sprintf("server: entity graph (%d triples) exceeds max_results limit of %d", len(triples), maxResults))
			return
		}
		triples = triples[:maxResults]
		truncated = true
		truncReason = sparql.TruncationReasonResultLimit
	}

	var buf bytes.Buffer
	if err := WriteGraphResult(&buf, triples, FormatJSONLD, s.mapper.BaseIRI()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "serialization_error"), err.Error())
		return
	}

	w.Header().Set("Content-Type", FormatJSONLD.ContentType())
	if truncated {
		w.Header().Set(HeaderTruncated, "true")
		w.Header().Set(HeaderTruncationReason, truncReason)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func acceptsJSONLD(acceptHeader string) bool {
	clauses, err := parseAcceptHeader(acceptHeader)
	if err != nil {
		return false
	}
	for _, c := range clauses {
		if c.q <= 0 {
			continue
		}
		switch c.mediaType {
		case MediaTypeJSONLD, "application/json", "application/*", "*/*":
			return true
		}
	}
	return false
}

type exportSelectorRequest struct {
	Prefix    *string `json:"prefix"`
	Construct *string `json:"construct"`
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method_not_allowed", fmt.Sprintf("method %s not allowed on /export", r.Method))
		return
	}
	if r.URL.RawQuery != "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", "POST /export does not accept URL query parameters")
		return
	}

	if ctHeader := strings.TrimSpace(r.Header.Get("Content-Type")); ctHeader != "" {
		mt, _, err := mime.ParseMediaType(ctHeader)
		if err != nil || !strings.EqualFold(mt, "application/json") {
			writeJSONError(w, http.StatusBadRequest, "invalid_content_type", fmt.Sprintf("POST /export requires Content-Type application/json, got %q", ctHeader))
			return
		}
	}

	maxExportBytes := s.cfg.MaxExportBytes
	if maxExportBytes <= 0 {
		maxExportBytes = DefaultMaxExportBytes
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxExportBytes)+1))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request", fmt.Errorf("read export body: %w", err).Error())
		return
	}
	if len(body) > maxExportBytes {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "export_body_too_large", ErrExportBodyTooLarge.Error())
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeJSONError(w, http.StatusBadRequest, "invalid_export_selector", "empty export request body")
		return
	}

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	var sel exportSelectorRequest
	if err := dec.Decode(&sel); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_export_selector", fmt.Errorf("invalid JSON export selector: %w", err).Error())
		return
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		writeJSONError(w, http.StatusBadRequest, "invalid_export_selector", "unexpected trailing JSON content in export request body")
		return
	}

	hasPrefix := sel.Prefix != nil
	hasConstruct := sel.Construct != nil
	if hasPrefix == hasConstruct {
		writeJSONError(w, http.StatusBadRequest, "invalid_export_selector",
			"POST /export requires exactly one of \"prefix\" or \"construct\"")
		return
	}

	format, err := NegotiateAccept(sparql.QueryConstruct, r.Header.Get("Accept"))
	if err != nil {
		writeJSONError(w, http.StatusNotAcceptable, "not_acceptable", err.Error())
		return
	}

	allowTruncation := strings.EqualFold(strings.TrimSpace(r.Header.Get(HeaderAllowTruncation)), "true")

	if hasPrefix {
		s.handlePrefixExport(w, r, *sel.Prefix, format, allowTruncation)
		return
	}
	s.handleConstructExport(w, r, *sel.Construct, format, allowTruncation)
}

func (s *Server) handlePrefixExport(
	w http.ResponseWriter,
	r *http.Request,
	prefix string,
	format ResultFormat,
	allowTruncation bool,
) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_prefix", "export \"prefix\" selector must not be empty")
		return
	}
	if err := semtypes.ValidateEntityIDPrefix(prefix); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_prefix", err.Error())
		return
	}

	timeout := s.cfg.QueryTimeout
	if timeout <= 0 {
		timeout = sparql.DefaultQueryTimeout
	}
	queryCtx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	maxScan := s.cfg.MaxScanEntities
	if maxScan <= 0 {
		maxScan = sparql.DefaultMaxScanEntities
	}
	maxResults := s.cfg.MaxResults
	if maxResults <= 0 {
		maxResults = sparql.DefaultMaxResults
	}

	truncated := false
	truncReason := ""
	entities, err := s.reader.ScanPrefix(queryCtx, prefix, maxScan)
	if err != nil {
		if errors.Is(err, reader.ErrScanBudgetExceeded) {
			if !allowTruncation {
				writeJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeScanBudgetExceeded, err.Error())
				return
			}
			truncated = true
			truncReason = sparql.TruncationReasonScanBudget
		} else {
			s.writeExecutionError(w, err)
			return
		}
	}

	base := s.mapper.BaseIRI()
	seen := make(map[string]struct{})
	triples := make([]message.Triple, 0, len(entities)*4)
	for i := range entities {
		if err := queryCtx.Err(); err != nil {
			s.writeExecutionError(w, err)
			return
		}
		unfolded, err := sparql.UnfoldEntity(&entities[i], base)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "serialization_error"), err.Error())
			return
		}
		var stopped bool
		for _, tr := range unfolded {
			ntLine, err := export.SerializeToString([]message.Triple{tr}, export.NTriples, sparql.ExportOptions(base)...)
			if err != nil || strings.TrimSpace(ntLine) == "" {
				writeJSONError(w, http.StatusInternalServerError, sparql.ErrorCodeInvalidOutputTriple,
					fmt.Sprintf("invalid output triple on entity %q", entities[i].ID))
				return
			}
			if _, exists := seen[ntLine]; exists {
				continue
			}
			if len(triples) >= maxResults {
				if !allowTruncation {
					writeJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeResultLimitExceeded,
						fmt.Sprintf("sparql: export exceeded max_results limit of %d", maxResults))
					return
				}
				if !truncated {
					truncated = true
					truncReason = sparql.TruncationReasonResultLimit
				}
				stopped = true
				break
			}
			seen[ntLine] = struct{}{}
			triples = append(triples, tr)
		}
		if stopped {
			break
		}
	}

	var buf bytes.Buffer
	if err := WriteGraphResult(&buf, triples, format, base); err != nil {
		writeJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "serialization_error"), err.Error())
		return
	}

	w.Header().Set("Content-Type", format.ContentType())
	if truncated {
		w.Header().Set(HeaderTruncated, "true")
		w.Header().Set(HeaderTruncationReason, truncReason)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) handleConstructExport(
	w http.ResponseWriter,
	r *http.Request,
	constructStr string,
	format ResultFormat,
	allowTruncation bool,
) {
	if strings.TrimSpace(constructStr) == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid_export_selector", "export \"construct\" selector must not be empty")
		return
	}

	q, err := sparql.Parse(constructStr)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, classifyErrorCode(err, sparql.ErrorCodeSyntax), err.Error())
		return
	}
	if q.Form != sparql.QueryConstruct {
		writeJSONError(w, http.StatusBadRequest, "invalid_export_selector",
			"POST /export \"construct\" selector must be a CONSTRUCT query")
		return
	}

	res, err := s.executor.Execute(r.Context(), q, sparql.WithAllowTruncation(allowTruncation))
	if err != nil {
		s.writeExecutionError(w, err)
		return
	}

	var buf bytes.Buffer
	if err := WriteGraphResult(&buf, res.Triples, format, s.mapper.BaseIRI()); err != nil {
		writeJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "serialization_error"), err.Error())
		return
	}

	w.Header().Set("Content-Type", format.ContentType())
	if res.Truncated {
		w.Header().Set(HeaderTruncated, "true")
		if res.TruncationReason != "" {
			w.Header().Set(HeaderTruncationReason, res.TruncationReason)
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) extractSPARQLQuery(r *http.Request) (queryStr string, status int, code string, err error) {
	maxBytes := s.cfg.MaxQueryBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxQueryBytes
	}

	switch r.Method {
	case http.MethodGet:
		if len(r.URL.RawQuery) > maxBytes*3 {
			// Check decoded query length first if query param exists.
			values, parseErr := url.ParseQuery(r.URL.RawQuery)
			if parseErr != nil {
				return "", http.StatusBadRequest, "invalid_request", fmt.Errorf("invalid URL query parameters: %w", parseErr)
			}
			for _, qv := range values["query"] {
				if len(qv) > maxBytes {
					return "", http.StatusRequestEntityTooLarge, "query_too_large", ErrQueryTooLarge
				}
			}
			return "", http.StatusRequestEntityTooLarge, "query_too_large", ErrQueryTooLarge
		}
		values, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			return "", http.StatusBadRequest, "invalid_request", fmt.Errorf("invalid URL query parameters: %w", err)
		}
		return validateQueryValues(values, maxBytes)

	case http.MethodPost:
		if r.URL.RawQuery != "" {
			return "", http.StatusBadRequest, "invalid_request", errors.New("POST /sparql does not accept URL query parameters")
		}
		ctHeader := strings.TrimSpace(r.Header.Get("Content-Type"))
		if ctHeader == "" {
			return "", http.StatusBadRequest, "invalid_content_type", errors.New("missing Content-Type on POST /sparql")
		}
		mt, _, err := mime.ParseMediaType(ctHeader)
		if err != nil {
			return "", http.StatusBadRequest, "invalid_content_type", fmt.Errorf("invalid Content-Type %q: %w", ctHeader, err)
		}
		switch strings.ToLower(mt) {
		case "application/sparql-query":
			body, err := io.ReadAll(io.LimitReader(r.Body, int64(maxBytes)+1))
			if err != nil {
				return "", http.StatusBadRequest, "invalid_request", fmt.Errorf("read request body: %w", err)
			}
			if len(body) > maxBytes {
				return "", http.StatusRequestEntityTooLarge, "query_too_large", ErrQueryTooLarge
			}
			if strings.TrimSpace(string(body)) == "" {
				return "", http.StatusBadRequest, "invalid_request", errors.New("empty SPARQL query body")
			}
			return string(body), 0, "", nil

		case "application/x-www-form-urlencoded":
			// Allow up to 3x expansion for percent-encoded form bodies before checking decoded query byte length.
			formCap := maxBytes*3 + 64
			body, err := io.ReadAll(io.LimitReader(r.Body, int64(formCap)+1))
			if err != nil {
				return "", http.StatusBadRequest, "invalid_request", fmt.Errorf("read form body: %w", err)
			}
			if len(body) > formCap {
				return "", http.StatusRequestEntityTooLarge, "query_too_large", ErrQueryTooLarge
			}
			values, err := url.ParseQuery(string(body))
			if err != nil {
				return "", http.StatusBadRequest, "invalid_request", fmt.Errorf("invalid form body: %w", err)
			}
			return validateQueryValues(values, maxBytes)

		default:
			return "", http.StatusBadRequest, "invalid_content_type", fmt.Errorf("unsupported POST Content-Type %q", mt)
		}

	default:
		return "", http.StatusMethodNotAllowed, "method_not_allowed", fmt.Errorf("method %s not allowed on /sparql", r.Method)
	}
}

func validateQueryValues(values url.Values, maxBytes int) (string, int, string, error) {
	for k := range values {
		switch k {
		case "query":
		case "default-graph-uri", "named-graph-uri":
			return "", http.StatusBadRequest, sparql.ErrorCodeUnsupportedFeature,
				errors.New("sparql: unsupported feature \"named graphs\": default-graph-uri and named-graph-uri parameters are not supported")
		default:
			return "", http.StatusBadRequest, "invalid_request", fmt.Errorf("unsupported query parameter %q", k)
		}
	}
	queries := values["query"]
	if len(queries) == 0 {
		return "", http.StatusBadRequest, "invalid_request", errors.New("missing required \"query\" parameter")
	}
	if len(queries) > 1 {
		return "", http.StatusBadRequest, "invalid_request", errors.New("multiple \"query\" parameters are not allowed")
	}
	q := queries[0]
	if len(q) > maxBytes {
		return "", http.StatusRequestEntityTooLarge, "query_too_large", ErrQueryTooLarge
	}
	if strings.TrimSpace(q) == "" {
		return "", http.StatusBadRequest, "invalid_request", errors.New("empty \"query\" parameter")
	}
	return q, 0, "", nil
}

func (s *Server) writeExecutionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sparql.ErrResultLimitExceeded):
		writeClassifiedJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeResultLimitExceeded, err)
	case errors.Is(err, sparql.ErrScanBudgetExceeded):
		writeClassifiedJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeScanBudgetExceeded, err)
	case errors.Is(err, sparql.ErrQueryTimeout), errors.Is(err, context.DeadlineExceeded):
		writeClassifiedJSONError(w, http.StatusGatewayTimeout, sparql.ErrorCodeQueryTimeout, err)
	case errors.Is(err, context.Canceled):
		writeClassifiedJSONError(w, http.StatusGatewayTimeout, "request_canceled", err)
	case errors.Is(err, sparql.ErrInvalidOutputTriple):
		writeClassifiedJSONError(w, http.StatusInternalServerError, sparql.ErrorCodeInvalidOutputTriple, err)
	case errs.IsFatal(err):
		writeClassifiedJSONError(w, http.StatusInternalServerError, classifyErrorCode(err, "internal_error"), err)
	case errs.IsTransient(err), natsclient.IsNoResponders(err):
		writeClassifiedJSONError(w, http.StatusServiceUnavailable, classifyErrorCode(err, "upstream_unavailable"), err)
	default:
		writeClassifiedJSONError(w, http.StatusBadRequest, classifyErrorCode(err, "query_execution_error"), err)
	}
}

func classifyErrorCode(err error, fallback string) string {
	var ce *errs.ClassifiedError
	if errors.As(err, &ce) && ce.Code != "" {
		return ce.Code
	}
	return fallback
}

type errorResponse struct {
	Error   string         `json:"error"`
	Class   string         `json:"class,omitempty"`
	Message string         `json:"message"`
	Detail  map[string]any `json:"detail,omitempty"`
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   code,
		Message: msg,
	})
}

func writeClassifiedJSONError(w http.ResponseWriter, status int, code string, err error) {
	resp := errorResponse{
		Error:   code,
		Message: err.Error(),
	}
	var ce *errs.ClassifiedError
	if errors.As(err, &ce) {
		resp.Class = ce.Class.String()
		if len(ce.Detail) > 0 {
			resp.Detail = ce.Detail
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(resp)
}
