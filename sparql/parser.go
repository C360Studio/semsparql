package sparql

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/c360studio/semengine/pkg/errs"
)

// Parse parses a SPARQL 1.1 query string in the supported v1 subset into an AST.
// Syntax errors match errors.Is(err, ErrSyntax); out-of-scope SPARQL features
// match errors.Is(err, ErrUnsupportedFeature) and name the rejected feature in
// both the error message and ClassifiedError.Detail["feature"].
func Parse(input string) (*Query, error) {
	tokens, err := lex(input)
	if err != nil {
		return nil, err
	}
	p := &parser{
		tokens: tokens,
		query: &Query{
			Prefixes: make(map[string]string),
		},
	}
	if err := p.parseQuery(); err != nil {
		return nil, err
	}
	return p.query, nil
}

func syntaxErrorf(pos int, format string, args ...any) error {
	msg := fmt.Sprintf(format, args...)
	return &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    ErrorCodeSyntax,
		Message: fmt.Sprintf("sparql: syntax error at byte %d: %s", pos, msg),
	}
}

func unsupportedErrorf(pos int, feature, detail string) error {
	msg := fmt.Sprintf("sparql: unsupported feature %s at byte %d", feature, pos)
	if detail != "" {
		msg = fmt.Sprintf("sparql: unsupported feature %s (%s) at byte %d", feature, detail, pos)
	}
	return &errs.ClassifiedError{
		Class:   errs.ErrorInvalid,
		Code:    ErrorCodeUnsupportedFeature,
		Message: msg,
		Detail: map[string]any{
			DetailKeyFeature: feature,
		},
	}
}

type tokenKind int

const (
	tokEOF tokenKind = iota
	tokIdent
	tokPrefixName // e.g. "rdf:" (Value="rdf")
	tokPrefixed   // e.g. "rdf:type" (Prefix="rdf", Value="type")
	tokIRI        // <...>
	tokVar        // ?x or $x (Value="x")
	tokBlankNode  // _:label (Value="_:label")
	tokString     // string literal (Value=unescaped)
	tokLangTag    // @en-US (Value="en-us")
	tokInteger
	tokDecimal
	tokDouble
	tokLBrace    // {
	tokRBrace    // }
	tokLParen    // (
	tokRParen    // )
	tokLBracket  // [
	tokRBracket  // ]
	tokDot       // .
	tokSemi      // ;
	tokComma     // ,
	tokSlash     // /
	tokPipe      // |
	tokOr        // ||
	tokAnd       // &&
	tokBang      // !
	tokNotEqual  // !=
	tokEqual     // =
	tokLess      // <
	tokLessEq    // <=
	tokGreater   // >
	tokGreaterEq // >=
	tokStar      // *
	tokPlus      // +
	tokMinus     // -
	tokCaret     // ^
	tokDatatype  // ^^
	tokQuestion  // ? (standalone path quantifier, not ?var)
)

type token struct {
	kind   tokenKind
	prefix string
	value  string
	pos    int
}

func lex(input string) ([]token, error) {
	if !utf8.ValidString(input) {
		return nil, syntaxErrorf(0, "query contains invalid UTF-8")
	}
	var tokens []token
	i := 0
	n := len(input)

	for i < n {
		ch := input[i]
		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			i++
			continue
		}
		if ch == '#' {
			for i < n && input[i] != '\n' && input[i] != '\r' {
				i++
			}
			continue
		}

		start := i
		switch ch {
		case '{':
			tokens = append(tokens, token{kind: tokLBrace, value: "{", pos: start})
			i++
			continue
		case '}':
			tokens = append(tokens, token{kind: tokRBrace, value: "}", pos: start})
			i++
			continue
		case '(':
			tokens = append(tokens, token{kind: tokLParen, value: "(", pos: start})
			i++
			continue
		case ')':
			tokens = append(tokens, token{kind: tokRParen, value: ")", pos: start})
			i++
			continue
		case '[':
			tokens = append(tokens, token{kind: tokLBracket, value: "[", pos: start})
			i++
			continue
		case ']':
			tokens = append(tokens, token{kind: tokRBracket, value: "]", pos: start})
			i++
			continue
		case ';':
			tokens = append(tokens, token{kind: tokSemi, value: ";", pos: start})
			i++
			continue
		case ',':
			tokens = append(tokens, token{kind: tokComma, value: ",", pos: start})
			i++
			continue
		case '/':
			tokens = append(tokens, token{kind: tokSlash, value: "/", pos: start})
			i++
			continue
		case '*':
			tokens = append(tokens, token{kind: tokStar, value: "*", pos: start})
			i++
			continue
		case '+':
			tokens = append(tokens, token{kind: tokPlus, value: "+", pos: start})
			i++
			continue
		case '-':
			tokens = append(tokens, token{kind: tokMinus, value: "-", pos: start})
			i++
			continue
		case '=':
			tokens = append(tokens, token{kind: tokEqual, value: "=", pos: start})
			i++
			continue
		case '^':
			if i+1 < n && input[i+1] == '^' {
				tokens = append(tokens, token{kind: tokDatatype, value: "^^", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokCaret, value: "^", pos: start})
				i++
			}
			continue
		case '|':
			if i+1 < n && input[i+1] == '|' {
				tokens = append(tokens, token{kind: tokOr, value: "||", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokPipe, value: "|", pos: start})
				i++
			}
			continue
		case '&':
			if i+1 < n && input[i+1] == '&' {
				tokens = append(tokens, token{kind: tokAnd, value: "&&", pos: start})
				i += 2
				continue
			}
			return nil, syntaxErrorf(start, "unexpected '&' (expected '&&')")
		case '!':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, token{kind: tokNotEqual, value: "!=", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokBang, value: "!", pos: start})
				i++
			}
			continue
		case '>':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, token{kind: tokGreaterEq, value: ">=", pos: start})
				i += 2
			} else {
				tokens = append(tokens, token{kind: tokGreater, value: ">", pos: start})
				i++
			}
			continue
		case '<':
			if i+1 < n && input[i+1] == '=' {
				tokens = append(tokens, token{kind: tokLessEq, value: "<=", pos: start})
				i += 2
				continue
			}
			if looksLikeIRIRef(input, i) {
				iriVal, nextIdx, err := lexIRIRef(input, i)
				if err != nil {
					return nil, err
				}
				tokens = append(tokens, token{kind: tokIRI, value: iriVal, pos: start})
				i = nextIdx
				continue
			}
			tokens = append(tokens, token{kind: tokLess, value: "<", pos: start})
			i++
			continue
		case '"', '\'':
			strVal, nextIdx, err := lexStringLiteral(input, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokString, value: strVal, pos: start})
			i = nextIdx
			continue
		case '@':
			langVal, nextIdx, err := lexLangTag(input, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, token{kind: tokLangTag, value: langVal, pos: start})
			i = nextIdx
			continue
		case '?', '$':
			if i+1 < n && isVarStartByte(input, i+1) {
				varName, nextIdx := lexVarName(input, i+1)
				tokens = append(tokens, token{kind: tokVar, value: varName, pos: start})
				i = nextIdx
				continue
			}
			if ch == '?' {
				tokens = append(tokens, token{kind: tokQuestion, value: "?", pos: start})
				i++
				continue
			}
			return nil, syntaxErrorf(start, "empty variable name after '$'")
		case '.':
			if i+1 < n && input[i+1] >= '0' && input[i+1] <= '9' {
				numTok, nextIdx, err := lexNumber(input, i)
				if err != nil {
					return nil, err
				}
				tokens = append(tokens, numTok)
				i = nextIdx
				continue
			}
			tokens = append(tokens, token{kind: tokDot, value: ".", pos: start})
			i++
			continue
		}

		if ch >= '0' && ch <= '9' {
			numTok, nextIdx, err := lexNumber(input, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, numTok)
			i = nextIdx
			continue
		}

		if ch == '_' && i+1 < n && input[i+1] == ':' {
			bnodeTok, nextIdx, err := lexBlankNode(input, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, bnodeTok)
			i = nextIdx
			continue
		}

		if ch == ':' || isPNCharsBase(input, i) {
			tok, nextIdx, err := lexIdentOrPrefixed(input, i)
			if err != nil {
				return nil, err
			}
			tokens = append(tokens, tok)
			i = nextIdx
			continue
		}

		r, _ := utf8.DecodeRuneInString(input[i:])
		return nil, syntaxErrorf(start, "unexpected character %q", r)
	}

	tokens = append(tokens, token{kind: tokEOF, pos: n})
	return tokens, nil
}

