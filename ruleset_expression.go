package main

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	maxExpressionBytes = 4096
	maxExpressionNodes = 256
	maxExpressionDepth = 48
	maxExpressionSteps = 4096
	maxExpressionSides = 10000
	maxRationalBits    = 128
)

// The lexer consumes the entire input. Identifiers and field access never
// dispatch to host functions or methods.
var expressionToken = regexp.MustCompile(`^(?:[0-9]+[dD][0-9]+|[0-9]+|[\pL_][\pL\pN_]*|"(?:\\.|[^"\\\r\n])*"|'(?:\\.|[^'\\\r\n])*'|==|!=|<=|>=|&&|\|\||[.+*/%()!<>-])`)

type exprNode struct {
	op            string
	offset, depth int
	literal       any
	path          []string
	address       string
	readType      exprType
	left, right   *exprNode
}
type exprParser struct {
	source, token   string
	pos, end, nodes int
}

func (p *exprParser) advance() {
	p.pos = p.end
	for p.pos < len(p.source) && strings.ContainsRune(" \r\n\t", rune(p.source[p.pos])) {
		p.pos++
	}
	p.token = expressionToken.FindString(p.source[p.pos:])
	p.end = p.pos + len(p.token)
	if p.token == "" && p.pos < len(p.source) {
		p.token = "<invalid>"
	}
}

var exprPrecedence = map[string]int{"||": 1, "or": 1, "&&": 2, "and": 2, "==": 3, "!=": 3, "<": 4, ">": 4, "<=": 4, ">=": 4, "+": 5, "-": 5, "*": 6, "/": 6, "%": 6}

func parseExpression(source string) (*exprNode, error) {
	if len(source) > maxExpressionBytes || !utf8.ValidString(source) {
		return nil, fmt.Errorf("invalid expression length or encoding")
	}
	p := exprParser{source: source}
	p.advance()
	node, err := p.parse(1, 1)
	if err == nil && p.token != "" {
		err = fmt.Errorf("byte %d: unexpected %q", p.pos, p.token)
	}
	return node, err
}
func (p *exprParser) parse(minPrec, depth int) (*exprNode, error) {
	if depth > maxExpressionDepth {
		return nil, fmt.Errorf("byte %d: expression nesting limit", p.pos)
	}
	t := p.token
	start := p.pos
	n := &exprNode{offset: start, depth: 1}
	if t == "" || t == "<invalid>" {
		return nil, fmt.Errorf("byte %d: expected expression", p.pos)
	}
	p.advance()
	var err error
	switch {
	case t == "(":
		n, err = p.parse(1, depth+1)
		if err == nil {
			err = p.closeParen()
		}
	case t == "!" || t == "not" || t == "-" || t == "+":
		n.op = "unary" + t
		// Parse a signed literal as a unit so MinInt64 is representable.
		if t == "-" && p.token != "" && p.token[0] >= '0' && p.token[0] <= '9' && !strings.ContainsAny(p.token, "dD") {
			n.op = "literal"
			n.literal, err = strconv.ParseInt("-"+p.token, 10, 64)
			p.advance()
		} else {
			precedence := 7
			if t == "not" {
				precedence = exprPrecedence["=="]
			}
			n.left, err = p.parse(precedence, depth+1)
		}
	case t[0] >= '0' && t[0] <= '9':
		n.op = "literal"
		if strings.ContainsAny(t, "dD") {
			n.op = "throw"
			n.literal, err = parseExprDice(t)
		} else {
			n.literal, err = strconv.ParseInt(t, 10, 64)
		}
	case t[0] == '"' || t[0] == '\'':
		n.op = "literal"
		n.literal, err = exprString(t)
	case t == "true" || t == "false":
		n.op = "literal"
		n.literal = t == "true"
	case isExprName(t):
		if p.token == "(" {
			if t != "floor" && t != "dice" {
				return nil, fmt.Errorf("byte %d: unknown function %q", n.offset, t)
			}
			n.op = t
			p.advance()
			n.left, err = p.parse(1, depth+1)
			if err == nil {
				err = p.closeParen()
			}
		} else {
			n.op = "read"
			n.path = []string{t}
			for p.token == "." {
				p.advance()
				key := p.token
				if key != "" && (key[0] == '"' || key[0] == '\'') {
					key, err = exprString(key)
					if err != nil {
						break
					}
				} else if !isExprName(key) {
					return nil, fmt.Errorf("byte %d: expected field name", p.pos)
				}
				n.path = append(n.path, key)
				p.advance()
			}
			n.address = rulesetPath(n.path)
		}
	default:
		return nil, fmt.Errorf("byte %d: unexpected %q", n.offset, t)
	}
	if err != nil {
		return nil, fmt.Errorf("byte %d: %w", start, err)
	}
	if t != "(" {
		if err = p.count(n); err != nil {
			return nil, err
		}
	}
	for exprPrecedence[p.token] >= minPrec {
		op, pos, prec := p.token, p.pos, exprPrecedence[p.token]
		p.advance()
		right, err := p.parse(prec+1, depth+1)
		if err != nil {
			return nil, err
		}
		n = &exprNode{op: op, offset: pos, left: n, right: right}
		if err = p.count(n); err != nil {
			return nil, err
		}
	}
	return n, nil
}
func (p *exprParser) count(n *exprNode) error {
	n.depth = 1
	if n.left != nil {
		n.depth = n.left.depth + 1
	}
	if n.right != nil {
		n.depth = max(n.depth, n.right.depth+1)
	}
	p.nodes++
	if p.nodes > maxExpressionNodes || n.depth > maxExpressionDepth {
		return fmt.Errorf("byte %d: expression complexity limit", n.offset)
	}
	return nil
}
func (p *exprParser) closeParen() error {
	if p.token != ")" {
		return fmt.Errorf("byte %d: expected closing parenthesis", p.pos)
	}
	p.advance()
	return nil
}
func isExprName(t string) bool {
	return t != "" && (t[0] == '_' || t[0] >= 'a' && t[0] <= 'z' || t[0] >= 'A' && t[0] <= 'Z' || t[0] >= 128)
}
func exprString(t string) (string, error) {
	quote := t[0]
	body := t[1 : len(t)-1]
	var out strings.Builder
	for body != "" {
		ch, _, rest, err := strconv.UnquoteChar(body, quote)
		if err != nil {
			return "", err
		}
		out.WriteRune(ch)
		body = rest
	}
	return out.String(), nil
}

