//go:build ignore

package main

// Raw database/sql shapes and non-pointer method chains must not match GORM
// patterns: sql.Open's first arg is a string (not a dialector call), and
// generic Find/Create calls without &target pointers are unrelated APIs.
func rawSQL(db *sql.DB) {
	db2, _ := sql.Open("postgres", dsn)
	rows, _ := db.Query("SELECT id FROM users")
	_ = rows
	_ = db2
	list.Find(predicate)
	registry.Create(name, options)
	index.First(3)
}