// looksLikeIRIRef distinguishes `<https://...>` or `<rel/path>` from `<` comparison.
// In SPARQL 1.1, an IRIREF contains `:` (scheme) or `/` or `#` or `.` or is enclosed in `<...>`
// without unescaped whitespace or `<` before the closing `>`. Moreover, in `<bad iri>`, if a `<`
// appears where an IRI is expected (after BASE, PREFIX, FROM, SERVICE, GRAPH, DESCRIBE, or in a
// triple subject/predicate/object), callers expect an invalid IRI error if spaces appear inside `<...>`.
func looksLikeIRIRef(s string, start int) bool {
	end := strings.IndexByte(s[start+1:], '>')
	if end < 0 {
		return false
	}
	inner := s[start+1 : start+1+end]
	if strings.ContainsAny(inner, "\n\r{}\"") {
		return false
	}
	if strings.Contains(inner, ":") || strings.Contains(inner, "/") || strings.Contains(inner, "#") || inner == "" {
		return true
	}
	// Check if there is any whitespace: if not, `<foo>` is a relative IRIREF.
	for i := 0; i < len(inner); i++ {
		if inner[i] <= 0x20 || inner[i] == '<' {
			return false
		}
	}
	return true
}

func lexIRIRef(s string, start int) (string, int, error) {
	i := start + 1
	var sb strings.Builder
	for i < len(s) {
		ch := s[i]
		if ch == '>' {
			return sb.String(), i + 1, nil
		}
		if ch == '\\' {
			r, nextIdx, err := decodeUnicodeEscape(s, i, true)
			if err != nil {
				return "", 0, err
			}
			if isForbiddenIRIRune(r) {
				return "", 0, syntaxErrorf(i, "invalid escaped character %U in IRIREF", r)
			}
			sb.WriteRune(r)
			i = nextIdx
			continue
		}
		r, width := utf8.DecodeRuneInString(s[i:])
		if isForbiddenIRIRune(r) {
			return "", 0, syntaxErrorf(i, "invalid character %q in IRIREF", r)
		}
		sb.WriteRune(r)
		i += width
	}
	return "", 0, syntaxErrorf(start, "unterminated IRIREF")
}

func isForbiddenIRIRune(r rune) bool {
	if r <= 0x20 {
		return true
	}
	switch r {
	case '<', '>', '"', '{', '}', '|', '^', '`', '\\':
		return true
	default:
		return false
	}
}

func lexStringLiteral(s string, start int) (string, int, error) {
	quote := s[start]
	longQuote := false
	i := start + 1
	if i+1 < len(s) && s[i] == quote && s[i+1] == quote {
		longQuote = true
		i = start + 3
	}

	var sb strings.Builder
	for i < len(s) {
		ch := s[i]
		if longQuote {
			if i+2 < len(s) && s[i] == quote && s[i+1] == quote && s[i+2] == quote {
				return sb.String(), i + 3, nil
			}
		} else {
			if ch == quote {
				return sb.String(), i + 1, nil
			}
			if ch == '\n' || ch == '\r' {
				return "", 0, syntaxErrorf(start, "unterminated string literal before newline")
			}
		}
		if ch == '\\' {
			if i+1 >= len(s) {
				return "", 0, syntaxErrorf(i, "unterminated escape in string literal")
			}
			switch s[i+1] {
			case 't':
				sb.WriteByte('\t')
				i += 2
			case 'b':
				sb.WriteByte('\b')
				i += 2
			case 'n':
				sb.WriteByte('\n')
				i += 2
			case 'r':
				sb.WriteByte('\r')
				i += 2
			case 'f':
				sb.WriteByte('\f')
				i += 2
			case '"':
				sb.WriteByte('"')
				i += 2
			case '\'':
				sb.WriteByte('\'')
				i += 2
			case '\\':
				sb.WriteByte('\\')
				i += 2
			case 'u', 'U':
				r, nextIdx, err := decodeUnicodeEscape(s, i, false)
				if err != nil {
					return "", 0, err
				}
				sb.WriteRune(r)
				i = nextIdx
			default:
				return "", 0, syntaxErrorf(i, "invalid escape sequence \\%c in string literal", s[i+1])
			}
			continue
		}
		r, width := utf8.DecodeRuneInString(s[i:])
		sb.WriteRune(r)
		i += width
	}
	return "", 0, syntaxErrorf(start, "unterminated string literal")
}

func decodeUnicodeEscape(s string, pos int, onlyUnicode bool) (rune, int, error) {
	if pos+1 >= len(s) {
		return 0, 0, syntaxErrorf(pos, "unterminated escape sequence")
	}
	kind := s[pos+1]
	var digits int
	switch kind {
	case 'u':
		digits = 4
	case 'U':
		digits = 8
	default:
		if onlyUnicode {
			return 0, 0, syntaxErrorf(pos, "invalid escape \\%c in IRIREF", kind)
		}
		return 0, 0, syntaxErrorf(pos, "invalid unicode escape \\%c", kind)
	}
	if pos+2+digits > len(s) {
		return 0, 0, syntaxErrorf(pos, "short unicode escape \\%c", kind)
	}
	hexStr := s[pos+2 : pos+2+digits]
	val, err := strconv.ParseUint(hexStr, 16, 32)
	if err != nil || !utf8.ValidRune(rune(val)) {
		return 0, 0, syntaxErrorf(pos, "invalid unicode escape \\%c%s", kind, hexStr)
	}
	return rune(val), pos + 2 + digits, nil
}

func lexLangTag(s string, start int) (string, int, error) {
	i := start + 1
	if i >= len(s) || !isASCIIAlpha(s[i]) {
		return "", 0, syntaxErrorf(start, "invalid language tag after '@'")
	}
	for i < len(s) && isASCIIAlpha(s[i]) {
		i++
	}
	for i < len(s) && s[i] == '-' {
		i++
		if i >= len(s) || !isASCIIAlphaNum(s[i]) {
			return "", 0, syntaxErrorf(start, "empty subtag in language tag")
		}
		for i < len(s) && isASCIIAlphaNum(s[i]) {
			i++
		}
	}
	return strings.ToLower(s[start+1 : i]), i, nil
}

func lexVarName(s string, start int) (string, int) {
	i := start
	for i < len(s) {
		r, width := utf8.DecodeRuneInString(s[i:])
		if r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			i += width
		} else {
			break
		}
	}
	return s[start:i], i
}

func isVarStartByte(s string, pos int) bool {
	r, _ := utf8.DecodeRuneInString(s[pos:])
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func lexBlankNode(s string, start int) (token, int, error) {
	i := start + 2
	if i >= len(s) {
		return token{}, 0, syntaxErrorf(start, "empty blank node label after '_:'")
	}
	r0, w0 := utf8.DecodeRuneInString(s[i:])
	if !isPNCharsU(r0) && !unicode.IsDigit(r0) {
		return token{}, 0, syntaxErrorf(start, "invalid start of blank node label %q", r0)
	}
	i += w0
	for i < len(s) {
		r, w := utf8.DecodeRuneInString(s[i:])
		if isPNChars(r) || r == '.' {
			i += w
		} else {
			break
		}
	}
	// Per SPARQL 1.1 [142] BLANK_NODE_LABEL, a blank node label cannot end with '.'.
	for s[i-1] == '.' {
		i--
	}
	return token{
		kind:  tokBlankNode,
		value: s[start:i],
		pos:   start,
	}, i, nil
}

func lexNumber(s string, start int) (token, int, error) {
	i := start
	kind := tokInteger
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i < len(s) && s[i] == '.' {
		// In SPARQL 1.1 [147] DECIMAL ::= [0-9]* '.' [0-9]+
		// A dot belongs to the number only when followed by at least one digit.
		if i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9' {
			kind = tokDecimal
			i++
			for i < len(s) && s[i] >= '0' && s[i] <= '9' {
				i++
			}
		}
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		kind = tokDouble
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		expStart := i
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		if i == expStart {
			return token{}, 0, syntaxErrorf(start, "missing exponent digits in numeric literal")
		}
	}
	return token{
		kind:  kind,
		value: s[start:i],
		pos:   start,
	}, i, nil
}

