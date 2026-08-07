package parser

import (
	"go/constant"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/ssa"
)

// Tier J.1 — static struct tables as broker topology.
//
// The authoritative AMQP topology of a Go service is frequently a package-level
// table rather than a sequence of literal calls:
//
//	func Queues() []QueueDecl {
//	    return []QueueDecl{
//	        {Name: QueueBuildLogs, Durable: true, Exchange: ExchangeBuildLogs,
//	         RoutingKeys: []string{"logs.build.*", "logs.workflow.*"}},
//	        …
//	    }
//	}
//
//	func declareQueues(d queueDeclarer) error {
//	    for _, q := range Queues() {
//	        for _, routingKey := range q.RoutingKeys {
//	            d.BindQueue(q.Name, routingKey, q.Exchange)   // ← the binding
//	        }
//	    }
//	}
//
// Tier W resolves a bind whose exchange is an *ssa.Const. Here the exchange
// reaching `channel.QueueBind` is a field load off a range element of a slice
// returned by another function — never a constant, which is the gap
// go_amqp_names.go:43 records as a follow-up.
//
// This file decodes such a table into rows of string constants and recognises a
// field load off one of its rows. Nothing is guessed: a field whose value is not
// a string constant (or a fully-constant []string) is simply absent from the
// decoded row, and a table shape the decoder does not fully understand yields
// ok=false rather than a partial topology.

// structTable is one decoded static []T composite-literal table.
// Scalar string fields land in Fields; []string fields land in Slices.
type structTable struct {
	TypeName string // "QueueDecl"
	Rows     []structTableRow
}

// structTableRow is one decoded row of a structTable. Fields the decoder could
// not reduce to constants are absent from both maps — never zero-valued, so a
// caller can tell "declared empty" from "not understood".
type structTableRow struct {
	Fields map[string]string   // {"Name":"build_logs_queue", "Exchange":"build_logs"}
	Slices map[string][]string // {"RoutingKeys":["logs.build.*","logs.workflow.*"]}
	Pos    token.Pos           // position of the row's composite literal
}

// maxTableDeref bounds the pointer/alias chases below so a pathological SSA
// shape (or a cycle a future builder change could introduce) cannot spin.
const maxTableDeref = 8

// resolveStructTable decodes the return value of a zero-argument function whose
// body is (or reduces to) a single `return []T{ {…}, {…} }` composite literal.
// String-const fields — including package-level const identifiers, which SSA has
// already inlined to *ssa.Const — are decoded; every other field is skipped,
// never guessed. Returns ok=false for any shape it does not fully understand
// (bug-class: never fabricate topology).
func resolveStructTable(fn *ssa.Function) (structTable, bool) {
	if fn == nil || len(fn.Blocks) == 0 || len(fn.Params) != 0 {
		return structTable{}, false
	}
	alloc, st, typeName, rowCount, ok := tableReturnAlloc(fn)
	if !ok {
		return structTable{}, false
	}

	rows := make([]structTableRow, rowCount)
	for i := range rows {
		rows[i] = structTableRow{Fields: map[string]string{}, Slices: map[string][]string{}}
	}
	// Two shapes reach the same table. A short row literal stores its fields
	// straight into the array slot (`*&t0[1].Name = …`); a row the builder
	// materialises separately fills a `local T (complit)` slot and copies the
	// whole struct in (`*&t0[0] = *t2`). Both are decoded.
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			store, ok := instr.(*ssa.Store)
			if !ok {
				continue
			}
			switch addr := store.Addr.(type) {
			case *ssa.FieldAddr:
				ia, ok := addr.X.(*ssa.IndexAddr)
				if !ok || ia.X != alloc {
					continue
				}
				idx, ok := constIntValue(ia.Index)
				if !ok || idx < 0 || idx >= rowCount {
					continue
				}
				row := &rows[idx]
				if !row.Pos.IsValid() {
					row.Pos = firstValidPos(store.Pos(), addr.Pos(), ia.Pos())
				}
				decodeTableField(row, st.Field(addr.Field).Name(), store.Val)
			case *ssa.IndexAddr:
				if addr.X != alloc {
					continue
				}
				idx, ok := constIntValue(addr.Index)
				if !ok || idx < 0 || idx >= rowCount {
					continue
				}
				load, ok := ssaUnwrap(store.Val).(*ssa.UnOp)
				if !ok || load.Op != token.MUL {
					continue
				}
				rowAlloc, ok := load.X.(*ssa.Alloc)
				if !ok {
					continue
				}
				row := &rows[idx]
				if !row.Pos.IsValid() {
					row.Pos = firstValidPos(rowAlloc.Pos(), store.Pos())
				}
				decodeStructAlloc(row, st, rowAlloc)
			}
		}
	}
	return structTable{TypeName: typeName, Rows: rows}, true
}

