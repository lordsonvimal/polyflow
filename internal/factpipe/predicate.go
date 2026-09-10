package factpipe

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// predicate.go is the `when:` mini-language shared by the emit spec's
// `confidence`, `abstain` and `unresolved` blocks (FX.5). It is a fixed,
// non-Turing-complete predicate set over a derived row's atoms plus the
// relation's full row set (for the aggregates):
//
//	X == Y   X != Y   X <= Y   X >= Y   X < Y   X > Y      comparison (numeric when both sides parse as int, else string)
//	X == null                                              null test (null is the empty string)
//	startswith(X, "prefix")                                prefix test
//	origin(X) == "graph"                                   the atom's Origin kind, looked up as row["origin:X"]
//	count(X)                     count(X by Y, Z)          row count, optionally within the group matching this row on Y, Z
//	count_distinct(X by Y, Z)    count_distinct(X)         distinct values of X, same grouping
//	and   or   not   ( )                                   boolean structure
//
// There is no `eval`, no user code, no arithmetic beyond the comparison. A
// framework that needs more is the signal for a new generic primitive or a
// datalog rule, never a Go branch (plan § Risks).

type valFn func(row map[string]string, all []map[string]string) string
type boolFn func(row map[string]string, all []map[string]string) bool

// compilePredicate parses a `when:` string into an evaluable boolean. An empty
// string compiles to nil (always-absent block).
func compilePredicate(src string) (boolFn, error) {
	if strings.TrimSpace(src) == "" {
		return nil, nil
	}
	toks, err := lexPredicate(src)
	if err != nil {
		return nil, err
	}
	p := &predParser{toks: toks}
	fn, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos != len(p.toks) {
		return nil, fmt.Errorf("predicate %q: trailing tokens at %q", src, p.toks[p.pos].val)
	}
	return fn, nil
}

// --- lexer ---

type ptok struct {
	kind string // ident | str | int | op | punc
	val  string
}

func lexPredicate(s string) ([]ptok, error) {
	var out []ptok
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == ',':
			out = append(out, ptok{"punc", string(c)})
			i++
		case c == '=' || c == '!' || c == '<' || c == '>':
			if i+1 < len(s) && s[i+1] == '=' {
				out = append(out, ptok{"op", s[i : i+2]})
				i += 2
			} else if c == '<' || c == '>' {
				out = append(out, ptok{"op", string(c)})
				i++
			} else {
				return nil, fmt.Errorf("predicate: bare %q (want %q=)", string(c), string(c))
			}
		case c == '"' || c == '\'':
			j := i + 1
			for j < len(s) && s[j] != c {
				j++
			}
			if j >= len(s) {
				return nil, fmt.Errorf("predicate: unterminated string")
			}
			out = append(out, ptok{"str", s[i+1 : j]})
			i = j + 1
		case c >= '0' && c <= '9':
			j := i
			for j < len(s) && s[j] >= '0' && s[j] <= '9' {
				j++
			}
			out = append(out, ptok{"int", s[i:j]})
			i = j
		case isIdentStart(c):
			j := i
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			out = append(out, ptok{"ident", s[i:j]})
			i = j
		default:
			return nil, fmt.Errorf("predicate: unexpected character %q", string(c))
		}
	}
	return out, nil
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')
}
func isIdentPart(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') }

// --- parser ---

type predParser struct {
	toks []ptok
	pos  int
}

func (p *predParser) peek() (ptok, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return ptok{}, false
}

func (p *predParser) next() ptok { t := p.toks[p.pos]; p.pos++; return t }

func (p *predParser) accept(kind, val string) bool {
	if t, ok := p.peek(); ok && t.kind == kind && (val == "" || t.val == val) {
		p.pos++
		return true
	}
	return false
}

func (p *predParser) expect(kind, val string) error {
	if !p.accept(kind, val) {
		return fmt.Errorf("predicate: expected %s %q at position %d", kind, val, p.pos)
	}
	return nil
}

func (p *predParser) parseOr() (boolFn, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.accept("ident", "or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(row map[string]string, all []map[string]string) bool {
			return l(row, all) || r(row, all)
		}
	}
	return left, nil
}

func (p *predParser) parseAnd() (boolFn, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.accept("ident", "and") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		l, r := left, right
		left = func(row map[string]string, all []map[string]string) bool {
			return l(row, all) && r(row, all)
		}
	}
	return left, nil
}

func (p *predParser) parseNot() (boolFn, error) {
	if p.accept("ident", "not") {
		inner, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return func(row map[string]string, all []map[string]string) bool { return !inner(row, all) }, nil
	}
	return p.parsePrimary()
}