func lexIdentOrPrefixed(s string, start int) (token, int, error) {
	i := start
	var prefix string
	if s[i] != ':' {
		for i < len(s) {
			r, w := utf8.DecodeRuneInString(s[i:])
			if isPNChars(r) || r == '.' {
				i += w
			} else {
				break
			}
		}
		// If the run does not reach ':', strip any trailing '.' so `?s a ?o.` leaves '.' as tokDot.
		if i >= len(s) || s[i] != ':' {
			for i > start && s[i-1] == '.' {
				i--
			}
			return token{
				kind:  tokIdent,
				value: s[start:i],
				pos:   start,
			}, i, nil
		}
		// We reached ':'; PN_PREFIX cannot end with '.'.
		if i > start && s[i-1] == '.' {
			return token{}, 0, syntaxErrorf(start, "prefix name %q cannot end with '.'", s[start:i])
		}
		prefix = s[start:i]
	}

	// Consume ':'
	i++
	localStart := i
	if localStart >= len(s) || !isPNLocalStart(s, localStart) {
		return token{
			kind:   tokPrefixName,
			prefix: prefix,
			value:  prefix + ":",
			pos:    start,
		}, localStart, nil
	}

	localVal, nextIdx, err := lexPNLocal(s, localStart)
	if err != nil {
		return token{}, 0, err
	}
	return token{
		kind:   tokPrefixed,
		prefix: prefix,
		value:  localVal,
		pos:    start,
	}, nextIdx, nil
}

func isPNLocalStart(s string, pos int) bool {
	ch := s[pos]
	if ch == ':' || ch == '_' || (ch >= '0' && ch <= '9') || ch == '%' || ch == '\\' {
		return true
	}
	return isPNCharsBase(s, pos)
}

func lexPNLocal(s string, start int) (string, int, error) {
	type span struct {
		rawEnd     int
		unescaped  string
		wasEscaped bool
	}
	var spans []span
	var sb strings.Builder
	i := start
	for i < len(s) {
		ch := s[i]
		if ch == '\\' {
			if i+1 >= len(s) || !isPNLocalEscapable(s[i+1]) {
				return "", 0, syntaxErrorf(i, "invalid escape in prefixed name local part")
			}
			sb.WriteByte(s[i+1])
			i += 2
			spans = append(spans, span{rawEnd: i, unescaped: sb.String(), wasEscaped: true})
			continue
		}
		if ch == '%' {
			if i+2 >= len(s) || !isHexByte(s[i+1]) || !isHexByte(s[i+2]) {
				return "", 0, syntaxErrorf(i, "invalid percent escape in prefixed name local part")
			}
			sb.WriteString(s[i : i+3])
			i += 3
			spans = append(spans, span{rawEnd: i, unescaped: sb.String(), wasEscaped: false})
			continue
		}
		r, w := utf8.DecodeRuneInString(s[i:])
		if isPNChars(r) || r == ':' || r == '.' {
			sb.WriteRune(r)
			i += w
			spans = append(spans, span{rawEnd: i, unescaped: sb.String(), wasEscaped: false})
		} else {
			break
		}
	}
	// Strip any unescaped trailing '.' so statement-ending dots remain tokDot.
	for len(spans) > 0 {
		last := spans[len(spans)-1]
		if !last.wasEscaped && strings.HasSuffix(last.unescaped, ".") {
			spans = spans[:len(spans)-1]
		} else {
			break
		}
	}
	if len(spans) == 0 {
		return "", start, nil
	}
	final := spans[len(spans)-1]
	return final.unescaped, final.rawEnd, nil
}

func isPNLocalEscapable(b byte) bool {
	switch b {
	case '_', '~', '.', '-', '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', '/', '?', '#', '@', '%':
		return true
	default:
		return false
	}
}

func isPNCharsBase(s string, pos int) bool {
	r, _ := utf8.DecodeRuneInString(s[pos:])
	return unicode.IsLetter(r)
}

func isPNCharsU(r rune) bool {
	return r == '_' || unicode.IsLetter(r)
}

func isPNChars(r rune) bool {
	return isPNCharsU(r) || r == '-' || unicode.IsDigit(r)
}

func isASCIIAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

func isASCIIAlphaNum(b byte) bool {
	return isASCIIAlpha(b) || (b >= '0' && b <= '9')
}

func isHexByte(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

const maxParserNestingDepth = 128

type parser struct {
	tokens     []token
	pos        int
	query      *Query
	baseURL    *url.URL
	anonCount  int
	groupDepth int
	bnodeDepth int
	exprDepth  int
	pathDepth  int
}

func (p *parser) cur() token {
	if p.pos >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[p.pos]
}

func (p *parser) peek(offset int) token {
	idx := p.pos + offset
	if idx >= len(p.tokens) {
		return p.tokens[len(p.tokens)-1]
	}
	return p.tokens[idx]
}

func (p *parser) next() token {
	tok := p.cur()
	if p.pos < len(p.tokens)-1 {
		p.pos++
	}
	return tok
}

func (p *parser) isKeyword(kw string) bool {
	tok := p.cur()
	return tok.kind == tokIdent && strings.EqualFold(tok.value, kw)
}

func (p *parser) consumeKeyword(kw string) bool {
	if p.isKeyword(kw) {
		p.next()
		return true
	}
	return false
}

func (p *parser) expect(kind tokenKind, desc string) (token, error) {
	tok := p.cur()
	if tok.kind != kind {
		return token{}, syntaxErrorf(tok.pos, "expected %s, got %q", desc, tok.value)
	}
	p.next()
	return tok, nil
}

func (p *parser) parseQuery() error {
	if err := p.parsePrologue(); err != nil {
		return err
	}

	tok := p.cur()
	if tok.kind == tokEOF {
		return syntaxErrorf(tok.pos, "empty SPARQL query")
	}
	if tok.kind != tokIdent {
		return syntaxErrorf(tok.pos, "expected query form (SELECT, ASK, CONSTRUCT, DESCRIBE), got %q", tok.value)
	}

	upper := strings.ToUpper(tok.value)
	if isUpdateKeyword(upper) {
		return unsupportedErrorf(tok.pos, "SPARQL Update ("+upper+")", "sidecar is strictly read-only")
	}

	switch upper {
	case "SELECT":
		if err := p.parseSelectQuery(); err != nil {
			return err
		}
	case "ASK":
		if err := p.parseAskQuery(); err != nil {
			return err
		}
	case "CONSTRUCT":
		if err := p.parseConstructQuery(); err != nil {
			return err
		}
	case "DESCRIBE":
		if err := p.parseDescribeQuery(); err != nil {
			return err
		}
	default:
		return syntaxErrorf(tok.pos, "unexpected query keyword %q", tok.value)
	}

	if p.cur().kind != tokEOF {
		extra := p.cur()
		if extra.kind == tokIdent {
			if err := p.checkUnsupportedModifierKeyword(extra); err != nil {
				return err
			}
		}
		return syntaxErrorf(extra.pos, "unexpected trailing token %q", extra.value)
	}
	return nil
}

func isUpdateKeyword(upper string) bool {
	switch upper {
	case "INSERT", "DELETE", "LOAD", "CLEAR", "DROP", "CREATE", "ADD", "MOVE", "COPY", "WITH":
		return true
	default:
		return false
	}
}

func isAggregateKeyword(upper string) bool {
	switch upper {
	case "COUNT", "SUM", "AVG", "MIN", "MAX", "SAMPLE", "GROUP_CONCAT":
		return true
	default:
		return false
	}
}

func (p *parser) parsePrologue() error {
	for {
		if p.consumeKeyword("BASE") {
			iriTok, err := p.expect(tokIRI, "BASE <IRI>")
			if err != nil {
				return err
			}
			resolved, parsedURL, err := resolveAndValidateBaseIRI(p.baseURL, iriTok.value, iriTok.pos)
			if err != nil {
				return err
			}
			p.query.Base = resolved
			p.baseURL = parsedURL
			continue
		}
		if p.consumeKeyword("PREFIX") {
			pfxTok := p.cur()
			if pfxTok.kind != tokPrefixName {
				return syntaxErrorf(pfxTok.pos, "expected prefix name (e.g. 'pfx:') after PREFIX, got %q", pfxTok.value)
			}
			p.next()
			iriTok, err := p.expect(tokIRI, "namespace <IRI> after PREFIX")
			if err != nil {
				return err
			}
			resolved, err := p.resolveIRI(iriTok.value, iriTok.pos)
			if err != nil {
				return err
			}
			p.query.Prefixes[pfxTok.prefix] = resolved
			continue
		}
		break
	}
	return nil
}

func resolveAndValidateBaseIRI(currentBase *url.URL, raw string, pos int) (string, *url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", nil, syntaxErrorf(pos, "invalid BASE IRI %q: %v", raw, err)
	}
	if !u.IsAbs() {
		if currentBase == nil {
			return "", nil, syntaxErrorf(pos, "relative BASE IRI %q requires a prior absolute BASE", raw)
		}
		u = currentBase.ResolveReference(u)
	}
	return u.String(), u, nil
}

