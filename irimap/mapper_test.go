package irimap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/types"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
	"pgregory.net/rapid"
)

func TestIRIMapperRoundTrip(t *testing.T) {
	const customBase = "https://rdf.c360.example/"
	defaultMapper := NewMapper("")
	customMapper := NewMapper(customBase)
	var zeroMapper Mapper

	if got := defaultMapper.BaseIRI(); got != vocabulary.SemEngineBase {
		t.Fatalf("defaultMapper.BaseIRI() = %q, want %q", got, vocabulary.SemEngineBase)
	}
	if got := customMapper.BaseIRI(); got != "https://rdf.c360.example" {
		t.Fatalf("customMapper.BaseIRI() = %q, want %q", got, "https://rdf.c360.example")
	}
	if got := zeroMapper.BaseIRI(); got != vocabulary.SemEngineBase {
		t.Fatalf("zeroMapper.BaseIRI() = %q, want %q", got, vocabulary.SemEngineBase)
	}
	var nilMapper *Mapper
	if got := nilMapper.BaseIRI(); got != vocabulary.SemEngineBase {
		t.Fatalf("nilMapper.BaseIRI() = %q, want %q", got, vocabulary.SemEngineBase)
	}

	acceptIDs := []struct {
		name     string
		entityID string
		wantPath string
	}{
		{
			name:     "standard_six_part_id",
			entityID: "c360.ops.sem.intel.platform.uav1",
			wantPath: "/entities/c360/ops/sem/intel/platform/uav1",
		},
		{
			name:     "hyphens_and_underscores",
			entityID: "Org_1.plat-2.sys_3.dom-4.type_5.inst-6",
			wantPath: "/entities/Org_1/plat-2/sys_3/dom-4/type_5/inst-6",
		},
		{
			name:     "single_char_segments",
			entityID: "a.b.c.d.e.f",
			wantPath: "/entities/a/b/c/d/e/f",
		},
		{
			name:     "max_length_entity_id_256_bytes",
			entityID: "c360.ops.sem.intel.platform." + strings.Repeat("a", types.MaxEntityIDBytes-len("c360.ops.sem.intel.platform.")),
			wantPath: "/entities/c360/ops/sem/intel/platform/" + strings.Repeat("a", types.MaxEntityIDBytes-len("c360.ops.sem.intel.platform.")),
		},
	}

	for _, tc := range acceptIDs {
		t.Run("accept/"+tc.name, func(t *testing.T) {
			for _, m := range []*Mapper{defaultMapper, customMapper, &zeroMapper} {
				iri, err := m.EntityIDToIRI(tc.entityID)
				if err != nil {
					t.Fatalf("EntityIDToIRI(%q) under base %q failed: %v", tc.entityID, m.BaseIRI(), err)
				}
				wantIRI := m.BaseIRI() + tc.wantPath
				if iri != wantIRI {
					t.Fatalf("EntityIDToIRI(%q) = %q, want %q", tc.entityID, iri, wantIRI)
				}
				gotID, err := m.IRIToEntityID(iri)
				if err != nil {
					t.Fatalf("IRIToEntityID(%q) failed: %v", iri, err)
				}
				if gotID != tc.entityID {
					t.Fatalf("IRIToEntityID(%q) = %q, want %q", iri, gotID, tc.entityID)
				}
			}
		})
	}

	rejectEntityIDs := []struct {
		name     string
		entityID string
	}{
		{name: "empty", entityID: ""},
		{name: "five_segments", entityID: "c360.ops.sem.intel.platform"},
		{name: "seven_segments", entityID: "c360.ops.sem.intel.platform.uav1.extra"},
		{name: "leading_hyphen", entityID: "-c360.ops.sem.intel.platform.uav1"},
		{name: "leading_underscore", entityID: "_c360.ops.sem.intel.platform.uav1"},
		{name: "empty_segment", entityID: "c360..sem.intel.platform.uav1"},
		{name: "exceeds_max_entity_id_bytes_257", entityID: "c360.ops.sem.intel.platform." + strings.Repeat("a", types.MaxEntityIDBytes-len("c360.ops.sem.intel.platform.")+1)},
	}

	for _, tc := range rejectEntityIDs {
		t.Run("reject_entity_id/"+tc.name, func(t *testing.T) {
			if got, err := defaultMapper.EntityIDToIRI(tc.entityID); err == nil {
				t.Fatalf("EntityIDToIRI(%q) = %q, nil; want validation error", tc.entityID, got)
			}
		})
	}

	rejectIRIs := []struct {
		name string
		iri  string
	}{
		{
			name: "empty_iri",
			iri:  "",
		},
		{
			name: "subject_fallback_iri_with_six_parts",
			iri:  vocabulary.SemEngineBase + "/subjects/c360/ops/sem/intel/platform/uav1",
		},
		{
			name: "subject_fallback_iri_two_parts",
			iri:  vocabulary.SemEngineBase + "/subjects/drone/alpha",
		},
		{
			name: "foreign_base_iri",
			iri:  "https://foreign.example.org/entities/c360/ops/sem/intel/platform/uav1",
		},
		{
			name: "five_path_segments",
			iri:  vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform",
		},
		{
			name: "seven_path_segments",
			iri:  vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/uav1/extra",
		},
		{
			name: "trailing_slash",
			iri:  vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/uav1/",
		},
		{
			name: "dot_inside_segment",
			iri:  vocabulary.SemEngineBase + "/entities/c360.ops/sem/intel/platform/uav1/extra",
		},
		{
			name: "empty_middle_segment",
			iri:  vocabulary.SemEngineBase + "/entities/c360//sem/intel/platform/uav1",
		},
		{
			name: "invalid_segment_start_char",
			iri:  vocabulary.SemEngineBase + "/entities/-c360/ops/sem/intel/platform/uav1",
		},
		{
			name: "exceeds_max_entity_id_bytes_257",
			iri:  vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/" + strings.Repeat("a", types.MaxEntityIDBytes-len("c360.ops.sem.intel.platform.")+1),
		},
		{
			name: "query_string_appended",
			iri:  vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/uav1?x=1",
		},
		{
			name: "fragment_appended",
			iri:  vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/uav1#frag",
		},
	}

	for _, tc := range rejectIRIs {
		t.Run("reject_iri/"+tc.name, func(t *testing.T) {
			got, err := defaultMapper.IRIToEntityID(tc.iri)
			if err == nil {
				t.Fatalf("IRIToEntityID(%q) = %q, nil; want ErrNotEntityIRI", tc.iri, got)
			}
			if !errors.Is(err, ErrNotEntityIRI) {
				t.Fatalf("IRIToEntityID(%q) error = %v, want errors.Is(_, ErrNotEntityIRI)", tc.iri, err)
			}
		})
	}
}