type exprDice struct{ count, sides int }

func parseExprDice(s string) (exprDice, error) {
	if len(s) > 20 {
		return exprDice{}, fmt.Errorf("invalid dice notation")
	}
	i := strings.IndexAny(s, "dD")
	if i < 1 || i == len(s)-1 {
		return exprDice{}, fmt.Errorf("expected NdM")
	}
	for j, c := range s {
		if j != i && (c < '0' || c > '9') {
			return exprDice{}, fmt.Errorf("expected NdM")
		}
	}
	count, e := strconv.Atoi(s[:i])
	sides, e2 := strconv.Atoi(s[i+1:])
	if e != nil || e2 != nil || count < 1 || count > MaxDicePerRoll || sides < 1 || sides > maxExpressionSides {
		return exprDice{}, fmt.Errorf("dice exceed limits")
	}
	return exprDice{count, sides}, nil
}

type exprType string

const (
	exprDynamic exprType = "contextual scalar"
	exprNumber  exprType = "number"
	exprBool    exprType = "boolean"
	exprText    exprType = "string"
)

func exprFits(actual, want exprType) bool {
	return actual == want || actual == exprDynamic || want == exprDynamic
}
func exprSignature(op string) (input, output exprType) {
	switch op {
	case "unary!", "unarynot", "and", "&&", "or", "||":
		return exprBool, exprBool
	case "==", "!=":
		return exprDynamic, exprBool
	case "<", "<=", ">", ">=":
		return exprNumber, exprBool
	case "dice":
		return exprText, exprNumber
	default:
		return exprNumber, exprNumber
	}
}
func literalType(v any) exprType {
	switch v.(type) {
	case string:
		return exprText
	case bool:
		return exprBool
	default:
		return exprNumber
	}
}

type Expression struct {
	root                           *exprNode
	Type                           exprType
	Dependencies                   []string
	UsesRandom, UsesOptionalTarget bool
	integer                        bool
	matchTarget                    bool
}
type exprResolver func([]string) (exprType, error)