// decodeStructAlloc reads the field stores of one `local T (complit)` slot into a
// row. Fields whose value is not a constant string (or fully-constant []string)
// are left absent.
func decodeStructAlloc(row *structTableRow, st *types.Struct, a *ssa.Alloc) {
	if a.Referrers() == nil {
		return
	}
	for _, ref := range *a.Referrers() {
		fa, ok := ref.(*ssa.FieldAddr)
		if !ok || fa.X != a || fa.Referrers() == nil {
			continue
		}
		for _, r2 := range *fa.Referrers() {
			store, ok := r2.(*ssa.Store)
			if !ok || store.Addr != fa {
				continue
			}
			decodeTableField(row, st.Field(fa.Field).Name(), store.Val)
		}
	}
}

// decodeTableField records one field value on a row, ignoring anything that is
// not a string constant or a fully-constant []string literal.
func decodeTableField(row *structTableRow, name string, val ssa.Value) {
	if s, ok := ssaConstString(val); ok {
		row.Fields[name] = s
		return
	}
	if vals, ok := resolveStringSliceLiteral(val); ok {
		row.Slices[name] = vals
	}
}

// tableReturnAlloc identifies the `[]T{…}` composite literal a table function
// returns: the single return of a single value that is a slice of a local array
// of a named struct type.
func tableReturnAlloc(fn *ssa.Function) (alloc *ssa.Alloc, st *types.Struct, typeName string, rowCount int, ok bool) {
	var ret *ssa.Return
	for _, b := range fn.Blocks {
		for _, instr := range b.Instrs {
			r, isRet := instr.(*ssa.Return)
			if !isRet {
				continue
			}
			if ret != nil {
				return nil, nil, "", 0, false // multiple returns: not a plain table
			}
			ret = r
		}
	}
	if ret == nil || len(ret.Results) != 1 {
		return nil, nil, "", 0, false
	}
	sl, isSlice := ssaUnwrap(ret.Results[0]).(*ssa.Slice)
	if !isSlice {
		return nil, nil, "", 0, false // e.g. `return buildQueues()`
	}
	a, isAlloc := sl.X.(*ssa.Alloc)
	if !isAlloc {
		return nil, nil, "", 0, false
	}
	arr, isArr := derefType(a.Type()).(*types.Array)
	if !isArr {
		return nil, nil, "", 0, false
	}
	named, isNamed := types.Unalias(arr.Elem()).(*types.Named)
	if !isNamed {
		return nil, nil, "", 0, false // anonymous struct element: no table type name
	}
	st, isStruct := named.Underlying().(*types.Struct)
	if !isStruct {
		return nil, nil, "", 0, false
	}
	return a, st, named.Obj().Name(), int(arr.Len()), true
}