func TestPropIRIMapperRoundTrip(t *testing.T) {
	segmentGen := rapid.StringMatching(`[a-zA-Z0-9][a-zA-Z0-9_-]{0,38}`)
	baseGen := rapid.SampledFrom([]string{
		"",
		vocabulary.SemEngineBase,
		vocabulary.SemEngineBase + "/",
		"https://kg.c360.io",
		"https://kg.c360.io/",
		"https://rdf.example.org/semsparql///",
	})

	rapid.Check(t, func(rt *rapid.T) {
		segs := make([]string, 6)
		for i := range segs {
			segs[i] = segmentGen.Draw(rt, "seg_"+strconv.Itoa(i))
		}
		entityID := strings.Join(segs, ".")
		base := baseGen.Draw(rt, "base")

		m := NewMapper(base)
		iri, err := m.EntityIDToIRI(entityID)
		if err != nil {
			rt.Fatalf("EntityIDToIRI(%q) failed: %v", entityID, err)
		}

		wantExporterIRI := export.SubjectIRI(entityID, base)
		if iri != wantExporterIRI {
			rt.Fatalf("EntityIDToIRI(%q) = %q, want exporter SubjectIRI %q", entityID, iri, wantExporterIRI)
		}

		gotID, err := m.IRIToEntityID(iri)
		if err != nil {
			rt.Fatalf("IRIToEntityID(%q) failed: %v", iri, err)
		}
		if gotID != entityID {
			rt.Fatalf("round-trip mismatch: got %q, want %q", gotID, entityID)
		}

		fallbackIRI := strings.Replace(iri, "/entities/", "/subjects/", 1)
		if _, err := m.IRIToEntityID(fallbackIRI); !errors.Is(err, ErrNotEntityIRI) {
			rt.Fatalf("IRIToEntityID(%q) err = %v, want ErrNotEntityIRI", fallbackIRI, err)
		}
	})
}

