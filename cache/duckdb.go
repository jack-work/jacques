package cache

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/data"
	"github.com/jack-work/jacques/logging"

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
	if err != nil {
		return err
	}
	// Ages are kept as Unix seconds written by Go. created_at/accessed_at are
	// TIMESTAMP columns filled from current_timestamp, which DuckDB converts
	// through the session time zone: read back into Go they carry local wall
	// time labelled UTC, so any age computed from them is off by the zone
	// offset. Rows written before these columns existed have NULL here and
	// count as infinitely old.
	for _, col := range []string{"created_unix", "accessed_unix"} {
		if _, err := db.Exec("ALTER TABLE cache_meta ADD COLUMN IF NOT EXISTS " + col + " BIGINT"); err != nil {
			return err
		}
	}
	return nil
}

func (c *DuckCache) Close() error { return nil }

func tableKey(connName, query string) string {
	h := sha256.Sum256([]byte(connName + "\x00" + query))
	return fmt.Sprintf("%x", h[:12])
}

func tableName(key string) string {
	return "cache_" + key
}

// Get returns the cached result for (connName, query). maxAge 0 accepts an
// entry of any age (the behaviour before -max-age existed); a positive maxAge
// accepts only an entry written less than maxAge ago, so automated callers can
// use the cache without risking an arbitrarily stale answer.
func (c *DuckCache) Get(ctx context.Context, connName, query string, maxAge time.Duration) (data.RowStore, bool) {
	db, err := openRO()
	if err != nil {
		return nil, false
	}
	defer db.Close()

	key := tableKey(connName, query)
	tbl := tableName(key)

	var rowCount int
	var created sql.NullInt64
	err = db.QueryRowContext(ctx,
		"SELECT row_count, created_unix FROM cache_meta WHERE key = ?", key,
	).Scan(&rowCount, &created)
	if err != nil {
		return nil, false
	}
	if maxAge > 0 {
		age := time.Duration(-1)
		if created.Valid {
			age = time.Since(time.Unix(created.Int64, 0))
		}
		if age < 0 || age >= maxAge {
			logging.Info(ctx, "duckdb cache stale",
				logging.String("key", key),
				logging.String("connection", connName),
				logging.String("age", age.Truncate(time.Second).String()),
				logging.String("max_age", maxAge.String()),
			)
			return nil, false
		}
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

	// Record the access for LRU eviction. Synchronous on purpose: a goroutine
	// here never ran in the CLI, which exits as soon as it has printed, so
	// accessed_at was never updated and "least recently used" meant "oldest".
	// A failure (another process holds the write lock) only costs recency.
	db.Close()
	if wdb, err := openRW(); err == nil {
		wdb.ExecContext(ctx,
			"UPDATE cache_meta SET accessed_at = current_timestamp, accessed_unix = ? WHERE key = ?",
			time.Now().Unix(), key)
		wdb.Close()
	}

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

	now := time.Now().Unix()
	if _, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO cache_meta
		   (key, conn_name, query, table_name, row_count, col_count, created_unix, accessed_unix)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		key, connName, query, tbl, store.RowCount(), len(cols), now, now); err != nil {
		return fmt.Errorf("record cache entry: %w", err)
	}

	if evicted, err := evict(ctx, db, MaxRows(), key); err != nil {
		logging.Warn(ctx, "duckdb cache eviction failed", logging.String("error", err.Error()))
	} else if evicted > 0 {
		logging.Info(ctx, "duckdb cache evicted", logging.Int("entries", evicted))
	}

	logging.Info(ctx, "duckdb cache put",
		logging.String("key", key),
		logging.String("connection", connName),
		logging.Int("rows", store.RowCount()),
	)

	return nil
}

// evict drops least recently used entries until the cached rows fit within
// maxRows. keep (the entry just written) is never evicted, even when it alone
// exceeds the budget. maxRows 0 disables eviction.
func evict(ctx context.Context, db *sql.DB, maxRows int, keep string) (int, error) {
	if maxRows <= 0 {
		return 0, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT key, table_name, row_count FROM cache_meta
		ORDER BY key = ? DESC, COALESCE(accessed_unix, 0) DESC`, keep)
	if err != nil {
		return 0, err
	}
	type victim struct{ key, table string }
	var victims []victim
	total := 0
	for rows.Next() {
		var k, t string
		var n int
		if err := rows.Scan(&k, &t, &n); err != nil {
			rows.Close()
			return 0, err
		}
		total += n
		if k != keep && total > maxRows {
			victims = append(victims, victim{k, t})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, v := range victims {
		if _, err := db.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", v.table)); err != nil {
			return 0, err
		}
		if _, err := db.ExecContext(ctx, "DELETE FROM cache_meta WHERE key = ?", v.key); err != nil {
			return 0, err
		}
	}
	return len(victims), nil
}

func (c *DuckCache) List(ctx context.Context) ([]CacheEntry, error) {
	db, err := openRO()
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx,
		"SELECT key, conn_name, query, row_count, col_count, created_unix FROM cache_meta ORDER BY COALESCE(accessed_unix, 0) DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []CacheEntry
	for rows.Next() {
		var e CacheEntry
		var created sql.NullInt64
		if err := rows.Scan(&e.Key, &e.Conn, &e.Query, &e.Rows, &e.Cols, &created); err != nil {
			continue
		}
		if created.Valid {
			e.Timestamp = time.Unix(created.Int64, 0)
		}
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