// resolveStringSliceLiteral decodes a `[]string{"a","b"}` composite literal into
// its values. Every element must be a string constant: a partially-constant
// slice would silently under-report the topology, so it is rejected wholesale.
func resolveStringSliceLiteral(v ssa.Value) ([]string, bool) {
	sl, isSlice := ssaUnwrap(v).(*ssa.Slice)
	if !isSlice {
		return nil, false
	}
	a, isAlloc := sl.X.(*ssa.Alloc)
	if !isAlloc {
		return nil, false
	}
	arr, isArr := derefType(a.Type()).(*types.Array)
	if !isArr {
		return nil, false
	}
	basic, isBasic := types.Unalias(arr.Elem()).(*types.Basic)
	if !isBasic || basic.Kind() != types.String {
		return nil, false
	}
	n := int(arr.Len())
	out := make([]string, n)
	filled := make([]bool, n)
	if a.Referrers() == nil {
		return nil, false
	}
	for _, ref := range *a.Referrers() {
		ia, isIndex := ref.(*ssa.IndexAddr)
		if !isIndex || ia.X != a || ia.Referrers() == nil {
			continue
		}
		idx, ok := constIntValue(ia.Index)
		if !ok || idx < 0 || idx >= n {
			continue
		}
		for _, r2 := range *ia.Referrers() {
			store, isStore := r2.(*ssa.Store)
			if !isStore || store.Addr != ia {
				continue
			}
			if s, ok := ssaConstString(store.Val); ok {
				out[idx], filled[idx] = s, true
			}
		}
	}
	for _, f := range filled {
		if !f {
			return nil, false
		}
	}
	return out, true
}

// tableFieldOf resolves an ssa.Value that is a field load off a range element of
// a static table to (fieldName, table, ok). It recognises the canonical
// `for _, q := range Queues() { … q.Exchange … }` shape — UnOp(Load) → FieldAddr
// → IndexAddr → the slice produced by a Call to a table function — in both its
// registerised (*ssa.Field over *ssa.Index) and address-taken forms, plus one
// extra layer for an element of a slice-valued field
// (`for _, k := range q.RoutingKeys`).
func tableFieldOf(v ssa.Value, inService map[*ssa.Function]bool) (field string, tbl structTable, ok bool) {
	v = ssaUnwrap(v)
	if field, tbl, ok := tableRowField(v, inService); ok {
		return field, tbl, true
	}
	if elem, ok := sliceElementSource(v); ok {
		if field, tbl, ok := tableRowField(ssaUnwrap(elem), inService); ok {
			return field, tbl, true
		}
	}
	return "", structTable{}, false
}

// tableRowField matches a single field selection off a table row and names it.
func tableRowField(v ssa.Value, inService map[*ssa.Function]bool) (string, structTable, bool) {
	switch t := v.(type) {
	case *ssa.Field:
		st, ok := structTypeOf(t.X.Type())
		if !ok {
			return "", structTable{}, false
		}
		tbl, ok := tableOfRowValue(t.X, inService, 0)
		if !ok {
			return "", structTable{}, false
		}
		return st.Field(t.Field).Name(), tbl, true
	case *ssa.UnOp:
		if t.Op != token.MUL {
			return "", structTable{}, false
		}
		fa, isField := t.X.(*ssa.FieldAddr)
		if !isField {
			return "", structTable{}, false
		}
		st, ok := structTypeOf(derefType(fa.X.Type()))
		if !ok {
			return "", structTable{}, false
		}
		tbl, ok := tableOfRowAddr(fa.X, inService, 0)
		if !ok {
			return "", structTable{}, false
		}
		return st.Field(fa.Field).Name(), tbl, true
	}
	return "", structTable{}, false
}

// tableOfRowValue resolves a struct *value* known to be one row of a table.
func tableOfRowValue(v ssa.Value, inService map[*ssa.Function]bool, depth int) (structTable, bool) {
	if depth > maxTableDeref {
		return structTable{}, false
	}
	switch t := ssaUnwrap(v).(type) {
	case *ssa.UnOp:
		if t.Op == token.MUL {
			return tableOfRowAddr(t.X, inService, depth+1)
		}
	case *ssa.Index:
		return tableOfSliceValue(t.X, inService, depth+1)
	}
	return structTable{}, false
}

