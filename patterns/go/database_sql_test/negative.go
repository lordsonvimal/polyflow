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

// A web-framework request context exposes Query/Exec-shaped methods whose
// argument is a short string LITERAL (a param name, not SQL). These must not
// be mistaken for database/sql call sites — otherwise every handler sprouts a
// phantom datastore node that fans out onto every driver in the module graph.
func handler(c RequestCtx) {
	q := c.Query("q")
	kind := c.Query("image_type")
	page := c.Query("page")
	_, _, _ = q, kind, page
}
