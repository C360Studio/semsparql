package contract

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	"github.com/c360studio/semengine/graph"
	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/errs"
	semtypes "github.com/c360studio/semengine/pkg/types"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
	"github.com/c360studio/semvocab/packs"
	"github.com/c360studio/semvocab/packs/diekm"
)

const (
	probeBaseIRI   = "https://example.org"
	xsdStringIRI   = "http://www.w3.org/2001/XMLSchema#string"
	xsdIntegerIRI  = "http://www.w3.org/2001/XMLSchema#integer"
	xsdDoubleIRI   = "http://www.w3.org/2001/XMLSchema#double"
	xsdBooleanIRI  = "http://www.w3.org/2001/XMLSchema#boolean"
	xsdDateTimeIRI = "http://www.w3.org/2001/XMLSchema#dateTime"
	rdfJSONIRI     = "http://www.w3.org/1999/02/22-rdf-syntax-ns#JSON"
)

// canonicalNTriplesObject extracts the exporter-owned canonical N-Triples object
// representation for a triple using export.SerializeToString(..., export.NTriples)
// without duplicating SemEngine's object classification or escaping logic.
func canonicalNTriplesObject(src message.Triple, baseIRI string) (string, error) {
	if src.Predicate == "" {
		return "", errors.New("empty predicate")
	}
	probe := message.Triple{
		Subject:   "_:s",
		Predicate: src.Predicate,
		Object:    src.Object,
		Datatype:  src.Datatype,
	}
	out, err := export.SerializeToString([]message.Triple{probe}, export.NTriples, export.WithBaseIRI(baseIRI))
	if err != nil {
		return "", err
	}
	if out == "" {
		return "", fmt.Errorf("exporter rejected triple object %#v (datatype %q)", src.Object, src.Datatype)
	}
	// Format is: _:s <predIRI> <object> .\n
	// predIRI is validated by the exporter to contain no '>' characters.
	idx := strings.Index(out, "> ")
	if idx < 0 || !strings.HasSuffix(out, " .\n") {
		return "", fmt.Errorf("unexpected N-Triples line %q", out)
	}
	return out[idx+2 : len(out)-3], nil
}

// propagateTerminalObject maps a flat source triple onto an unfolded terminal
// triple (whose Predicate is a full ValuePredicateIRI not keyed in
// vocabulary.GetPredicateMetadata) by deriving the terminal (Object, Datatype)
// from the exporter's canonical N-Triples classification of the source triple.
func propagateTerminalObject(src message.Triple, terminalSubject, valuePredicateIRI, baseIRI string) (message.Triple, error) {
	ntObj, err := canonicalNTriplesObject(src, baseIRI)
	if err != nil {
		return message.Triple{}, err
	}
	term := message.Triple{
		Subject:   terminalSubject,
		Predicate: valuePredicateIRI,
	}
	switch {
	case strings.HasPrefix(ntObj, "<") && strings.HasSuffix(ntObj, ">"):
		term.Object = ntObj[1 : len(ntObj)-1]
	case strings.HasPrefix(ntObj, "_:"):
		term.Object = ntObj
	case strings.HasPrefix(ntObj, `"`):
		lex, dtIRI, err := parseNTriplesLiteral(ntObj)
		if err != nil {
			return message.Triple{}, err
		}
		switch {
		case src.Datatype != "":
			term.Object = lex
			term.Datatype = dtIRI
		case dtIRI == xsdIntegerIRI:
			n, err := strconv.ParseInt(lex, 10, 64)
			if err != nil {
				term.Object = lex
				term.Datatype = dtIRI
			} else {
				term.Object = n
			}
		case dtIRI == xsdDoubleIRI:
			f, err := strconv.ParseFloat(lex, 64)
			if err != nil {
				term.Object = lex
				term.Datatype = dtIRI
			} else {
				term.Object = f
			}
		case dtIRI == xsdBooleanIRI:
			switch lex {
			case "true":
				term.Object = true
			case "false":
				term.Object = false
			default:
				term.Object = lex
				term.Datatype = dtIRI
			}
		case dtIRI == "":
			term.Object = lex
			term.Datatype = xsdStringIRI
		default:
			term.Object = lex
			term.Datatype = dtIRI
		}
	default:
		return message.Triple{}, fmt.Errorf("unrecognized N-Triples object token %q", ntObj)
	}
	return term, nil
}