func (p *parser) resolveIRI(raw string, pos int) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", syntaxErrorf(pos, "invalid IRI %q: %v", raw, err)
	}
	if u.IsAbs() {
		return raw, nil
	}
	if p.baseURL == nil {
		return "", syntaxErrorf(pos, "relative IRI <%s> requires a BASE declaration", raw)
	}
	return p.baseURL.ResolveReference(u).String(), nil
}

func (p *parser) resolvePrefixed(tok token) (string, error) {
	ns, ok := p.query.Prefixes[tok.prefix]
	if !ok {
		return "", syntaxErrorf(tok.pos, "undeclared prefix %q", tok.prefix)
	}
	return ns + tok.value, nil
}

func (p *parser) parseSelectQuery() error {
	p.next() // consume SELECT
	p.query.Form = QuerySelect

	if p.consumeKeyword("DISTINCT") {
		p.query.Distinct = true
	} else if p.isKeyword("REDUCED") {
		return unsupportedErrorf(p.cur().pos, "REDUCED", "use DISTINCT instead")
	}

	if p.cur().kind == tokStar {
		p.next()
		p.query.SelectAll = true
	} else {
		for {
			tok := p.cur()
			if tok.kind == tokVar {
				p.next()
				p.query.Variables = append(p.query.Variables, tok.value)
				continue
			}
			if tok.kind == tokLParen {
				return p.rejectSelectParenthesizedProjection(tok.pos)
			}
			if tok.kind == tokIdent && isAggregateKeyword(strings.ToUpper(tok.value)) {
				return unsupportedErrorf(tok.pos, "aggregates ("+strings.ToUpper(tok.value)+")", "")
			}
			break
		}
		if len(p.query.Variables) == 0 {
			return syntaxErrorf(p.cur().pos, "SELECT requires '*' or at least one projected variable")
		}
	}

	if err := p.checkDatasetClause(); err != nil {
		return err
	}

	p.consumeKeyword("WHERE")
	group, err := p.parseGroupGraphPattern()
	if err != nil {
		return err
	}
	p.query.HasWhere = true
	p.query.Where = group

	return p.parseSolutionModifiers()
}

func (p *parser) rejectSelectParenthesizedProjection(startPos int) error {
	// Scan inside (...) to report aggregate names specifically when present,
	// otherwise report BIND / projection expressions.
	depth := 0
	for idx := p.pos; idx < len(p.tokens); idx++ {
		t := p.tokens[idx]
		if t.kind == tokLParen {
			depth++
		} else if t.kind == tokRParen {
			depth--
			if depth <= 0 {
				break
			}
		} else if t.kind == tokIdent && isAggregateKeyword(strings.ToUpper(t.value)) {
			return unsupportedErrorf(t.pos, "aggregates ("+strings.ToUpper(t.value)+")", "SELECT aggregate projection")
		}
	}
	return unsupportedErrorf(startPos, "BIND", "SELECT projection expressions (expr AS ?var) are not supported")
}

func (p *parser) parseAskQuery() error {
	p.next() // consume ASK
	p.query.Form = QueryAsk

	if err := p.checkDatasetClause(); err != nil {
		return err
	}

	p.consumeKeyword("WHERE")
	group, err := p.parseGroupGraphPattern()
	if err != nil {
		return err
	}
	p.query.HasWhere = true
	p.query.Where = group

	return p.parseSolutionModifiers()
}

func (p *parser) parseConstructQuery() error {
	p.next() // consume CONSTRUCT
	p.query.Form = QueryConstruct

	if err := p.checkDatasetClause(); err != nil {
		return err
	}

	if p.consumeKeyword("WHERE") {
		// Short-form `CONSTRUCT WHERE { ...triples... }`: template and WHERE pattern are the same BGP.
		templateTriples, err := p.parseConstructTemplate()
		if err != nil {
			return err
		}
		p.query.Construct = templateTriples
		p.query.HasWhere = true
		if len(templateTriples) > 0 {
			p.query.Where = GroupPattern{
				Elements: []PatternElement{BGPPattern{Triples: templateTriples}},
			}
		}
		return p.parseSolutionModifiers()
	}

	templateTriples, err := p.parseConstructTemplate()
	if err != nil {
		return err
	}
	p.query.Construct = templateTriples

	if err := p.checkDatasetClause(); err != nil {
		return err
	}

	p.consumeKeyword("WHERE")
	group, err := p.parseGroupGraphPattern()
	if err != nil {
		return err
	}
	p.query.HasWhere = true
	p.query.Where = group

	return p.parseSolutionModifiers()
}

func (p *parser) parseConstructTemplate() ([]TriplePattern, error) {
	if _, err := p.expect(tokLBrace, "'{' for CONSTRUCT template"); err != nil {
		return nil, err
	}
	var triples []TriplePattern
	for p.cur().kind != tokRBrace && p.cur().kind != tokEOF {
		if p.cur().kind == tokIdent {
			upper := strings.ToUpper(p.cur().value)
			switch upper {
			case "FILTER", "OPTIONAL", "UNION", "GRAPH", "SERVICE", "BIND", "VALUES", "MINUS":
				return nil, syntaxErrorf(p.cur().pos, "%s is not allowed inside a CONSTRUCT template", upper)
			}
		}
		block, err := p.parseTriplesSameSubject(true)
		if err != nil {
			return nil, err
		}
		triples = append(triples, block...)
		if p.cur().kind == tokDot {
			p.next()
		} else if p.cur().kind != tokRBrace {
			return nil, syntaxErrorf(p.cur().pos, "expected '.' or '}' after triple in CONSTRUCT template, got %q", p.cur().value)
		}
	}
	if _, err := p.expect(tokRBrace, "'}' after CONSTRUCT template"); err != nil {
		return nil, err
	}
	return triples, nil
}

func (p *parser) parseDescribeQuery() error {
	p.next() // consume DESCRIBE
	p.query.Form = QueryDescribe

	if p.consumeKeyword("DISTINCT") {
		p.query.Distinct = true
	} else if p.isKeyword("REDUCED") {
		return unsupportedErrorf(p.cur().pos, "REDUCED", "use DISTINCT instead")
	}

	if p.cur().kind == tokStar {
		p.next()
		p.query.DescribeAll = true
	} else {
		for {
			tok := p.cur()
			if tok.kind == tokVar {
				p.next()
				p.query.Variables = append(p.query.Variables, tok.value)
				continue
			}
			if tok.kind == tokIRI {
				p.next()
				resolved, err := p.resolveIRI(tok.value, tok.pos)
				if err != nil {
					return err
				}
				p.query.DescribeIRIs = append(p.query.DescribeIRIs, resolved)
				continue
			}
			if tok.kind == tokPrefixed || tok.kind == tokPrefixName {
				p.next()
				resolved, err := p.resolvePrefixed(tok)
				if err != nil {
					return err
				}
				p.query.DescribeIRIs = append(p.query.DescribeIRIs, resolved)
				continue
			}
			break
		}
		if len(p.query.Variables) == 0 && len(p.query.DescribeIRIs) == 0 {
			return syntaxErrorf(p.cur().pos, "DESCRIBE requires '*', a variable, or an IRI")
		}
	}

	if err := p.checkDatasetClause(); err != nil {
		return err
	}

	if p.consumeKeyword("WHERE") || p.cur().kind == tokLBrace {
		group, err := p.parseGroupGraphPattern()
		if err != nil {
			return err
		}
		p.query.HasWhere = true
		p.query.Where = group
	}

	return p.parseSolutionModifiers()
}

