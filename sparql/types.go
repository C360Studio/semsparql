package sparql

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/c360studio/semengine/pkg/errs"
	"github.com/c360studio/semengine/pkg/types"
	"github.com/c360studio/semengine/vocabulary"
)

// ErrorCodeIncompleteSuperclassClosure is the classified ADR-060 error code
// returned by ValidateSuperclassClosure when a registered entity type omits an
// ancestor of one of its registered superclasses.
const ErrorCodeIncompleteSuperclassClosure = "incomplete_superclass_closure"

// ErrIncompleteSuperclassClosure is the classified ADR-060 sentinel returned
// when startup ancestor-closure validation fails.
var ErrIncompleteSuperclassClosure error = &errs.ClassifiedError{
	Class:   errs.ErrorInvalid,
	Code:    ErrorCodeIncompleteSuperclassClosure,
	Message: "sparql: incomplete superclass closure",
}

// ValidateSuperclassClosure verifies that every registered entity type in
// vocabulary.ListRegisteredEntityTypes() includes the complete ancestor closure
// of any superclass that is itself a registered ClassIRI. If a child entity
// type lists superclass S, and S is registered as a ClassIRI with superclass A
// absent from the child's Superclasses, ValidateSuperclassClosure returns an
// error matching ErrIncompleteSuperclassClosure naming the child, S, and A.
func ValidateSuperclassClosure() error {
	domainTypes := vocabulary.ListRegisteredEntityTypes()
	if len(domainTypes) == 0 {
		return nil
	}

	metas := make([]*vocabulary.EntityTypeMetadata, 0, len(domainTypes))
	directSupersByClass := make(map[string][]string, len(domainTypes))

	for _, dt := range domainTypes {
		meta := vocabulary.GetEntityTypeMetadata(dt)
		if meta == nil {
			continue
		}
		metas = append(metas, meta)
		if meta.ClassIRI == "" {
			continue
		}
		existing := directSupersByClass[meta.ClassIRI]
		for _, sup := range meta.Superclasses {
			if sup != "" && !slices.Contains(existing, sup) {
				existing = append(existing, sup)
			}
		}
		directSupersByClass[meta.ClassIRI] = existing
	}

	for _, child := range metas {
		for _, sup := range child.Superclasses {
			if sup == "" {
				continue
			}
			ancestors := transitiveSuperclasses(sup, directSupersByClass)
			for _, anc := range ancestors {
				if anc == "" || anc == child.ClassIRI {
					continue
				}
				if !slices.Contains(child.Superclasses, anc) {
					return errs.ClassifiedCode(
						errs.ErrorInvalid,
						ErrorCodeIncompleteSuperclassClosure,
						fmt.Errorf(
							"sparql: entity type %q (class %q) declares superclass %q which requires missing ancestor %q in Superclasses",
							child.DomainType,
							child.ClassIRI,
							sup,
							anc,
						),
					)
				}
			}
		}
	}

	return nil
}

func transitiveSuperclasses(startClassIRI string, direct map[string][]string) []string {
	var out []string
	visited := map[string]bool{startClassIRI: true}
	queue := slices.Clone(direct[startClassIRI])
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		if curr == "" || visited[curr] {
			continue
		}
		visited[curr] = true
		out = append(out, curr)
		for _, next := range direct[curr] {
			if !visited[next] {
				queue = append(queue, next)
			}
		}
	}
	return out
}

// entityTypePattern formats and validates the six-part ADR-102 ScanPattern
// selector `<org>.<platform>.*.<domain>.<type>.*` for a `<domain>.<type>` key.
// Empty org or platform arguments become `"*"` wildcards.
func entityTypePattern(domainType, org, platform string) (string, error) {
	domain, entityType, err := vocabulary.ParseEntityTypeKey(domainType)
	if err != nil {
		return "", err
	}
	orgSeg := strings.TrimSpace(org)
	if orgSeg == "" {
		orgSeg = "*"
	}
	platSeg := strings.TrimSpace(platform)
	if platSeg == "" {
		platSeg = "*"
	}
	pattern := fmt.Sprintf("%s.%s.*.%s.%s.*", orgSeg, platSeg, domain, entityType)
	if err := types.ValidateEntityIDPattern(pattern); err != nil {
		return "", err
	}
	return pattern, nil
}

// TypeScanPatternsForClass resolves classIRI via vocabulary.LookupEntityTypesByClassIRI
// into sorted, deduplicated `<org>.<platform>.*.<domain>.<type>.*` patterns for
// GraphReader.ScanPattern. It returns (nil, nil) if classIRI matches no
// registered entity types.
func TypeScanPatternsForClass(classIRI, org, platform string) ([]string, error) {
	if strings.TrimSpace(classIRI) == "" {
		return nil, nil
	}
	domainTypes := vocabulary.LookupEntityTypesByClassIRI(classIRI)
	if len(domainTypes) == 0 {
		return nil, nil
	}
	patterns := make([]string, 0, len(domainTypes))
	for _, dt := range domainTypes {
		pat, err := entityTypePattern(dt, org, platform)
		if err != nil {
			return nil, err
		}
		patterns = append(patterns, pat)
	}
	slices.Sort(patterns)
	patterns = slices.Compact(patterns)
	return patterns, nil
}

