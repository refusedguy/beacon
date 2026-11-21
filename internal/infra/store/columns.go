package store

import (
	"database/sql"
	"strings"
)

// tableColumns returns the column names of a SQLite table as lowercase keys.
// PRAGMA table_info rows have six fields: cid, name, type, notnull, dflt_value
// and pk. Only the name is needed by callers, which use the result as a set.
// table must be a fixed internal name; the value is placed in the PRAGMA
// statement, so it is never taken from external input.
func tableColumns(q columnQuerier, table string) (map[string]bool, error) {
	rows, err := q.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return nil, err
	}
	cols := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, pk int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		cols[strings.ToLower(name)] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return cols, nil
}
