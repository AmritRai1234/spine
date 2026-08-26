package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

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
	resolved, err := mathResolveTokens(expr, eventName, payload)
	if err != nil {
		return fmt.Errorf("math.calc '%s': %w", setKey, err)
	}
	val, err := evalArithmetic(resolved)
	if err != nil {
		return fmt.Errorf("math.calc '%s': %w", setKey, err)
	}
	payload[setKey] = val
	return nil
}

// mathResolveTokens substitutes $event.payload.PATH and $env.KEY tokens in a
// math expression with PLAIN-NUMBER literals only. A resolved payload value is
// an OPERAND, never expression text: a payload of "0 + 9999" must fail the
// step, not rewrite the expression (otherwise a client could forge computed
// totals — exactly what server-side math.calc is meant to prevent). $now,
// $uuid and $event.name are not numbers and are left for the parser to reject
// loudly.
func mathResolveTokens(expr string, eventName string, payload map[string]interface{}) (string, error) {
	if strings.IndexByte(expr, '$') == -1 {
		return expr, nil
	}
	var res strings.Builder
	idx := 0
	for idx < len(expr) {
		if expr[idx] != '$' {
			res.WriteByte(expr[idx])
			idx++
			continue
		}
		// Token boundary: identifier chars (mirrors ResolveVariables).
		end := idx + 1
		for end < len(expr) {
			c := expr[end]
			if c == '_' || c == '.' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				end++
				continue
			}
			break
		}
		token := expr[idx:end]

		var raw interface{}
		switch {
		case strings.HasPrefix(token, "$event.payload."):
			var ok bool
			raw, ok = resolvePath(payload, token[len("$event.payload."):])
			if !ok {
				return "", fmt.Errorf("unresolvable operand %q", token)
			}
		case strings.HasPrefix(token, "$env."):
			raw = os.Getenv(token[len("$env."):])
		default:
			// $now/$uuid/$event.name and anything else: not numeric operands.
			res.WriteString(token)
			idx = end
			continue
		}

		numStr, err := mathOperandString(raw)
		if err != nil {
			return "", fmt.Errorf("operand %q must be a plain number, got %v", token, raw)
		}
		res.WriteString(numStr)
		idx = end
	}
	return res.String(), nil
}

// mathOperandString converts a resolved payload/env value into a plain-number
// literal, rejecting anything that is not a decimal number. Strings may carry
// one optional leading '-' (a negative operand is safe: the parser treats it
// as unary minus); everything else — "0 + 9999", "1; DROP TABLE", booleans,
// maps — is rejected.
func mathOperandString(v interface{}) (string, error) {
	switch val := v.(type) {
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), nil
	case float32:
		return strconv.FormatFloat(float64(val), 'f', -1, 64), nil
	case int:
		return strconv.Itoa(val), nil
	case int64:
		return strconv.FormatInt(val, 10), nil
	case json.Number:
		return string(val), nil
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return "", fmt.Errorf("empty operand")
		}
		body := s
		if body[0] == '-' {
			body = body[1:]
		}
		if !isPlainNumber(body) {
			return "", fmt.Errorf("not a plain number")
		}
		return s, nil
	default:
		return "", fmt.Errorf("unsupported operand type %T", v)
	}
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
