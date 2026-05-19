package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/data"
	"github.com/jokellih/jacques/logging"

	_ "github.com/duckdb/duckdb-go/v2"
)

// DuckCache provides query result caching backed by DuckDB.
// Connections are opened on demand and released immediately after each
// operation so that multiple processes can access the cache concurrently.
type DuckCache struct{}

func NewDuckCache() (*DuckCache, error) {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return nil, err
	}
	db, err := openRW()
	if err != nil {
		return nil, err
	}
	err = initSchema(db)
	db.Close()
	if err != nil {
		return nil, err
	}
	return &DuckCache{}, nil
}

func DuckDBPath() string {
	return config.Dir() + "/cache.duckdb"
}

func openRW() (*sql.DB, error) {
	db, err := sql.Open("duckdb", DuckDBPath())
	if err != nil {
		return nil, fmt.Errorf("open cache db %q: %w", DuckDBPath(), err)
	}
	return db, nil
}

func openRO() (*sql.DB, error) {
	db, err := sql.Open("duckdb", DuckDBPath()+"?access_mode=read_only")
	if err != nil {
		return nil, fmt.Errorf("open cache db (ro) %q: %w", DuckDBPath(), err)
	}
	return db, nil
}

func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS cache_meta (
			key         VARCHAR PRIMARY KEY,
			conn_name   VARCHAR,
			query       VARCHAR,
			table_name  VARCHAR,
			row_count   INTEGER,
			col_count   INTEGER,
			created_at  TIMESTAMP DEFAULT current_timestamp,
			accessed_at TIMESTAMP DEFAULT current_timestamp
		)
	`)
	return err
}

func (c *DuckCache) Close() error { return nil }

func tableKey(connName, query string) string {
	h := sha256.Sum256([]byte(connName + "\x00" + query))
	return fmt.Sprintf("%x", h[:12])
}

func tableName(key string) string {
	return "cache_" + key
}

func (c *DuckCache) Get(ctx context.Context, connName, query string) (data.RowStore, bool) {
	db, err := openRO()
	if err != nil {
		return nil, false
	}
	defer db.Close()

	key := tableKey(connName, query)
	tbl := tableName(key)

	var rowCount int
	err = db.QueryRowContext(ctx,
		"SELECT row_count FROM cache_meta WHERE key = ?", key,
	).Scan(&rowCount)
	if err != nil {
		return nil, false
	}

	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s", tbl))
	if err != nil {
		return nil, false
	}
	defer rows.Close()

	store, err := rowsToStore(rows)
	if err != nil {
		return nil, false
	}

	logging.Info(ctx, "duckdb cache hit",
		logging.String("key", key),
		logging.String("connection", connName),
		logging.Int("rows", store.RowCount()),
	)

	// Touch accessed_at in a brief write connection
	go func() {
		wdb, err := openRW()
		if err != nil {
			return
		}
		defer wdb.Close()
		wdb.ExecContext(context.Background(),
			"UPDATE cache_meta SET accessed_at = current_timestamp WHERE key = ?", key)
	}()

	return store, true
}

func (c *DuckCache) Put(ctx context.Context, connName, query string, store data.RowStore) error {
	db, err := openRW()
	if err != nil {
		return err
	}
	defer db.Close()

	key := tableKey(connName, query)
	tbl := tableName(key)
	cols := store.Columns()

	db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tbl))
	db.ExecContext(ctx, "DELETE FROM cache_meta WHERE key = ?", key)

	var colDefs []string
	for _, col := range cols {
		colDefs = append(colDefs, fmt.Sprintf("%s VARCHAR", quoteIdent(col.Name)))
	}
	createSQL := fmt.Sprintf("CREATE TABLE %s (%s)", tbl, strings.Join(colDefs, ", "))
	if _, err := db.ExecContext(ctx, createSQL); err != nil {
		return fmt.Errorf("create cache table: %w", err)
	}

	if store.RowCount() > 0 {
		placeholders := "(" + strings.Repeat("?,", len(cols)-1) + "?)"
		batchSize := 1000
		for start := 0; start < store.RowCount(); start += batchSize {
			end := start + batchSize
			if end > store.RowCount() {
				end = store.RowCount()
			}

			var valueClauses []string
			var args []interface{}
			for r := start; r < end; r++ {
				row, err := store.Row(r)
				if err != nil {
					return err
				}
				valueClauses = append(valueClauses, placeholders)
				for _, v := range row {
					args = append(args, fmt.Sprintf("%v", v))
				}
			}

			insertSQL := fmt.Sprintf("INSERT INTO %s VALUES %s",
				tbl, strings.Join(valueClauses, ", "))
			if _, err := db.ExecContext(ctx, insertSQL, args...); err != nil {
				return fmt.Errorf("insert cache rows: %w", err)
			}
		}
	}

	db.ExecContext(ctx,
		`INSERT OR REPLACE INTO cache_meta (key, conn_name, query, table_name, row_count, col_count)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		key, connName, query, tbl, store.RowCount(), len(cols))

	logging.Info(ctx, "duckdb cache put",
		logging.String("key", key),
		logging.String("connection", connName),
		logging.Int("rows", store.RowCount()),
	)

	return nil
}

func (c *DuckCache) List(ctx context.Context) ([]CacheEntry, error) {
	db, err := openRO()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		"SELECT key, conn_name, query, row_count, col_count, created_at, accessed_at FROM cache_meta ORDER BY accessed_at DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []CacheEntry
	for rows.Next() {
		var e CacheEntry
		var createdAt, accessedAt time.Time
		if err := rows.Scan(&e.Key, &e.Conn, &e.Query, &e.Rows, &e.Cols, &createdAt, &accessedAt); err != nil {
			continue
		}
		e.Timestamp = createdAt
		entries = append(entries, e)
	}
	return entries, nil
}

func (c *DuckCache) Clear(ctx context.Context) error {
	db, err := openRW()
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, "SELECT table_name FROM cache_meta")
	if err != nil {
		return err
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var tbl string
		rows.Scan(&tbl)
		tables = append(tables, tbl)
	}

	for _, tbl := range tables {
		db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tbl))
	}
	_, err = db.ExecContext(ctx, "DELETE FROM cache_meta")
	return err
}

func (c *DuckCache) QueryCache(ctx context.Context, sqlStr string) (data.RowStore, error) {
	db, err := openRO()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, sqlStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return rowsToStore(rows)
}

func rowsToStore(rows *sql.Rows) (data.RowStore, error) {
	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, err
	}

	columns := make([]data.Column, len(colTypes))
	for i, ct := range colTypes {
		columns[i] = data.Column{Name: ct.Name(), Type: "string"}
	}

	scanDest := make([]interface{}, len(columns))
	for i := range scanDest {
		scanDest[i] = new(interface{})
	}

	var resultRows [][]interface{}
	for rows.Next() {
		if err := rows.Scan(scanDest...); err != nil {
			return nil, err
		}
		row := make([]interface{}, len(columns))
		for i, ptr := range scanDest {
			row[i] = *(ptr.(*interface{}))
		}
		resultRows = append(resultRows, row)
	}
	return data.NewMemoryStore(columns, resultRows), nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