func (p *parser) checkDatasetClause() error {
	if p.isKeyword("FROM") {
		return unsupportedErrorf(p.cur().pos, "named graphs (FROM)", "dataset clauses FROM / FROM NAMED are not supported")
	}
	return nil
}

func (p *parser) isGraphPatternKeyword() bool {
	if p.cur().kind != tokIdent {
		return false
	}
	upper := strings.ToUpper(p.cur().value)
	switch upper {
	case "OPTIONAL", "FILTER", "SERVICE", "GRAPH", "BIND", "VALUES", "MINUS", "SELECT":
		return true
	default:
		return isUpdateKeyword(upper)
	}
}

func (p *parser) parseGroupGraphPattern() (GroupPattern, error) {
	p.groupDepth++
	defer func() { p.groupDepth-- }()
	if p.groupDepth > maxParserNestingDepth {
		return GroupPattern{}, syntaxErrorf(p.cur().pos, "group graph pattern nesting depth exceeds maximum of %d", maxParserNestingDepth)
	}

	if _, err := p.expect(tokLBrace, "'{'"); err != nil {
		return GroupPattern{}, err
	}

	if p.isKeyword("SELECT") {
		return GroupPattern{}, unsupportedErrorf(p.cur().pos, "subquery", "nested SELECT subqueries are not supported")
	}

	var group GroupPattern
	var pendingTriples []TriplePattern

	flushTriples := func() {
		if len(pendingTriples) > 0 {
			group.Elements = append(group.Elements, BGPPattern{Triples: pendingTriples})
			pendingTriples = nil
		}
	}

	for p.cur().kind != tokRBrace && p.cur().kind != tokEOF {
		tok := p.cur()

		if tok.kind == tokIdent {
			upper := strings.ToUpper(tok.value)
			switch upper {
			case "OPTIONAL":
				flushTriples()
				p.next()
				optGroup, err := p.parseGroupGraphPattern()
				if err != nil {
					return GroupPattern{}, err
				}
				group.Elements = append(group.Elements, OptionalPattern{Group: optGroup})
				if p.cur().kind == tokDot {
					p.next()
				}
				continue
			case "FILTER":
				p.next()
				expr, err := p.parseFilterConstraint()
				if err != nil {
					return GroupPattern{}, err
				}
				group.Filters = append(group.Filters, expr)
				if p.cur().kind == tokDot {
					p.next()
				}
				continue
			case "SERVICE":
				return GroupPattern{}, unsupportedErrorf(tok.pos, "SERVICE", "federated SERVICE queries are not supported")
			case "GRAPH":
				return GroupPattern{}, unsupportedErrorf(tok.pos, "named graphs (GRAPH)", "GRAPH clauses are not supported")
			case "BIND":
				return GroupPattern{}, unsupportedErrorf(tok.pos, "BIND", "BIND assignments are not supported")
			case "VALUES":
				return GroupPattern{}, unsupportedErrorf(tok.pos, "VALUES", "inline VALUES clauses are not supported")
			case "MINUS":
				return GroupPattern{}, unsupportedErrorf(tok.pos, "MINUS", "MINUS patterns are not supported")
			case "SELECT":
				return GroupPattern{}, unsupportedErrorf(tok.pos, "subquery", "nested SELECT subqueries are not supported")
			}
			if isUpdateKeyword(upper) {
				return GroupPattern{}, unsupportedErrorf(tok.pos, "SPARQL Update ("+upper+")", "sidecar is strictly read-only")
			}
		}

		if tok.kind == tokLBrace {
			flushTriples()
			firstBranch, err := p.parseGroupGraphPattern()
			if err != nil {
				return GroupPattern{}, err
			}
			if p.isKeyword("UNION") {
				branches := []GroupPattern{firstBranch}
				for p.consumeKeyword("UNION") {
					nextBranch, err := p.parseGroupGraphPattern()
					if err != nil {
						return GroupPattern{}, err
					}
					branches = append(branches, nextBranch)
				}
				group.Elements = append(group.Elements, UnionPattern{Branches: branches})
			} else {
				group.Elements = append(group.Elements, SubGroupPattern{Group: firstBranch})
			}
			if p.cur().kind == tokDot {
				p.next()
			}
			continue
		}

		triples, err := p.parseTriplesSameSubject(false)
		if err != nil {
			return GroupPattern{}, err
		}
		pendingTriples = append(pendingTriples, triples...)
		if p.cur().kind == tokDot {
			p.next()
		} else if p.cur().kind != tokRBrace && p.cur().kind != tokLBrace && !p.isGraphPatternKeyword() {
			return GroupPattern{}, syntaxErrorf(p.cur().pos, "expected '.' after triple pattern before %q", p.cur().value)
		}
	}

	flushTriples()

	if _, err := p.expect(tokRBrace, "'}'"); err != nil {
		return GroupPattern{}, err
	}
	return group, nil
}

func (p *parser) parseTriplesSameSubject(inTemplate bool) ([]TriplePattern, error) {
	var triples []TriplePattern

	if p.cur().kind == tokLBracket {
		subj, preTriples, err := p.parseBlankNodePropertyList(inTemplate)
		if err != nil {
			return nil, err
		}
		triples = append(triples, preTriples...)
		// A blank node property list `[ :p :o ]` at subject position may stand alone
		// or be followed by a predicate-object list.
		if p.cur().kind == tokDot || p.cur().kind == tokRBrace || p.cur().kind == tokLBrace || p.isGraphPatternKeyword() {
			if len(triples) == 0 {
				return nil, syntaxErrorf(p.cur().pos, "standalone '[]' requires a predicate and object")
			}
			return triples, nil
		}
		more, err := p.parsePropertyListNotEmpty(subj, inTemplate)
		if err != nil {
			return nil, err
		}
		triples = append(triples, more...)
		return triples, nil
	}

	subj, err := p.parseSubjectTerm()
	if err != nil {
		return nil, err
	}
	more, err := p.parsePropertyListNotEmpty(subj, inTemplate)
	if err != nil {
		return nil, err
	}
	triples = append(triples, more...)
	return triples, nil
}

func (p *parser) parseBlankNodePropertyList(inTemplate bool) (Term, []TriplePattern, error) {
	p.bnodeDepth++
	defer func() { p.bnodeDepth-- }()
	if p.bnodeDepth > maxParserNestingDepth {
		return Term{}, nil, syntaxErrorf(p.cur().pos, "blank node property list nesting depth exceeds maximum of %d", maxParserNestingDepth)
	}

	openTok, err := p.expect(tokLBracket, "'['")
	if err != nil {
		return Term{}, nil, err
	}
	p.anonCount++
	bnode := Term{
		Kind:  TermBlankNode,
		Value: fmt.Sprintf("_:anon_%d", p.anonCount),
	}
	if p.cur().kind == tokRBracket {
		p.next()
		return bnode, nil, nil
	}
	triples, err := p.parsePropertyListNotEmpty(bnode, inTemplate)
	if err != nil {
		return Term{}, nil, err
	}
	if _, err := p.expect(tokRBracket, "']' closing blank node property list opened at byte "+strconv.Itoa(openTok.pos)); err != nil {
		return Term{}, nil, err
	}
	return bnode, triples, nil
}

func (p *parser) parsePropertyListNotEmpty(subj Term, inTemplate bool) ([]TriplePattern, error) {
	var triples []TriplePattern
	for {
		pred, pathKind, err := p.parseVerbOrPath(inTemplate)
		if err != nil {
			return nil, err
		}
		objs, preTriples, err := p.parseObjectList(inTemplate)
		if err != nil {
			return nil, err
		}
		triples = append(triples, preTriples...)
		for _, obj := range objs {
			triples = append(triples, TriplePattern{
				Subject:   subj,
				Predicate: pred,
				Path:      pathKind,
				Object:    obj,
			})
		}

		if p.cur().kind != tokSemi {
			break
		}
		for p.cur().kind == tokSemi {
			p.next()
		}
		if p.cur().kind == tokDot || p.cur().kind == tokRBrace || p.cur().kind == tokRBracket || p.cur().kind == tokEOF {
			break
		}
	}
	return triples, nil
}

