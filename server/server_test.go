package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semsparql/irimap"
	"github.com/c360studio/semsparql/reader"
	"github.com/c360studio/semsparql/sparql"
	"github.com/c360studio/semvocab/packs"
	"github.com/c360studio/semvocab/packs/diekm"
)

const testBaseIRI = "https://semengine.local"

func registerServerTestVocab(t testing.TB) func() {
	t.Helper()
	restore := vocabulary.SnapshotRegistry()
	vocabulary.ClearRegistry()

	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithIRI("https://example.org/onto#callsign"),
		vocabulary.WithDataType(vocabulary.DataTypeString))
	vocabulary.Register("c360.intel.priority",
		vocabulary.WithIRI("https://example.org/onto#priority"),
		vocabulary.WithDataType(vocabulary.DataTypeInt))
	vocabulary.Register("c360.intel.active",
		vocabulary.WithIRI("https://example.org/onto#active"),
		vocabulary.WithDataType(vocabulary.DataTypeBool))
	vocabulary.Register("c360.intel.note",
		vocabulary.WithIRI("https://example.org/onto#note"),
		vocabulary.WithDataType(vocabulary.DataTypeString))
	vocabulary.Register("c360.intel.tracks",
		vocabulary.WithIRI("https://example.org/onto#tracks"),
		vocabulary.WithDataType(vocabulary.DataTypeEntityID))

	return restore
}

func newTestFixtureReader(t testing.TB) *reader.MemoryReader {
	t.Helper()
	const (
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idBravo = "c360.ops.uas1.intel.platform.bravo"
	)
	mr, err := reader.NewMemoryReader(
		graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
				{Subject: idAlpha, Predicate: "c360.intel.priority", Object: float64(10)},
				{Subject: idAlpha, Predicate: "c360.intel.active", Object: true},
				{Subject: idAlpha, Predicate: "c360.intel.note", Object: "lead", Datatype: "@en"},
				{Subject: idAlpha, Predicate: "c360.intel.tracks", Object: "_:b_track1"},
			},
		},
		graph.EntityState{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "BRAVO-2"},
				{Subject: idBravo, Predicate: "c360.intel.priority", Object: float64(20)},
				{Subject: idBravo, Predicate: "c360.intel.active", Object: false},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}
	return mr
}

type sparqlJSONResponse struct {
	Head struct {
		Vars []string `json:"vars"`
	} `json:"head"`
	Results *struct {
		Bindings []map[string]struct {
			Type     string `json:"type"`
			Value    string `json:"value"`
			Datatype string `json:"datatype"`
			XMLLang  string `json:"xml:lang"`
		} `json:"bindings"`
	} `json:"results"`
	Boolean *bool `json:"boolean"`
}

type sparqlXMLResponse struct {
	XMLName xml.Name `xml:"sparql"`
	Head    struct {
		Variables []struct {
			Name string `xml:"name,attr"`
		} `xml:"variable"`
	} `xml:"head"`
	Results *struct {
		Results []struct {
			Bindings []struct {
				Name    string `xml:"name,attr"`
				URI     string `xml:"uri"`
				BNode   string `xml:"bnode"`
				Literal *struct {
					Value    string `xml:",chardata"`
					Datatype string `xml:"datatype,attr"`
					Lang     string `xml:"lang,attr"`
				} `xml:"literal"`
			} `xml:"binding"`
		} `xml:"result"`
	} `xml:"results"`
	Boolean *bool `xml:"boolean"`
}

