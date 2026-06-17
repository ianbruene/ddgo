package macro

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

type EvalContext struct {
	Offsets WCSOffsets
	Vars    *VariableStore
}

func expressionNeedsWCS(input string) bool {
	p := &exprParser{input: input}
	for {
		p.skipSpace()
		if p.pos >= len(p.input) {
			return false
		}
		if ref, ok := p.scanWCSRef(); ok {
			_ = ref
			return true
		}
		_, _ = p.nextRune()
	}
}

func EvalArithmeticExpression(input string, ctx EvalContext) (float64, error) {
	p := &exprParser{input: input, ctx: ctx}
	v, err := p.parseExpression()
	if err != nil {
		return 0, fmt.Errorf("invalid expression: %w", err)
	}
	p.skipSpace()
	if p.pos != len(p.input) {
		return 0, fmt.Errorf("invalid expression: unexpected token %q", p.input[p.pos:])
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("non-finite expression result")
	}
	return v, nil
}

func EvalOperand(input string, ctx EvalContext) (float64, error) {
	return EvalArithmeticExpression(input, ctx)
}

type exprParser struct {
	input string
	pos   int
	ctx   EvalContext
}

func (p *exprParser) parseExpression() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.match('+') {
			r, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			v += r
			continue
		}
		if p.match('-') {
			r, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			v -= r
			continue
		}
		return v, nil
	}
}
func (p *exprParser) parseTerm() (float64, error) {
	v, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		if p.match('*') {
			r, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			v *= r
			continue
		}
		if p.match('/') {
			r, err := p.parseFactor()
			if err != nil {
				return 0, err
			}
			if r == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= r
			continue
		}
		return v, nil
	}
}
func (p *exprParser) parseFactor() (float64, error) {
	p.skipSpace()
	if p.pos >= len(p.input) {
		return 0, fmt.Errorf("incomplete expression")
	}
	if p.match('+') {
		return p.parseFactor()
	}
	if p.match('-') {
		v, err := p.parseFactor()
		return -v, err
	}
	if p.match('(') {
		v, err := p.parseExpression()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if !p.match(')') {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		return v, nil
	}
	if ref, ok := p.scanWCSRef(); ok {
		return (WCSResolver{Offsets: p.ctx.Offsets}).Resolve(ref)
	}
	if p.peekRune() == 'G' || p.peekRune() == 'g' {
		tok := p.scanIdent()
		if len(tok) == 4 && strings.HasPrefix(strings.ToUpper(tok), "G") {
			_, err := ParseWCSAxisRef(tok)
			if err != nil {
				return 0, err
			}
		}
		if p.ctx.Vars != nil {
			if v, ok := p.ctx.Vars.Get(tok); ok {
				return v, nil
			}
		}
		return 0, fmt.Errorf("unknown variable %q", tok)
	}
	if isIdentStart(p.peekRune()) {
		name := p.scanIdent()
		if p.ctx.Vars == nil {
			return 0, fmt.Errorf("variable store is not available")
		}
		v, ok := p.ctx.Vars.Get(name)
		if !ok {
			return 0, fmt.Errorf("unknown variable %q", name)
		}
		return v, nil
	}
	return p.scanNumber()
}
func (p *exprParser) scanNumber() (float64, error) {
	start := p.pos
	hasDigit := false
	for p.pos < len(p.input) && unicode.IsDigit(rune(p.input[p.pos])) {
		p.pos++
		hasDigit = true
	}
	if p.pos < len(p.input) && p.input[p.pos] == '.' {
		p.pos++
		for p.pos < len(p.input) && unicode.IsDigit(rune(p.input[p.pos])) {
			p.pos++
			hasDigit = true
		}
	}
	if !hasDigit {
		return 0, fmt.Errorf("unexpected token %q", string(p.peekRune()))
	}
	if p.pos < len(p.input) && (p.input[p.pos] == 'e' || p.input[p.pos] == 'E') {
		ep := p.pos
		p.pos++
		if p.pos < len(p.input) && (p.input[p.pos] == '+' || p.input[p.pos] == '-') {
			p.pos++
		}
		expStart := p.pos
		for p.pos < len(p.input) && unicode.IsDigit(rune(p.input[p.pos])) {
			p.pos++
		}
		if expStart == p.pos {
			p.pos = ep
		}
	}
	v, err := strconv.ParseFloat(p.input[start:p.pos], 64)
	if err != nil {
		if strings.Contains(err.Error(), "value out of range") {
			return 0, fmt.Errorf("non-finite expression result")
		}
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("non-finite expression result")
	}
	return v, nil
}
func (p *exprParser) scanWCSRef() (WCSAxisRef, bool) {
	if p.pos+4 > len(p.input) {
		return WCSAxisRef{}, false
	}
	if p.pos > 0 && isIdentPart(rune(p.input[p.pos-1])) {
		return WCSAxisRef{}, false
	}
	rem := p.input[p.pos:]
	if len(rem) >= 4 && (rem[0] == 'G' || rem[0] == 'g') && rem[1] == '5' && rem[2] >= '4' && rem[2] <= '9' && strings.ContainsRune("XxYyZz", rune(rem[3])) {
		if len(rem) > 4 && isIdentPart(rune(rem[4])) {
			return WCSAxisRef{}, false
		}

		ref, err := ParseWCSAxisRef(rem[:4])
		if err == nil {
			p.pos += 4
			return ref, true
		}
	}
	return WCSAxisRef{}, false
}
func (p *exprParser) scanIdent() string {
	start := p.pos
	for p.pos < len(p.input) && isIdentPart(rune(p.input[p.pos])) {
		p.pos++
	}
	return p.input[start:p.pos]
}
func (p *exprParser) skipSpace() {
	for p.pos < len(p.input) && unicode.IsSpace(rune(p.input[p.pos])) {
		p.pos++
	}
}
func (p *exprParser) match(ch byte) bool {
	if p.pos < len(p.input) && p.input[p.pos] == ch {
		p.pos++
		return true
	}
	return false
}
func (p *exprParser) peekRune() rune {
	if p.pos >= len(p.input) {
		return 0
	}
	return rune(p.input[p.pos])
}
func (p *exprParser) nextRune() (rune, int) { r := rune(p.input[p.pos]); p.pos++; return r, 1 }
func isIdentStart(r rune) bool              { return r == '_' || unicode.IsLetter(r) }
func isIdentPart(r rune) bool               { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