func (p *parser) parseObjectList(inTemplate bool) ([]Term, []TriplePattern, error) {
	var objs []Term
	var extraTriples []TriplePattern
	for {
		if p.cur().kind == tokLBracket {
			bnode, pre, err := p.parseBlankNodePropertyList(inTemplate)
			if err != nil {
				return nil, nil, err
			}
			extraTriples = append(extraTriples, pre...)
			objs = append(objs, bnode)
		} else {
			obj, err := p.parseObjectTerm()
			if err != nil {
				return nil, nil, err
			}
			objs = append(objs, obj)
		}
		if p.cur().kind != tokComma {
			break
		}
		p.next()
	}
	return objs, extraTriples, nil
}

func (p *parser) parseSubjectTerm() (Term, error) {
	tok := p.cur()
	if tok.kind == tokLParen {
		return Term{}, unsupportedErrorf(tok.pos, "RDF collection", "RDF list collections '(...)' are not supported")
	}
	term, err := p.parseObjectTerm()
	if err != nil {
		return Term{}, err
	}
	if term.Kind == TermLiteral {
		return Term{}, syntaxErrorf(tok.pos, "literal %q is not allowed in subject position", term.Value)
	}
	return term, nil
}

func (p *parser) parseObjectTerm() (Term, error) {
	tok := p.cur()
	switch tok.kind {
	case tokLParen:
		return Term{}, unsupportedErrorf(tok.pos, "RDF collection", "RDF list collections '(...)' are not supported")
	case tokVar:
		p.next()
		return Term{Kind: TermVariable, Value: tok.value}, nil
	case tokBlankNode:
		p.next()
		return Term{Kind: TermBlankNode, Value: tok.value}, nil
	case tokIRI:
		p.next()
		resolved, err := p.resolveIRI(tok.value, tok.pos)
		if err != nil {
			return Term{}, err
		}
		return Term{Kind: TermIRI, Value: resolved}, nil
	case tokPrefixed, tokPrefixName:
		p.next()
		resolved, err := p.resolvePrefixed(tok)
		if err != nil {
			return Term{}, err
		}
		return Term{Kind: TermIRI, Value: resolved}, nil
	case tokString:
		return p.parseRDFLiteral()
	case tokInteger:
		p.next()
		return Term{Kind: TermLiteral, Value: tok.value, Datatype: XSDIntegerIRI}, nil
	case tokDecimal:
		p.next()
		return Term{Kind: TermLiteral, Value: tok.value, Datatype: XSDDecimalIRI}, nil
	case tokDouble:
		p.next()
		return Term{Kind: TermLiteral, Value: tok.value, Datatype: XSDDoubleIRI}, nil
	case tokPlus, tokMinus:
		sign := tok.value
		nextTok := p.peek(1)
		switch nextTok.kind {
		case tokInteger:
			p.next()
			p.next()
			return Term{Kind: TermLiteral, Value: sign + nextTok.value, Datatype: XSDIntegerIRI}, nil
		case tokDecimal:
			p.next()
			p.next()
			return Term{Kind: TermLiteral, Value: sign + nextTok.value, Datatype: XSDDecimalIRI}, nil
		case tokDouble:
			p.next()
			p.next()
			return Term{Kind: TermLiteral, Value: sign + nextTok.value, Datatype: XSDDoubleIRI}, nil
		default:
			return Term{}, syntaxErrorf(tok.pos, "expected numeric literal after %q", sign)
		}
	case tokIdent:
		if strings.EqualFold(tok.value, "true") || strings.EqualFold(tok.value, "false") {
			p.next()
			return Term{Kind: TermLiteral, Value: strings.ToLower(tok.value), Datatype: XSDBooleanIRI}, nil
		}
		return Term{}, syntaxErrorf(tok.pos, "unexpected identifier %q in term position", tok.value)
	default:
		return Term{}, syntaxErrorf(tok.pos, "expected RDF term or variable, got %q", tok.value)
	}
}

func (p *parser) parseRDFLiteral() (Term, error) {
	strTok, err := p.expect(tokString, "string literal")
	if err != nil {
		return Term{}, err
	}
	term := Term{
		Kind:     TermLiteral,
		Value:    strTok.value,
		Datatype: XSDStringIRI,
	}
	if p.cur().kind == tokLangTag {
		langTok := p.next()
		term.Language = langTok.value
		term.Datatype = RDFLangStringIRI
		if p.cur().kind == tokDatatype {
			return Term{}, syntaxErrorf(p.cur().pos, "string literal cannot have both language tag and datatype")
		}
		return term, nil
	}
	if p.cur().kind == tokDatatype {
		p.next()
		dtTok := p.cur()
		switch dtTok.kind {
		case tokIRI:
			p.next()
			resolved, err := p.resolveIRI(dtTok.value, dtTok.pos)
			if err != nil {
				return Term{}, err
			}
			term.Datatype = resolved
		case tokPrefixed, tokPrefixName:
			p.next()
			resolved, err := p.resolvePrefixed(dtTok)
			if err != nil {
				return Term{}, err
			}
			term.Datatype = resolved
		default:
			return Term{}, syntaxErrorf(dtTok.pos, "expected datatype IRI after '^^', got %q", dtTok.value)
		}
	}
	return term, nil
}

type pathExpr interface {
	pathExprNode()
}

type pathIRI struct {
	iri string
}

func (pathIRI) pathExprNode() {}

type pathZeroOrMore struct {
	elt pathExpr
}

func (pathZeroOrMore) pathExprNode() {}

type pathSeq struct {
	left  pathExpr
	right pathExpr
}

func (pathSeq) pathExprNode() {}

func (p *parser) parseVerbOrPath(inTemplate bool) (Term, PathKind, error) {
	tok := p.cur()
	if tok.kind == tokVar {
		p.next()
		if p.cur().kind == tokPlus && isSignedNumberObject(p.peek(1).kind, p.peek(2).kind) {
			return Term{Kind: TermVariable, Value: tok.value}, PathNone, nil
		}
		if isPathContinuation(p.cur().kind) {
			return Term{}, PathNone, unsupportedErrorf(p.cur().pos, "property path", "variable cannot participate in a property path")
		}
		return Term{Kind: TermVariable, Value: tok.value}, PathNone, nil
	}

	pe, err := p.parsePathAlternative(tok.pos)
	if err != nil {
		return Term{}, PathNone, err
	}

	switch node := pe.(type) {
	case pathIRI:
		return Term{Kind: TermIRI, Value: node.iri}, PathNone, nil
	case pathZeroOrMore:
		if inTemplate {
			return Term{}, PathNone, unsupportedErrorf(tok.pos, "property path", "property paths are not allowed in CONSTRUCT templates")
		}
		inner, ok := node.elt.(pathIRI)
		if ok && inner.iri == RDFSSubClassOfIRI {
			return Term{Kind: TermIRI, Value: RDFSSubClassOfIRI}, PathSubClassOfStar, nil
		}
		return Term{}, PathNone, unsupportedErrorf(tok.pos, "property path", "only rdf:type/rdfs:subClassOf* and rdfs:subClassOf* are supported")
	case pathSeq:
		if inTemplate {
			return Term{}, PathNone, unsupportedErrorf(tok.pos, "property path", "property paths are not allowed in CONSTRUCT templates")
		}
		leftIRI, okL := node.left.(pathIRI)
		rightStar, okR := node.right.(pathZeroOrMore)
		if okL && okR && leftIRI.iri == RDFTypeIRI {
			if inner, okI := rightStar.elt.(pathIRI); okI && inner.iri == RDFSSubClassOfIRI {
				return Term{Kind: TermIRI, Value: RDFTypeIRI}, PathRDFTypeSubClassOfStar, nil
			}
		}
		return Term{}, PathNone, unsupportedErrorf(tok.pos, "property path", "only rdf:type/rdfs:subClassOf* and rdfs:subClassOf* are supported")
	default:
		return Term{}, PathNone, unsupportedErrorf(tok.pos, "property path", "only rdf:type/rdfs:subClassOf* and rdfs:subClassOf* are supported")
	}
}

func isPathContinuation(k tokenKind) bool {
	switch k {
	case tokSlash, tokPipe, tokStar, tokPlus, tokQuestion:
		return true
	default:
		return false
	}
}

func (p *parser) parsePathAlternative(startPos int) (pathExpr, error) {
	left, err := p.parsePathSequence(startPos)
	if err != nil {
		return nil, err
	}
	if p.cur().kind == tokPipe {
		return nil, unsupportedErrorf(p.cur().pos, "property path", "alternative path '|' is not supported")
	}
	return left, nil
}