func TestSPARQL11ProtocolConformance(t *testing.T) {
	defer registerServerTestVocab(t)()

	mr := newTestFixtureReader(t)
	srv, err := New(mr, Config{
		BaseIRI: testBaseIRI,
	})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	selectQuery := `
		PREFIX ex: <https://example.org/onto#>
		SELECT ?s ?cs ?prio ?note ?trk WHERE {
			?s ex:callsign ?cs ;
			   ex:priority ?prio .
			OPTIONAL { ?s ex:note ?note }
			OPTIONAL { ?s ex:tracks ?trk }
		}
		ORDER BY ASC(?prio)
	`
	askQuery := `
		PREFIX ex: <https://example.org/onto#>
		ASK WHERE { ?s ex:callsign "ALPHA-1" }
	`

	sendRequest := func(t *testing.T, transport, query, accept string) (*http.Response, []byte) {
		t.Helper()
		var req *http.Request
		var err error
		switch transport {
		case "GET":
			u := ts.URL + "/sparql?query=" + url.QueryEscape(query)
			req, err = http.NewRequest(http.MethodGet, u, nil)
		case "POST-raw":
			req, err = http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(query))
			if err == nil {
				req.Header.Set("Content-Type", "application/sparql-query; charset=utf-8")
			}
		case "POST-form":
			form := url.Values{"query": {query}}.Encode()
			req, err = http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(form))
			if err == nil {
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			}
		default:
			t.Fatalf("unknown transport %q", transport)
		}
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("Do(%s): %v", transport, err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		return resp, body
	}

	transports := []string{"GET", "POST-raw", "POST-form"}

	t.Run("SELECT equivalent results across GET, raw POST, and form POST in JSON/XML/CSV/TSV", func(t *testing.T) {
		formats := []struct {
			accept   string
			wantMime string
		}{
			{accept: "", wantMime: MediaTypeSPARQLJSON},
			{accept: MediaTypeSPARQLJSON, wantMime: MediaTypeSPARQLJSON},
			{accept: MediaTypeSPARQLXML, wantMime: MediaTypeSPARQLXML},
			{accept: MediaTypeCSV, wantMime: MediaTypeCSV},
			{accept: MediaTypeTSV, wantMime: MediaTypeTSV},
		}

		for _, fmtSpec := range formats {
			var baseline []byte
			for _, tr := range transports {
				resp, body := sendRequest(t, tr, selectQuery, fmtSpec.accept)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("%s (%s) status = %d, want 200: %s", tr, fmtSpec.accept, resp.StatusCode, string(body))
				}
				if !strings.HasPrefix(resp.Header.Get("Content-Type"), fmtSpec.wantMime) {
					t.Fatalf("%s (%s) Content-Type = %q, want prefix %q", tr, fmtSpec.accept, resp.Header.Get("Content-Type"), fmtSpec.wantMime)
				}
				if baseline == nil {
					baseline = body
				} else if string(body) != string(baseline) {
					t.Fatalf("%s (%s) body mismatch across transports:\ngot:\n%s\nwant:\n%s", tr, fmtSpec.accept, string(body), string(baseline))
				}
			}

			switch fmtSpec.wantMime {
			case MediaTypeSPARQLJSON:
				var parsed sparqlJSONResponse
				if err := json.Unmarshal(baseline, &parsed); err != nil {
					t.Fatalf("JSON unmarshal: %v\n%s", err, string(baseline))
				}
				if !slices.Equal(parsed.Head.Vars, []string{"s", "cs", "prio", "note", "trk"}) {
					t.Fatalf("JSON vars = %v", parsed.Head.Vars)
				}
				if parsed.Results == nil || len(parsed.Results.Bindings) != 2 {
					t.Fatalf("JSON bindings = %+v, want 2 rows", parsed.Results)
				}
				row0 := parsed.Results.Bindings[0]
				if row0["cs"].Type != "literal" || row0["cs"].Value != "ALPHA-1" {
					t.Fatalf("JSON row0 cs = %+v", row0["cs"])
				}
				if row0["prio"].Datatype != "http://www.w3.org/2001/XMLSchema#integer" || row0["prio"].Value != "10" {
					t.Fatalf("JSON row0 prio = %+v", row0["prio"])
				}
				if row0["note"].XMLLang != "en" || row0["note"].Value != "lead" {
					t.Fatalf("JSON row0 note = %+v", row0["note"])
				}
				if row0["trk"].Type != "bnode" || row0["trk"].Value != "b_track1" {
					t.Fatalf("JSON row0 trk = %+v, want bnode b_track1", row0["trk"])
				}
			case MediaTypeSPARQLXML:
				var parsed sparqlXMLResponse
				if err := xml.Unmarshal(baseline, &parsed); err != nil {
					t.Fatalf("XML unmarshal: %v\n%s", err, string(baseline))
				}
				if parsed.Results == nil || len(parsed.Results.Results) != 2 {
					t.Fatalf("XML results = %+v, want 2 rows", parsed.Results)
				}
			case MediaTypeCSV:
				rows, err := csv.NewReader(strings.NewReader(string(baseline))).ReadAll()
				if err != nil {
					t.Fatalf("CSV parse: %v\n%s", err, string(baseline))
				}
				if len(rows) != 3 || !slices.Equal(rows[0], []string{"s", "cs", "prio", "note", "trk"}) {
					t.Fatalf("CSV rows = %v", rows)
				}
				if rows[1][1] != "ALPHA-1" || rows[1][2] != "10" || rows[1][3] != "lead" || rows[1][4] != "_:b_track1" {
					t.Fatalf("CSV row[1] = %v", rows[1])
				}
				if rows[2][1] != "BRAVO-2" || rows[2][3] != "" || rows[2][4] != "" {
					t.Fatalf("CSV row[2] = %v", rows[2])
				}
			case MediaTypeTSV:
				lines := strings.Split(strings.TrimSuffix(string(baseline), "\n"), "\n")
				if len(lines) != 3 || lines[0] != "?s\t?cs\t?prio\t?note\t?trk" {
					t.Fatalf("TSV lines = %v", lines)
				}
				fields1 := strings.Split(lines[1], "\t")
				if fields1[1] != `"ALPHA-1"` || fields1[2] != `"10"^^<http://www.w3.org/2001/XMLSchema#integer>` || fields1[3] != `"lead"@en` || fields1[4] != `_:b_track1` {
					t.Fatalf("TSV fields1 = %v", fields1)
				}
			}
		}
	})

	t.Run("ASK equivalent results across transports in JSON and XML", func(t *testing.T) {
		for _, accept := range []string{MediaTypeSPARQLJSON, MediaTypeSPARQLXML} {
			var baseline []byte
			for _, tr := range transports {
				resp, body := sendRequest(t, tr, askQuery, accept)
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("ASK %s (%s) status = %d: %s", tr, accept, resp.StatusCode, string(body))
				}
				if baseline == nil {
					baseline = body
				} else if string(body) != string(baseline) {
					t.Fatalf("ASK %s (%s) mismatch:\n%s\nvs\n%s", tr, accept, string(body), string(baseline))
				}
			}
			if accept == MediaTypeSPARQLJSON {
				var parsed sparqlJSONResponse
				if err := json.Unmarshal(baseline, &parsed); err != nil || parsed.Boolean == nil || !*parsed.Boolean {
					t.Fatalf("ASK JSON = %+v, err = %v", parsed, err)
				}
			} else {
				var parsed sparqlXMLResponse
				if err := xml.Unmarshal(baseline, &parsed); err != nil || parsed.Boolean == nil || !*parsed.Boolean {
					t.Fatalf("ASK XML = %+v, err = %v", parsed, err)
				}
			}
		}
	})

	t.Run("CONSTRUCT and DESCRIBE equivalent results across transports in Turtle, N-Triples, and JSON-LD", func(t *testing.T) {
		constructQuery := `
			PREFIX ex: <https://example.org/onto#>
			CONSTRUCT { ?s ex:callsign ?cs ; ex:priority ?prio }
			WHERE { ?s ex:callsign ?cs ; ex:priority ?prio }
		`
		describeQuery := `DESCRIBE <https://semengine.local/entities/c360/ops/uas1/intel/platform/alpha>`
		graphFormats := []struct {
			accept   string
			wantMime string
		}{
			{accept: "", wantMime: MediaTypeTurtle},
			{accept: MediaTypeTurtle, wantMime: MediaTypeTurtle},
			{accept: MediaTypeNTriples, wantMime: MediaTypeNTriples},
			{accept: MediaTypeJSONLD, wantMime: MediaTypeJSONLD},
		}
		for _, qSpec := range []struct {
			name  string
			query string
		}{
			{name: "CONSTRUCT", query: constructQuery},
			{name: "DESCRIBE", query: describeQuery},
		} {
			for _, fmtSpec := range graphFormats {
				var baseline []byte
				for _, tr := range transports {
					resp, body := sendRequest(t, tr, qSpec.query, fmtSpec.accept)
					if resp.StatusCode != http.StatusOK {
						t.Fatalf("%s %s (%s) status = %d, want 200: %s", qSpec.name, tr, fmtSpec.accept, resp.StatusCode, string(body))
					}
					if !strings.HasPrefix(resp.Header.Get("Content-Type"), fmtSpec.wantMime) {
						t.Fatalf("%s %s (%s) Content-Type = %q, want prefix %q", qSpec.name, tr, fmtSpec.accept, resp.Header.Get("Content-Type"), fmtSpec.wantMime)
					}
					if !strings.Contains(string(body), "ALPHA-1") {
						t.Fatalf("%s %s (%s) missing ALPHA-1 in body:\n%s", qSpec.name, tr, fmtSpec.accept, string(body))
					}
					if baseline == nil {
						baseline = body
					} else if string(body) != string(baseline) {
						t.Fatalf("%s %s (%s) body mismatch across transports", qSpec.name, tr, fmtSpec.accept)
					}
				}
			}
		}
	})

	t.Run("incompatible Accept header returns 406 Not Acceptable", func(t *testing.T) {
		incompatible := []struct {
			name   string
			query  string
			accept string
		}{
			{name: "SELECT with Turtle", query: selectQuery, accept: "text/turtle"},
			{name: "SELECT with N-Triples", query: selectQuery, accept: "application/n-triples"},
			{name: "SELECT with JSON-LD", query: selectQuery, accept: "application/ld+json"},
			{name: "ASK with CSV", query: askQuery, accept: "text/csv"},
			{name: "ASK with TSV", query: askQuery, accept: "text/tab-separated-values"},
			{name: "ASK with Turtle", query: askQuery, accept: "text/turtle"},
			{name: "CONSTRUCT with SPARQL JSON", query: "CONSTRUCT { ?s ?p ?o } WHERE { ?s ?p ?o }", accept: MediaTypeSPARQLJSON},
			{name: "DESCRIBE with CSV", query: "DESCRIBE <https://example.org/x>", accept: MediaTypeCSV},
			{name: "SELECT with unknown media type", query: selectQuery, accept: "application/octet-stream"},
			{name: "SELECT with q=0 on only matching type", query: selectQuery, accept: "application/sparql-results+json;q=0"},
		}
		for _, tc := range incompatible {
			t.Run(tc.name, func(t *testing.T) {
				resp, body := sendRequest(t, "GET", tc.query, tc.accept)
				if resp.StatusCode != http.StatusNotAcceptable {
					t.Fatalf("status = %d, want 406 Not Acceptable (body: %s)", resp.StatusCode, string(body))
				}
			})
		}
	})

	t.Run("malformed requests and invalid query parameters return 400 Bad Request", func(t *testing.T) {
		badRequests := []struct {
			name string
			req  func() (*http.Request, error)
		}{
			{
				name: "GET missing query param",
				req:  func() (*http.Request, error) { return http.NewRequest(http.MethodGet, ts.URL+"/sparql", nil) },
			},
			{
				name: "GET duplicate query params",
				req: func() (*http.Request, error) {
					return http.NewRequest(http.MethodGet, ts.URL+"/sparql?query=ASK%7B%7D&query=ASK%7B%7D", nil)
				},
			},
			{
				name: "GET unknown parameter",
				req: func() (*http.Request, error) {
					return http.NewRequest(http.MethodGet, ts.URL+"/sparql?query=ASK%7B%7D&foo=bar", nil)
				},
			},
			{
				name: "GET named-graph-uri parameter",
				req: func() (*http.Request, error) {
					return http.NewRequest(http.MethodGet, ts.URL+"/sparql?query=ASK%7B%7D&named-graph-uri=https://example.org/g", nil)
				},
			},
			{
				name: "POST unsupported Content-Type",
				req: func() (*http.Request, error) {
					r, err := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(askQuery))
					if err == nil {
						r.Header.Set("Content-Type", "text/plain")
					}
					return r, err
				},
			},
			{
				name: "POST form missing query field",
				req: func() (*http.Request, error) {
					r, err := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader("other=1"))
					if err == nil {
						r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					}
					return r, err
				},
			},
			{
				name: "SPARQL syntax error",
				req: func() (*http.Request, error) {
					return http.NewRequest(http.MethodGet, ts.URL+"/sparql?query="+url.QueryEscape("SELECT ?s WHERE { ?s ?p"), nil)
				},
			},
		}

		for _, tc := range badRequests {
			t.Run(tc.name, func(t *testing.T) {
				req, err := tc.req()
				if err != nil {
					t.Fatalf("req: %v", err)
				}
				resp, err := ts.Client().Do(req)
				if err != nil {
					t.Fatalf("Do: %v", err)
				}
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusBadRequest {
					body, _ := io.ReadAll(resp.Body)
					t.Fatalf("status = %d, want 400 Bad Request: %s", resp.StatusCode, string(body))
				}
			})
		}
	})

	t.Run("oversized query > 64 KiB returns 413 Content Too Large on GET and POST", func(t *testing.T) {
		bigLiteral := strings.Repeat("a", DefaultMaxQueryBytes+1)
		bigQuery := fmt.Sprintf(`SELECT ?s WHERE { ?s <https://example.org/onto#callsign> "%s" }`, bigLiteral)

		for _, tr := range []string{"GET", "POST-raw", "POST-form"} {
			resp, body := sendRequest(t, tr, bigQuery, MediaTypeSPARQLJSON)
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("%s oversized status = %d, want 413 Content Too Large: %s", tr, resp.StatusCode, string(body))
			}
		}
	})
}