func TestIRIMapperMatchesExporter(t *testing.T) {
	const entityID = "org1.plat2.sys3.dom4.type5.inst6"
	bases := []struct {
		name     string
		input    string
		wantBase string
	}{
		{name: "empty_default", input: "", wantBase: vocabulary.SemEngineBase},
		{name: "slashes_only_default", input: "///", wantBase: vocabulary.SemEngineBase},
		{name: "explicit_default", input: vocabulary.SemEngineBase, wantBase: vocabulary.SemEngineBase},
		{name: "custom_without_slash", input: "https://kg.c360.io", wantBase: "https://kg.c360.io"},
		{name: "custom_with_trailing_slashes", input: "https://kg.c360.io///", wantBase: "https://kg.c360.io"},
	}

	for _, b := range bases {
		t.Run(b.name, func(t *testing.T) {
			m := NewMapper(b.input)
			if m.BaseIRI() != b.wantBase {
				t.Fatalf("BaseIRI() = %q, want %q", m.BaseIRI(), b.wantBase)
			}

			gotIRI, err := m.EntityIDToIRI(entityID)
			if err != nil {
				t.Fatalf("EntityIDToIRI(%q) failed: %v", entityID, err)
			}

			wantOrderIRI := b.wantBase + "/entities/org1/plat2/sys3/dom4/type5/inst6"
			if gotIRI != wantOrderIRI {
				t.Fatalf("EntityIDToIRI(%q) = %q, want positional order %q", entityID, gotIRI, wantOrderIRI)
			}

			exporterSubjectIRI := export.SubjectIRI(entityID, b.input)
			if gotIRI != exporterSubjectIRI {
				t.Fatalf("EntityIDToIRI(%q) = %q != export.SubjectIRI %q", entityID, gotIRI, exporterSubjectIRI)
			}

			ntLine, err := export.SerializeToString([]message.Triple{
				{
					Subject:   entityID,
					Predicate: "http://www.w3.org/1999/02/22-rdf-syntax-ns#type",
					Object:    "https://example.org/Class",
				},
			}, export.NTriples, export.WithBaseIRI(b.input))
			if err != nil {
				t.Fatalf("export.SerializeToString failed: %v", err)
			}
			wantPrefix := "<" + gotIRI + "> "
			if !strings.HasPrefix(ntLine, wantPrefix) {
				t.Fatalf("serialized N-Triples line %q does not start with %q", ntLine, wantPrefix)
			}
		})
	}
}