func checkExpression(root *exprNode, resolve exprResolver, random bool) (*Expression, error) {
	program := &Expression{root: root}
	seen := map[string]bool{}
	var check func(*exprNode) (exprType, error)
	check = func(n *exprNode) (exprType, error) {
		switch n.op {
		case "literal":
			return literalType(n.literal), nil
		case "read":
			typ, err := resolve(n.path)
			if err != nil {
				return "", fmt.Errorf("byte %d: %w", n.offset, err)
			}
			if n.readType == "" {
				n.readType = typ
			} else if n.readType != typ {
				n.readType = exprDynamic
			}
			if !seen[n.address] {
				program.Dependencies = append(program.Dependencies, n.address)
				seen[n.address] = true
			}
			return typ, nil
		case "throw", "dice":
			if !random {
				return "", fmt.Errorf("byte %d: random expression in deterministic context", n.offset)
			}
			program.UsesRandom = true
			if n.op == "throw" {
				return exprNumber, nil
			}
			if n.left.op == "literal" {
				if s, ok := n.left.literal.(string); ok {
					d, err := parseExprDice(s)
					if err != nil {
						return "", err
					}
					n.op = "throw"
					n.literal = d
					n.left = nil
					return exprNumber, nil
				}
			}
		}
		input, output := exprSignature(n.op)
		left, err := check(n.left)
		if err != nil {
			return "", err
		}
		if !exprFits(left, input) {
			return "", fmt.Errorf("byte %d: %s requires %s, got %s", n.offset, n.op, input, left)
		}
		if n.right != nil {
			right, err := check(n.right)
			if err != nil {
				return "", err
			}
			if !exprFits(right, input) || (input == exprDynamic && !exprFits(left, right)) {
				return "", fmt.Errorf("byte %d: incompatible %s operands: %s, %s", n.offset, n.op, left, right)
			}
		}
		return output, nil
	}
	typ, err := check(root)
	program.Type = typ
	return program, err
}

// Values are immutable scalars. Division uses a bounded exact rational until
// floor or an integer-valued destination consumes the result.
type exprValue struct {
	typ  exprType
	num  *big.Rat
	text string
	flag bool
}

func exprInteger(i int64) exprValue { return exprValue{typ: exprNumber, num: big.NewRat(i, 1)} }
func exprBoolean(b bool) exprValue  { return exprValue{typ: exprBool, flag: b} }
func exprScalar(raw any) (exprValue, error) {
	switch v := raw.(type) {
	case *big.Rat:
		return checkedRat(v)
	case StatValue:
		return exprScalar(v.Interface())
	case int64:
		return exprInteger(v), nil
	case string:
		if len(v) <= MaxDefinitionDocumentBytes {
			return exprValue{typ: exprText, text: v}, nil
		}
	case bool:
		return exprBoolean(v), nil
	}
	return exprValue{}, fmt.Errorf("expected integer, string or boolean, got %T", raw)
}

// One frame belongs to one invocation with stable actor/source/target bindings.
// Results and failures are memoized, including a failed or successful roll.
type ExpressionFrame struct {
	// In a character stat pass, actor.stat and stat address the same projection.
	characterStats     map[string]any
	Bindings           map[string]any
	RNG                DiceRNG
	Locals             map[string]*Expression
	cache              map[*Expression]exprResult
	steps, dice, depth int
}
type exprResult struct {
	value exprValue
	err   error
}

