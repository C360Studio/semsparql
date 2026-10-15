package irimap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/c360studio/semengine/message"
	"github.com/c360studio/semengine/pkg/types"
	"github.com/c360studio/semengine/vocabulary"
	"github.com/c360studio/semengine/vocabulary/export"
)

// ErrNotEntityIRI indicates that an IRI does not lie under the mapper's
// canonical "{base}/entities/{org}/{platform}/{system}/{domain}/{type}/{instance}"
// namespace or does not decode to a valid six-part SemEngine entity ID.
var ErrNotEntityIRI = errors.New("irimap: IRI is not a canonical entity IRI")

// Mapper translates between six-part SemEngine entity IDs and canonical entity
// IRIs under a single shared base IRI, delegating forward construction to
// export.SubjectIRI and enforcing strict inverse validation.
type Mapper struct {
	baseIRI string
}

// NewMapper returns a Mapper configured with baseIRI. Trailing slashes are
// stripped, and an empty baseIRI defaults to vocabulary.SemEngineBase.
func NewMapper(baseIRI string) *Mapper {
	return &Mapper{
		baseIRI: normalizeBaseIRI(baseIRI),
	}
}

// BaseIRI returns the normalized base IRI shared by this mapper and RDF export
// calls. A nil or zero-value Mapper returns vocabulary.SemEngineBase.
func (m *Mapper) BaseIRI() string {
	if m == nil || m.baseIRI == "" {
		return vocabulary.SemEngineBase
	}
	return m.baseIRI
}

// EntityIDToIRI validates entityID as a canonical six-part SemEngine entity ID
// and delegates forward IRI construction to export.SubjectIRI.
func (m *Mapper) EntityIDToIRI(entityID string) (string, error) {
	if err := types.ValidateEntityID(entityID); err != nil {
		return "", fmt.Errorf("irimap: invalid entity ID %q: %w", entityID, err)
	}
	return export.SubjectIRI(entityID, m.BaseIRI()), nil
}

// IRIToEntityID inverts a canonical entity IRI under "{base}/entities/" back to
// its six-part SemEngine entity ID. Any IRI outside the configured canonical
// entity namespace (including "{base}/subjects/" fallback IRIs) or with invalid
// segment syntax is rejected with an error wrapping ErrNotEntityIRI.
func (m *Mapper) IRIToEntityID(iri string) (string, error) {
	base := m.BaseIRI()
	prefix := base + "/entities/"
	rest, ok := strings.CutPrefix(iri, prefix)
	if !ok {
		return "", fmt.Errorf("%w: %q is outside %q", ErrNotEntityIRI, iri, prefix)
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 6 {
		return "", fmt.Errorf("%w: expected 6 entity path segments in %q, got %d", ErrNotEntityIRI, iri, len(parts))
	}
	for _, part := range parts {
		if strings.ContainsRune(part, '.') {
			return "", fmt.Errorf("%w: segment %q in %q contains '.'", ErrNotEntityIRI, part, iri)
		}
	}
	candidate := strings.Join(parts, ".")
	eid, err := types.ParseEntityID(candidate)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNotEntityIRI, err)
	}
	return eid.Key(), nil
}

// CanonicalNTriplesObject extracts the exporter-owned canonical N-Triples
// object representation for tr under baseIRI by serializing a single probe
// triple through export.SerializeToString(..., export.NTriples).
func CanonicalNTriplesObject(tr message.Triple, baseIRI string) (string, error) {
	if strings.TrimSpace(tr.Predicate) == "" {
		return "", errors.New("irimap: empty predicate")
	}
	probe := message.Triple{
		Subject:   "_:s",
		Predicate: tr.Predicate,
		Object:    tr.Object,
		Datatype:  tr.Datatype,
	}
	out, err := export.SerializeToString([]message.Triple{probe}, export.NTriples, export.WithBaseIRI(normalizeBaseIRI(baseIRI)))
	if err != nil {
		return "", fmt.Errorf("irimap: serialize N-Triples object: %w", err)
	}
	if out == "" {
		return "", fmt.Errorf("irimap: exporter rejected triple object %#v (datatype %q)", tr.Object, tr.Datatype)
	}
	// Format is: _:s <predIRI> <object> .\n
	// predIRI is validated by the exporter to contain no '>' characters.
	idx := strings.Index(out, "> ")
	if idx < 0 || !strings.HasSuffix(out, " .\n") {
		return "", fmt.Errorf("irimap: unexpected N-Triples line %q", out)
	}
	return out[idx+2 : len(out)-3], nil
}

// BlankNodeID computes the deterministic intermediate blank-node identifier
// shared by BFO/CCO reification chain folding and unfolding:
//
//	_:b_<hex(sha256(entityID + "\x00" + predicate + "\x00" + canonicalObject + "\x00" + strconv.Itoa(stepIdx)))[:32]>
//
// stepIdx is a 0-based decimal ASCII index matching ProjectionSpec.Steps[stepIdx]
// (owner ruling D.1e).
func BlankNodeID(entityID, predicate, canonicalObject string, stepIdx int) (string, error) {
	if err := types.ValidateEntityID(entityID); err != nil {
		return "", fmt.Errorf("irimap: invalid entity ID %q: %w", entityID, err)
	}
	if predicate == "" {
		return "", errors.New("irimap: empty predicate")
	}
	if strings.ContainsRune(predicate, '\x00') {
		return "", errors.New("irimap: predicate contains NUL byte")
	}
	if canonicalObject == "" {
		return "", errors.New("irimap: empty canonical object")
	}
	if strings.ContainsRune(canonicalObject, '\x00') {
		return "", errors.New("irimap: canonical object contains NUL byte")
	}
	if stepIdx < 0 {
		return "", fmt.Errorf("irimap: negative step index %d", stepIdx)
	}
	payload := entityID + "\x00" + predicate + "\x00" + canonicalObject + "\x00" + strconv.Itoa(stepIdx)
	sum := sha256.Sum256([]byte(payload))
	return "_:b_" + hex.EncodeToString(sum[:16]), nil
}

func normalizeBaseIRI(base string) string {
	base = strings.TrimRight(base, "/")
	if base == "" {
		return vocabulary.SemEngineBase
	}
	return base
}