// tableOfRowAddr resolves the *address* of one row of a table.
func tableOfRowAddr(addr ssa.Value, inService map[*ssa.Function]bool, depth int) (structTable, bool) {
	if depth > maxTableDeref {
		return structTable{}, false
	}
	switch t := ssaUnwrap(addr).(type) {
	case *ssa.IndexAddr:
		return tableOfSliceValue(t.X, inService, depth+1)
	case *ssa.Alloc:
		// The range variable kept its slot (its address is taken by FieldAddr,
		// so lifting cannot registerise it): follow the single value stored.
		if val, ok := soleStoredValue(t); ok {
			return tableOfRowValue(val, inService, depth+1)
		}
	}
	return structTable{}, false
}

// tableOfSliceValue resolves the slice being ranged over to its table function.
func tableOfSliceValue(v ssa.Value, inService map[*ssa.Function]bool, depth int) (structTable, bool) {
	if depth > maxTableDeref {
		return structTable{}, false
	}
	switch t := ssaUnwrap(v).(type) {
	case *ssa.Call:
		callee, ok := t.Common().Value.(*ssa.Function)
		if !ok || t.Common().IsInvoke() || !inService[callee] {
			return structTable{}, false
		}
		return resolveStructTable(callee)
	case *ssa.UnOp:
		if t.Op == token.MUL {
			return tableOfSliceValue(t.X, inService, depth+1)
		}
	case *ssa.Alloc:
		if val, ok := soleStoredValue(t); ok {
			return tableOfSliceValue(val, inService, depth+1)
		}
	}
	return structTable{}, false
}

// sliceElementSource reports the slice a value was indexed out of, for the
// `for _, k := range q.RoutingKeys` layer.
func sliceElementSource(v ssa.Value) (ssa.Value, bool) {
	switch t := ssaUnwrap(v).(type) {
	case *ssa.Index:
		return t.X, true
	case *ssa.UnOp:
		if t.Op != token.MUL {
			return nil, false
		}
		if ia, ok := t.X.(*ssa.IndexAddr); ok {
			return ia.X, true
		}
	}
	return nil, false
}

// soleStoredValue returns the value stored into an alloc when there is exactly
// one such store. More than one store means the slot is reassigned and no
// single table can be claimed.
func soleStoredValue(a *ssa.Alloc) (ssa.Value, bool) {
	if a.Referrers() == nil {
		return nil, false
	}
	var val ssa.Value
	for _, ref := range *a.Referrers() {
		store, ok := ref.(*ssa.Store)
		if !ok || store.Addr != a {
			continue
		}
		if val != nil {
			return nil, false
		}
		val = store.Val
	}
	return val, val != nil
}

// structTypeOf returns the underlying struct of a type, or ok=false.
func structTypeOf(t types.Type) (*types.Struct, bool) {
	st, ok := t.Underlying().(*types.Struct)
	return st, ok
}

// derefType strips one pointer level, if present.
func derefType(t types.Type) types.Type {
	if p, ok := types.Unalias(t).Underlying().(*types.Pointer); ok {
		return p.Elem()
	}
	return t
}

// constIntValue returns the int value of a constant SSA value.
func constIntValue(v ssa.Value) (int, bool) {
	c, ok := ssaUnwrap(v).(*ssa.Const)
	if !ok || c.Value == nil || c.Value.Kind() != constant.Int {
		return 0, false
	}
	i64, exact := constant.Int64Val(c.Value)
	if !exact {
		return 0, false
	}
	return int(i64), true
}

// firstValidPos returns the first valid position among its arguments.
func firstValidPos(candidates ...token.Pos) token.Pos {
	for _, p := range candidates {
		if p.IsValid() {
			return p
		}
	}
	return token.NoPos
}
