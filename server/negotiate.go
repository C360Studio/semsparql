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

	var (
		bestFormat ResultFormat
		bestQ      = -1.0
		bestSpec   = -1
		bestOrder  = len(clauses) + 1
	)

	for _, c := range clauses {
		if c.q <= 0 {
			continue
		}
		f, ok := matchMediaRange(form, c.mediaType)
		if !ok {
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

func matchMediaRange(form sparql.QueryForm, mediaType string) (ResultFormat, bool) {
	switch form {
	case sparql.QuerySelect:
		switch mediaType {
		case MediaTypeSPARQLJSON, "application/json", "*/*", "application/*":
			return FormatSPARQLJSON, true
		case MediaTypeSPARQLXML, "application/xml", "text/xml":
			return FormatSPARQLXML, true
		case MediaTypeCSV, "text/*":
			return FormatCSV, true
		case MediaTypeTSV:
			return FormatTSV, true
		default:
			return 0, false
		}

	case sparql.QueryAsk:
		switch mediaType {
		case MediaTypeSPARQLJSON, "application/json", "*/*", "application/*":
			return FormatSPARQLJSON, true
		case MediaTypeSPARQLXML, "application/xml", "text/xml":
			return FormatSPARQLXML, true
		default:
			return 0, false
		}

	case sparql.QueryConstruct, sparql.QueryDescribe:
		switch mediaType {
		case MediaTypeTurtle, "application/x-turtle", "*/*", "text/*":
			return FormatTurtle, true
		case MediaTypeNTriples, "text/plain":
			return FormatNTriples, true
		case MediaTypeJSONLD, "application/json", "application/*":
			return FormatJSONLD, true
		default:
			return 0, false
		}

	default:
		return 0, false
	}
}
