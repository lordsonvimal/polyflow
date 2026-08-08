package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsRubyBuiltinCall(t *testing.T) {
	const app = "/repo/app/services/thing.rb"
	const migration = "/repo/db/migrate/20240101_add_users.rb"

	t.Run("language and framework builtins can never resolve", func(t *testing.T) {
		for _, name := range []string{"raise", "puts", "render", "respond_to", "t", "super", "Pathname", "Array"} {
			assert.True(t, isRubyBuiltinCall(name, app), name)
		}
	})

	// The loudest entries on the fleet are app-defined and every one of them is
	// a genuine missing `calls` edge, resolvable through the module-include
	// chain. Suppressing them would hide the gap instead of closing it.
	t.Run("app-defined helpers stay in the ledger", func(t *testing.T) {
		for _, name := range []string{"logger_context", "logger_yes_no", "lean_backtrace", "set_flash", "render_success_status"} {
			assert.False(t, isRubyBuiltinCall(name, app), name)
		}
	})

	// `redirect_to` is as much a builtin as `render`, but phase C.3 mines
	// redirect call sites to build the navigates_to graph and the ledger is
	// currently the only inventory of where they are.
	t.Run("redirect_to is deliberately still ledgered until C.3", func(t *testing.T) {
		assert.False(t, isRubyBuiltinCall("redirect_to", app))
	})

	// Migration DSL is scoped: a service object that defines its own `execute`
	// really is a blind spot when the call does not bind.
	t.Run("migration DSL only applies inside db/migrate", func(t *testing.T) {
		for _, name := range []string{"add_column", "remove_foreign_key", "create_table", "execute", "say"} {
			assert.True(t, isRubyBuiltinCall(name, migration), name+" in migration")
			assert.False(t, isRubyBuiltinCall(name, app), name+" in app code")
		}
	})
}

func TestIsRailsMigrationFile(t *testing.T) {
	assert.True(t, isRailsMigrationFile("/repo/db/migrate/20240101_x.rb"))
	assert.True(t, isRailsMigrationFile(`C:\repo\db\migrate\20240101_x.rb`))
	assert.False(t, isRailsMigrationFile("/repo/app/models/migrate_helper.rb"))
}