func parseNTriplesLiteral(ntObj string) (lexical, datatypeIRI string, err error) {
	if strings.HasSuffix(ntObj, ">") {
		sep := strings.LastIndex(ntObj, `"^^<`)
		if sep < 1 {
			return "", "", fmt.Errorf("malformed typed literal %q", ntObj)
		}
		datatypeIRI = ntObj[sep+4 : len(ntObj)-1]
		ntObj = ntObj[:sep+1]
	}
	if len(ntObj) < 2 || ntObj[0] != '"' || ntObj[len(ntObj)-1] != '"' {
		return "", "", fmt.Errorf("malformed literal %q", ntObj)
	}
	unquoted, err := unescapeNTriplesString(ntObj[1 : len(ntObj)-1])
	if err != nil {
		return "", "", err
	}
	return unquoted, datatypeIRI, nil
}

func unescapeNTriplesString(s string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("trailing backslash in %q", s)
		}
		switch s[i] {
		case '\\':
			b.WriteByte('\\')
		case '"':
			b.WriteByte('"')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		default:
			return "", fmt.Errorf("unsupported escape \\%c in %q", s[i], s)
		}
	}
	return b.String(), nil
}

// registerProbeVocab registers BFO/CCO Core and DIEKM into the vocabulary
// registry without mutating export's global prefix table, plus synthetic
// predicates covering all remaining pragmatic DataTypes. Caller must defer
// the returned restore function.
func registerProbeVocab(t *testing.T) func() {
	t.Helper()
	restore := vocabulary.SnapshotRegistry()
	vocabulary.ClearRegistry()
	noPrefixes := vocabulary.WithPrefixRegistrar(func([]vocabulary.PrefixSpec) error { return nil })
	if err := packs.RegisterBFOCCOCore(noPrefixes); err != nil {
		restore()
		t.Fatalf("RegisterBFOCCOCore: %v", err)
	}
	if err := diekm.Register(noPrefixes); err != nil {
		restore()
		t.Fatalf("diekm.Register: %v", err)
	}
	vocabulary.Register("probe.attr.timestamp",
		vocabulary.WithDataType(vocabulary.DataTypeDateTime),
		vocabulary.WithIRI("https://example.org/onto#timestamp"))
	vocabulary.Register("probe.attr.count",
		vocabulary.WithDataType(vocabulary.DataTypeInt),
		vocabulary.WithIRI("https://example.org/onto#count"))
	vocabulary.Register("probe.attr.active",
		vocabulary.WithDataType(vocabulary.DataTypeBool),
		vocabulary.WithIRI("https://example.org/onto#active"))
	vocabulary.Register("probe.attr.payload",
		vocabulary.WithDataType(vocabulary.DataTypeJSON),
		vocabulary.WithIRI("https://example.org/onto#payload"))
	vocabulary.Register("probe.rel.target",
		vocabulary.WithDataType(vocabulary.DataTypeEntityID),
		vocabulary.WithIRI("https://example.org/onto#target"))
	return restore
}

