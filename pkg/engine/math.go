package engine

import (
	"fmt"
	"strconv"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// mathCalc evaluates a safe arithmetic expression over payload values and
// stores the result in payload[set]. Added for the commerce template so that
// order totals (shipping, tax, discounts) are computed SERVER-SIDE — the
// client sends only facts (quantities, country), never dollar amounts.
//
// Expression grammar: plain decimal numbers, payload variables
// ($event.payload.x), + - * /, parentheses, unary minus. Any other character
// is rejected, so a payload-injected value can never smuggle SQL, function
// calls, or scientific-notation tricks into the expression. Division by zero
// and unparsable operands are hard errors that fail the route step.
func (b *Bus) mathCalc(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	setKey := step.Config["set"]
	expr := step.Config["expr"]
	if setKey == "" || expr == "" {
		return fmt.Errorf("math.calc requires 'set' and 'expr' config")
	}
	resolved := ResolveVariables(expr, eventName, payload)
	val, err := evalArithmetic(resolved)
	if err != nil {
		return fmt.Errorf("math.calc '%s': %w", setKey, err)
	}
	payload[setKey] = val
	return nil
}

// evalArithmetic parses and evaluates expr with + - * / and parentheses.
func evalArithmetic(expr string) (float64, error) {
	p := &arithParser{src: expr}
	p.skipSpace()
	v, err := p.parseExpr()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos < len(p.src) {
		return 0, fmt.Errorf("unexpected character %q at position %d", p.src[p.pos], p.pos)
	}
	return v, nil
}

// arithParser is a tiny recursive-descent parser for the safe arithmetic
// grammar. It never executes anything — just numbers and operators.
type arithParser struct {
	src string
	pos int
}

func (p *arithParser) skipSpace() {
	for p.pos < len(p.src) && (p.src[p.pos] == ' ' || p.src[p.pos] == '\t') {
		p.pos++
	}
}

func (p *arithParser) peek() byte {
	if p.pos < len(p.src) {
		return p.src[p.pos]
	}
	return 0
}

// parseExpr handles + and - (lowest precedence).
func (p *arithParser) parseExpr() (float64, error) {
	v, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		c := p.peek()
		if c != '+' && c != '-' {
			return v, nil
		}
		p.pos++
		rhs, err := p.parseTerm()
		if err != nil {
			return 0, err
		}
		if c == '+' {
			v += rhs
		} else {
			v -= rhs
		}
	}
}

// parseTerm handles * and / (middle precedence).
func (p *arithParser) parseTerm() (float64, error) {
	v, err := p.parseFactor()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		c := p.peek()
		if c != '*' && c != '/' {
			return v, nil
		}
		p.pos++
		rhs, err := p.parseFactor()
		if err != nil {
			return 0, err
		}
		if c == '*' {
			v *= rhs
		} else {
			if rhs == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			v /= rhs
		}
	}
}

// parseFactor handles unary minus, parenthesized sub-expressions, and numbers.
func (p *arithParser) parseFactor() (float64, error) {
	p.skipSpace()
	c := p.peek()
	if c == '-' {
		p.pos++
		v, err := p.parseFactor()
		return -v, err
	}
	if c == '(' {
		p.pos++
		v, err := p.parseExpr()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return v, nil
	}
	if c == 0 {
		return 0, fmt.Errorf("unexpected end of expression")
	}

	start := p.pos
	for p.pos < len(p.src) {
		ch := p.src[p.pos]
		if (ch >= '0' && ch <= '9') || ch == '.' {
			p.pos++
			continue
		}
		break
	}
	if p.pos == start {
		return 0, fmt.Errorf("unexpected character %q at position %d", c, p.pos)
	}
	numStr := p.src[start:p.pos]
	if !isPlainNumber(numStr) {
		return 0, fmt.Errorf("invalid number %q", numStr)
	}
	v, err := strconv.ParseFloat(numStr, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", numStr, err)
	}
	return v, nil
}

// isPlainNumber rejects scientific notation ("1e3"), hex ("0x10"), and any
// sign inside a token — signs are parser grammar, digits and at most one dot
// are all a plain decimal may contain.
func isPlainNumber(s string) bool {
	if s == "" {
		return false
	}
	dots := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' {
			dots++
			if dots > 1 {
				return false
			}
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