func (f *ExpressionFrame) Evaluate(p *Expression) (exprValue, error) {
	if p == nil || p.root == nil {
		return exprValue{}, fmt.Errorf("missing compiled expression")
	}
	if r, ok := f.cache[p]; ok {
		return r.value, r.err
	}
	if f.depth >= maxExpressionDepth || f.steps >= maxExpressionSteps {
		return exprValue{}, fmt.Errorf("expression execution limit")
	}
	if f.cache == nil {
		f.cache = map[*Expression]exprResult{}
	}
	f.cache[p] = exprResult{err: fmt.Errorf("cyclic expression")}
	f.depth++
	v, err := f.eval(p.root)
	f.depth--
	if err == nil && (!exprFits(v.typ, p.Type) || p.integer && (v.typ != exprNumber || !v.num.IsInt())) {
		err = fmt.Errorf("incompatible expression result, expected %s", p.Type)
	}
	if err == nil && p.matchTarget {
		target, e := exprScalar(f.Bindings["target"])
		if e != nil || target.typ != v.typ || (v.typ == exprNumber && !v.num.IsInt()) {
			err = fmt.Errorf("effect result must match the current target type")
		}
	}
	f.cache[p] = exprResult{v, err}
	return v, err
}
func (f *ExpressionFrame) eval(n *exprNode) (exprValue, error) {
	f.steps++
	if f.steps > maxExpressionSteps {
		return exprValue{}, fmt.Errorf("expression operation limit")
	}
	switch n.op {
	case "literal":
		return exprScalar(n.literal)
	case "read":
		v, err := f.read(n)
		if err == nil && !exprFits(v.typ, n.readType) {
			return exprValue{}, fmt.Errorf("%s requires %s, got %s", n.address, n.readType, v.typ)
		}
		return v, err
	case "throw":
		return f.throw(n.literal.(exprDice))
	}
	a, err := f.eval(n.left)
	if err != nil {
		return exprValue{}, err
	}
	want, _ := exprSignature(n.op)
	if !exprFits(a.typ, want) {
		return exprValue{}, fmt.Errorf("byte %d: %s requires %s", n.offset, n.op, want)
	}
	switch n.op {
	case "unary+":
		return a, nil
	case "unary-":
		return checkedRat(new(big.Rat).Neg(a.num))
	case "unary!", "unarynot":
		return exprBoolean(!a.flag), nil
	case "floor":
		return checkedRat(new(big.Rat).SetInt(new(big.Int).Div(a.num.Num(), a.num.Denom())))
	case "dice":
		d, err := parseExprDice(a.text)
		if err != nil {
			return exprValue{}, err
		}
		return f.throw(d)
	case "&&", "and":
		if !a.flag {
			return a, nil
		}
	case "||", "or":
		if a.flag {
			return a, nil
		}
	}
	b, err := f.eval(n.right)
	if err != nil {
		return exprValue{}, err
	}
	if a.typ != b.typ {
		return exprValue{}, fmt.Errorf("byte %d: incompatible operand types", n.offset)
	}
	switch n.op {
	case "&&", "and", "||", "or":
		return b, nil
	case "==", "!=":
		same := a.text == b.text && a.flag == b.flag
		if a.typ == exprNumber {
			same = a.num.Cmp(b.num) == 0
		}
		return exprBoolean(same == (n.op == "==")), nil
	case "<":
		return exprBoolean(a.num.Cmp(b.num) < 0), nil
	case ">":
		return exprBoolean(a.num.Cmp(b.num) > 0), nil
	case "<=":
		return exprBoolean(a.num.Cmp(b.num) <= 0), nil
	case ">=":
		return exprBoolean(a.num.Cmp(b.num) >= 0), nil
	}
	r := new(big.Rat)
	switch n.op {
	case "+":
		r.Add(a.num, b.num)
	case "-":
		r.Sub(a.num, b.num)
	case "*":
		r.Mul(a.num, b.num)
	case "/":
		if b.num.Sign() == 0 {
			return exprValue{}, fmt.Errorf("division by zero")
		}
		r.Quo(a.num, b.num)
	case "%":
		if !a.num.IsInt() || !b.num.IsInt() || b.num.Sign() == 0 {
			return exprValue{}, fmt.Errorf("remainder requires integers and nonzero divisor")
		}
		r.SetInt(new(big.Int).Rem(a.num.Num(), b.num.Num()))
	default:
		return exprValue{}, fmt.Errorf("unknown operator")
	}
	return checkedRat(r)
}

func (f *ExpressionFrame) read(n *exprNode) (exprValue, error) {
	if local := f.Locals[n.address]; local != nil {
		return f.Evaluate(local)
	}
	raw, ok := f.Bindings[n.address]
	if !ok && f.characterStats != nil {
		raw, ok = f.characterStats[strings.TrimPrefix(n.address, "actor.")]
	}
	if !ok {
		raw = f.Bindings
		for _, key := range n.path {
			table, _ := raw.(map[string]any)
			raw, ok = table[key]
			if !ok {
				return exprValue{}, fmt.Errorf("missing context address %s", n.address)
			}
		}
	}
	v, err := exprScalar(raw)
	f.steps += len(n.path) + len(v.text)/64
	if f.steps > maxExpressionSteps {
		return exprValue{}, fmt.Errorf("expression execution limit")
	}
	return v, err
}
func checkedRat(r *big.Rat) (exprValue, error) {
	if r.Num().BitLen() > maxRationalBits || r.Denom().BitLen() > maxRationalBits || r.IsInt() && !r.Num().IsInt64() {
		return exprValue{}, fmt.Errorf("numeric overflow")
	}
	return exprValue{typ: exprNumber, num: r}, nil
}
func (f *ExpressionFrame) throw(d exprDice) (exprValue, error) {
	if f.dice+d.count > MaxDicePerRoll {
		return exprValue{}, fmt.Errorf("execution dice limit")
	}
	f.dice += d.count
	rng := f.RNG
	if rng == nil {
		rng = CryptoDiceRNG{}
	}
	var sum int64
	for i := 0; i < d.count; i++ {
		v, err := rng.Intn(d.sides)
		if err != nil {
			return exprValue{}, err
		}
		if v < 0 || v >= d.sides {
			return exprValue{}, fmt.Errorf("invalid server RNG result")
		}
		sum += int64(v + 1)
	}
	return exprInteger(sum), nil
}