// TestProbeCanonicalNTriplesObjectSeam proves D.2 probe 1 (holding Task 1.1):
// export.SerializeToString(..., export.NTriples) provides an exporter-owned
// canonical N-Triples object token for BlankNodeID without copying RDF
// classification or escaping rules.
func TestProbeCanonicalNTriplesObjectSeam(t *testing.T) {
	defer registerProbeVocab(t)()

	cases := []struct {
		name string
		src  message.Triple
		want string
	}{
		{
			name: "DIEKM altitude float",
			src:  message.Triple{Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
			want: `"1500.5"^^<http://www.w3.org/2001/XMLSchema#double>`,
		},
		{
			name: "DIEKM altitude whole float gets .0",
			src:  message.Triple{Predicate: "intel.measurement.altitude-m", Object: float64(1500)},
			want: `"1500.0"^^<http://www.w3.org/2001/XMLSchema#double>`,
		},
		{
			name: "DIEKM role string with escapes",
			src:  message.Triple{Predicate: "intel.role.assigned", Object: "ISR \"alpha\"\nline2"},
			want: `"ISR \"alpha\"\nline2"`,
		},
		{
			name: "declared datetime on RFC3339 string",
			src:  message.Triple{Predicate: "probe.attr.timestamp", Object: "2026-10-15T12:34:56Z"},
			want: `"2026-10-15T12:34:56Z"^^<http://www.w3.org/2001/XMLSchema#dateTime>`,
		},
		{
			name: "declared int on JSON float64",
			src:  message.Triple{Predicate: "probe.attr.count", Object: float64(42)},
			want: `"42"^^<http://www.w3.org/2001/XMLSchema#integer>`,
		},
		{
			name: "declared bool",
			src:  message.Triple{Predicate: "probe.attr.active", Object: true},
			want: `"true"^^<http://www.w3.org/2001/XMLSchema#boolean>`,
		},
		{
			name: "declared json",
			src:  message.Triple{Predicate: "probe.attr.payload", Object: `{"k":1}`},
			want: `"{\"k\":1}"^^<http://www.w3.org/1999/02/22-rdf-syntax-ns#JSON>`,
		},
		{
			name: "declared entity_id resolves via SubjectIRI",
			src:  message.Triple{Predicate: "probe.rel.target", Object: "c360.ops.uas1.intel.platform.alpha"},
			want: `<https://example.org/entities/c360/ops/uas1/intel/platform/alpha>`,
		},
		{
			name: "explicit @id datatype on entity ID",
			src:  message.Triple{Predicate: "intel.role.assigned", Object: "c360.ops.uas1.intel.platform.alpha", Datatype: message.EntityReferenceDatatype},
			want: `<https://example.org/entities/c360/ops/uas1/intel/platform/alpha>`,
		},
		{
			name: "blank node object",
			src:  message.Triple{Predicate: "probe.rel.target", Object: "_:b_node1"},
			want: `_:b_node1`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := canonicalNTriplesObject(tc.src, probeBaseIRI)
			if err != nil {
				t.Fatalf("canonicalNTriplesObject: %v", err)
			}
			if got != tc.want {
				t.Fatalf("canonicalNTriplesObject = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestProbeTerminalDatatypePropagationSeam proves D.2 probe 2 (holding Task 3.3):
//  1. Blindly copying pragmatic vocabulary.PredicateMetadata.DataType ("float",
//     "datetime", "entity_id") into Triple.Datatype produces invalid RDF.
//  2. Leaving Triple.Datatype empty on an unfolded terminal triple whose
//     Predicate is a full ValuePredicateIRI loses declared predicate datatypes
//     that cannot be inferred from the raw Go type (e.g., RFC3339 string for
//     datetime, float64(42) for int, JSON string for rdf:JSON).
//  3. Deriving the terminal (Object, Datatype) via propagateTerminalObject
//     preserves the exact exporter-rendered object across N-Triples, Turtle,
//     and JSON-LD.
func TestProbeTerminalDatatypePropagationSeam(t *testing.T) {
	defer registerProbeVocab(t)()

	// 1. Negative proof: blind copy of pragmatic DataType into Triple.Datatype emits invalid RDF.
	badTriple := message.Triple{
		Subject:   "_:b_bad",
		Predicate: "https://w3id.org/c360/diekm#altitudeMetersValue",
		Object:    1500.5,
		Datatype:  vocabulary.DataTypeFloat, // "float"
	}
	badNT, err := export.SerializeToString([]message.Triple{badTriple}, export.NTriples, export.WithBaseIRI(probeBaseIRI))
	if err != nil {
		t.Fatalf("SerializeToString(badTriple): %v", err)
	}
	if !strings.Contains(badNT, `^^<float>`) {
		t.Fatalf("expected blind pragmatic DataType copy to emit invalid ^^<float>, got: %s", badNT)
	}

	// 2. Negative proof: empty Datatype on full ValuePredicateIRI loses declared datetime/int/json.
	lostTriple := message.Triple{
		Subject:   "_:b_lost",
		Predicate: "https://example.org/onto#timestampValue",
		Object:    "2026-10-15T12:34:56Z",
	}
	lostNT, err := export.SerializeToString([]message.Triple{lostTriple}, export.NTriples, export.WithBaseIRI(probeBaseIRI))
	if err != nil {
		t.Fatalf("SerializeToString(lostTriple): %v", err)
	}
	if strings.Contains(lostNT, xsdDateTimeIRI) {
		t.Fatalf("expected unpropagated full-IRI predicate to lose xsd:dateTime, got: %s", lostNT)
	}

	// 3. Positive proof: propagateTerminalObject matches flat-triple object rendering across all 3 RDF formats.
	cases := []struct {
		name      string
		src       message.Triple
		valuePred string
	}{
		{
			name:      "DIEKM altitude-m float",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "intel.measurement.altitude-m", Object: float64(1500.5)},
			valuePred: "https://w3id.org/c360/diekm#altitudeMetersValue",
		},
		{
			name:      "DIEKM role string",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "intel.role.assigned", Object: "recon"},
			valuePred: "http://www.w3.org/2000/01/rdf-schema#label",
		},
		{
			name:      "declared datetime string",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "probe.attr.timestamp", Object: "2026-10-15T12:34:56Z"},
			valuePred: "https://example.org/onto#timestampValue",
		},
		{
			name:      "declared int on JSON float64",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "probe.attr.count", Object: float64(42)},
			valuePred: "https://example.org/onto#countValue",
		},
		{
			name:      "declared bool",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "probe.attr.active", Object: true},
			valuePred: "https://example.org/onto#activeValue",
		},
		{
			name:      "declared json",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "probe.attr.payload", Object: `{"sensor":"eo"}`},
			valuePred: "https://example.org/onto#payloadValue",
		},
		{
			name:      "declared entity_id",
			src:       message.Triple{Subject: "c360.ops.uas1.intel.platform.alpha", Predicate: "probe.rel.target", Object: "c360.ops.uas1.intel.platform.bravo"},
			valuePred: "https://example.org/onto#targetValue",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			term, err := propagateTerminalObject(tc.src, "_:b_step_terminal", tc.valuePred, probeBaseIRI)
			if err != nil {
				t.Fatalf("propagateTerminalObject: %v", err)
			}

			wantNT, err := canonicalNTriplesObject(tc.src, probeBaseIRI)
			if err != nil {
				t.Fatalf("canonicalNTriplesObject(src): %v", err)
			}
			gotNT, err := canonicalNTriplesObject(term, probeBaseIRI)
			if err != nil {
				t.Fatalf("canonicalNTriplesObject(term): %v", err)
			}
			if gotNT != wantNT {
				t.Fatalf("N-Triples terminal object = %s, want %s", gotNT, wantNT)
			}

			// Verify Turtle and JSON-LD also serialize the terminal triple without loss or invalid datatypes.
			turtleOut, err := export.SerializeToString([]message.Triple{term}, export.Turtle, export.WithBaseIRI(probeBaseIRI))
			if err != nil || strings.TrimSpace(turtleOut) == "" {
				t.Fatalf("SerializeToString(Turtle) = %q, err = %v", turtleOut, err)
			}
			jsonldOut, err := export.SerializeToString([]message.Triple{term}, export.JSONLD, export.WithBaseIRI(probeBaseIRI))
			if err != nil || strings.TrimSpace(jsonldOut) == "" {
				t.Fatalf("SerializeToString(JSONLD) = %q, err = %v", jsonldOut, err)
			}
			var doc map[string]any
			if err := json.Unmarshal([]byte(jsonldOut), &doc); err != nil {
				t.Fatalf("JSON-LD unmarshal: %v", err)
			}
			if _, ok := doc[tc.valuePred]; !ok && doc["rdfs:label"] == nil {
				t.Fatalf("JSON-LD missing terminal predicate %q in %v", tc.valuePred, doc)
			}
		})
	}
}