func (p *parser) parsePathSequence(startPos int) (pathExpr, error) {
	left, err := p.parsePathEltOrInverse(startPos)
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tokSlash {
		slashPos := p.cur().pos
		p.next()
		right, err := p.parsePathEltOrInverse(slashPos)
		if err != nil {
			return nil, err
		}
		left = pathSeq{left: left, right: right}
	}
	return left, nil
}

func (p *parser) parsePathEltOrInverse(startPos int) (pathExpr, error) {
	tok := p.cur()
	if tok.kind == tokCaret {
		return nil, unsupportedErrorf(tok.pos, "property path", "inverse path '^' is not supported")
	}
	if tok.kind == tokBang {
		return nil, unsupportedErrorf(tok.pos, "property path", "negated property set '!' is not supported")
	}
	primary, err := p.parsePathPrimary(startPos)
	if err != nil {
		return nil, err
	}
	switch p.cur().kind {
	case tokStar:
		p.next()
		return pathZeroOrMore{elt: primary}, nil
	case tokPlus:
		if isSignedNumberObject(p.peek(1).kind, p.peek(2).kind) {
			return primary, nil
		}
		return nil, unsupportedErrorf(p.cur().pos, "property path", "one-or-more path '+' is not supported")
	case tokQuestion:
		return nil, unsupportedErrorf(p.cur().pos, "property path", "zero-or-one path '?' is not supported")
	default:
		return primary, nil
	}
}

func isSignedNumberObject(nextKind, afterKind tokenKind) bool {
	if nextKind != tokInteger && nextKind != tokDecimal && nextKind != tokDouble {
		return false
	}
	switch afterKind {
	case tokDot, tokSemi, tokComma, tokRBrace, tokRBracket, tokEOF:
		return true
	default:
		return false
	}
}

func (p *parser) parsePathPrimary(startPos int) (pathExpr, error) {
	tok := p.cur()
	switch tok.kind {
	case tokIdent:
		if tok.value == "a" {
			p.next()
			return pathIRI{iri: RDFTypeIRI}, nil
		}
		return nil, syntaxErrorf(tok.pos, "unexpected identifier %q in predicate position", tok.value)
	case tokIRI:
		p.next()
		resolved, err := p.resolveIRI(tok.value, tok.pos)
		if err != nil {
			return nil, err
		}
		return pathIRI{iri: resolved}, nil
	case tokPrefixed, tokPrefixName:
		p.next()
		resolved, err := p.resolvePrefixed(tok)
		if err != nil {
			return nil, err
		}
		return pathIRI{iri: resolved}, nil
	case tokLParen:
		p.pathDepth++
		defer func() { p.pathDepth-- }()
		if p.pathDepth > maxParserNestingDepth {
			return nil, syntaxErrorf(tok.pos, "property path nesting depth exceeds maximum of %d", maxParserNestingDepth)
		}
		p.next()
		inner, err := p.parsePathAlternative(startPos)
		if err != nil {
			return nil, err
		}
		if _, err := p.expect(tokRParen, "')' in property path"); err != nil {
			return nil, err
		}
		return inner, nil
	default:
		return nil, syntaxErrorf(tok.pos, "expected predicate IRI, variable, or 'a', got %q", tok.value)
	}
}

func (p *parser) parseFilterConstraint() (FilterExpr, error) {
	tok := p.cur()
	if tok.kind == tokLParen {
		return p.parseBrackettedExpression()
	}
	if tok.kind == tokIdent {
		return p.parseBuiltinOrFunctionCall()
	}
	return nil, syntaxErrorf(tok.pos, "expected '(' or built-in function call after FILTER, got %q", tok.value)
}

func (p *parser) parseBrackettedExpression() (FilterExpr, error) {
	p.exprDepth++
	defer func() { p.exprDepth-- }()
	if p.exprDepth > maxParserNestingDepth {
		return nil, syntaxErrorf(p.cur().pos, "expression nesting depth exceeds maximum of %d", maxParserNestingDepth)
	}
	if _, err := p.expect(tokLParen, "'('"); err != nil {
		return nil, err
	}
	expr, err := p.parseOrExpr()
	if err != nil {
		return nil, err
	}
	if _, err := p.expect(tokRParen, "')'"); err != nil {
		return nil, err
	}
	return expr, nil
}

func (p *parser) parseOrExpr() (FilterExpr, error) {
	left, err := p.parseAndExpr()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tokOr {
		p.next()
		right, err := p.parseAndExpr()
		if err != nil {
			return nil, err
		}
		left = BinaryExpr{Op: BinaryOr, Left: left, Right: right}
	}
	return left, nil
}

func (p *parser) parseAndExpr() (FilterExpr, error) {
	left, err := p.parseComparisonExpr()
	if err != nil {
		return nil, err
	}
	for p.cur().kind == tokAnd {
		p.next()
		right, err := p.parseComparisonExpr()
		if err != nil {
			return nil, err
		}
		left = BinaryExpr{Op: BinaryAnd, Left: left, Right: right}
	}
	return left, nil
}

func checkUnsupportedArithmeticToken(tok token) error {
	switch tok.kind {
	case tokPlus, tokMinus, tokStar, tokSlash:
		return unsupportedErrorf(tok.pos, "arithmetic expression ("+tok.value+")", "arithmetic operators are not supported in FILTER")
	default:
		return nil
	}
}

func (p *parser) parseComparisonExpr() (FilterExpr, error) {
	left, err := p.parseUnaryExpr()
	if err != nil {
		return nil, err
	}
	tok := p.cur()
	if err := checkUnsupportedArithmeticToken(tok); err != nil {
		return nil, err
	}
	if tok.kind == tokIdent {
		upper := strings.ToUpper(tok.value)
		if upper == "IN" {
			return nil, unsupportedErrorf(tok.pos, "IN", "FILTER IN is not supported")
		}
		if upper == "NOT" && strings.EqualFold(p.peek(1).value, "IN") {
			return nil, unsupportedErrorf(tok.pos, "IN", "FILTER NOT IN is not supported")
		}
	}
	var op BinaryOp
	switch tok.kind {
	case tokEqual:
		op = BinaryEqual
	case tokNotEqual:
		op = BinaryNotEqual
	case tokLess:
		op = BinaryLess
	case tokLessEq:
		op = BinaryLessEqual
	case tokGreater:
		op = BinaryGreater
	case tokGreaterEq:
		op = BinaryGreaterEqual
	default:
		return left, nil
	}
	p.next()
	right, err := p.parseUnaryExpr()
	if err != nil {
		return nil, err
	}
	if err := checkUnsupportedArithmeticToken(p.cur()); err != nil {
		return nil, err
	}
	return BinaryExpr{Op: op, Left: left, Right: right}, nil
}

func (p *parser) parseUnaryExpr() (FilterExpr, error) {
	p.exprDepth++
	defer func() { p.exprDepth-- }()
	if p.exprDepth > maxParserNestingDepth {
		return nil, syntaxErrorf(p.cur().pos, "expression nesting depth exceeds maximum of %d", maxParserNestingDepth)
	}
	tok := p.cur()
	switch tok.kind {
	case tokBang:
		p.next()
		operand, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: UnaryNot, Operand: operand}, nil
	case tokPlus:
		p.next()
		operand, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: UnaryPlus, Operand: operand}, nil
	case tokMinus:
		p.next()
		operand, err := p.parseUnaryExpr()
		if err != nil {
			return nil, err
		}
		return UnaryExpr{Op: UnaryMinus, Operand: operand}, nil
	default:
		return p.parsePrimaryExpr()
	}
}

func (p *parser) parsePrimaryExpr() (FilterExpr, error) {
	tok := p.cur()
	switch tok.kind {
	case tokLParen:
		return p.parseBrackettedExpression()
	case tokBlankNode, tokLBracket:
		return nil, syntaxErrorf(tok.pos, "blank node %q is not allowed in FILTER or ORDER BY expression", tok.value)
	case tokVar, tokString, tokInteger, tokDecimal, tokDouble:
		term, err := p.parseObjectTerm()
		if err != nil {
			return nil, err
		}
		return TermExpr{Term: term}, nil
	case tokIRI, tokPrefixed, tokPrefixName:
		term, err := p.parseObjectTerm()
		if err != nil {
			return nil, err
		}
		if p.cur().kind == tokLParen {
			return nil, unsupportedErrorf(tok.pos, "custom function "+term.Value, "only BOUND, REGEX, STR, LANG, and DATATYPE are supported")
		}
		return TermExpr{Term: term}, nil
	case tokIdent:
		if strings.EqualFold(tok.value, "true") || strings.EqualFold(tok.value, "false") {
			term, err := p.parseObjectTerm()
			if err != nil {
				return nil, err
			}
			return TermExpr{Term: term}, nil
		}
		return p.parseBuiltinOrFunctionCall()
	default:
		return nil, syntaxErrorf(tok.pos, "expected expression, got %q", tok.value)
	}
}