func TestBlankNodeIDMultiValued(t *testing.T) {
	const (
		entityID  = "c360.ops.sem.intel.platform.uav1"
		predicate = "intel.role.assigned"
		baseIRI   = "https://rdf.c360.example"
	)

	oracleBlankNodeID := func(entID, pred, canonObj string, step int) string {
		raw := entID + "\x00" + pred + "\x00" + canonObj + "\x00" + strconv.Itoa(step)
		sum := sha256.Sum256([]byte(raw))
		return "_:b_" + hex.EncodeToString(sum[:])[:32]
	}

	trISR := message.Triple{
		Subject:   entityID,
		Predicate: predicate,
		Object:    "ISR",
	}
	trStrike := message.Triple{
		Subject:   entityID,
		Predicate: predicate,
		Object:    "Strike",
	}

	canonISR, err := CanonicalNTriplesObject(trISR, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesObject(ISR) failed: %v", err)
	}
	if canonISR != `"ISR"` {
		t.Fatalf("CanonicalNTriplesObject(ISR) = %q, want %q", canonISR, `"ISR"`)
	}

	canonStrike, err := CanonicalNTriplesObject(trStrike, baseIRI)
	if err != nil {
		t.Fatalf("CanonicalNTriplesObject(Strike) failed: %v", err)
	}
	if canonStrike != `"Strike"` {
		t.Fatalf("CanonicalNTriplesObject(Strike) = %q, want %q", canonStrike, `"Strike"`)
	}

	for _, stepIdx := range []int{0, 1, 2, 10} {
		bISR1, err := BlankNodeID(entityID, predicate, canonISR, stepIdx)
		if err != nil {
			t.Fatalf("BlankNodeID(ISR, step=%d) failed: %v", stepIdx, err)
		}
		bISR2, err := BlankNodeID(entityID, predicate, canonISR, stepIdx)
		if err != nil {
			t.Fatalf("BlankNodeID(ISR repeat, step=%d) failed: %v", stepIdx, err)
		}
		if bISR1 != bISR2 {
			t.Fatalf("BlankNodeID not deterministic at step %d: %q != %q", stepIdx, bISR1, bISR2)
		}

		wantOracle := oracleBlankNodeID(entityID, predicate, canonISR, stepIdx)
		if bISR1 != wantOracle {
			t.Fatalf("BlankNodeID(ISR, step=%d) = %q, want oracle %q", stepIdx, bISR1, wantOracle)
		}

		bStrike, err := BlankNodeID(entityID, predicate, canonStrike, stepIdx)
		if err != nil {
			t.Fatalf("BlankNodeID(Strike, step=%d) failed: %v", stepIdx, err)
		}
		if bISR1 == bStrike {
			t.Fatalf("multi-valued collision at step %d: both values produced %q", stepIdx, bISR1)
		}
	}

	// Distinct step indices on the same value must not collide.
	bStep0, err := BlankNodeID(entityID, predicate, canonISR, 0)
	if err != nil {
		t.Fatalf("BlankNodeID step 0: %v", err)
	}
	bStep1, err := BlankNodeID(entityID, predicate, canonISR, 1)
	if err != nil {
		t.Fatalf("BlankNodeID step 1: %v", err)
	}
	if bStep0 == bStep1 {
		t.Fatalf("step 0 and step 1 collided: %q", bStep0)
	}

	// Verify CanonicalNTriplesObject agrees byte-for-byte with exporter output across
	// typed literals, numeric values, and entity-ID references.
	const targetEntityID = "c360.ops.sem.intel.unit.sqn1"
	termCases := []struct {
		name       string
		tr         message.Triple
		wantSuffix string
	}{
		{
			name: "float_literal",
			tr: message.Triple{
				Subject:   entityID,
				Predicate: "intel.measurement.altitude-m",
				Object:    1524.5,
			},
			wantSuffix: `"1524.5"^^<http://www.w3.org/2001/XMLSchema#double>`,
		},
		{
			name: "explicit_datatype_datetime",
			tr: message.Triple{
				Subject:   entityID,
				Predicate: "intel.observation.recorded-at",
				Object:    "2026-04-15T12:00:00Z",
				Datatype:  "http://www.w3.org/2001/XMLSchema#dateTime",
			},
			wantSuffix: `"2026-04-15T12:00:00Z"^^<http://www.w3.org/2001/XMLSchema#dateTime>`,
		},
		{
			name: "entity_reference",
			tr: message.Triple{
				Subject:   entityID,
				Predicate: "intel.platform.assigned-unit",
				Object:    targetEntityID,
			},
			wantSuffix: "<" + export.SubjectIRI(targetEntityID, baseIRI) + ">",
		},
	}

	for _, tc := range termCases {
		t.Run("canonical_term/"+tc.name, func(t *testing.T) {
			gotObj, err := CanonicalNTriplesObject(tc.tr, baseIRI)
			if err != nil {
				t.Fatalf("CanonicalNTriplesObject failed: %v", err)
			}
			if gotObj != tc.wantSuffix {
				t.Fatalf("CanonicalNTriplesObject = %q, want %q", gotObj, tc.wantSuffix)
			}
			fullNT, err := export.SerializeToString([]message.Triple{tc.tr}, export.NTriples, export.WithBaseIRI(baseIRI))
			if err != nil {
				t.Fatalf("export.SerializeToString failed: %v", err)
			}
			if !strings.HasSuffix(fullNT, " "+gotObj+" .\n") {
				t.Fatalf("exporter N-Triples %q does not end with canonical object %q", fullNT, gotObj)
			}
		})
	}

	// Verify input validation errors on BlankNodeID and CanonicalNTriplesObject.
	invalidBlankNodeCases := []struct {
		name     string
		entID    string
		pred     string
		canonObj string
		stepIdx  int
	}{
		{name: "invalid_entity_id", entID: "not-six-parts", pred: predicate, canonObj: `"ISR"`, stepIdx: 0},
		{name: "empty_predicate", entID: entityID, pred: "", canonObj: `"ISR"`, stepIdx: 0},
		{name: "nul_in_predicate", entID: entityID, pred: "intel\x00role", canonObj: `"ISR"`, stepIdx: 0},
		{name: "empty_canonical_object", entID: entityID, pred: predicate, canonObj: "", stepIdx: 0},
		{name: "nul_in_canonical_object", entID: entityID, pred: predicate, canonObj: "\"ISR\x00\"", stepIdx: 0},
		{name: "negative_step_index", entID: entityID, pred: predicate, canonObj: `"ISR"`, stepIdx: -1},
	}
	for _, tc := range invalidBlankNodeCases {
		t.Run("invalid_blank_node/"+tc.name, func(t *testing.T) {
			if got, err := BlankNodeID(tc.entID, tc.pred, tc.canonObj, tc.stepIdx); err == nil {
				t.Fatalf("BlankNodeID(%+v) = %q, nil; want error", tc, got)
			}
		})
	}

	if got, err := CanonicalNTriplesObject(message.Triple{Subject: entityID, Object: "v"}, baseIRI); err == nil {
		t.Fatalf("CanonicalNTriplesObject with empty predicate = %q, nil; want error", got)
	}
	if got, err := CanonicalNTriplesObject(message.Triple{Subject: entityID, Predicate: predicate, Object: nil}, baseIRI); err == nil {
		t.Fatalf("CanonicalNTriplesObject with nil object = %q, nil; want error", got)
	}
}

