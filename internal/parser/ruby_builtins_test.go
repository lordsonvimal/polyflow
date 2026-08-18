package parser

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsRubyBuiltinCall(t *testing.T) {
	t.Parallel()
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

	// C.3 shipped: rails_nav.yaml now emits a nav_link producer per redirect
	// with a resolved destination, so the call sites are recorded as nodes and
	// the ledger entries are redundant noise.
	t.Run("redirect_to is a builtin now that C.3 reads its targets", func(t *testing.T) {
		assert.True(t, isRubyBuiltinCall("redirect_to", app))
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
	t.Parallel()
	assert.True(t, isRailsMigrationFile("/repo/db/migrate/20240101_x.rb"))
	assert.True(t, isRailsMigrationFile(`C:\repo\db\migrate\20240101_x.rb`))
	assert.False(t, isRailsMigrationFile("/repo/app/models/migrate_helper.rb"))
}
