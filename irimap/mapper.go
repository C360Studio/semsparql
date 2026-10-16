package irimap

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
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

// ErrTripleRejectedByExporter indicates that semengine/vocabulary/export
// produced an empty N-Triples output for a probe triple (for example on NaN/Inf floats).
var ErrTripleRejectedByExporter = errors.New("irimap: exporter rejected triple object")

// ValidateBaseIRI checks that baseIRI is either empty (defaulting to
// vocabulary.SemEngineBase) or an absolute http(s):// IRI with a non-empty host,
// no userinfo/query/fragment, and no N-Triples unsafe characters.
func ValidateBaseIRI(baseIRI string) error {
	if baseIRI == "" {
		return nil
	}
	trimmed := strings.TrimRight(baseIRI, "/")
	if trimmed == "" {
		return fmt.Errorf("irimap: invalid base IRI %q: empty after stripping trailing slashes", baseIRI)
	}
	if err := validateSafeHTTPIRI(trimmed); err != nil {
		return fmt.Errorf("irimap: invalid base IRI %q: %w", baseIRI, err)
	}
	return nil
}

func validateSafeHTTPIRI(raw string) error {
	for _, r := range raw {
		if r <= 0x20 || r == 0x7f || strings.ContainsRune(`<>"{}|^`+"`"+`\`, r) {
			return fmt.Errorf("contains disallowed N-Triples IRI character %q", r)
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("scheme must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("host must not be empty")
	}
	if u.User != nil {
		return errors.New("userinfo is not permitted")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("query string and fragment are not permitted")
	}
	return nil
}

func validatePredicateForExport(pred string) error {
	if strings.TrimSpace(pred) == "" {
		return errors.New("irimap: empty predicate")
	}
	if strings.Contains(pred, "://") || strings.HasPrefix(pred, "urn:") {
		for _, r := range pred {
			if r <= 0x20 || r == 0x7f || strings.ContainsRune(`<>"{}|^`+"`"+`\`, r) {
				return fmt.Errorf("irimap: invalid predicate IRI %q: contains disallowed character %q", pred, r)
			}
		}
		if strings.HasPrefix(pred, "urn:") {
			if len(pred) <= len("urn:") {
				return fmt.Errorf("irimap: invalid predicate IRI %q", pred)
			}
			return nil
		}
		u, err := url.Parse(pred)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("irimap: invalid predicate IRI %q", pred)
		}
		return nil
	}
	if !vocabulary.IsValidPredicate(pred) {
		return fmt.Errorf("irimap: invalid three-part predicate %q", pred)
	}
	return nil
}

// normalizeSourceTripleDatatype maps explicit "iri" / "@id" or prefixed/full
// XSD/RDF datatype declarations onto tr.Datatype while leaving SemEngine's
// pragmatic DataType constants ("float", "int", "bool", "string", "datetime",
// "json", "entity_id") untouched so vocabulary/export's classifyByDeclaredDataType
// handles them without emitting relative datatype IRIs.
func normalizeSourceTripleDatatype(tr message.Triple) message.Triple {
	if tr.Datatype != "" {
		if tr.Datatype == "iri" {
			tr.Datatype = message.EntityReferenceDatatype
		}
		return tr
	}
	meta := vocabulary.GetPredicateMetadata(tr.Predicate)
	if meta == nil || meta.DataType == "" {
		return tr
	}
	switch {
	case meta.DataType == "iri" || meta.DataType == message.EntityReferenceDatatype:
		tr.Datatype = message.EntityReferenceDatatype
	case strings.HasPrefix(meta.DataType, "xsd:") ||
		strings.HasPrefix(meta.DataType, "rdf:") ||
		strings.HasPrefix(meta.DataType, "http://") ||
		strings.HasPrefix(meta.DataType, "https://"):
		tr.Datatype = meta.DataType
	}
	return tr
}

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

// CanonicalNTriplesPredicateAndObject extracts the exporter-owned canonical
// predicate IRI and N-Triples object representation for tr under baseIRI by
// normalizing tr's datatype metadata and serializing a single probe triple
// through export.SerializeToString(..., export.NTriples).
func CanonicalNTriplesPredicateAndObject(tr message.Triple, baseIRI string) (string, string, error) {
	if err := ValidateBaseIRI(baseIRI); err != nil {
		return "", "", err
	}
	if err := validatePredicateForExport(tr.Predicate); err != nil {
		return "", "", err
	}
	if tr.Object == nil {
		return "", "", fmt.Errorf("irimap: nil triple object for predicate %q", tr.Predicate)
	}
	normTr := normalizeSourceTripleDatatype(tr)
	probe := message.Triple{
		Subject:   "_:s",
		Predicate: normTr.Predicate,
		Object:    normTr.Object,
		Datatype:  normTr.Datatype,
	}
	out, err := export.SerializeToString([]message.Triple{probe}, export.NTriples, export.WithBaseIRI(normalizeBaseIRI(baseIRI)))
	if err != nil {
		return "", "", fmt.Errorf("irimap: serialize N-Triples object: %w", err)
	}
	if out == "" {
		return "", "", fmt.Errorf("%w: %#v (datatype %q)", ErrTripleRejectedByExporter, tr.Object, tr.Datatype)
	}
	const prefix = "_:s <"
	idx := strings.Index(out, "> ")
	if !strings.HasPrefix(out, prefix) || idx < len(prefix) || !strings.HasSuffix(out, " .\n") {
		return "", "", fmt.Errorf("irimap: unexpected N-Triples line %q", out)
	}
	predIRI := out[len(prefix):idx]
	canonObj := out[idx+2 : len(out)-3]
	return predIRI, canonObj, nil
}

// CanonicalNTriplesObject extracts the exporter-owned canonical N-Triples
// object representation for tr under baseIRI by serializing a single probe
// triple through export.SerializeToString(..., export.NTriples).
func CanonicalNTriplesObject(tr message.Triple, baseIRI string) (string, error) {
	_, canonObj, err := CanonicalNTriplesPredicateAndObject(tr, baseIRI)
	return canonObj, err
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