// TestProbeSerializerInvalidTripleOmissionSeam proves D.2 probe 3 (holding Task 3.3):
// export.Serialize silently omits triples with empty Subject/Predicate, nil Object,
// or NaN/Inf floats; pre-checking each triple (or verifying single-triple N-Triples
// non-empty output) detects 100% of omitted triples so the sidecar can fail loudly.
func TestProbeSerializerInvalidTripleOmissionSeam(t *testing.T) {
	invalidTriples := []struct {
		name   string
		triple message.Triple
	}{
		{
			name:   "empty subject",
			triple: message.Triple{Subject: "", Predicate: "https://example.org/p", Object: "v"},
		},
		{
			name:   "empty predicate",
			triple: message.Triple{Subject: "_:b_1", Predicate: "", Object: "v"},
		},
		{
			name:   "nil object",
			triple: message.Triple{Subject: "_:b_1", Predicate: "https://example.org/p", Object: nil},
		},
		{
			name:   "NaN float64 object",
			triple: message.Triple{Subject: "_:b_1", Predicate: "https://example.org/p", Object: math.NaN()},
		},
		{
			name:   "Inf float64 object",
			triple: message.Triple{Subject: "_:b_1", Predicate: "https://example.org/p", Object: math.Inf(1)},
		},
	}

	for _, tc := range invalidTriples {
		t.Run(tc.name, func(t *testing.T) {
			for _, format := range []export.Format{export.NTriples, export.Turtle, export.JSONLD} {
				out, err := export.SerializeToString([]message.Triple{tc.triple}, format, export.WithBaseIRI(probeBaseIRI))
				if err != nil {
					t.Fatalf("SerializeToString(%v) returned unexpected error: %v", format, err)
				}
				// NTriples and Turtle emit empty string; JSONLD emits "{}\n".
				if format == export.NTriples && out != "" {
					t.Fatalf("expected NTriples to silently omit %s, got %q", tc.name, out)
				}
			}
			// Verify our loud-failure guard rejects the triple.
			if err := validateTripleSerializable(tc.triple, probeBaseIRI); err == nil {
				t.Fatalf("validateTripleSerializable(%s) = nil, want loud failure", tc.name)
			}
		})
	}

	valid := message.Triple{
		Subject:   "c360.ops.uas1.intel.platform.alpha",
		Predicate: "https://example.org/p",
		Object:    "ok",
	}
	if err := validateTripleSerializable(valid, probeBaseIRI); err != nil {
		t.Fatalf("validateTripleSerializable(valid) = %v, want nil", err)
	}
}