func TestUnsupportedFeatureErrors(t *testing.T) {
	defer registerServerTestVocab(t)()

	mr := newTestFixtureReader(t)
	srv, err := New(mr, Config{BaseIRI: testBaseIRI})
	if err != nil {
		t.Fatalf("New server: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	cases := []struct {
		name        string
		query       string
		wantFeature string
	}{
		{name: "INSERT DATA update", query: `INSERT DATA { <https://example.org/s> <https://example.org/p> "v" }`, wantFeature: "SPARQL Update"},
		{name: "SERVICE federated query", query: `SELECT ?s WHERE { SERVICE <https://example.org/sparql> { ?s ?p ?o } }`, wantFeature: "SERVICE"},
		{name: "GRAPH named graph", query: `SELECT ?s WHERE { GRAPH <https://example.org/g> { ?s ?p ?o } }`, wantFeature: "GRAPH"},
		{name: "GROUP BY", query: `SELECT ?s WHERE { ?s ?p ?o } GROUP BY ?s`, wantFeature: "GROUP BY"},
		{name: "COUNT aggregate", query: `SELECT (COUNT(?s) AS ?c) WHERE { ?s ?p ?o }`, wantFeature: "COUNT"},
		{name: "Subquery", query: `SELECT ?s WHERE { { SELECT ?s WHERE { ?s ?p ?o } } }`, wantFeature: "subqueries"},
		{name: "VALUES", query: `SELECT ?s WHERE { VALUES ?s { <https://example.org/a> } ?s ?p ?o }`, wantFeature: "VALUES"},
		{name: "BIND", query: `SELECT ?s ?x WHERE { ?s ?p ?o . BIND(?o AS ?x) }`, wantFeature: "BIND"},
		{name: "REDUCED", query: `SELECT REDUCED ?s WHERE { ?s ?p ?o }`, wantFeature: "REDUCED"},
		{name: "Unsupported property path", query: `SELECT ?s ?o WHERE { ?s <https://example.org/p>+ ?o }`, wantFeature: "property path"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(tc.query))
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			req.Header.Set("Content-Type", "application/sparql-query")
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("Do: %v", err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 Bad Request: %s", resp.StatusCode, string(body))
			}
			if !strings.Contains(string(body), tc.wantFeature) {
				t.Fatalf("response body %q does not name unsupported feature %q", string(body), tc.wantFeature)
			}
		})
	}
}

func TestQueryLimits(t *testing.T) {
	defer registerServerTestVocab(t)()

	entities := make([]graph.EntityState, 0, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("c360.ops.uas1.intel.platform.uav%d", i)
		entities = append(entities, graph.EntityState{
			ID: id,
			Triples: []message.Triple{
				{Subject: id, Predicate: "c360.intel.callsign", Object: fmt.Sprintf("UAV-%d", i)},
			},
		})
	}
	mr, err := reader.NewMemoryReader(entities...)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}
	mapper := irimap.NewMapper(testBaseIRI)
	uav0IRI, _ := mapper.EntityIDToIRI("c360.ops.uas1.intel.platform.uav0")
	uav1IRI, _ := mapper.EntityIDToIRI("c360.ops.uas1.intel.platform.uav1")

	t.Run("result limit breach returns 422 without truncation header and 200 with X-Semsparql-Truncated on opt-in", func(t *testing.T) {
		srv, err := New(mr, Config{
			BaseIRI:         testBaseIRI,
			MaxResults:      2,
			MaxScanEntities: 100,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		query := `PREFIX ex: <https://example.org/onto#> SELECT ?s ?cs WHERE { ?s ex:callsign ?cs } ORDER BY ?cs`

		// Default: 422 Unprocessable Entity naming result_limit_exceeded.
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/sparql-query")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("Do default: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422: %s", resp.StatusCode, string(body))
		}
		if !strings.Contains(string(body), "result_limit_exceeded") {
			t.Fatalf("body = %q, want result_limit_exceeded", string(body))
		}
		if resp.Header.Get(HeaderTruncated) != "" {
			t.Fatalf("unexpected %s header on 422 response", HeaderTruncated)
		}

		// Opt-in: X-Semsparql-Allow-Truncation: true -> 200 OK with X-Semsparql-Truncated: true and reason result_limit.
		reqTrunc, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(query))
		reqTrunc.Header.Set("Content-Type", "application/sparql-query")
		reqTrunc.Header.Set(HeaderAllowTruncation, "true")
		respTrunc, err := ts.Client().Do(reqTrunc)
		if err != nil {
			t.Fatalf("Do opt-in: %v", err)
		}
		bodyTrunc, _ := io.ReadAll(respTrunc.Body)
		respTrunc.Body.Close()
		if respTrunc.StatusCode != http.StatusOK {
			t.Fatalf("opt-in status = %d, want 200: %s", respTrunc.StatusCode, string(bodyTrunc))
		}
		if respTrunc.Header.Get(HeaderTruncated) != "true" {
			t.Fatalf("%s = %q, want \"true\"", HeaderTruncated, respTrunc.Header.Get(HeaderTruncated))
		}
		if respTrunc.Header.Get(HeaderTruncationReason) != "result_limit" {
			t.Fatalf("%s = %q, want \"result_limit\"", HeaderTruncationReason, respTrunc.Header.Get(HeaderTruncationReason))
		}
	})

	t.Run("scan budget breach returns 422 without truncation header and 200 with scan_budget reason on opt-in", func(t *testing.T) {
		srv, err := New(mr, Config{
			BaseIRI:         testBaseIRI,
			MaxResults:      100,
			MaxScanEntities: 1,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		query := fmt.Sprintf(`
			PREFIX ex: <https://example.org/onto#>
			SELECT ?cs WHERE {
				{ <%s> ex:callsign ?cs }
				UNION
				{ <%s> ex:callsign ?cs }
			}
		`, uav0IRI, uav1IRI)

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(query))
		req.Header.Set("Content-Type", "application/sparql-query")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("Do default: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "scan_budget_exceeded") {
			t.Fatalf("status=%d body=%q, want 422 scan_budget_exceeded", resp.StatusCode, string(body))
		}

		reqTrunc, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(query))
		reqTrunc.Header.Set("Content-Type", "application/sparql-query")
		reqTrunc.Header.Set(HeaderAllowTruncation, "true")
		respTrunc, err := ts.Client().Do(reqTrunc)
		if err != nil {
			t.Fatalf("Do opt-in: %v", err)
		}
		bodyTrunc, _ := io.ReadAll(respTrunc.Body)
		respTrunc.Body.Close()
		if respTrunc.StatusCode != http.StatusOK {
			t.Fatalf("opt-in status = %d, want 200: %s", respTrunc.StatusCode, string(bodyTrunc))
		}
		if respTrunc.Header.Get(HeaderTruncated) != "true" || respTrunc.Header.Get(HeaderTruncationReason) != "scan_budget" {
			t.Fatalf("headers: Truncated=%q Reason=%q, want true/scan_budget",
				respTrunc.Header.Get(HeaderTruncated), respTrunc.Header.Get(HeaderTruncationReason))
		}
	})

	t.Run("query timeout returns 504 Gateway Timeout naming query_timeout", func(t *testing.T) {
		slowReader := &deadlineExceededReader{GraphReader: mr}
		srv, err := New(slowReader, Config{
			BaseIRI:      testBaseIRI,
			QueryTimeout: 50 * time.Millisecond,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(`SELECT ?s ?p ?o WHERE { ?s ?p ?o }`))
		req.Header.Set("Content-Type", "application/sparql-query")
		req.Header.Set(HeaderAllowTruncation, "true")
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("Do timeout: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusGatewayTimeout || !strings.Contains(string(body), "query_timeout") {
			t.Fatalf("status=%d body=%q, want 504 query_timeout", resp.StatusCode, string(body))
		}
	})

	t.Run("CONSTRUCT and POST /export enforce MaxResults on deduplicated output triples and 256 KiB export body limit", func(t *testing.T) {
		srv, err := New(mr, Config{
			BaseIRI:         testBaseIRI,
			MaxResults:      3,
			MaxScanEntities: 100,
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		ts := httptest.NewServer(srv.Handler())
		defer ts.Close()

		constructQuery := `PREFIX ex: <https://example.org/onto#> CONSTRUCT { ?s ex:callsign ?cs } WHERE { ?s ex:callsign ?cs }`
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(constructQuery))
		req.Header.Set("Content-Type", "application/sparql-query")
		req.Header.Set("Accept", MediaTypeNTriples)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("Do CONSTRUCT default: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(body), "result_limit_exceeded") {
			t.Fatalf("CONSTRUCT default status=%d body=%q, want 422 result_limit_exceeded", resp.StatusCode, string(body))
		}

		reqTrunc, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(constructQuery))
		reqTrunc.Header.Set("Content-Type", "application/sparql-query")
		reqTrunc.Header.Set("Accept", MediaTypeNTriples)
		reqTrunc.Header.Set(HeaderAllowTruncation, "true")
		respTrunc, err := ts.Client().Do(reqTrunc)
		if err != nil {
			t.Fatalf("Do CONSTRUCT opt-in: %v", err)
		}
		bodyTrunc, _ := io.ReadAll(respTrunc.Body)
		respTrunc.Body.Close()
		if respTrunc.StatusCode != http.StatusOK || respTrunc.Header.Get(HeaderTruncated) != "true" || respTrunc.Header.Get(HeaderTruncationReason) != "result_limit" {
			t.Fatalf("CONSTRUCT opt-in status=%d Truncated=%q Reason=%q", respTrunc.StatusCode, respTrunc.Header.Get(HeaderTruncated), respTrunc.Header.Get(HeaderTruncationReason))
		}
		lines := strings.Split(strings.TrimSpace(string(bodyTrunc)), "\n")
		if len(lines) != 3 {
			t.Fatalf("CONSTRUCT truncated N-Triples lines = %d, want 3:\n%s", len(lines), string(bodyTrunc))
		}

		// POST /export with prefix exceeding MaxResults=3 -> 422 by default, 200 with truncation header on opt-in.
		exportBody := `{"prefix":"c360.ops.uas1.intel.platform"}`
		reqExp, _ := http.NewRequest(http.MethodPost, ts.URL+"/export", strings.NewReader(exportBody))
		reqExp.Header.Set("Content-Type", "application/json")
		reqExp.Header.Set("Accept", MediaTypeNTriples)
		respExp, err := ts.Client().Do(reqExp)
		if err != nil {
			t.Fatalf("Do POST /export default: %v", err)
		}
		expOut, _ := io.ReadAll(respExp.Body)
		respExp.Body.Close()
		if respExp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(string(expOut), "result_limit_exceeded") {
			t.Fatalf("POST /export default status=%d body=%q, want 422 result_limit_exceeded", respExp.StatusCode, string(expOut))
		}

		reqExpTrunc, _ := http.NewRequest(http.MethodPost, ts.URL+"/export", strings.NewReader(exportBody))
		reqExpTrunc.Header.Set("Content-Type", "application/json")
		reqExpTrunc.Header.Set("Accept", MediaTypeNTriples)
		reqExpTrunc.Header.Set(HeaderAllowTruncation, "true")
		respExpTrunc, err := ts.Client().Do(reqExpTrunc)
		if err != nil {
			t.Fatalf("Do POST /export opt-in: %v", err)
		}
		expTruncOut, _ := io.ReadAll(respExpTrunc.Body)
		respExpTrunc.Body.Close()
		if respExpTrunc.StatusCode != http.StatusOK || respExpTrunc.Header.Get(HeaderTruncated) != "true" || respExpTrunc.Header.Get(HeaderTruncationReason) != "result_limit" {
			t.Fatalf("POST /export opt-in status=%d Truncated=%q Reason=%q", respExpTrunc.StatusCode, respExpTrunc.Header.Get(HeaderTruncated), respExpTrunc.Header.Get(HeaderTruncationReason))
		}
		if gotLines := strings.Split(strings.TrimSpace(string(expTruncOut)), "\n"); len(gotLines) != 3 {
			t.Fatalf("POST /export truncated N-Triples lines = %d, want 3:\n%s", len(gotLines), string(expTruncOut))
		}

		// POST /export body > 256 KiB returns 413 Content Too Large.
		bigConstruct := fmt.Sprintf(`{"construct":"CONSTRUCT WHERE { ?s <https://example.org/onto#callsign> \"%s\" }"}`, strings.Repeat("x", DefaultMaxExportBytes+1))
		reqBig, _ := http.NewRequest(http.MethodPost, ts.URL+"/export", strings.NewReader(bigConstruct))
		reqBig.Header.Set("Content-Type", "application/json")
		respBig, err := ts.Client().Do(reqBig)
		if err != nil {
			t.Fatalf("Do POST /export oversized: %v", err)
		}
		respBig.Body.Close()
		if respBig.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("POST /export oversized status = %d, want 413", respBig.StatusCode)
		}
	})
}

