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

	// lower-case verb and a leading CTE must still be recognised as SQL
	lc, _ := db.Query("select 1")
	cte := db.QueryRowContext(ctx, "WITH recent AS (SELECT id FROM games) SELECT count(*) FROM recent")
	_, _, _, _ = rows, row, lc, cte
}