func (p *parser) parseBuiltinOrFunctionCall() (FilterExpr, error) {
	tok := p.cur()
	upper := strings.ToUpper(tok.value)

	if upper == "EXISTS" {
		return nil, unsupportedErrorf(tok.pos, "EXISTS", "FILTER EXISTS is not supported")
	}
	if upper == "NOT" && strings.EqualFold(p.peek(1).value, "EXISTS") {
		return nil, unsupportedErrorf(tok.pos, "EXISTS", "FILTER NOT EXISTS is not supported")
	}
	if isAggregateKeyword(upper) {
		return nil, unsupportedErrorf(tok.pos, "aggregates ("+upper+")", "aggregate functions are not supported")
	}

	switch BuiltinFunc(upper) {
	case FuncBound:
		p.next()
		if _, err := p.expect(tokLParen, "'(' after BOUND"); err != nil {
			return nil, err
		}
		varTok := p.cur()
		if varTok.kind != tokVar {
			return nil, syntaxErrorf(varTok.pos, "BOUND requires a variable argument, got %q", varTok.value)
		}
		p.next()
		if _, err := p.expect(tokRParen, "')' after BOUND(?var)"); err != nil {
			return nil, err
		}
		return CallExpr{
			Func: FuncBound,
			Args: []FilterExpr{TermExpr{Term: Term{Kind: TermVariable, Value: varTok.value}}},
		}, nil

	case FuncRegex:
		p.next()
		args, err := p.parseFuncArgList(tok.pos, "REGEX", 2, 3)
		if err != nil {
			return nil, err
		}
		return CallExpr{Func: FuncRegex, Args: args}, nil

	case FuncStr, FuncLang, FuncDatatype:
		fn := BuiltinFunc(upper)
		p.next()
		args, err := p.parseFuncArgList(tok.pos, upper, 1, 1)
		if err != nil {
			return nil, err
		}
		return CallExpr{Func: fn, Args: args}, nil

	default:
		if p.peek(1).kind == tokLParen {
			return nil, unsupportedErrorf(tok.pos, upper, "only BOUND, REGEX, STR, LANG, and DATATYPE are supported in FILTER")
		}
		return nil, syntaxErrorf(tok.pos, "unexpected identifier %q in expression", tok.value)
	}
}

func (p *parser) parseFuncArgList(pos int, fnName string, minArgs, maxArgs int) ([]FilterExpr, error) {
	if _, err := p.expect(tokLParen, "'(' after "+fnName); err != nil {
		return nil, err
	}
	if p.cur().kind == tokRParen {
		return nil, syntaxErrorf(pos, "%s requires %d..%d argument(s), got 0", fnName, minArgs, maxArgs)
	}
	var args []FilterExpr
	for {
		arg, err := p.parseOrExpr()
		if err != nil {
			return nil, err
		}
		args = append(args, arg)
		if p.cur().kind != tokComma {
			break
		}
		p.next()
	}
	if _, err := p.expect(tokRParen, "')' after "+fnName+" arguments"); err != nil {
		return nil, err
	}
	if len(args) < minArgs || len(args) > maxArgs {
		return nil, syntaxErrorf(pos, "%s requires %d..%d argument(s), got %d", fnName, minArgs, maxArgs, len(args))
	}
	return args, nil
}

func (p *parser) checkUnsupportedModifierKeyword(tok token) error {
	upper := strings.ToUpper(tok.value)
	switch upper {
	case "GROUP":
		return unsupportedErrorf(tok.pos, "GROUP BY", "GROUP BY is not supported")
	case "HAVING":
		return unsupportedErrorf(tok.pos, "HAVING", "HAVING is not supported")
	case "VALUES":
		return unsupportedErrorf(tok.pos, "VALUES", "VALUES is not supported")
	default:
		return nil
	}
}

func (p *parser) parseSolutionModifiers() error {
	if p.cur().kind == tokIdent {
		if err := p.checkUnsupportedModifierKeyword(p.cur()); err != nil {
			return err
		}
	}

	if p.consumeKeyword("ORDER") {
		if !p.consumeKeyword("BY") {
			return syntaxErrorf(p.cur().pos, "expected BY after ORDER")
		}
		for {
			if p.cur().kind == tokEOF || p.isKeyword("LIMIT") || p.isKeyword("OFFSET") || p.isKeyword("VALUES") || p.isKeyword("GROUP") || p.isKeyword("HAVING") {
				break
			}
			cond, err := p.parseOrderCondition()
			if err != nil {
				return err
			}
			p.query.OrderBy = append(p.query.OrderBy, cond)
		}
		if len(p.query.OrderBy) == 0 {
			return syntaxErrorf(p.cur().pos, "ORDER BY requires at least one order condition")
		}
	}

	if p.cur().kind == tokIdent {
		if err := p.checkUnsupportedModifierKeyword(p.cur()); err != nil {
			return err
		}
	}

	hasLimit := false
	hasOffset := false
	for p.isKeyword("LIMIT") || p.isKeyword("OFFSET") {
		if p.consumeKeyword("LIMIT") {
			if hasLimit {
				return syntaxErrorf(p.cur().pos, "duplicate LIMIT clause")
			}
			val, err := p.parseNonNegativeInt("LIMIT")
			if err != nil {
				return err
			}
			hasLimit = true
			p.query.Limit = &val
			continue
		}
		if p.consumeKeyword("OFFSET") {
			if hasOffset {
				return syntaxErrorf(p.cur().pos, "duplicate OFFSET clause")
			}
			val, err := p.parseNonNegativeInt("OFFSET")
			if err != nil {
				return err
			}
			hasOffset = true
			p.query.Offset = val
			continue
		}
	}

	if p.cur().kind == tokIdent {
		if err := p.checkUnsupportedModifierKeyword(p.cur()); err != nil {
			return err
		}
	}
	return nil
}

func (p *parser) parseOrderCondition() (OrderCondition, error) {
	if p.consumeKeyword("ASC") {
		expr, err := p.parseBrackettedExpression()
		if err != nil {
			return OrderCondition{}, err
		}
		return OrderCondition{Direction: OrderAsc, Expr: expr}, nil
	}
	if p.consumeKeyword("DESC") {
		expr, err := p.parseBrackettedExpression()
		if err != nil {
			return OrderCondition{}, err
		}
		return OrderCondition{Direction: OrderDesc, Expr: expr}, nil
	}
	tok := p.cur()
	if tok.kind == tokVar {
		p.next()
		return OrderCondition{
			Direction: OrderAsc,
			Expr:      TermExpr{Term: Term{Kind: TermVariable, Value: tok.value}},
		}, nil
	}
	if tok.kind == tokLParen {
		expr, err := p.parseBrackettedExpression()
		if err != nil {
			return OrderCondition{}, err
		}
		return OrderCondition{Direction: OrderAsc, Expr: expr}, nil
	}
	if tok.kind == tokIdent {
		expr, err := p.parseBuiltinOrFunctionCall()
		if err != nil {
			return OrderCondition{}, err
		}
		return OrderCondition{Direction: OrderAsc, Expr: expr}, nil
	}
	return OrderCondition{}, syntaxErrorf(tok.pos, "expected ORDER BY condition (variable, ASC(...), DESC(...), or expression), got %q", tok.value)
}

func (p *parser) parseNonNegativeInt(clause string) (int, error) {
	tok := p.cur()
	if tok.kind != tokInteger {
		return 0, syntaxErrorf(tok.pos, "%s requires a non-negative integer, got %q", clause, tok.value)
	}
	p.next()
	v, err := strconv.Atoi(tok.value)
	if err != nil || v < 0 {
		return 0, syntaxErrorf(tok.pos, "invalid %s integer %q", clause, tok.value)
	}
	return v, nil
}
