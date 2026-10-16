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
	"testing"
	"time"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
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

	// 3. Non-local bind with mTLS ClientAuth + ClientCAs succeeds.
	mtlsCfg := &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  x509.NewCertPool(),
	}
	if _, err := New(mr, Config{BindAddress: "0.0.0.0:8080", TLSConfig: mtlsCfg}); err != nil {
		t.Fatalf("New with mTLS = %v, want nil", err)
	}

	// 4. Non-local bind with BearerToken succeeds and enforces Authorization: Bearer <token>.
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

	// Authenticated request -> 200 OK.
	reqAuth, _ := http.NewRequest(http.MethodGet, ts.URL+"/sparql?query=ASK%7B%7D", nil)
	reqAuth.Header.Set("Authorization", "Bearer secret-token-123")
	respAuth, err := ts.Client().Do(reqAuth)
	if err != nil {
		t.Fatalf("Do auth: %v", err)
	}
	respAuth.Body.Close()
	if respAuth.StatusCode != http.StatusOK {
		t.Fatalf("auth status = %d, want 200", respAuth.StatusCode)
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

	mr, err := reader.NewMemoryReader()
	if err != nil {
		f.Fatalf("NewMemoryReader: %v", err)
	}
	srv, err := New(mr, Config{BaseIRI: testBaseIRI})
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	handler := srv.Handler()

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
		if rec.Code == 0 {
			t.Fatal("recorder status code is 0")
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

	// 2. NegotiateAccept weighted clauses, wildcards, graph forms, and invalid q values.
	f1, err := NegotiateAccept(2 /* QueryAsk */, "text/turtle;q=0.9, application/xml;q=0.8")
	if err != nil || f1 != FormatSPARQLXML {
		t.Fatalf("NegotiateAccept weighted ASK = (%v, %v), want FormatSPARQLXML", f1, err)
	}
	f2, err := NegotiateAccept(1 /* QuerySelect */, "text/*;q=0.4, */*;q=0.9")
	if err != nil || f2 != FormatSPARQLJSON {
		t.Fatalf("NegotiateAccept higher q wildcard = (%v, %v), want FormatSPARQLJSON", f2, err)
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

	// 4. Additional HTTP protocol validation branches (405 PUT, POST with URL query, missing/bad Content-Type, empty query).
	extraReqs := []struct {
		name       string
		method     string
		target     string
		ct         string
		body       string
		wantStatus int
	}{
		{name: "PUT method not allowed", method: http.MethodPut, target: "/sparql", wantStatus: http.StatusMethodNotAllowed},
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
	}

	// 5. Server constructor, ValidateConfig, Serve, and Shutdown edge cases.
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
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown before Serve = %v, want nil", err)
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

		// 406 Not Acceptable when Accept excludes application/ld+json.
		req406, _ := http.NewRequest(http.MethodGet, ts.URL+"/entities/"+idAlpha+".jsonld", nil)
		req406.Header.Set("Accept", MediaTypeTurtle)
		resp406, err := ts.Client().Do(req406)
		if err != nil {
			t.Fatalf("Do 406: %v", err)
		}
		resp406.Body.Close()
		if resp406.StatusCode != http.StatusNotAcceptable {
			t.Fatalf("entity JSON-LD with Accept=text/turtle status = %d, want 406", resp406.StatusCode)
		}

		// 405 Method Not Allowed on POST.
		req405, _ := http.NewRequest(http.MethodPost, ts.URL+"/entities/"+idAlpha+".jsonld", strings.NewReader("{}"))
		resp405, err := ts.Client().Do(req405)
		if err != nil {
			t.Fatalf("Do 405: %v", err)
		}
		resp405.Body.Close()
		if resp405.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("POST /entities/*.jsonld status = %d, want 405", resp405.StatusCode)
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

		// GET /export.ttl is not registered (404 Not Found), and GET /export returns 405 Method Not Allowed.
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
		if respGetExport.StatusCode != http.StatusMethodNotAllowed {
			t.Fatalf("GET /export status = %d, want 405", respGetExport.StatusCode)
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

	mr, err := reader.NewMemoryReader()
	if err != nil {
		f.Fatalf("NewMemoryReader: %v", err)
	}
	srv, err := New(mr, Config{BaseIRI: testBaseIRI})
	if err != nil {
		f.Fatalf("New: %v", err)
	}
	handler := srv.Handler()

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
		if rec.Code == 0 {
			t.Fatal("recorder status code is 0")
		}
	})
}