// VirtualTypeIRIsForEntityID extracts `<domain>.<type>` from entityID using
// SemEngine's named types.ParseEntityID fields and returns the deduplicated
// virtual rdf:type class IRIs (ClassIRI followed by registered Superclasses).
func VirtualTypeIRIsForEntityID(entityID string) []string {
	eid, err := types.ParseEntityID(entityID)
	if err != nil {
		return nil
	}
	meta := vocabulary.GetEntityTypeMetadata(eid.Domain + "." + eid.Type)
	if meta == nil {
		return nil
	}
	out := make([]string, 0, 1+len(meta.Superclasses))
	seen := make(map[string]struct{}, 1+len(meta.Superclasses))
	if meta.ClassIRI != "" {
		seen[meta.ClassIRI] = struct{}{}
		out = append(out, meta.ClassIRI)
	}
	for _, sup := range meta.Superclasses {
		if sup == "" {
			continue
		}
		if _, dup := seen[sup]; !dup {
			seen[sup] = struct{}{}
			out = append(out, sup)
		}
	}
	return out
}

// SuperclassesForClassIRI returns all registered superclasses (direct and
// transitive) for classIRI across vocabulary.ListRegisteredEntityTypes().
func SuperclassesForClassIRI(classIRI string) []string {
	if classIRI == "" {
		return nil
	}
	domainTypes := vocabulary.ListRegisteredEntityTypes()
	if len(domainTypes) == 0 {
		return nil
	}
	direct := make(map[string][]string, len(domainTypes))
	for _, dt := range domainTypes {
		meta := vocabulary.GetEntityTypeMetadata(dt)
		if meta == nil || meta.ClassIRI == "" {
			continue
		}
		existing := direct[meta.ClassIRI]
		for _, sup := range meta.Superclasses {
			if sup != "" && !slices.Contains(existing, sup) {
				existing = append(existing, sup)
			}
		}
		direct[meta.ClassIRI] = existing
	}
	return transitiveSuperclasses(classIRI, direct)
}

// SubClassClosurePairs evaluates standalone `?sub rdfs:subClassOf* ?sup` over
// the finite registered ontology class graph in vocabulary (ClassIRI and
// Superclasses across ListRegisteredEntityTypes()), including reflexive (C, C)
// for every registered class/superclass IRI C and for any non-empty boundSubIRI
// or boundSupIRI (owner ruling D.1c). The returned slice is sorted and
// deduplicated.
func SubClassClosurePairs(boundSubIRI, boundSupIRI string) [][2]string {
	domainTypes := vocabulary.ListRegisteredEntityTypes()
	direct := make(map[string][]string, len(domainTypes))
	var classes []string
	seenClass := make(map[string]struct{})

	addClass := func(c string) {
		if c == "" {
			return
		}
		if _, ok := seenClass[c]; !ok {
			seenClass[c] = struct{}{}
			classes = append(classes, c)
		}
	}

	for _, dt := range domainTypes {
		meta := vocabulary.GetEntityTypeMetadata(dt)
		if meta == nil {
			continue
		}
		addClass(meta.ClassIRI)
		existing := direct[meta.ClassIRI]
		for _, sup := range meta.Superclasses {
			addClass(sup)
			if meta.ClassIRI != "" && sup != "" && !slices.Contains(existing, sup) {
				existing = append(existing, sup)
			}
		}
		if meta.ClassIRI != "" {
			direct[meta.ClassIRI] = existing
		}
	}

	// Per D.1c, reflexive zero-length (C, C) also holds for any bound IRI C.
	addClass(boundSubIRI)
	addClass(boundSupIRI)

	pairs := make([][2]string, 0, len(classes)*2)
	appendPair := func(sub, sup string) {
		if sub == "" || sup == "" {
			return
		}
		if boundSubIRI != "" && sub != boundSubIRI {
			return
		}
		if boundSupIRI != "" && sup != boundSupIRI {
			return
		}
		pairs = append(pairs, [2]string{sub, sup})
	}

	for _, c := range classes {
		appendPair(c, c)
		for _, sup := range transitiveSuperclasses(c, direct) {
			appendPair(c, sup)
		}
	}

	slices.SortFunc(pairs, func(a, b [2]string) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(a[1], b[1])
	})
	pairs = slices.Compact(pairs)
	return pairs
}
