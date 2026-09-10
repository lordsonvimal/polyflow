package datalog

import (
	"fmt"
	"strconv"
	"strings"
)

// FX.3 — comparison builtins and the integer atom domain.
//
// The declarative framework pipeline (docs/declarative-framework-pipeline-plan.md)
// needs ordering: "the method is below a `private` marker" is
// `preceded_by(...) > 0`, "the callback resolved within two frames" is a depth
// comparison. Pure Datalog has no `<`, and adding string operations or unbounded
// arithmetic would let a rule smuggle in a normalizer. Instead the fact IR
// carries integers as factpipe.AtomInt, the interner reserves a symbol range for
// them, and three always-complete comparison builtins — lt / le / ne — read that
// range. Every argument must be bound by an earlier positive literal (checkSafe),
// exactly as for negation, so a builtin never generates bindings.
//
// Builtins are not relations: they never appear in the dependency graph, carry
// no derivation step, and cannot be a rule head or be negated.

// intSymFlag marks an interned symbol as a direct integer rather than an index
// into interner.strs. An int64 in [0, 2^31) round-trips as `intSymFlag |
// uint32(n)` with no strs entry, so a join comparison stays a uint32 op and
// lt/le never touch a map. Values outside that range stay ordinary interned
// strings and lt/le reject them at evaluation, naming the rule.
const intSymFlag uint32 = 1 << 31

// parseIntSym reports whether s is a non-negative decimal integer inside the
// reserved range, and its value. Ordering indices, resolution depths, source
// lines and ports are all non-negative and small, so the range covers them;
// a negative or oversized numeral is left as a string.
func parseIntSym(s string) (uint32, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n >= 1<<31 {
		return 0, false
	}
	return uint32(n), true
}

// decodeIntSym returns the integer an interned symbol carries, or ok=false when
// the symbol is an ordinary string.
func decodeIntSym(id uint32) (int64, bool) {
	if id&intSymFlag != 0 {
		return int64(id &^ intSymFlag), true
	}
	return 0, false
}

var builtinArity = map[string]int{"lt": 2, "le": 2, "ne": 2, "contains": 2, "prefix": 2}

func isBuiltin(rel string) bool { _, ok := builtinArity[rel]; return ok }

// evalBuiltin decides one comparison literal against already-bound operands.
// lt and le require both operands in the integer domain; ne is defined on any
// two atoms as raw symbol identity, because "these two node ids differ" is a
// sound, useful guard that cannot be gotten wrong. contains / prefix are
// substring / prefix tests on the revealed strings — a rule needs "this file
// is under app/controllers/" and the fact IR carries no path structure.
func evalBuiltin(rel string, a, b uint32, in *interner) (bool, error) {
	if rel == "ne" {
		return a != b, nil
	}
	if rel == "contains" {
		return strings.Contains(in.sym(a), in.sym(b)), nil
	}
	if rel == "prefix" {
		return strings.HasPrefix(in.sym(a), in.sym(b)), nil
	}
	x, xok := decodeIntSym(a)
	y, yok := decodeIntSym(b)
	if !xok || !yok {
		return false, fmt.Errorf("datalog: %s expects integer operands, got %q and %q", rel, in.sym(a), in.sym(b))
	}
	if rel == "lt" {
		return x < y, nil
	}
	return x <= y, nil // le
}
