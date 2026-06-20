package graph

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// Condition expression parser shared by If-Else (Condition) and loop
// "until" (UntilCondition). This file implements ONLY the static syntax
// parse/validation required at save time. Runtime evaluation (variable
// existence checks, string comparison with options) belongs to the execution
// engine and is intentionally not implemented here.
//
// Grammar (§1 of the design doc), precedence high→low:
//
//	()  >  非  >  比较  >  且  >  或   (same level left-associative)
//
//	expr       := orExpr
//	orExpr     := andExpr ( '或' andExpr )*
//	andExpr    := notExpr ( '且' notExpr )*
//	notExpr    := '非' notExpr | primary
//	primary    := '(' orExpr ')' | comparison
//	comparison := operand BINOP operand option* | operand UNOP option*
//	operand    := '{{' name '}}' | '"' string '"'
//	BINOP      := == | != | > | >= | < | <= | StartWith | EndWith
//	UNOP       := 是偶数
//	option     := 忽略大小写 | 忽略空格
//
// 是偶数 is a postfix unary operator: it takes only a left operand and is true
// when that operand parses as an integer whose value is even. A non-numeric
// operand makes it false (never an evaluation error).
//
// A bare operand is never a boolean term — `{{x}}` alone is rejected, every
// `{{var}}` must appear inside an explicit comparison.

// ConditionExpr is the parsed AST root. Exported so the future execution
// engine can evaluate it; this module only constructs and syntactically
// validates it.
type ConditionExpr interface {
	isCondExpr()
}

// CondBinary is a 且/或 node.
type CondBinary struct {
	Op    string // "且" or "或"
	Left  ConditionExpr
	Right ConditionExpr
}

// CondNot is a 非 node.
type CondNot struct {
	X ConditionExpr
}

// CondCompare is a leaf comparison.
type CondCompare struct {
	Left        CondOperand
	Op          string
	Right       CondOperand
	IgnoreCase  bool
	IgnoreSpace bool
}

// CondOperand is a variable reference or a string literal.
type CondOperand struct {
	IsVar bool
	Var   string // variable name when IsVar
	Lit   string // literal value (already unescaped) otherwise
}

func (*CondBinary) isCondExpr()  {}
func (*CondNot) isCondExpr()     {}
func (*CondCompare) isCondExpr() {}

const (
	optIgnoreCase  = "忽略大小写"
	optIgnoreSpace = "忽略空格"
)

// Unary (postfix) operators take only a left operand. They are written as a
// trailing keyword, e.g. `{{n}} 是偶数`.
const opIsEven = "是偶数"

// unaryOperators lists every postfix unary operator keyword.
var unaryOperators = []string{opIsEven}

func isUnaryOp(op string) bool {
	return slices.Contains(unaryOperators, op)
}

type tokenKind int

const (
	tkEOF tokenKind = iota
	tkVar
	tkStr
	tkOp
	tkAnd // 且
	tkOr  // 或
	tkNot // 非
	tkLParen
	tkRParen
	tkOptIgnoreCase
	tkOptIgnoreSpace
)

type token struct {
	kind tokenKind
	val  string // variable name / literal value / operator text
	pos  int    // rune offset in the source for error messages
}

// ParseCondition parses and statically validates a condition expression.
// Returns a descriptive error (with position) on any unknown token, illegal
// escape, unterminated string, unmatched parenthesis, missing operator, or
// bare-variable truth test.
func ParseCondition(expr string) (ConditionExpr, error) {
	toks, err := tokenizeCondition(expr)
	if err != nil {
		return nil, err
	}
	p := &condParser{toks: toks}
	node, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tkEOF {
		t := p.peek()
		return nil, fmt.Errorf("unexpected token %q at position %d", t.val, t.pos)
	}
	return node, nil
}

// --- tokenizer ---

var compareOperators = []string{"==", "!=", ">=", "<=", ">", "<"}