func FuzzIRIToEntityID(f *testing.F) {
	seeds := []struct {
		iri  string
		base string
	}{
		{vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/uav1", ""},
		{"https://rdf.c360.example/entities/c360/ops/sem/intel/platform/uav1", "https://rdf.c360.example/"},
		{vocabulary.SemEngineBase + "/subjects/c360/ops/sem/intel/platform/uav1", ""},
		{vocabulary.SemEngineBase + "/entities/c360.ops/sem/intel/platform/uav1/x", ""},
		{vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform", ""},
		{vocabulary.SemEngineBase + "/entities/c360/ops/sem/intel/platform/uav1/extra", ""},
		{vocabulary.SemEngineBase + "/entities/c360//sem/intel/platform/uav1", ""},
		{vocabulary.SemEngineBase + "/entities/-c360/ops/sem/intel/platform/uav1", ""},
		{"", ""},
		{"https://semengine.semanticstream.ing/entities/\x00/a/b/c/d/e", ""},
	}
	for _, s := range seeds {
		f.Add(s.iri, s.base)
	}

	f.Fuzz(func(t *testing.T, iri, base string) {
		m := NewMapper(base)
		id, err := m.IRIToEntityID(iri)
		if err != nil {
			if !errors.Is(err, ErrNotEntityIRI) {
				t.Fatalf("IRIToEntityID(%q) under base %q returned non-ErrNotEntityIRI error: %v", iri, base, err)
			}
			return
		}
		if err := types.ValidateEntityID(id); err != nil {
			t.Fatalf("IRIToEntityID(%q) returned invalid entity ID %q: %v", iri, id, err)
		}
		roundTripIRI, err := m.EntityIDToIRI(id)
		if err != nil {
			t.Fatalf("EntityIDToIRI(%q) failed after IRIToEntityID(%q) succeeded: %v", id, iri, err)
		}
		if roundTripIRI != iri {
			t.Fatalf("round-trip mismatch: EntityIDToIRI(%q) = %q, want %q", id, roundTripIRI, iri)
		}
		if expIRI := export.SubjectIRI(id, m.BaseIRI()); expIRI != iri {
			t.Fatalf("exporter mismatch: export.SubjectIRI(%q, %q) = %q, want %q", id, m.BaseIRI(), expIRI, iri)
		}
	})
}
