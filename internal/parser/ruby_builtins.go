package parser

import "strings"

// Ruby call-reference deny-lists.
//
// A bare call the extractor cannot bind to a node is normally a real blind
// spot, and the ledger exists so those never vanish silently. But a call to
// `raise` or `render` is not a blind spot: nothing in the repo defines them and
// nothing ever will, so no future linker pass can resolve them. Recording them
// costs twice — they inflate the "verify these N unresolved references
// manually" footer on every trace and impact answer, which is an instruction to
// an agent to go read files, and they bury the app-defined names that *are*
// worth chasing.
//
// Measured on the juniper fleet: 8861 of 11407 `call_ref` entries were
// Ruby, and the names below account for roughly a third of them.
//
// The bar for inclusion is "no repository could define this and mean something
// else". App-defined helpers stay in the ledger even when they are noisy —
// `logger_context` (2210), `logger_yes_no` (365) and `lean_backtrace` (323) are
// the loudest entries on the fleet and they are all genuine missing `calls`
// edges, resolvable through the module-include chain. Suppressing those would
// hide the gap instead of closing it.

// rubyBuiltinCalls are Kernel/Object methods and Rails controller/view methods
// that resolve inside the framework, never inside the repository.
var rubyBuiltinCalls = map[string]bool{
	// Kernel / Object
	"raise": true, "fail": true, "puts": true, "print": true, "p": true,
	"pp": true, "require": true, "require_relative": true, "load": true,
	"loop": true, "lambda": true, "proc": true, "format": true,
	"sprintf": true, "sleep": true, "rand": true, "srand": true,
	"gets": true, "warn": true, "exit": true, "abort": true,
	"catch": true, "throw": true, "block_given?": true, "binding": true,
	"caller": true, "freeze": true, "frozen?": true, "dup": true,
	"clone": true, "send": true, "__send__": true, "public_send": true,
	"super": true, "class": true, "tap": true, "itself": true,
	"instance_variable_get": true, "instance_variable_set": true,
	"instance_variables": true, "respond_to?": true, "is_a?": true,
	"kind_of?": true, "nil?": true, "extend": true, "include": true,
	// Kernel conversion methods that read like constants
	"Array": true, "String": true, "Integer": true, "Float": true,
	"Hash": true, "Rational": true, "Complex": true, "Pathname": true,

	// ActionController / ActionView
	"render": true, "respond_to": true, "head": true, "send_data": true,
	"send_file": true, "url_for": true, "escape": true, "raw": true,
	"sanitize": true, "t": true, "l": true, "params": true, "session": true,
	"cookies": true, "flash": true, "number_to_human_size": true,
	"number_to_human": true, "content_tag": true,

	// `redirect_to` was held out of this list until phase C.3 read its targets
	// from the AST, because the ledger was the only inventory of where those
	// calls were. patterns/ruby/rails_nav.yaml now emits a nav_link producer
	// per redirect, so the call sites survive as nodes with a resolved
	// destination — strictly more than the ledger recorded — and the 129
	// entries are pure noise on every trace footer.
	"redirect_to": true,
}

// railsMigrationDSL are ActiveRecord::Migration schema methods. They are
// scoped to migration files rather than added above because several are
// plausible application method names elsewhere — a service object may well
// define its own `execute` or `say`, and that one *is* a blind spot.
var railsMigrationDSL = map[string]bool{
	"create_table": true, "drop_table": true, "rename_table": true,
	"create_join_table": true, "drop_join_table": true,
	"add_column": true, "remove_column": true, "rename_column": true,
	"change_column": true, "change_column_null": true,
	"change_column_default": true, "column_exists?": true,
	"add_index": true, "remove_index": true, "rename_index": true,
	"index_exists?": true, "table_exists?": true,
	"add_foreign_key": true, "remove_foreign_key": true,
	"validate_foreign_key": true, "foreign_key_exists?": true,
	"add_reference": true, "remove_reference": true,
	"add_timestamps": true, "remove_timestamps": true,
	"enable_extension": true, "disable_extension": true,
	"execute": true, "say": true, "say_with_time": true,
	"reversible": true, "revert": true, "direction": true,
}

// isRubyBuiltinCall reports whether a bare call is framework- or
// language-provided and therefore can never resolve to a node in this graph.
func isRubyBuiltinCall(name, file string) bool {
	if rubyBuiltinCalls[name] {
		return true
	}
	return railsMigrationDSL[name] && isRailsMigrationFile(file)
}

func isRailsMigrationFile(file string) bool {
	return strings.Contains(strings.ReplaceAll(file, "\\", "/"), "/db/migrate/")
}
