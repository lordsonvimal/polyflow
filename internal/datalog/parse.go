package datalog

import (
	"fmt"
	"strings"
	"unicode"
)

// Term is one argument of a literal: a variable or a constant.
//
// Constants are always strings — the engine has no types, because the emitter
// that asserts the facts is the only thing that knows what a key means. A rule
// that could compare an integer to a string is a rule that has smuggled a
// normalizer into the join, which the tier's second design rule forbids.
type Term struct {
	IsVar bool
	Name  string // variable name when IsVar, literal value otherwise
	// Var is a dense per-rule slot index for this variable, assigned at load by
	// assignVars. It replaces the variable name as the binding-frame key on the
	// hot path: join indexes a []string frame by Var instead of hashing Name
	// into an env map (docs/datalog-engine-performance-plan.md D.2). Only
	// meaningful when IsVar.
	Var int
	// Sym is the interned id of Name for a constant term, resolved by LoadRules
	// against the engine's symbol table (docs/datalog-engine-performance-plan.md
	// D.12). Only meaningful when !IsVar; it lets join compare a rule constant to
	// an interned tuple element without hashing the string.
	Sym uint32
}

// Literal is one goal: `reg_callback(R, CB)` or `not own_filter(C, R)`.
type Literal struct {
	Rel  string
	Args []Term
	Neg  bool
}

// Rule is `head :- body`. A rule with an empty body is a ground fact, which is
// legal but rare: facts normally arrive through Assert, because a fact written
// into a .dl file is a fact no emitter can see.
type Rule struct {
	Head Literal
	Body []Literal

	// Name is what goes into SourceRef.Rule (SA.1), formatted
	// "<rulefile>/<head relation>". Every rule with the same head in the same
	// file shares it: the head relation is the claim, the individual clause is
	// an implementation detail of how it is proved.
	Name string
	File string
	Line int

	// NVars is the number of distinct variables in the rule — the size of the
	// binding frame join allocates. assignVars sets it.
	NVars int

	// pat is the rule's sub-call patterns, cached across evalRule calls (D.4).
	// The structure is immutable; only vals/head are written during a join and
	// both are fully overwritten before each read, and evaluation is
	// single-threaded. patBusy guards the one case the cache is unsafe for —
	// a recursive rule re-entered top-down while an outer join over it is still
	// live — by handing the re-entrant call a fresh set instead.
	pat     *rulePatterns
	patBusy bool

	// plan resolves each body literal's relation once — base vs derived, and the
	// base relation pointer — so join and both bodySources stop hashing lit.Rel
	// into e.base / e.rules on every firing (docs/datalog-engine-performance-plan.md
	// D.9). Built lazily by Engine.planBody, cleared by Engine.reset when facts or
	// rules change.
	plan []litPlan
}

func (l Literal) arity() int { return len(l.Args) }

// assignVars gives every distinct variable in a rule a dense slot index, shared
// between the head and the body, and records the count on the rule. The join
// frame is a []string of NVars entries indexed by Term.Var, which is what lets
// D.2 drop the per-evalRule env map.
func assignVars(r *Rule) {
	ids := map[string]int{}
	number := func(args []Term) {
		for i := range args {
			if !args[i].IsVar {
				continue
			}
			id, ok := ids[args[i].Name]
			if !ok {
				id = len(ids)
				ids[args[i].Name] = id
			}
			args[i].Var = id
		}
	}
	number(r.Head.Args)
	for bi := range r.Body {
		number(r.Body[bi].Args)
	}
	r.NVars = len(ids)
}

// ---------------------------------------------------------------------------
// tokenizer
// ---------------------------------------------------------------------------

type token struct {
	kind string // ident | string | punct
	text string
	line int
}

// lex splits a rule file into tokens. `%` starts a comment to end of line —
// Prolog's spelling, since the rule syntax is Prolog's.
func lex(src []byte, file string) ([]token, error) {
	var out []token
	line := 1
	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '\n':
			line++
			i++
		case c == ' ' || c == '\t' || c == '\r':
			i++
		case c == '%':
			for i < len(src) && src[i] != '\n' {
				i++
			}
		case c == '"':
			j := i + 1
			var sb strings.Builder
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' && j+1 < len(src) {
					j++
				}
				sb.WriteByte(src[j])
				j++
			}
			if j >= len(src) {
				return nil, fmt.Errorf("%s:%d: unterminated string", file, line)
			}
			out = append(out, token{"string", sb.String(), line})
			i = j + 1
		case c == '(' || c == ')' || c == ',' || c == '.':
			out = append(out, token{"punct", string(c), line})
			i++
		case c == ':':
			if i+1 < len(src) && src[i+1] == '-' {
				out = append(out, token{"punct", ":-", line})
				i += 2
				continue
			}
			return nil, fmt.Errorf("%s:%d: stray ':'", file, line)
		case isIdentStart(rune(c)):
			j := i
			for j < len(src) && isIdentRune(rune(src[j])) {
				j++
			}
			out = append(out, token{"ident", string(src[i:j]), line})
			i = j
		default:
			return nil, fmt.Errorf("%s:%d: unexpected character %q", file, line, string(c))
		}
	}
	return out, nil
}

func isIdentStart(r rune) bool {
	return unicode.IsLetter(r) || r == '_'
}

func isIdentRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// ---------------------------------------------------------------------------
// parser
// ---------------------------------------------------------------------------

type parser struct {
	toks []token
	pos  int
	file string
	// anon counts `_` occurrences so each one becomes a distinct variable.
	// Sharing one name across two anonymous positions would silently join
	// them, which is the difference between "some action" and "the same
	// action twice".
	anon int
}