type deadlineExceededReader struct {
	reader.GraphReader
}

func (d *deadlineExceededReader) ScanPattern(_ context.Context, _ string, _ int) ([]graph.EntityState, error) {
	return nil, context.DeadlineExceeded
}

func TestServerAuthAndBindValidation(t *testing.T) {
	defer registerServerTestVocab(t)()
	mr := newTestFixtureReader(t)

	// 1. Default bind (empty -> 127.0.0.1:8080) and explicit loopback binds succeed without auth.
	for _, addr := range []string{"", "127.0.0.1:0", "localhost:0", "[::1]:0"} {
		if _, err := New(mr, Config{BindAddress: addr}); err != nil {
			t.Fatalf("New(BindAddress=%q) = %v, want nil", addr, err)
		}
	}

	// 2. Non-local binds without bearer token or mTLS refuse at startup with ErrUnsafeExposure.
	for _, addr := range []string{"0.0.0.0:8080", ":8080", "[::]:8080", "192.168.1.10:8080"} {
		_, err := New(mr, Config{BindAddress: addr})
		if !errors.Is(err, ErrUnsafeExposure) {
			t.Fatalf("New(BindAddress=%q) without auth = %v, want ErrUnsafeExposure", addr, err)
		}
	}

	// 3. Non-local bind with mTLS ClientAuth + ClientCAs but no server certificate is refused.
	mtlsNoServerCert := &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  x509.NewCertPool(),
	}
	if _, err := New(mr, Config{BindAddress: "0.0.0.0:8080", TLSConfig: mtlsNoServerCert}); err == nil {
		t.Fatal("New with mTLS missing server certificate succeeded, want error")
	}

	// Non-local bind with mTLS ClientAuth + ClientCAs + server certificate succeeds.
	mtlsCfg := &tls.Config{
		Certificates: []tls.Certificate{{}},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    x509.NewCertPool(),
	}
	if _, err := New(mr, Config{BindAddress: "0.0.0.0:8080", TLSConfig: mtlsCfg}); err != nil {
		t.Fatalf("New with mTLS = %v, want nil", err)
	}

	// Negative limits and invalid BaseIRI are rejected by ValidateConfig / New.
	for _, badCfg := range []Config{
		{BaseIRI: "not-a-valid-http-iri"},
		{BaseIRI: "https://bad>base.example"},
		{QueryTimeout: -time.Second},
		{MaxResults: -1},
		{MaxScanEntities: -1},
		{MaxQueryBytes: -1},
		{MaxExportBytes: -1},
	} {
		if _, err := New(mr, badCfg); err == nil {
			t.Fatalf("New(%+v) = nil, want validation error", badCfg)
		}
	}

	// 4. Non-local bind with BearerToken succeeds and enforces Authorization: Bearer <token> (case-insensitive scheme).
	srv, err := New(mr, Config{
		BindAddress: "0.0.0.0:8080",
		BearerToken: "secret-token-123",
	})
	if err != nil {
		t.Fatalf("New with BearerToken: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Unauthenticated request -> 401 Unauthorized.
	respUnauth, err := ts.Client().Get(ts.URL + "/sparql?query=ASK%7B%7D")
	if err != nil {
		t.Fatalf("Get unauth: %v", err)
	}
	respUnauth.Body.Close()
	if respUnauth.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want 401", respUnauth.StatusCode)
	}

	// Authenticated request (including lowercase "bearer" scheme) -> 200 OK.
	for _, hdr := range []string{"Bearer secret-token-123", "bearer secret-token-123"} {
		reqAuth, _ := http.NewRequest(http.MethodGet, ts.URL+"/sparql?query=ASK%7B%7D", nil)
		reqAuth.Header.Set("Authorization", hdr)
		respAuth, err := ts.Client().Do(reqAuth)
		if err != nil {
			t.Fatalf("Do auth (%s): %v", hdr, err)
		}
		respAuth.Body.Close()
		if respAuth.StatusCode != http.StatusOK {
			t.Fatalf("auth (%s) status = %d, want 200", hdr, respAuth.StatusCode)
		}
	}

	// 5. Incomplete registered entity-type superclass closure fails server.New at startup.
	vocabulary.RegisterEntityType("uxs.vehicle",
		vocabulary.WithClassIRI("https://www.commoncoreontologies.org/ont00000713"),
		vocabulary.WithSuperclass("http://purl.obolibrary.org/obo/BFO_0000030"),
	)
	vocabulary.RegisterEntityType("intel.platform",
		vocabulary.WithClassIRI("https://w3id.org/c360/diekm#IntelligencePlatform"),
		vocabulary.WithSuperclass("https://www.commoncoreontologies.org/ont00000713"),
	)
	if _, err := New(mr, Config{}); !errors.Is(err, sparql.ErrIncompleteSuperclassClosure) {
		t.Fatalf("New with incomplete superclass closure = %v, want ErrIncompleteSuperclassClosure", err)
	}
}

type blockingCancelReader struct {
	reader.GraphReader
	started  chan struct{}
	canceled chan error
}

func (b *blockingCancelReader) ScanPattern(ctx context.Context, _ string, _ int) ([]graph.EntityState, error) {
	close(b.started)
	<-ctx.Done()
	err := ctx.Err()
	b.canceled <- err
	return nil, err
}

func TestRequestCancellationAndServeLifecycle(t *testing.T) {
	defer registerServerTestVocab(t)()
	mr := newTestFixtureReader(t)

	br := &blockingCancelReader{
		GraphReader: mr,
		started:     make(chan struct{}),
		canceled:    make(chan error, 1),
	}
	srv, err := New(br, Config{
		BindAddress:  "127.0.0.1:0",
		QueryTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var nilCtx context.Context
	if err := srv.Shutdown(nilCtx); !errors.Is(err, reader.ErrNilContext) {
		t.Fatalf("Shutdown(nil) = %v, want reader.ErrNilContext", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}

	serveCtx, serveCancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(serveCtx, ln)
	}()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, "http://"+ln.Addr().String()+"/sparql?query="+url.QueryEscape("SELECT ?s WHERE { ?s ?p ?o }"), nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	clientDone := make(chan error, 1)
	go func() {
		resp, err := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		clientDone <- err
	}()

	<-br.started
	reqCancel()

	if err := <-br.canceled; !errors.Is(err, context.Canceled) {
		t.Fatalf("reader cancel err = %v, want context.Canceled", err)
	}
	<-clientDone

	serveCancel()
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve exit err = %v, want nil", err)
	}
}

type pipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
}

