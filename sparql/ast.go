package sparql

import "github.com/c360studio/semengine/pkg/errs"

// Standard RDF, RDFS, and XSD IRIs used by the SPARQL 1.1 AST and parser.
const (
	RDFTypeIRI        = "http://www.w3.org/1999/02/22-rdf-syntax-ns#type"
	RDFLangStringIRI  = "http://www.w3.org/1999/02/22-rdf-syntax-ns#langString"
	RDFSSubClassOfIRI = "http://www.w3.org/2000/01/rdf-schema#subClassOf"

	XSDStringIRI   = "http://www.w3.org/2001/XMLSchema#string"
	XSDBooleanIRI  = "http://www.w3.org/2001/XMLSchema#boolean"
	XSDIntegerIRI  = "http://www.w3.org/2001/XMLSchema#integer"
	XSDDecimalIRI  = "http://www.w3.org/2001/XMLSchema#decimal"
	XSDDoubleIRI   = "http://www.w3.org/2001/XMLSchema#double"
	XSDDateTimeIRI = "http://www.w3.org/2001/XMLSchema#dateTime"
)

// ADR-060 classified error codes for SPARQL parse failures.
const (
	ErrorCodeSyntax             = "sparql_syntax_error"
	ErrorCodeUnsupportedFeature = "sparql_unsupported_feature"
	DetailKeyFeature            = "feature"
)

var (
	// ErrSyntax is the classified ADR-060 sentinel matching any syntax error
	// returned by Parse via errors.Is(err, ErrSyntax).
	ErrSyntax error = &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    ErrorCodeSyntax,
		Message: "sparql: syntax error",
	}

	// ErrUnsupportedFeature is the classified ADR-060 sentinel matching any
	// out-of-scope SPARQL feature error returned by Parse via
	// errors.Is(err, ErrUnsupportedFeature).
	ErrUnsupportedFeature error = &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    ErrorCodeUnsupportedFeature,
		Message: "sparql: unsupported feature",
	}
)

// QueryForm identifies one of the four SPARQL 1.1 query forms.
type QueryForm int

const (
	// QuerySelect projects variable bindings in a result table.
	QuerySelect QueryForm = iota + 1
	// QueryAsk tests whether at least one solution satisfies the query pattern.
	QueryAsk
	// QueryConstruct builds an RDF graph by instantiating a template.
	QueryConstruct
	// QueryDescribe builds an outgoing Concise Bounded Description RDF graph.
	QueryDescribe
)

// Query is the parsed representation of a SPARQL 1.1 query in the v1 subset.
type Query struct {
	Form         QueryForm
	Base         string
	Prefixes     map[string]string
	Distinct     bool
	SelectAll    bool
	Variables    []string
	DescribeAll  bool
	DescribeIRIs []string
	Construct    []TriplePattern
	HasWhere     bool
	Where        GroupPattern
	OrderBy      []OrderCondition
	Limit        *int
	Offset       int
}

// GroupPattern represents a `{ ... }` group graph pattern containing ordered
// pattern elements (BGPs, OPTIONALs, UNIONs, or nested sub-groups) and group-scoped
// FILTER expressions.
type GroupPattern struct {
	Elements []PatternElement
	Filters  []FilterExpr
}

// PatternElement is a member of a GroupPattern.
type PatternElement interface {
	patternElement()
}

// BGPPattern is a Basic Graph Pattern consisting of one or more TriplePatterns.
type BGPPattern struct {
	Triples []TriplePattern
}

func (BGPPattern) patternElement() {}

// OptionalPattern is an `OPTIONAL { ... }` left-outer join pattern.
type OptionalPattern struct {
	Group GroupPattern
}

func (OptionalPattern) patternElement() {}

// UnionPattern is a `{ ... } UNION { ... }` alternation of two or more branches.
type UnionPattern struct {
	Branches []GroupPattern
}

func (UnionPattern) patternElement() {}

// SubGroupPattern is an explicit nested `{ ... }` group pattern.
type SubGroupPattern struct {
	Group GroupPattern
}

func (SubGroupPattern) patternElement() {}

// PathKind identifies whether a TriplePattern uses a plain predicate term or one
// of the two supported v1 property paths.
type PathKind int

const (
	// PathNone indicates an ordinary predicate (variable or IRI, including `a` / `rdf:type`).
	PathNone PathKind = iota
	// PathRDFTypeSubClassOfStar indicates `rdf:type/rdfs:subClassOf*` (or `a/rdfs:subClassOf*`).
	PathRDFTypeSubClassOfStar
	// PathSubClassOfStar indicates standalone `rdfs:subClassOf*`.
	PathSubClassOfStar
)

// TriplePattern is a single subject-predicate/path-object pattern in a BGP or
// CONSTRUCT template.
type TriplePattern struct {
	Subject   Term
	Predicate Term
	Path      PathKind
	Object    Term
}