func (p *predParser) parsePrimary() (boolFn, error) {
	if p.accept("punc", "(") {
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		if err := p.expect("punc", ")"); err != nil {
			return nil, err
		}
		return inner, nil
	}

	if t, ok := p.peek(); ok && t.kind == "ident" && t.val == "startswith" {
		p.next()
		if err := p.expect("punc", "("); err != nil {
			return nil, err
		}
		a, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if err := p.expect("punc", ","); err != nil {
			return nil, err
		}
		b, err := p.parseValue()
		if err != nil {
			return nil, err
		}
		if err := p.expect("punc", ")"); err != nil {
			return nil, err
		}
		return func(row map[string]string, all []map[string]string) bool {
			return strings.HasPrefix(a(row, all), b(row, all))
		}, nil
	}

	// value OP value
	lhs, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	t, ok := p.peek()
	if !ok || t.kind != "op" {
		return nil, fmt.Errorf("predicate: expected a comparison operator after value")
	}
	op := p.next().val
	rhsTok, _ := p.peek()
	rhs, err := p.parseValue()
	if err != nil {
		return nil, err
	}
	isNull := rhsTok.kind == "ident" && rhsTok.val == "null"
	return func(row map[string]string, all []map[string]string) bool {
		l, r := lhs(row, all), rhs(row, all)
		if isNull {
			switch op {
			case "==":
				return l == ""
			case "!=":
				return l != ""
			}
		}
		return compareVals(l, r, op)
	}, nil
}

func compareVals(l, r, op string) bool {
	li, lerr := strconv.Atoi(l)
	ri, rerr := strconv.Atoi(r)
	if lerr == nil && rerr == nil {
		switch op {
		case "==":
			return li == ri
		case "!=":
			return li != ri
		case "<":
			return li < ri
		case "<=":
			return li <= ri
		case ">":
			return li > ri
		case ">=":
			return li >= ri
		}
	}
	switch op {
	case "==":
		return l == r
	case "!=":
		return l != r
	case "<":
		return l < r
	case "<=":
		return l <= r
	case ">":
		return l > r
	case ">=":
		return l >= r
	}
	return false
}

func (p *predParser) parseValue() (valFn, error) {
	t, ok := p.peek()
	if !ok {
		return nil, fmt.Errorf("predicate: expected a value, got end of input")
	}
	switch t.kind {
	case "str":
		p.next()
		v := t.val
		return func(map[string]string, []map[string]string) string { return v }, nil
	case "int":
		p.next()
		v := t.val
		return func(map[string]string, []map[string]string) string { return v }, nil
	case "ident":
		switch t.val {
		case "null":
			p.next()
			return func(map[string]string, []map[string]string) string { return "" }, nil
		case "count", "count_distinct":
			return p.parseAggregate()
		case "origin":
			p.next()
			if err := p.expect("punc", "("); err != nil {
				return nil, err
			}
			col, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			if err := p.expect("punc", ")"); err != nil {
				return nil, err
			}
			return func(row map[string]string, _ []map[string]string) string { return row["origin:"+col] }, nil
		default:
			p.next()
			col := t.val
			return func(row map[string]string, _ []map[string]string) string { return row[col] }, nil
		}
	}
	return nil, fmt.Errorf("predicate: unexpected %s %q in value position", t.kind, t.val)
}

func (p *predParser) parseIdent() (string, error) {
	t, ok := p.peek()
	if !ok || t.kind != "ident" {
		return "", fmt.Errorf("predicate: expected an identifier at position %d", p.pos)
	}
	p.next()
	return t.val, nil
}

func (p *predParser) parseAggregate() (valFn, error) {
	kind := p.next().val // count | count_distinct
	if err := p.expect("punc", "("); err != nil {
		return nil, err
	}
	col, err := p.parseIdent()
	if err != nil {
		return nil, err
	}
	var by []string
	if p.accept("ident", "by") {
		for {
			g, err := p.parseIdent()
			if err != nil {
				return nil, err
			}
			by = append(by, g)
			if !p.accept("punc", ",") {
				break
			}
		}
	}
	if err := p.expect("punc", ")"); err != nil {
		return nil, err
	}
	distinct := kind == "count_distinct"
	return func(row map[string]string, all []map[string]string) string {
		if distinct {
			seen := map[string]bool{}
			for _, r := range all {
				if sameGroup(row, r, by) {
					seen[r[col]] = true
				}
			}
			return strconv.Itoa(len(seen))
		}
		n := 0
		for _, r := range all {
			if sameGroup(row, r, by) {
				n++
			}
		}
		return strconv.Itoa(n)
	}, nil
}

func sameGroup(a, b map[string]string, by []string) bool {
	for _, k := range by {
		if a[k] != b[k] {
			return false
		}
	}
	return true
}

// sortedKeys is a small helper shared with emit.go.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