func newPipeListener() *pipeListener {
	return &pipeListener{
		conns:  make(chan net.Conn),
		closed: make(chan struct{}),
	}
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case <-l.closed:
		return nil, net.ErrClosed
	case c := <-l.conns:
		return c, nil
	}
}

func (l *pipeListener) Close() error {
	l.once.Do(func() {
		close(l.closed)
	})
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 8080}
}

func (l *pipeListener) dial(ctx context.Context) (net.Conn, error) {
	c1, c2 := net.Pipe()
	select {
	case <-l.closed:
		_ = c1.Close()
		_ = c2.Close()
		return nil, net.ErrClosed
	case <-ctx.Done():
		_ = c1.Close()
		_ = c2.Close()
		return nil, ctx.Err()
	case l.conns <- c2:
		return c1, nil
	}
}

func TestServerServeAndShutdownInSynctestBubble(t *testing.T) {
	defer registerServerTestVocab(t)()

	synctest.Test(t, func(t *testing.T) {
		mr := newTestFixtureReader(t)
		srv, err := New(mr, Config{BaseIRI: testBaseIRI})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		ln := newPipeListener()
		serveCtx, serveCancel := context.WithCancel(t.Context())
		serveErrCh := make(chan error, 1)
		go func() {
			serveErrCh <- srv.Serve(serveCtx, ln)
		}()

		client := &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return ln.dial(ctx)
				},
				DisableKeepAlives: true,
			},
		}

		resp, err := client.Get("http://pipe.local/sparql?query=" + url.QueryEscape(`PREFIX ex: <https://example.org/onto#> ASK WHERE { ?s ex:callsign "ALPHA-1" }`))
		if err != nil {
			t.Fatalf("GET /sparql over pipe listener: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"boolean":true`) {
			t.Fatalf("status=%d body=%s, want 200 boolean:true", resp.StatusCode, string(body))
		}

		serveCancel()
		synctest.Wait()

		if err := <-serveErrCh; err != nil {
			t.Fatalf("Serve exit err = %v, want nil", err)
		}
	})
}

func FuzzSPARQLHTTPRequest(f *testing.F) {
	seeds := []struct {
		method      string
		rawQuery    string
		contentType string
		body        string
		accept      string
	}{
		{http.MethodGet, "query=ASK+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D", "", "", "application/sparql-results+json"},
		{http.MethodGet, "query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D", "", "", "text/csv"},
		{http.MethodPost, "", "application/sparql-query", "SELECT ?s WHERE { ?s ?p ?o } LIMIT 1", "text/tab-separated-values"},
		{http.MethodPost, "", "application/x-www-form-urlencoded", "query=ASK+%7B+%3Fs+%3Fp+%3Fo+%7D", "application/sparql-results+xml"},
		{http.MethodGet, "query=SELECT+REDUCED+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D", "", "", "application/sparql-results+json"},
		{http.MethodGet, "query=SELECT+%3Fs+WHERE+%7B+%3Fs+%3Fp+%3Fo+%7D", "", "", "text/turtle"},
		{http.MethodGet, "invalid=1", "", "", "*/*"},
		{http.MethodGet, " ", "", "", "text/turtle;q=0.5, application/sparql-results+json;q=0.9"},
		{http.MethodPost, "", "text/plain", "ASK {}", "*/*"},
	}
	for _, s := range seeds {
		f.Add(s.method, s.rawQuery, s.contentType, s.body, s.accept)
	}

	defer registerServerTestVocab(f)()
	mr := newTestFixtureReader(f)
	srv, err := New(mr, Config{
		BaseIRI:         testBaseIRI,
		MaxResults:      4,
		MaxScanEntities: 4,
		MaxQueryBytes:   512,
		MaxExportBytes:  512,
	})
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	handler := srv.Handler()
	knownMediaTypes := []string{
		MediaTypeSPARQLJSON,
		MediaTypeSPARQLXML,
		MediaTypeCSV,
		MediaTypeTSV,
		MediaTypeTurtle,
		MediaTypeNTriples,
		MediaTypeJSONLD,
	}

	f.Fuzz(func(t *testing.T, method, rawQuery, contentType, body, accept string) {
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
		default:
			method = http.MethodGet
		}
		req := httptest.NewRequest(method, "/sparql", strings.NewReader(body))
		req.URL.RawQuery = rawQuery
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		switch rec.Code {
		case http.StatusOK:
			ct := rec.Header().Get("Content-Type")
			matchedCT := false
			for _, mt := range knownMediaTypes {
				if strings.HasPrefix(ct, mt) {
					matchedCT = true
					break
				}
			}
			if !matchedCT {
				t.Fatalf("200 OK returned unknown Content-Type %q", ct)
			}
			if rec.Header().Get(HeaderTruncated) != "" {
				t.Fatalf("200 OK set %s without %s request header", HeaderTruncated, HeaderAllowTruncation)
			}
		case http.StatusBadRequest,
			http.StatusMethodNotAllowed,
			http.StatusNotAcceptable,
			http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("error status %d Content-Type = %q, want application/json", rec.Code, rec.Header().Get("Content-Type"))
			}
			var payload errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("error status %d body failed JSON decode: %v (%q)", rec.Code, err, rec.Body.String())
			}
			if payload.Error == "" || payload.Message == "" {
				t.Fatalf("error status %d payload missing error or message: %+v", rec.Code, payload)
			}
		default:
			t.Fatalf("unexpected HTTP status code %d", rec.Code)
		}
	})
}

func TestNegotiationSerializationAndServerEdgeCases(t *testing.T) {
	defer registerServerTestVocab(t)()

	// 1. ResultFormat.ContentType() across all values.
	wantCT := map[ResultFormat]string{
		FormatSPARQLJSON: MediaTypeSPARQLJSON + "; charset=utf-8",
		FormatSPARQLXML:  MediaTypeSPARQLXML + "; charset=utf-8",
		FormatCSV:        MediaTypeCSV + "; charset=utf-8",
		FormatTSV:        MediaTypeTSV + "; charset=utf-8",
		FormatTurtle:     MediaTypeTurtle + "; charset=utf-8",
		FormatNTriples:   MediaTypeNTriples + "; charset=utf-8",
		FormatJSONLD:     MediaTypeJSONLD + "; charset=utf-8",
		ResultFormat(99): "application/octet-stream",
	}
	for f, want := range wantCT {
		if got := f.ContentType(); got != want {
			t.Fatalf("ResultFormat(%d).ContentType() = %q, want %q", f, got, want)
		}
	}

	// 2. NegotiateAccept weighted clauses, wildcards, explicit q=0 exclusions, graph forms, and invalid q values.
	f1, err := NegotiateAccept(2 /* QueryAsk */, "text/turtle;q=0.9, application/xml;q=0.8")
	if err != nil || f1 != FormatSPARQLXML {
		t.Fatalf("NegotiateAccept weighted ASK = (%v, %v), want FormatSPARQLXML", f1, err)
	}
	f2, err := NegotiateAccept(1 /* QuerySelect */, "text/*;q=0.4, */*;q=0.9")
	if err != nil || f2 != FormatSPARQLJSON {
		t.Fatalf("NegotiateAccept higher q wildcard = (%v, %v), want FormatSPARQLJSON", f2, err)
	}
	fq0, err := NegotiateAccept(1 /* QuerySelect */, "application/sparql-results+json;q=0, */*;q=0.8")
	if err != nil || fq0 != FormatSPARQLXML {
		t.Fatalf("NegotiateAccept explicit q=0 on JSON with wildcard = (%v, %v), want FormatSPARQLXML", fq0, err)
	}
	if _, err := NegotiateAccept(2 /* QueryAsk */, "application/sparql-results+json;q=0, application/sparql-results+xml;q=0, */*;q=0.8"); !errors.Is(err, ErrNotAcceptable) {
		t.Fatalf("NegotiateAccept with all ASK formats excluded by q=0 = %v, want ErrNotAcceptable", err)
	}
	f3, err := NegotiateAccept(3 /* QueryConstruct */, "")
	if err != nil || f3 != FormatTurtle {
		t.Fatalf("NegotiateAccept default CONSTRUCT = (%v, %v), want FormatTurtle", f3, err)
	}
	for _, mt := range []struct {
		accept string
		want   ResultFormat
	}{
		{"text/turtle", FormatTurtle},
		{"application/n-triples", FormatNTriples},
		{"application/ld+json", FormatJSONLD},
		{"application/*", FormatJSONLD},
	} {
		got, err := NegotiateAccept(3 /* QueryConstruct */, mt.accept)
		if err != nil || got != mt.want {
			t.Fatalf("NegotiateAccept(CONSTRUCT, %q) = (%v, %v), want %v", mt.accept, got, err, mt.want)
		}
	}
	for _, badAccept := range []string{
		"application/sparql-results+json;q=1.5",
		"application/sparql-results+json;q=-0.1",
		"application/sparql-results+json;q=abc",
		"/*;q=0.5",
		", ,",
	} {
		if _, err := NegotiateAccept(1, badAccept); !errors.Is(err, ErrNotAcceptable) {
			t.Fatalf("NegotiateAccept(%q) = %v, want ErrNotAcceptable", badAccept, err)
		}
	}

	// 3. WriteSelectAskResult error cases and TSV special-character escaping.
	if err := WriteSelectAskResult(io.Discard, nil, FormatSPARQLJSON); err == nil {
		t.Fatal("WriteSelectAskResult(nil) = nil, want error")
	}
	const idEsc = "c360.ops.uas1.intel.platform.esc"
	mrEsc, err := reader.NewMemoryReader(graph.EntityState{
		ID: idEsc,
		Triples: []message.Triple{
			{Subject: idEsc, Predicate: "c360.intel.callsign", Object: "a\tb\nc\rd\"e\\f"},
		},
	})
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}
	srv, err := New(mrEsc, Config{BaseIRI: testBaseIRI})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	reqTSV, _ := http.NewRequest(http.MethodPost, ts.URL+"/sparql", strings.NewReader(`
		PREFIX ex: <https://example.org/onto#>
		SELECT ?cs WHERE { ?s ex:callsign ?cs }
	`))
	reqTSV.Header.Set("Content-Type", "application/sparql-query")
	reqTSV.Header.Set("Accept", MediaTypeTSV)
	respTSV, err := ts.Client().Do(reqTSV)
	if err != nil {
		t.Fatalf("Do TSV: %v", err)
	}
	tsvBody, _ := io.ReadAll(respTSV.Body)
	respTSV.Body.Close()
	if !strings.Contains(string(tsvBody), `"a\tb\nc\rd\"e\\f"`) {
		t.Fatalf("TSV escaped body = %q", string(tsvBody))
	}

	// 4. Additional HTTP protocol validation branches (405 PUT with Allow header, POST with URL query, missing/bad Content-Type, empty query).
	extraReqs := []struct {
		name       string
		method     string
		target     string
		ct         string
		body       string
		wantStatus int
		wantAllow  string
	}{
		{name: "PUT method not allowed", method: http.MethodPut, target: "/sparql", wantStatus: http.StatusMethodNotAllowed, wantAllow: "GET, POST"},
		{name: "POST with URL query params", method: http.MethodPost, target: "/sparql?query=ASK%7B%7D", ct: "application/sparql-query", body: "ASK {}", wantStatus: http.StatusBadRequest},
		{name: "POST missing Content-Type", method: http.MethodPost, target: "/sparql", ct: "", body: "ASK {}", wantStatus: http.StatusBadRequest},
		{name: "POST malformed Content-Type", method: http.MethodPost, target: "/sparql", ct: "application/sparql-query; =", body: "ASK {}", wantStatus: http.StatusBadRequest},
		{name: "POST empty raw body", method: http.MethodPost, target: "/sparql", ct: "application/sparql-query", body: "   ", wantStatus: http.StatusBadRequest},
		{name: "GET empty query value", method: http.MethodGet, target: "/sparql?query=+++", wantStatus: http.StatusBadRequest},
	}
	for _, tc := range extraReqs {
		req, _ := http.NewRequest(tc.method, ts.URL+tc.target, strings.NewReader(tc.body))
		if tc.ct != "" {
			req.Header.Set("Content-Type", tc.ct)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		resp.Body.Close()
		if resp.StatusCode != tc.wantStatus {
			t.Fatalf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.wantStatus)
		}
		if tc.wantAllow != "" && resp.Header.Get("Allow") != tc.wantAllow {
			t.Fatalf("%s: Allow = %q, want %q", tc.name, resp.Header.Get("Allow"), tc.wantAllow)
		}
	}

	// 5. Server constructor, ValidateConfig, Serve, and Shutdown edge cases.
	checkServerLifecycleEdgeCases(t, srv)
}