func tokenizeCondition(expr string) ([]token, error) {
	rs := []rune(expr)
	var toks []token
	i := 0
	n := len(rs)
	for i < n {
		r := rs[i]
		if unicode.IsSpace(r) {
			i++
			continue
		}
		switch {
		case r == '(':
			toks = append(toks, token{kind: tkLParen, val: "(", pos: i})
			i++
		case r == ')':
			toks = append(toks, token{kind: tkRParen, val: ")", pos: i})
			i++
		case r == '{':
			// variable reference {{name}}
			if i+1 >= n || rs[i+1] != '{' {
				return nil, fmt.Errorf("unexpected '{' at position %d (expected '{{' to open a variable reference)", i)
			}
			tok, next, err := scanVariable(rs, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = next
		case r == '"':
			tok, next, err := scanString(rs, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = next
		case isCompareOpStart(r):
			tok, next, err := scanCompareOp(rs, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = next
		case strings.HasPrefix(string(rs[i:]), opIsEven):
			toks = append(toks, token{kind: tkOp, val: opIsEven, pos: i})
			i += len([]rune(opIsEven))
		case r == '且':
			toks = append(toks, token{kind: tkAnd, val: "且", pos: i})
			i++
		case r == '或':
			toks = append(toks, token{kind: tkOr, val: "或", pos: i})
			i++
		case r == '非':
			toks = append(toks, token{kind: tkNot, val: "非", pos: i})
			i++
		case strings.HasPrefix(string(rs[i:]), optIgnoreCase):
			toks = append(toks, token{kind: tkOptIgnoreCase, val: optIgnoreCase, pos: i})
			i += len([]rune(optIgnoreCase))
		case strings.HasPrefix(string(rs[i:]), optIgnoreSpace):
			toks = append(toks, token{kind: tkOptIgnoreSpace, val: optIgnoreSpace, pos: i})
			i += len([]rune(optIgnoreSpace))
		case isASCIILetter(r):
			// Word operators StartWith / EndWith are the only legal ASCII
			// identifiers; anything else is an unknown token.
			tok, next, err := scanWordOperator(rs, i)
			if err != nil {
				return nil, err
			}
			toks = append(toks, tok)
			i = next
		default:
			return nil, fmt.Errorf("unknown token %q at position %d", string(r), i)
		}
	}
	toks = append(toks, token{kind: tkEOF, pos: n})
	return toks, nil
}

func scanVariable(rs []rune, start int) (token, int, error) {
	// rs[start]=='{' and rs[start+1]=='{'
	i := start + 2
	n := len(rs)
	var name []rune
	for i < n {
		if rs[i] == '}' {
			if i+1 < n && rs[i+1] == '}' {
				varName := string(name)
				if !isValidVarName(varName) {
					return token{}, 0, fmt.Errorf("invalid variable name %q at position %d (must match [A-Za-z_][A-Za-z0-9_]*)", varName, start)
				}
				return token{kind: tkVar, val: varName, pos: start}, i + 2, nil
			}
			return token{}, 0, fmt.Errorf("unexpected '}' inside variable reference at position %d", i)
		}
		name = append(name, rs[i])
		i++
	}
	return token{}, 0, fmt.Errorf("unterminated variable reference starting at position %d", start)
}

func scanString(rs []rune, start int) (token, int, error) {
	// rs[start]=='"'
	i := start + 1
	n := len(rs)
	var sb strings.Builder
	for i < n {
		c := rs[i]
		switch c {
		case '"':
			return token{kind: tkStr, val: sb.String(), pos: start}, i + 1, nil
		case '\n', '\r':
			return token{}, 0, fmt.Errorf("string literal at position %d cannot span multiple lines", start)
		case '\\':
			if i+1 >= n {
				return token{}, 0, fmt.Errorf("dangling escape at position %d", i)
			}
			esc := rs[i+1]
			switch esc {
			case '"':
				sb.WriteRune('"')
			case '\\':
				sb.WriteRune('\\')
			default:
				return token{}, 0, fmt.Errorf("illegal escape %q at position %d (only \\\" and \\\\ are allowed)", "\\"+string(esc), i)
			}
			i += 2
		default:
			sb.WriteRune(c)
			i++
		}
	}
	return token{}, 0, fmt.Errorf("unterminated string literal starting at position %d", start)
}

func isCompareOpStart(r rune) bool {
	return r == '=' || r == '!' || r == '>' || r == '<'
}

func scanCompareOp(rs []rune, start int) (token, int, error) {
	rest := string(rs[start:])
	for _, op := range compareOperators {
		if strings.HasPrefix(rest, op) {
			return token{kind: tkOp, val: op, pos: start}, start + len([]rune(op)), nil
		}
	}
	// '=' alone or '!' alone are not valid operators.
	return token{}, 0, fmt.Errorf("invalid operator starting at position %d (did you mean '==', '!=', '>=' or '<='?)", start)
}

func scanWordOperator(rs []rune, start int) (token, int, error) {
	i := start
	n := len(rs)
	for i < n && isASCIILetter(rs[i]) {
		i++
	}
	word := string(rs[start:i])
	switch word {
	case "StartWith", "EndWith":
		return token{kind: tkOp, val: word, pos: start}, i, nil
	default:
		return token{}, 0, fmt.Errorf("unknown identifier %q at position %d (only StartWith/EndWith are valid word operators)", word, start)
	}
}

func isASCIILetter(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isValidVarName(name string) bool {
	if name == "" {
		return false
	}
	for idx, r := range name {
		if idx == 0 {
			if !(isASCIILetter(r) || r == '_') {
				return false
			}
			continue
		}
		if !(isASCIILetter(r) || r == '_' || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

// --- parser (recursive descent) ---

type condParser struct {
	toks []token
	idx  int
}

func (p *condParser) peek() token { return p.toks[p.idx] }

func (p *condParser) next() token {
	t := p.toks[p.idx]
	if p.idx < len(p.toks)-1 {
		p.idx++
	}
	return t
}

func (p *condParser) parseOr() (ConditionExpr, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tkOr {
		p.next()
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = &CondBinary{Op: "或", Left: left, Right: right}
	}
	return left, nil
}

func (p *condParser) parseAnd() (ConditionExpr, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.peek().kind == tkAnd {
		p.next()
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = &CondBinary{Op: "且", Left: left, Right: right}
	}
	return left, nil
}

func (p *condParser) parseNot() (ConditionExpr, error) {
	if p.peek().kind == tkNot {
		p.next()
		x, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return &CondNot{X: x}, nil
	}
	return p.parsePrimary()
}

func (p *condParser) parsePrimary() (ConditionExpr, error) {
	t := p.peek()
	if t.kind == tkLParen {
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if p.peek().kind != tkRParen {
			return nil, fmt.Errorf("missing closing ')' for group opened at position %d", t.pos)
		}
		p.next()
		return inner, nil
	}
	return p.parseComparison()
}

func (p *condParser) parseComparison() (ConditionExpr, error) {
	left, err := p.parseOperand()
	if err != nil {
		return nil, err
	}
	opTok := p.peek()
	if opTok.kind != tkOp {
		return nil, fmt.Errorf("expected a comparison operator after operand at position %d (bare variables are not allowed as truth tests; use an explicit comparison)", opTok.pos)
	}
	p.next()
	cmp := &CondCompare{Left: left, Op: opTok.val}
	// Binary operators consume a right operand; unary (postfix) operators do not.
	if !isUnaryOp(opTok.val) {
		right, err := p.parseOperand()
		if err != nil {
			return nil, err
		}
		cmp.Right = right
	}
	// Greedily consume comparison options.
	for {
		switch p.peek().kind {
		case tkOptIgnoreCase:
			cmp.IgnoreCase = true
			p.next()
		case tkOptIgnoreSpace:
			cmp.IgnoreSpace = true
			p.next()
		default:
			return cmp, nil
		}
	}
}

func (p *condParser) parseOperand() (CondOperand, error) {
	t := p.peek()
	switch t.kind {
	case tkVar:
		p.next()
		return CondOperand{IsVar: true, Var: t.val}, nil
	case tkStr:
		p.next()
		return CondOperand{IsVar: false, Lit: t.val}, nil
	default:
		return CondOperand{}, fmt.Errorf("expected a variable reference or string literal at position %d, got %q", t.pos, t.val)
	}
}
