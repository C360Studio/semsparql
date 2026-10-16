package server

import (
	"errors"
	"fmt"
	"mime"
	"strconv"
	"strings"

	"github.com/c360studio/semsparql/sparql"
)

// Standard SPARQL 1.1 result and RDF graph media types.
const (
	MediaTypeSPARQLJSON = "application/sparql-results+json"
	MediaTypeSPARQLXML  = "application/sparql-results+xml"
	MediaTypeCSV        = "text/csv"
	MediaTypeTSV        = "text/tab-separated-values"

	MediaTypeTurtle   = "text/turtle"
	MediaTypeNTriples = "application/n-triples"
	MediaTypeJSONLD   = "application/ld+json"
)

// ErrNotAcceptable indicates that none of the client's Accept media ranges are
// compatible with the parsed SPARQL query form (HTTP 406 Not Acceptable, D.1a).
var ErrNotAcceptable = errors.New("server: Accept media type is not compatible with query form")

// ResultFormat identifies a negotiated wire representation for a SPARQL query response.
type ResultFormat int

const (
	// FormatSPARQLJSON is W3C SPARQL 1.1 Query Results JSON Format.
	FormatSPARQLJSON ResultFormat = iota + 1
	// FormatSPARQLXML is W3C SPARQL Query Results XML Format.
	FormatSPARQLXML
	// FormatCSV is W3C SPARQL 1.1 Query Results CSV Format.
	FormatCSV
	// FormatTSV is W3C SPARQL 1.1 Query Results TSV Format.
	FormatTSV
	// FormatTurtle is W3C RDF 1.1 Turtle.
	FormatTurtle
	// FormatNTriples is W3C RDF 1.1 N-Triples.
	FormatNTriples
	// FormatJSONLD is W3C JSON-LD.
	FormatJSONLD
)

// ContentType returns the HTTP Content-Type header value for the negotiated format.
func (f ResultFormat) ContentType() string {
	switch f {
	case FormatSPARQLJSON:
		return MediaTypeSPARQLJSON + "; charset=utf-8"
	case FormatSPARQLXML:
		return MediaTypeSPARQLXML + "; charset=utf-8"
	case FormatCSV:
		return MediaTypeCSV + "; charset=utf-8"
	case FormatTSV:
		return MediaTypeTSV + "; charset=utf-8"
	case FormatTurtle:
		return MediaTypeTurtle + "; charset=utf-8"
	case FormatNTriples:
		return MediaTypeNTriples + "; charset=utf-8"
	case FormatJSONLD:
		return MediaTypeJSONLD + "; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

type acceptClause struct {
	mediaType   string
	q           float64
	specificity int
	order       int
}

// NegotiateAccept selects the best ResultFormat compatible with form for the
// given HTTP Accept header value. An empty Accept header selects the form's
// default representation (application/sparql-results+json for SELECT/ASK,
// text/turtle for CONSTRUCT/DESCRIBE).
func NegotiateAccept(form sparql.QueryForm, acceptHeader string) (ResultFormat, error) {
	trimmed := strings.TrimSpace(acceptHeader)
	if trimmed == "" {
		return defaultFormatForForm(form)
	}

	clauses, err := parseAcceptHeader(trimmed)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrNotAcceptable, err)
	}

	candidates := candidateFormatsForForm(form)
	if len(candidates) == 0 {
		return 0, ErrNotAcceptable
	}

	var (
		bestFormat ResultFormat
		bestQ      = -1.0
		bestSpec   = -1
		bestOrder  = len(clauses) + 1
	)

	for _, f := range candidates {
		c, ok := mostSpecificClauseForFormat(f, clauses)
		if !ok || c.q <= 0 {
			continue
		}
		if c.q > bestQ ||
			(c.q == bestQ && c.specificity > bestSpec) ||
			(c.q == bestQ && c.specificity == bestSpec && c.order < bestOrder) {
			bestFormat = f
			bestQ = c.q
			bestSpec = c.specificity
			bestOrder = c.order
		}
	}

	if bestFormat == 0 {
		return 0, fmt.Errorf("%w: Accept %q is incompatible with query form", ErrNotAcceptable, acceptHeader)
	}
	return bestFormat, nil
}

func defaultFormatForForm(form sparql.QueryForm) (ResultFormat, error) {
	switch form {
	case sparql.QuerySelect, sparql.QueryAsk:
		return FormatSPARQLJSON, nil
	case sparql.QueryConstruct, sparql.QueryDescribe:
		return FormatTurtle, nil
	default:
		return 0, ErrNotAcceptable
	}
}