func checkServerLifecycleEdgeCases(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := New(nil, Config{}); err == nil {
		t.Fatal("New(nil) = nil, want error")
	}
	if err := ValidateConfig(Config{BindAddress: "not-a-host-port"}); err == nil {
		t.Fatal("ValidateConfig(invalid) = nil, want error")
	}
	var nilCtx context.Context
	if err := srv.Serve(nilCtx, nil); !errors.Is(err, reader.ErrNilContext) {
		t.Fatalf("Serve(nilCtx) = %v, want reader.ErrNilContext", err)
	}
	if err := srv.Serve(context.Background(), nil); err == nil {
		t.Fatal("Serve(nil listener) = nil, want error")
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err == nil {
		t.Fatal("Shutdown before Serve = nil, want error")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	serveDone := make(chan error, 1)
	go func() {
		serveDone <- srv.Serve(context.Background(), ln)
	}()
	// Wait until server responds to a request, then shut down via Shutdown(ctx).
	resp, err := http.Get("http://" + ln.Addr().String() + "/sparql?query=" + url.QueryEscape("ASK WHERE { ?s ?p ?o }"))
	if err != nil {
		t.Fatalf("Get on live listener: %v", err)
	}
	resp.Body.Close()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown active server: %v", err)
	}
	if err := <-serveDone; err != nil {
		t.Fatalf("Serve after Shutdown = %v, want nil", err)
	}
}

func TestEntityJSONLDAndExportRoutes(t *testing.T) {
	restore := vocabulary.SnapshotRegistry()
	vocabulary.ClearRegistry()
	defer restore()

	if err := packs.RegisterBFOCCOCore(); err != nil {
		t.Fatalf("RegisterBFOCCOCore: %v", err)
	}
	if err := diekm.Register(); err != nil {
		t.Fatalf("diekm.Register: %v", err)
	}
	vocabulary.Register("c360.intel.callsign",
		vocabulary.WithIRI("https://example.org/onto#callsign"),
		vocabulary.WithDataType(vocabulary.DataTypeString))

	const (
		idAlpha = "c360.ops.uas1.intel.platform.alpha"
		idBravo = "c360.ops.uas1.intel.platform.bravo"
	)
	mr, err := reader.NewMemoryReader(
		graph.EntityState{
			ID: idAlpha,
			Triples: []message.Triple{
				{Subject: idAlpha, Predicate: "c360.intel.callsign", Object: "ALPHA-1"},
				{Subject: idAlpha, Predicate: "intel.role.assigned", Object: "ISR"},
				{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
			},
		},
		graph.EntityState{
			ID: idBravo,
			Triples: []message.Triple{
				{Subject: idBravo, Predicate: "c360.intel.callsign", Object: "BRAVO-2"},
				{Subject: idBravo, Predicate: "intel.role.assigned", Object: "Strike"},
			},
		},
	)
	if err != nil {
		t.Fatalf("NewMemoryReader: %v", err)
	}

	srv, err := New(mr, Config{BaseIRI: testBaseIRI})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	t.Run("GET /entities/{entity_id}.jsonld returns unfolded JSON-LD and 400/404/405/406 on errors", func(t *testing.T) {
		resp, err := ts.Client().Get(ts.URL + "/entities/" + idAlpha + ".jsonld")
		if err != nil {
			t.Fatalf("Get entity JSON-LD: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200: %s", resp.StatusCode, string(body))
		}
		if !strings.HasPrefix(resp.Header.Get("Content-Type"), MediaTypeJSONLD) {
			t.Fatalf("Content-Type = %q, want prefix %q", resp.Header.Get("Content-Type"), MediaTypeJSONLD)
		}
		bRole, err := irimap.BlankNodeID(idAlpha, "intel.role.assigned", `"ISR"`, 0)
		if err != nil {
			t.Fatalf("BlankNodeID: %v", err)
		}
		if !strings.Contains(string(body), bRole) || !strings.Contains(string(body), "IntelligencePlatform") || !strings.Contains(string(body), "1500.5") {
			t.Fatalf("entity JSON-LD missing unfolded blank node, virtual rdf:type, or altitude:\n%s", string(body))
		}

		// 404 Not Found for valid 6-part entity ID that does not exist.
		resp404, err := ts.Client().Get(ts.URL + "/entities/c360.ops.uas1.intel.platform.missing.jsonld")
		if err != nil {
			t.Fatalf("Get missing entity: %v", err)
		}
		resp404.Body.Close()
		if resp404.StatusCode != http.StatusNotFound {
			t.Fatalf("missing entity status = %d, want 404", resp404.StatusCode)
		}

		// 400 Bad Request for malformed entity IDs or missing .jsonld suffix.
		for _, badPath := range []string{
			"/entities/not-six-parts.jsonld",
			"/entities/.jsonld",
			"/entities/" + idAlpha,
		} {
			r, err := ts.Client().Get(ts.URL + badPath)
			if err != nil {
				t.Fatalf("Get(%s): %v", badPath, err)
			}
			r.Body.Close()
			if r.StatusCode != http.StatusBadRequest {
				t.Fatalf("Get(%s) status = %d, want 400", badPath, r.StatusCode)
			}
		}

		// 406 Not Acceptable when Accept excludes application/ld+json (including explicit q=0 with wildcard).
		for _, badAccept := range []string{MediaTypeTurtle, "application/ld+json;q=0, */*;q=0.8"} {
			req406, _ := http.NewRequest(http.MethodGet, ts.URL+"/entities/"+idAlpha+".jsonld", nil)
			req406.Header.Set("Accept", badAccept)
			resp406, err := ts.Client().Do(req406)
			if err != nil {
				t.Fatalf("Do 406 (%s): %v", badAccept, err)
			}
			resp406.Body.Close()
			if resp406.StatusCode != http.StatusNotAcceptable {
				t.Fatalf("entity JSON-LD with Accept=%q status = %d, want 406", badAccept, resp406.StatusCode)
			}
		}

		// 405 Method Not Allowed on POST with Allow: GET.
		req405, _ := http.NewRequest(http.MethodPost, ts.URL+"/entities/"+idAlpha+".jsonld", strings.NewReader("{}"))
		resp405, err := ts.Client().Do(req405)
		if err != nil {
			t.Fatalf("Do 405: %v", err)
		}
		resp405.Body.Close()
		if resp405.StatusCode != http.StatusMethodNotAllowed || resp405.Header.Get("Allow") != "GET" {
			t.Fatalf("POST /entities/*.jsonld status = %d Allow = %q, want 405 Allow=GET", resp405.StatusCode, resp405.Header.Get("Allow"))
		}
	})

	t.Run("POST /export supports prefix and construct selectors across Turtle, N-Triples, and JSON-LD and rejects GET /export.ttl", func(t *testing.T) {
		selectors := []struct {
			name string
			body string
		}{
			{name: "1-part prefix", body: `{"prefix":"c360"}`},
			{name: "2-part prefix", body: `{"prefix":"c360.ops"}`},
			{name: "5-part prefix", body: `{"prefix":"c360.ops.uas1.intel.platform"}`},
			{name: "6-part exact prefix", body: `{"prefix":"c360.ops.uas1.intel.platform.alpha"}`},
			{name: "construct selector", body: `{"construct":"CONSTRUCT WHERE { ?s ?p ?o }"}`},
		}
		formats := []struct {
			accept   string
			wantMime string
		}{
			{accept: "", wantMime: MediaTypeTurtle},
			{accept: MediaTypeTurtle, wantMime: MediaTypeTurtle},
			{accept: MediaTypeNTriples, wantMime: MediaTypeNTriples},
			{accept: MediaTypeJSONLD, wantMime: MediaTypeJSONLD},
		}
		for _, sel := range selectors {
			for _, fmtSpec := range formats {
				req, _ := http.NewRequest(http.MethodPost, ts.URL+"/export", strings.NewReader(sel.body))
				req.Header.Set("Content-Type", "application/json; charset=utf-8")
				if fmtSpec.accept != "" {
					req.Header.Set("Accept", fmtSpec.accept)
				}
				resp, err := ts.Client().Do(req)
				if err != nil {
					t.Fatalf("POST /export (%s, %s): %v", sel.name, fmtSpec.accept, err)
				}
				out, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					t.Fatalf("POST /export (%s, %s) status = %d, want 200: %s", sel.name, fmtSpec.accept, resp.StatusCode, string(out))
				}
				if !strings.HasPrefix(resp.Header.Get("Content-Type"), fmtSpec.wantMime) {
					t.Fatalf("POST /export (%s, %s) Content-Type = %q, want %q", sel.name, fmtSpec.accept, resp.Header.Get("Content-Type"), fmtSpec.wantMime)
				}
				if !strings.Contains(string(out), "ALPHA-1") || !strings.Contains(string(out), "1500.5") {
					t.Fatalf("POST /export (%s, %s) missing unfolded triples:\n%s", sel.name, fmtSpec.accept, string(out))
				}
			}
		}

		// POST /export with prefix exceeding MaxScanEntities=1 returns bounded partial non-empty graph when X-Semsparql-Allow-Truncation: true is set.
		smallScanSrv, err := New(mr, Config{
			BaseIRI:         testBaseIRI,
			MaxScanEntities: 1,
		})
		if err != nil {
			t.Fatalf("New smallScanSrv: %v", err)
		}
		recScan := httptest.NewRecorder()
		reqScan := httptest.NewRequest(http.MethodPost, "/export", strings.NewReader(`{"prefix":"c360.ops.uas1.intel.platform"}`))
		reqScan.Header.Set("Content-Type", "application/json")
		reqScan.Header.Set("Accept", MediaTypeNTriples)
		reqScan.Header.Set(HeaderAllowTruncation, "true")
		smallScanSrv.Handler().ServeHTTP(recScan, reqScan)
		if recScan.Code != http.StatusOK ||
			recScan.Header().Get(HeaderTruncated) != "true" ||
			recScan.Header().Get(HeaderTruncationReason) != sparql.TruncationReasonScanBudget ||
			!strings.Contains(recScan.Body.String(), "ALPHA-1") ||
			strings.Contains(recScan.Body.String(), "BRAVO-2") {
			t.Fatalf("POST /export scan_budget truncation: code=%d Truncated=%q Reason=%q body=%q",
				recScan.Code, recScan.Header().Get(HeaderTruncated), recScan.Header().Get(HeaderTruncationReason), recScan.Body.String())
		}

		// Missing 6-part exact entity prefix returns 200 OK with empty graph.
		reqMissing, _ := http.NewRequest(http.MethodPost, ts.URL+"/export", strings.NewReader(`{"prefix":"c360.ops.uas1.intel.platform.missing"}`))
		reqMissing.Header.Set("Content-Type", "application/json")
		reqMissing.Header.Set("Accept", MediaTypeNTriples)
		respMissing, err := ts.Client().Do(reqMissing)
		if err != nil {
			t.Fatalf("POST /export missing exact prefix: %v", err)
		}
		outMissing, _ := io.ReadAll(respMissing.Body)
		respMissing.Body.Close()
		if respMissing.StatusCode != http.StatusOK || strings.TrimSpace(string(outMissing)) != "" {
			t.Fatalf("POST /export missing exact prefix status=%d body=%q, want 200 empty", respMissing.StatusCode, string(outMissing))
		}

		// GET /export.ttl is not registered (404 Not Found), and GET /export returns 405 Method Not Allowed with Allow: POST.
		respTTL, err := ts.Client().Get(ts.URL + "/export.ttl")
		if err != nil {
			t.Fatalf("Get /export.ttl: %v", err)
		}
		respTTL.Body.Close()
		if respTTL.StatusCode != http.StatusNotFound {
			t.Fatalf("GET /export.ttl status = %d, want 404", respTTL.StatusCode)
		}

		respGetExport, err := ts.Client().Get(ts.URL + "/export")
		if err != nil {
			t.Fatalf("Get /export: %v", err)
		}
		respGetExport.Body.Close()
		if respGetExport.StatusCode != http.StatusMethodNotAllowed || respGetExport.Header.Get("Allow") != "POST" {
			t.Fatalf("GET /export status = %d Allow = %q, want 405 Allow=POST", respGetExport.StatusCode, respGetExport.Header.Get("Allow"))
		}

		// Invalid POST /export requests -> 400 Bad Request or 406 Not Acceptable.
		badExports := []struct {
			name       string
			ct         string
			accept     string
			body       string
			wantStatus int
		}{
			{name: "wrong Content-Type", ct: "text/plain", body: `{"prefix":"c360.ops"}`, wantStatus: http.StatusBadRequest},
			{name: "invalid JSON", ct: "application/json", body: `{not-json}`, wantStatus: http.StatusBadRequest},
			{name: "trailing JSON token", ct: "application/json", body: `{"prefix":"c360.ops"} {"extra":1}`, wantStatus: http.StatusBadRequest},
			{name: "unknown JSON field", ct: "application/json", body: `{"unknown":"x"}`, wantStatus: http.StatusBadRequest},
			{name: "empty selector object", ct: "application/json", body: `{}`, wantStatus: http.StatusBadRequest},
			{name: "both prefix and construct", ct: "application/json", body: `{"prefix":"c360.ops","construct":"CONSTRUCT WHERE { ?s ?p ?o }"}`, wantStatus: http.StatusBadRequest},
			{name: "wildcard in prefix", ct: "application/json", body: `{"prefix":"c360.ops.*"}`, wantStatus: http.StatusBadRequest},
			{name: "trailing dot in prefix", ct: "application/json", body: `{"prefix":"c360.ops."}`, wantStatus: http.StatusBadRequest},
			{name: "whitespace around prefix", ct: "application/json", body: `{"prefix":" c360.ops "}`, wantStatus: http.StatusBadRequest},
			{name: "7-part prefix", ct: "application/json", body: `{"prefix":"a.b.c.d.e.f.g"}`, wantStatus: http.StatusBadRequest},
			{name: "empty segment in prefix", ct: "application/json", body: `{"prefix":"c360..ops"}`, wantStatus: http.StatusBadRequest},
			{name: "SELECT in construct field", ct: "application/json", body: `{"construct":"SELECT ?s WHERE { ?s ?p ?o }"}`, wantStatus: http.StatusBadRequest},
			{name: "DESCRIBE in construct field", ct: "application/json", body: `{"construct":"DESCRIBE <https://example.org/x>"}`, wantStatus: http.StatusBadRequest},
			{name: "syntax error in construct field", ct: "application/json", body: `{"construct":"CONSTRUCT WHERE { ?s ?p"}`, wantStatus: http.StatusBadRequest},
			{name: "incompatible Accept on /export", ct: "application/json", accept: MediaTypeSPARQLJSON, body: `{"prefix":"c360.ops"}`, wantStatus: http.StatusNotAcceptable},
		}
		for _, tc := range badExports {
			req, _ := http.NewRequest(http.MethodPost, ts.URL+"/export", strings.NewReader(tc.body))
			if tc.ct != "" {
				req.Header.Set("Content-Type", tc.ct)
			}
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			r, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			b, _ := io.ReadAll(r.Body)
			r.Body.Close()
			if r.StatusCode != tc.wantStatus {
				t.Fatalf("%s: status = %d, want %d (%s)", tc.name, r.StatusCode, tc.wantStatus, string(b))
			}
		}
	})

	t.Run("WriteGraphResult fails loudly on invalid triples or unsupported format", func(t *testing.T) {
		err := WriteGraphResult(io.Discard, []message.Triple{{Subject: "", Predicate: "c360.intel.callsign", Object: "x"}}, FormatTurtle, testBaseIRI)
		if !errors.Is(err, sparql.ErrInvalidOutputTriple) {
			t.Fatalf("WriteGraphResult(empty subject) = %v, want ErrInvalidOutputTriple", err)
		}
		errNaN := WriteGraphResult(io.Discard, []message.Triple{{Subject: idAlpha, Predicate: "intel.measurement.altitude-m", Object: math.NaN()}}, FormatNTriples, testBaseIRI)
		if !errors.Is(errNaN, sparql.ErrInvalidOutputTriple) {
			t.Fatalf("WriteGraphResult(NaN) = %v, want ErrInvalidOutputTriple", errNaN)
		}
		if err := WriteGraphResult(io.Discard, nil, FormatSPARQLJSON, testBaseIRI); err == nil {
			t.Fatal("WriteGraphResult(FormatSPARQLJSON) = nil, want error")
		}
	})

	t.Run("Classified transient, fatal, invalid, response_too_large, canceled, and unclassified reader errors map to expected HTTP statuses", func(t *testing.T) {
		transientErr := &errs.ClassifiedError{
			Class:     errs.ErrorTransient,
			Err:       errors.New("kv store unavailable"),
			Component: "graph-ingest",
			Operation: "handleQueryPrefix",
			Message:   "kv bucket busy",
			Code:      "kv_transient",
			Detail:    map[string]any{"bucket": "ENTITY_STATES"},
		}
		fatalErr := &errs.ClassifiedError{
			Class:     errs.ErrorFatal,
			Err:       errors.New("incompatible server"),
			Component: "NATSRPCReader",
			Operation: "ScanPattern",
			Message:   "server ignored pattern",
			Code:      "pattern_capability_unsupported",
		}
		respTooLargeErr := &errs.ClassifiedError{
			Class:   errs.ErrorInvalid,
			Err:     errors.New("response exceeds active NATS maximum payload"),
			Message: "response exceeds active NATS maximum payload",
			Code:    "response_too_large",
		}
		invalidErr := &errs.ClassifiedError{
			Class:   errs.ErrorInvalid,
			Err:     errors.New("invalid selector"),
			Message: "invalid selector",
			Code:    "invalid_request",
		}

		for _, tc := range []struct {
			name         string
			readerErr    error
			wantStatus   int
			wantCode     string
			wantClass    string
			wantRetryHdr bool
		}{
			{
				name:         "classified transient",
				readerErr:    transientErr,
				wantStatus:   http.StatusServiceUnavailable,
				wantCode:     "kv_transient",
				wantClass:    errs.ErrorTransient.String(),
				wantRetryHdr: true,
			},
			{
				name:       "classified fatal",
				readerErr:  fatalErr,
				wantStatus: http.StatusInternalServerError,
				wantCode:   "pattern_capability_unsupported",
				wantClass:  errs.ErrorFatal.String(),
			},
			{
				name:         "response_too_large maps to 503",
				readerErr:    respTooLargeErr,
				wantStatus:   http.StatusServiceUnavailable,
				wantCode:     "response_too_large",
				wantClass:    errs.ErrorInvalid.String(),
				wantRetryHdr: true,
			},
			{
				name:       "classified invalid maps to 400",
				readerErr:  invalidErr,
				wantStatus: http.StatusBadRequest,
				wantCode:   "invalid_request",
				wantClass:  errs.ErrorInvalid.String(),
			},
			{
				name:         "context.Canceled maps to 503 with Retry-After",
				readerErr:    context.Canceled,
				wantStatus:   http.StatusServiceUnavailable,
				wantCode:     "service_unavailable",
				wantRetryHdr: true,
			},
			{
				name:       "unclassified error maps to 500 internal_error",
				readerErr:  errors.New("unexpected transport fault"),
				wantStatus: http.StatusInternalServerError,
				wantCode:   "internal_error",
			},
		} {
			errReader := &errorInjectingReader{base: mr, scanErr: tc.readerErr}
			errSrv, err := New(errReader, Config{BaseIRI: testBaseIRI})
			if err != nil {
				t.Fatalf("%s New: %v", tc.name, err)
			}
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/sparql?query="+url.QueryEscape("SELECT ?s WHERE { ?s ?p ?o }"), nil)
			req.Header.Set(HeaderAllowTruncation, "true")
			errSrv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("%s status = %d, want %d (%s)", tc.name, rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantRetryHdr && rec.Header().Get("Retry-After") == "" {
				t.Fatalf("%s missing Retry-After header", tc.name)
			}
			var payload errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("%s json.Unmarshal: %v", tc.name, err)
			}
			if payload.Error != tc.wantCode || payload.Class != tc.wantClass {
				t.Fatalf("%s payload = %+v, want error=%q class=%q", tc.name, payload, tc.wantCode, tc.wantClass)
			}
		}
	})
}

type errorInjectingReader struct {
	base    reader.GraphReader
	scanErr error
}

func (r *errorInjectingReader) GetEntity(ctx context.Context, id string) (*graph.EntityState, error) {
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	return r.base.GetEntity(ctx, id)
}

func (r *errorInjectingReader) GetBatch(ctx context.Context, ids []string) (*graph.EntityBatchResponse, error) {
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	return r.base.GetBatch(ctx, ids)
}

func (r *errorInjectingReader) ScanPrefix(ctx context.Context, prefix string, budget int) ([]graph.EntityState, error) {
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	return r.base.ScanPrefix(ctx, prefix, budget)
}

func (r *errorInjectingReader) ScanPattern(ctx context.Context, pattern string, budget int) ([]graph.EntityState, error) {
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	return r.base.ScanPattern(ctx, pattern, budget)
}

func (r *errorInjectingReader) EntitiesByPredicate(ctx context.Context, predicate string, budget int) ([]graph.EntityState, error) {
	if r.scanErr != nil {
		return nil, r.scanErr
	}
	return r.base.EntitiesByPredicate(ctx, predicate, budget)
}

func FuzzExportHTTPRequest(f *testing.F) {
	seeds := []struct {
		method      string
		path        string
		contentType string
		body        string
		accept      string
	}{
		{http.MethodPost, "/export", "application/json", `{"prefix":"c360.ops.uas1"}`, "text/turtle"},
		{http.MethodPost, "/export", "application/json", `{"construct":"CONSTRUCT WHERE { ?s ?p ?o }"}`, "application/n-triples"},
		{http.MethodPost, "/export", "application/json", `{"prefix":"c360.ops.uas1.intel.platform.alpha"}`, "application/ld+json"},
		{http.MethodPost, "/export", "application/json", `{"prefix":"c360.*"}`, "*/*"},
		{http.MethodGet, "/entities/c360.ops.uas1.intel.platform.alpha.jsonld", "", "", "application/ld+json"},
		{http.MethodGet, "/entities/invalid.jsonld", "", "", "*/*"},
	}
	for _, s := range seeds {
		f.Add(s.method, s.path, s.contentType, s.body, s.accept)
	}

	defer registerServerTestVocab(f)()
	mr := newTestFixtureReader(f)
	srv, err := New(mr, Config{
		BaseIRI:         testBaseIRI,
		MaxResults:      4,
		MaxScanEntities: 4,
		MaxQueryBytes:   512,
		MaxExportBytes:  512,
	})
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	handler := srv.Handler()
	graphMediaTypes := []string{
		MediaTypeTurtle,
		MediaTypeNTriples,
		MediaTypeJSONLD,
	}

	f.Fuzz(func(t *testing.T, method, path, contentType, body, accept string) {
		switch method {
		case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
		default:
			method = http.MethodPost
		}
		if !strings.HasPrefix(path, "/") {
			path = "/export"
		}
		req := httptest.NewRequest(method, "/export", strings.NewReader(body))
		req.URL.Path = path
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		switch rec.Code {
		case http.StatusOK:
			ct := rec.Header().Get("Content-Type")
			matchedCT := false
			for _, mt := range graphMediaTypes {
				if strings.HasPrefix(ct, mt) {
					matchedCT = true
					break
				}
			}
			if !matchedCT {
				t.Fatalf("200 OK returned unknown Content-Type %q", ct)
			}
			if rec.Header().Get(HeaderTruncated) != "" {
				t.Fatalf("200 OK set %s without %s request header", HeaderTruncated, HeaderAllowTruncation)
			}
		case http.StatusNotFound:
			// Unregistered paths or missing /entities/{id}.jsonld return 404.
		case http.StatusBadRequest,
			http.StatusMethodNotAllowed,
			http.StatusNotAcceptable,
			http.StatusRequestEntityTooLarge,
			http.StatusUnprocessableEntity,
			http.StatusInternalServerError,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout:
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("error status %d Content-Type = %q, want application/json", rec.Code, rec.Header().Get("Content-Type"))
			}
			var payload errorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatalf("error status %d body failed JSON decode: %v (%q)", rec.Code, err, rec.Body.String())
			}
			if payload.Error == "" || payload.Message == "" {
				t.Fatalf("error status %d payload missing error or message: %+v", rec.Code, payload)
			}
		default:
			t.Fatalf("unexpected HTTP status code %d", rec.Code)
		}
	})
}
