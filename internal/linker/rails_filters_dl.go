package linker

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/lordsonvimal/polyflow/internal/datalog"
	"github.com/lordsonvimal/polyflow/rules"
)

// Tier DL.1: the same filter chain, decided by rules/ruby/rails_filters.dl
// instead of by effectiveFilters/eachSuperclass/eachIncludedModule/chainSkips/
// retracted/skippedFor/appliesTo.
//
// What moves and what does not is the point of the spike. Moving:
//
//   - the ancestor closure, which the walk recomputes per class;
//   - "which declaration owns a registration that binds to this class",
//     including concerns included by concerns included by a superclass;
//   - retraction, whole-registration and per-action, which is the one piece of
//     genuine stratified negation in the pass.
//
// Staying in Go, on purpose:
//
//   - parsing. A rule cannot read a tree-sitter node.
//   - constant resolution (resolveSuper). Ruby resolves `< Super` by walking
//     enclosing namespaces outward and stopping at the first hit — an ordered
//     search whose *first* answer is the answer. Datalog has no first.
//   - callback resolution (resolveCallback), for the same reason, and because
//     its depth grades the edge's confidence.
//   - every normalizer. filterFamily and the only:/except: lists are applied
//     here, at assertion time, so the rules join pre-normalized keys. A
//     normalizer called from inside a rule is both a boundary crossing and an
//     unindexable join; contracts/http.yaml already works this way.
//
// The emitter therefore asserts facts about *declarations*, not about names:
// every key identifies one parsed class or module body, so a reopened class and
// two same-named controllers in different namespaces stay distinct.

const (
	dlRuleFile      = "ruby/rails_filters.dl"
	dlClassFilter   = "rails_filters/class_filter"
	dlActionFilter  = "rails_filters/action_filter"
	dlRelClassScope = "class_filter"
	dlRelActionScop = "action_filter"
)

// dlReg is one registration, keyed so the rules can join on it.
type dlReg struct {
	reg   filterReg
	owner *ctrlClass
	ord   int // source order across the whole service, for deterministic rows
}

// dlProgram is a loaded engine plus the two maps that translate its string
// keys back into parsed declarations. Kept as a value so the tests can query
// relations the emitter itself never asks for — the separation of `ancestor`
// from `includes_module` is a claim about the fact base, not about the edges.
type dlProgram struct {
	engine    *datalog.Engine
	classKeys map[*ctrlClass]string
	regs      map[string]dlReg
}

// datalogProgram builds the fact base and loads rules/ruby/rails_filters.dl.
//
// The queries run once for the whole service rather than once per class: the
// engine tables the closure, so a per-class query would pay for the same
// ancestor set repeatedly — which is exactly the cost the hand-written walk
// has and the reason this tier exists.
func (ix *filterIndex) datalogProgram() (*dlProgram, error) {
	e := datalog.New(datalog.Options{})

	classKeys := make(map[*ctrlClass]string, len(ix.classes))
	classByKey := make(map[string]*ctrlClass, len(ix.classes))
	for _, c := range ix.classes {
		k := c.qualified() + "@" + c.file + ":" + strconv.Itoa(c.line)
		if _, dup := classByKey[k]; dup {
			// Two declarations cannot share a qualified name, file and line;
			// if they somehow do, the fact base would silently merge them.
			return nil, fmt.Errorf("duplicate class key %q", k)
		}
		classKeys[c] = k
		classByKey[k] = c
	}

	var (
		classDecl  []datalog.Tuple
		subclassOf []datalog.Tuple
		includes   []datalog.Tuple
		actions    []datalog.Tuple
		filterRegs []datalog.Tuple
		skipReg    []datalog.Tuple
		regFamily  []datalog.Tuple
		regCB      []datalog.Tuple
		skipTotal  []datalog.Tuple
		regHasOnly []datalog.Tuple
		regOnly    []datalog.Tuple
		regExcept  []datalog.Tuple
	)
	regs := map[string]dlReg{}
	ord := 0

	addReg := func(owner *ctrlClass, r filterReg, isSkip bool) string {
		key := classKeys[owner] + "#" + r.kind + "@" + strconv.Itoa(r.line) + "/" + strconv.Itoa(ord)
		regs[key] = dlReg{reg: r, owner: owner, ord: ord}
		ord++
		regFamily = append(regFamily, datalog.Tuple{key, filterFamily(r.kind)})
		for _, cb := range r.callbacks {
			regCB = append(regCB, datalog.Tuple{key, cb})
		}
		if len(r.only) > 0 {
			regHasOnly = append(regHasOnly, datalog.Tuple{key})
			for _, a := range r.only {
				regOnly = append(regOnly, datalog.Tuple{key, a})
			}
		}
		for _, a := range r.except {
			regExcept = append(regExcept, datalog.Tuple{key, a})
		}
		if isSkip && len(r.only) == 0 && len(r.except) == 0 {
			skipTotal = append(skipTotal, datalog.Tuple{key})
		}
		return key
	}

	for _, c := range ix.classes {
		ck := classKeys[c]
		classDecl = append(classDecl, datalog.Tuple{ck})
		for _, sup := range ix.resolveSuper(c) {
			if sup != c {
				subclassOf = append(subclassOf, datalog.Tuple{ck, classKeys[sup]})
			}
		}
		// Only `include`/`extend` carries registrations. `prepend` feeds the
		// method-lookup order in ancestorNames and nothing else, which is where
		// the hand-written walk uses it too.
		for _, name := range c.includes {
			for _, mod := range ix.byName[name] {
				if mod != c {
					includes = append(includes, datalog.Tuple{ck, classKeys[mod]})
				}
			}
		}
		for _, a := range c.actions {
			actions = append(actions, datalog.Tuple{ck, a.name})
		}
		for _, r := range c.filters {
			filterRegs = append(filterRegs, datalog.Tuple{ck, addReg(c, r, false)})
		}
		for _, r := range c.skips {
			skipReg = append(skipReg, datalog.Tuple{ck, addReg(c, r, true)})
		}
	}

	for _, f := range []struct {
		rel    string
		tuples []datalog.Tuple
	}{
		{"class_decl", classDecl},
		{"subclass_of", subclassOf},
		{"includes_module", includes},
		{"action", actions},
		{"filter_reg", filterRegs},
		{"skip_reg", skipReg},
		{"reg_family", regFamily},
		{"reg_callback", regCB},
		{"skip_total", skipTotal},
		{"reg_has_only", regHasOnly},
		{"reg_only", regOnly},
		{"reg_except", regExcept},
	} {
		// Asserted even when empty: an empty relation is a fact ("this service
		// has no skips"), and the engine treats an unasserted one as a typo.
		if err := e.Assert(f.rel, f.tuples); err != nil {
			return nil, err
		}
	}

	src, err := rules.Load(dlRuleFile)
	if err != nil {
		return nil, err
	}
	if err := e.LoadRules(src, dlRuleFile); err != nil {
		return nil, err
	}
	return &dlProgram{engine: e, classKeys: classKeys, regs: regs}, nil
}

