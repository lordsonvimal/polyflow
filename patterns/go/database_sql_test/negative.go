//go:build ignore

package main

// Same method names with non-SQL-string arguments must not match: URL query
// parsing, os file opens, exec.Command, dynamic SQL held in a variable.
func other(u *URL, cmd Runner, stmt string) {
	values := u.Query()
	f, _ := os.Open("/etc/hosts")
	cmd.Exec(program)
	db.Query(stmt)
	gorm.Open(postgres.Open(dsn), config)
	_, _ = values, f
}