func parseRules(src []byte, file string) ([]*Rule, error) {
	toks, err := lex(src, file)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks, file: file}
	base := strings.TrimSuffix(file, ".dl")
	if i := strings.LastIndexAny(base, "/\\"); i >= 0 {
		base = base[i+1:]
	}

	var rules []*Rule
	for p.pos < len(p.toks) {
		r, err := p.rule(base)
		if err != nil {
			return nil, err
		}
		assignVars(r)
		rules = append(rules, r)
	}
	return rules, nil
}

func (p *parser) peek() token {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return token{kind: "eof"}
}

func (p *parser) rule(rulefile string) (*Rule, error) {
	p.anon = 0
	line := p.peek().line
	head, err := p.literal(false)
	if err != nil {
		return nil, err
	}
	r := &Rule{Head: head, Name: rulefile + "/" + head.Rel, File: p.file, Line: line}

	switch t := p.peek(); {
	case t.kind == "punct" && t.text == ".":
		p.pos++
		return r, nil
	case t.kind == "punct" && t.text == ":-":
		p.pos++
	default:
		return nil, fmt.Errorf("%s:%d: expected ':-' or '.' after head %s", p.file, line, head.Rel)
	}

	for {
		lit, err := p.literal(true)
		if err != nil {
			return nil, err
		}
		r.Body = append(r.Body, lit)
		t := p.peek()
		if t.kind == "punct" && t.text == "," {
			p.pos++
			continue
		}
		if t.kind == "punct" && t.text == "." {
			p.pos++
			return r, nil
		}
		return nil, fmt.Errorf("%s:%d: expected ',' or '.' in body of %s", p.file, line, head.Rel)
	}
}

func (p *parser) literal(allowNeg bool) (Literal, error) {
	var lit Literal
	t := p.peek()
	if t.kind == "ident" && t.text == "not" {
		if !allowNeg {
			return lit, fmt.Errorf("%s:%d: 'not' is a body-only operator", p.file, t.line)
		}
		lit.Neg = true
		p.pos++
		t = p.peek()
	}
	if t.kind != "ident" {
		return lit, fmt.Errorf("%s:%d: expected a relation name, got %q", p.file, t.line, t.text)
	}
	if !unicode.IsLower(rune(t.text[0])) {
		return lit, fmt.Errorf("%s:%d: relation names start lowercase (%q looks like a variable)", p.file, t.line, t.text)
	}
	lit.Rel = t.text
	p.pos++

	if nt := p.peek(); nt.kind != "punct" || nt.text != "(" {
		return lit, fmt.Errorf("%s:%d: expected '(' after %s", p.file, t.line, lit.Rel)
	}
	p.pos++
	for {
		a := p.peek()
		switch {
		case a.kind == "string":
			lit.Args = append(lit.Args, Term{Name: a.text})
			p.pos++
		case a.kind == "ident":
			if a.text == "_" {
				p.anon++
				lit.Args = append(lit.Args, Term{IsVar: true, Name: fmt.Sprintf("_%d", p.anon)})
			} else if unicode.IsUpper(rune(a.text[0])) || a.text[0] == '_' {
				lit.Args = append(lit.Args, Term{IsVar: true, Name: a.text})
			} else {
				lit.Args = append(lit.Args, Term{Name: a.text})
			}
			p.pos++
		default:
			return lit, fmt.Errorf("%s:%d: expected an argument, got %q", p.file, a.line, a.text)
		}
		nt := p.peek()
		if nt.kind == "punct" && nt.text == "," {
			p.pos++
			continue
		}
		if nt.kind == "punct" && nt.text == ")" {
			p.pos++
			return lit, nil
		}
		return lit, fmt.Errorf("%s:%d: expected ',' or ')' in %s", p.file, nt.line, lit.Rel)
	}
}

// ---------------------------------------------------------------------------
// safety
// ---------------------------------------------------------------------------

// checkSafe enforces range restriction: every head variable and every variable
// under `not` must be bound by a positive body literal.
//
// This is not pedantry. An unbound variable under negation asks "is there no
// tuple at all", not "is this tuple absent", and the two differ on exactly the
// rows a filter-chain rule exists to distinguish. An unbound head variable is
// an infinite relation. Both are rejected at load, with the rule named.
func checkSafe(r *Rule) error {
	if len(r.Body) == 0 {
		for _, a := range r.Head.Args {
			if a.IsVar {
				return fmt.Errorf("%s:%d: rule %s is a fact, so every argument must be a constant (%s is a variable)",
					r.File, r.Line, r.Name, a.Name)
			}
		}
		return nil
	}
	pos := map[string]bool{}
	for _, l := range r.Body {
		if l.Neg {
			continue
		}
		for _, a := range l.Args {
			if a.IsVar {
				pos[a.Name] = true
			}
		}
	}
	for _, a := range r.Head.Args {
		if !a.IsVar {
			continue
		}
		if strings.HasPrefix(a.Name, "_") && len(r.Body) > 0 {
			return fmt.Errorf("%s:%d: rule %s has an anonymous variable in its head", r.File, r.Line, r.Name)
		}
		if len(r.Body) > 0 && !pos[a.Name] {
			return fmt.Errorf("%s:%d: rule %s: head variable %s is not bound by a positive body literal", r.File, r.Line, r.Name, a.Name)
		}
	}
	for _, l := range r.Body {
		if !l.Neg {
			continue
		}
		for _, a := range l.Args {
			if a.IsVar && !pos[a.Name] {
				return fmt.Errorf("%s:%d: rule %s: variable %s in `not %s(...)` is not bound by a positive body literal",
					r.File, r.Line, r.Name, a.Name, l.Rel)
			}
		}
	}
	return nil
}