// datalogRows runs the two goals and returns a per-class row provider with the
// same shape walkRows produces.
func (ix *filterIndex) datalogRows() (func(*ctrlClass) []filterRow, error) {
	p, err := ix.datalogProgram()
	if err != nil {
		return nil, err
	}
	e, classKeys, regs := p.engine, p.classKeys, p.regs

	classRows, err := e.Query(dlRelClassScope)
	if err != nil {
		return nil, err
	}
	actionRows, err := e.Query(dlRelActionScop)
	if err != nil {
		return nil, err
	}

	// Group: (class, reg, callback) -> the actions it reaches.
	type rowKey struct{ class, reg, cb string }
	byKey := map[rowKey][]string{}
	order := map[string][]rowKey{}
	for _, t := range classRows {
		k := rowKey{t[0], t[1], t[2]}
		if _, seen := byKey[k]; !seen {
			byKey[k] = nil
			order[t[0]] = append(order[t[0]], k)
		}
	}
	for _, t := range actionRows {
		k := rowKey{t[0], t[1], t[2]}
		if _, seen := byKey[k]; !seen {
			// An action-scope row with no class-scope row is impossible —
			// action_filter's body contains effective_filter and reg_callback,
			// which is exactly class_filter's body. Guard anyway: silently
			// dropping it would be a lost edge with nothing to notice it.
			byKey[k] = nil
			order[t[0]] = append(order[t[0]], k)
		}
		byKey[k] = append(byKey[k], t[3])
	}

	return func(c *ctrlClass) []filterRow {
		ck := classKeys[c]
		keys := order[ck]
		// The walk emits the class's own registrations first, then inherited
		// ones; within each, source order. Sorting reproduces that from a set,
		// which a set has no memory of. It matters only because the emitter
		// dedupes edges by (from, to, filter kind) and the first row through
		// decides the surviving meta.
		sorted := append([]rowKey(nil), keys...)
		sort.SliceStable(sorted, func(i, j int) bool {
			ri, rj := regs[sorted[i].reg], regs[sorted[j].reg]
			oi, oj := 1, 1
			if ri.owner == c {
				oi = 0
			}
			if rj.owner == c {
				oj = 0
			}
			if oi != oj {
				return oi < oj
			}
			if ri.ord != rj.ord {
				return ri.ord < rj.ord
			}
			return cbIndex(ri.reg, sorted[i].cb) < cbIndex(rj.reg, sorted[j].cb)
		})

		out := make([]filterRow, 0, len(sorted))
		for _, k := range sorted {
			r := regs[k.reg]
			row := filterRow{
				reg: r.reg, owner: r.owner, cb: k.cb,
				classRule: dlClassFilter, actionRule: dlActionFilter,
			}
			// Actions in the class's source order, not the engine's: an agent
			// reading the chain reads it in the order the file declares.
			acts := map[string]bool{}
			for _, a := range byKey[k] {
				acts[a] = true
			}
			for _, a := range c.actions {
				if acts[a.name] {
					row.actions = append(row.actions, a.name)
				}
			}
			out = append(out, row)
		}
		return out
	}, nil
}

// cbIndex is a callback's position in its registration, so `before_action :a, :b`
// emits a before b the way the walk does. The rules return a set, and a set has
// no memory of the order the symbols were written in.
func cbIndex(r filterReg, cb string) int {
	for i, c := range r.callbacks {
		if c == cb {
			return i
		}
	}
	return len(r.callbacks)
}
