package cache

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/jack-work/jacques/config"
)

// Dir is the directory holding the cache database.
func Dir() string {
	return filepath.Join(config.Dir(), "cache")
}

// DefaultMaxRows is the total number of cached rows kept before the least
// recently used entries are evicted. Every cached cell is a VARCHAR, so rows
// are the honest proxy for size: the database file does not shrink when a
// table is dropped (DuckDB reuses the freed blocks), so its byte size cannot
// be the budget.
const DefaultMaxRows = 2_000_000

// MaxRows is the eviction budget: JACQUES_CACHE_MAX_ROWS when set to an
// integer (0 disables eviction), DefaultMaxRows otherwise.
func MaxRows() int {
	if v, err := strconv.Atoi(os.Getenv("JACQUES_CACHE_MAX_ROWS")); err == nil && v >= 0 {
		return v
	}
	return DefaultMaxRows
}

type CacheEntry struct {
	Key       string
	Conn      string
	Query     string
	Rows      int
	Cols      int
	Timestamp time.Time
	SizeBytes int64
}
