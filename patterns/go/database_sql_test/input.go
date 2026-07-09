//go:build ignore

package main

func open() (*sql.DB, error) {
	return sql.Open("sqlite3", "file:app.db")
}

func queries(ctx Context, db *sql.DB) {
	rows, _ := db.Query("SELECT id, name FROM users WHERE active = ?", true)
	row := db.QueryRowContext(ctx, "SELECT count(*) FROM games")
	db.Exec("INSERT INTO logs (msg) VALUES (?)", msg)
	db.ExecContext(ctx, `UPDATE users SET seen = ? WHERE id = ?`, now, id)
	_, _ = rows, row
}