func candidateFormatsForForm(form sparql.QueryForm) []ResultFormat {
	switch form {
	case sparql.QuerySelect:
		return []ResultFormat{FormatSPARQLJSON, FormatSPARQLXML, FormatCSV, FormatTSV}
	case sparql.QueryAsk:
		return []ResultFormat{FormatSPARQLJSON, FormatSPARQLXML}
	case sparql.QueryConstruct, sparql.QueryDescribe:
		return []ResultFormat{FormatTurtle, FormatJSONLD, FormatNTriples}
	default:
		return nil
	}
}

func mostSpecificClauseForFormat(f ResultFormat, clauses []acceptClause) (acceptClause, bool) {
	var (
		best     acceptClause
		bestSpec = -1
		matched  bool
	)
	for _, c := range clauses {
		spec, ok := matchFormatSpecificity(f, c.mediaType)
		if !ok {
			continue
		}
		if !matched ||
			spec > bestSpec ||
			(spec == bestSpec && c.q > best.q) ||
			(spec == bestSpec && c.q == best.q && c.order < best.order) {
			best = c
			best.specificity = spec
			bestSpec = spec
			matched = true
		}
	}
	return best, matched
}

func matchFormatSpecificity(f ResultFormat, mediaType string) (int, bool) {
	switch f {
	case FormatSPARQLJSON:
		switch mediaType {
		case MediaTypeSPARQLJSON:
			return 3, true
		case "application/json":
			return 2, true
		case "application/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	case FormatSPARQLXML:
		switch mediaType {
		case MediaTypeSPARQLXML:
			return 3, true
		case "application/xml", "text/xml":
			return 2, true
		case "application/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	case FormatCSV:
		switch mediaType {
		case MediaTypeCSV:
			return 3, true
		case "text/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	case FormatTSV:
		switch mediaType {
		case MediaTypeTSV:
			return 3, true
		case "text/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	case FormatTurtle:
		switch mediaType {
		case MediaTypeTurtle:
			return 3, true
		case "application/x-turtle":
			return 2, true
		case "text/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	case FormatNTriples:
		switch mediaType {
		case MediaTypeNTriples:
			return 3, true
		case "text/plain":
			return 2, true
		case "application/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	case FormatJSONLD:
		switch mediaType {
		case MediaTypeJSONLD:
			return 3, true
		case "application/json":
			return 2, true
		case "application/*":
			return 1, true
		case "*/*":
			return 0, true
		}
	}
	return 0, false
}

func parseAcceptHeader(header string) ([]acceptClause, error) {
	parts := strings.Split(header, ",")
	out := make([]acceptClause, 0, len(parts))
	for i, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		mt, params, err := parseMediaRange(part)
		if err != nil {
			return nil, err
		}
		q := 1.0
		if qStr, ok := params["q"]; ok {
			parsedQ, err := strconv.ParseFloat(qStr, 64)
			if err != nil || parsedQ < 0 || parsedQ > 1 {
				return nil, fmt.Errorf("invalid quality weight q=%q", qStr)
			}
			q = parsedQ
		}
		spec := 2
		switch {
		case mt == "*/*":
			spec = 0
		case strings.HasSuffix(mt, "/*"):
			spec = 1
		}
		out = append(out, acceptClause{
			mediaType:   mt,
			q:           q,
			specificity: spec,
			order:       i,
		})
	}
	if len(out) == 0 {
		return nil, errors.New("empty Accept clauses")
	}
	return out, nil
}

func parseMediaRange(raw string) (string, map[string]string, error) {
	// mime.ParseMediaType rejects "*/*" and "type/*" in some standard library
	// configurations only if malformed, so handle standard wildcard tokens cleanly.
	semi := strings.IndexByte(raw, ';')
	typePart := raw
	paramPart := ""
	if semi >= 0 {
		typePart = strings.TrimSpace(raw[:semi])
		paramPart = raw[semi:]
	}
	if typePart == "*/*" || strings.HasSuffix(typePart, "/*") {
		slash := strings.IndexByte(typePart, '/')
		if slash <= 0 {
			return "", nil, fmt.Errorf("invalid wildcard media range %q", raw)
		}
		params := make(map[string]string)
		if paramPart != "" {
			_, p, err := mime.ParseMediaType("application/octet-stream" + paramPart)
			if err != nil {
				return "", nil, err
			}
			params = p
		}
		return strings.ToLower(typePart), params, nil
	}
	mt, params, err := mime.ParseMediaType(raw)
	if err != nil {
		return "", nil, err
	}
	return strings.ToLower(mt), params, nil
}
