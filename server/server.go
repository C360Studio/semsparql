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

	"github.com/c360studio/semengine/pkg/errs"
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
	if err := ValidateConfig(cfg); err != nil {
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
		mapper:   mapper,
		executor: exec,
		mux:      http.NewServeMux(),
	}
	s.mux.HandleFunc("/sparql", s.handleSPARQL)
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

	errCh := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errCh <- err
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithCancel(context.Background())
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
	if err := WriteSelectAskResult(&buf, res, format); err != nil {
		writeJSONError(w, http.StatusInternalServerError, "serialization_error", err.Error())
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
		writeJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeResultLimitExceeded, err.Error())
	case errors.Is(err, sparql.ErrScanBudgetExceeded):
		writeJSONError(w, http.StatusUnprocessableEntity, sparql.ErrorCodeScanBudgetExceeded, err.Error())
	case errors.Is(err, sparql.ErrQueryTimeout), errors.Is(err, context.DeadlineExceeded):
		writeJSONError(w, http.StatusGatewayTimeout, sparql.ErrorCodeQueryTimeout, err.Error())
	case errors.Is(err, context.Canceled):
		writeJSONError(w, http.StatusGatewayTimeout, "request_canceled", err.Error())
	default:
		writeJSONError(w, http.StatusBadRequest, classifyErrorCode(err, "query_execution_error"), err.Error())
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
	Error   string `json:"error"`
	Message string `json:"message"`
}

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorResponse{
		Error:   code,
		Message: msg,
	})
}