// IsPlainRDFType reports whether the triple pattern uses a plain `a` or `rdf:type` predicate.
func (tp TriplePattern) IsPlainRDFType() bool {
	return tp.Path == PathNone && tp.Predicate.Kind == TermIRI && tp.Predicate.Value == RDFTypeIRI
}

// IsRDFTypeWithSubClassPath reports whether the triple pattern uses `rdf:type/rdfs:subClassOf*`.
func (tp TriplePattern) IsRDFTypeWithSubClassPath() bool {
	return tp.Path == PathRDFTypeSubClassOfStar
}

// IsStandaloneSubClassPath reports whether the triple pattern uses standalone `rdfs:subClassOf*`.
func (tp TriplePattern) IsStandaloneSubClassPath() bool {
	return tp.Path == PathSubClassOfStar
}

// TermKind classifies a SPARQL RDF term or query variable.
type TermKind int

const (
	// TermVariable is a query variable (`?x` or `$x`), with Value storing the name without prefix (`"x"`).
	TermVariable TermKind = iota + 1
	// TermIRI is an absolute or base/prefix-resolved IRI, with Value storing the full IRI string.
	TermIRI
	// TermBlankNode is a blank node (`_:label` or `[]`), with Value storing `"_:..."`.
	TermBlankNode
	// TermLiteral is an RDF literal, with Value storing the lexical form, Datatype storing the full
	// datatype IRI, and Language storing the lowercase language tag when present.
	TermLiteral
)

// Term represents a variable, IRI, blank node, or RDF literal in a triple pattern or expression.
type Term struct {
	Kind     TermKind
	Value    string
	Datatype string
	Language string
}

// FilterExpr is a boolean or value expression inside a `FILTER` or `ORDER BY` clause.
type FilterExpr interface {
	filterExpr()
}

// TermExpr wraps a Term (variable, IRI, blank node, or literal) as a FilterExpr.
type TermExpr struct {
	Term Term
}

func (TermExpr) filterExpr() {}

// UnaryOp identifies a unary expression operator.
type UnaryOp int

const (
	// UnaryNot is logical negation (`!expr`).
	UnaryNot UnaryOp = iota + 1
	// UnaryPlus is numeric unary plus (`+expr`).
	UnaryPlus
	// UnaryMinus is numeric unary negation (`-expr`).
	UnaryMinus
)

// UnaryExpr is a unary operation (`!`, `+`, `-`).
type UnaryExpr struct {
	Op      UnaryOp
	Operand FilterExpr
}

func (UnaryExpr) filterExpr() {}

// BinaryOp identifies a binary logical or comparison operator.
type BinaryOp int

const (
	// BinaryOr is logical OR (`||`).
	BinaryOr BinaryOp = iota + 1
	// BinaryAnd is logical AND (`&&`).
	BinaryAnd
	// BinaryEqual is equality (`=`).
	BinaryEqual
	// BinaryNotEqual is inequality (`!=`).
	BinaryNotEqual
	// BinaryLess is less-than (`<`).
	BinaryLess
	// BinaryLessEqual is less-than-or-equal (`<=`).
	BinaryLessEqual
	// BinaryGreater is greater-than (`>`).
	BinaryGreater
	// BinaryGreaterEqual is greater-than-or-equal (`>=`).
	BinaryGreaterEqual
)

// BinaryExpr is a binary logical (`||`, `&&`) or comparison (`=`, `!=`, `<`, `<=`, `>`, `>=`) expression.
type BinaryExpr struct {
	Op    BinaryOp
	Left  FilterExpr
	Right FilterExpr
}

func (BinaryExpr) filterExpr() {}

// BuiltinFunc identifies one of the supported v1 SPARQL built-in FILTER functions.
type BuiltinFunc string

// Supported v1 SPARQL 1.1 built-in FILTER functions.
const (
	FuncBound    BuiltinFunc = "BOUND"
	FuncRegex    BuiltinFunc = "REGEX"
	FuncStr      BuiltinFunc = "STR"
	FuncLang     BuiltinFunc = "LANG"
	FuncDatatype BuiltinFunc = "DATATYPE"
)

// CallExpr is a call to one of the supported v1 built-in functions
// (`BOUND`, `REGEX`, `STR`, `LANG`, `DATATYPE`).
type CallExpr struct {
	Func BuiltinFunc
	Args []FilterExpr
}

func (CallExpr) filterExpr() {}

// OrderDirection specifies ascending or descending sort order in `ORDER BY`.
type OrderDirection int

const (
	// OrderAsc orders solutions in ascending SPARQL 1.1 order.
	OrderAsc OrderDirection = iota
	// OrderDesc orders solutions in descending SPARQL 1.1 order.
	OrderDesc
)

// OrderCondition is a single sort key in an `ORDER BY` clause.
type OrderCondition struct {
	Direction OrderDirection
	Expr      FilterExpr
}