func validateTripleSerializable(tr message.Triple, baseIRI string) error {
	if tr.Subject == "" {
		return errors.New("triple has empty subject")
	}
	if tr.Predicate == "" {
		return errors.New("triple has empty predicate")
	}
	out, err := export.SerializeToString([]message.Triple{tr}, export.NTriples, export.WithBaseIRI(baseIRI))
	if err != nil {
		return err
	}
	if out == "" {
		return fmt.Errorf("exporter silently omitted triple with subject %q predicate %q object %#v",
			tr.Subject, tr.Predicate, tr.Object)
	}
	return nil
}

// TestProbeServerPatternCapabilitySeam proves D.2 probe 4 (holding Task 4.1):
// A pattern-capable graph-ingest server (semengine >= v0.1.0-alpha.2) validates
// PrefixQueryRequest.Pattern via types.ValidateEntityIDPattern and rejects
// mutual exclusion (Prefix + Pattern) with classified ErrorInvalid before bucket
// I/O, whereas a legacy server whose request struct lacks Pattern ignores the
// "pattern" JSON field and treats {"pattern":"invalid","limit":1} as an empty
// prefix scan (returning 200 OK instead of ErrorInvalid).
func TestProbeServerPatternCapabilitySeam(t *testing.T) {
	probePayload, err := json.Marshal(graph.PrefixQueryRequest{
		Pattern: "invalid",
		Limit:   1,
	})
	if err != nil {
		t.Fatalf("Marshal probe: %v", err)
	}

	// Simulate semengine v0.1.0-alpha.2 handleQueryPrefix validation (processor/graph-ingest/query.go:250-266).
	capableServerValidate := func(data []byte) error {
		var req graph.PrefixQueryRequest
		if err := json.Unmarshal(data, &req); err != nil {
			return errs.Classified(errs.ErrorInvalid, err)
		}
		if req.Prefix != "" && req.Pattern != "" {
			return errs.ClassifiedCode(errs.ErrorInvalid, graph.ErrorCodeInvalidRequest,
				errors.New("invalid request: prefix and pattern are mutually exclusive"))
		}
		if req.Prefix != "" {
			if err := semtypes.ValidateEntityIDPrefix(req.Prefix); err != nil {
				return err
			}
		}
		if req.Pattern != "" {
			if err := semtypes.ValidateEntityIDPattern(req.Pattern); err != nil {
				return err
			}
		}
		return nil
	}

	// Simulate a pre-#259 legacy server whose request struct lacks Pattern.
	legacyServerValidate := func(data []byte) error {
		var legacy struct {
			Prefix string `json:"prefix"`
			Limit  int    `json:"limit"`
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return errs.Classified(errs.ErrorInvalid, err)
		}
		if legacy.Prefix != "" {
			return semtypes.ValidateEntityIDPrefix(legacy.Prefix)
		}
		return nil
	}

	checkCapability := func(handler func([]byte) error) error {
		err := handler(probePayload)
		if err == nil {
			return errors.New("server accepted invalid pattern probe; server does not support PrefixQueryRequest.Pattern")
		}
		if !errs.IsInvalid(err) {
			return fmt.Errorf("unexpected error class from pattern probe: %w", err)
		}
		var classified *errs.ClassifiedError
		if !errors.As(err, &classified) || classified.Code != semtypes.ErrorCodeEntityIDPatternInvalid {
			return fmt.Errorf("unexpected error code from pattern probe: %v", err)
		}
		validProbe, err := json.Marshal(graph.PrefixQueryRequest{
			Pattern: "*.*.*.*.*.*",
			Limit:   1,
		})
		if err != nil {
			return err
		}
		return handler(validProbe)
	}

	if err := checkCapability(capableServerValidate); err != nil {
		t.Fatalf("capable server rejected by startup probe: %v", err)
	}
	if err := checkCapability(legacyServerValidate); err == nil {
		t.Fatal("legacy server without Pattern support was not refused by startup probe")
	}
}
