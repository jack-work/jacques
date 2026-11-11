package cache

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jack-work/jacques/data"
)

func newTestCache(t *testing.T) *DuckCache {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("JACQUES_CACHE_MAX_ROWS", "")
	dc, err := NewDuckCache()
	if err != nil {
		t.Fatalf("NewDuckCache: %v", err)
	}
	return dc
}

func store(n int) data.RowStore {
	rows := make([][]interface{}, n)
	for i := range rows {
		rows[i] = []interface{}{i}
	}
	return data.NewMemoryStore([]data.Column{{Name: "n", Type: "int"}}, rows)
}

// exec runs SQL directly against the cache file, to age or legacy-ify rows.
func exec(t *testing.T, q string, args ...interface{}) {
	t.Helper()
	db, err := openRW()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func hit(t *testing.T, dc *DuckCache, query string, maxAge time.Duration) bool {
	t.Helper()
	s, ok := dc.Get(context.Background(), "c", query, maxAge)
	if ok {
		s.Close()
	}
	return ok
}

func TestMaxAge(t *testing.T) {
	dc := newTestCache(t)
	ctx := context.Background()
	if err := dc.Put(ctx, "c", "q", store(3)); err != nil {
		t.Fatal(err)
	}

	// Positive control: a fresh entry is a hit with and without a bound.
	if !hit(t, dc, "q", 0) || !hit(t, dc, "q", time.Hour) {
		t.Fatal("fresh entry should hit")
	}

	// Age it two hours.
	exec(t, "UPDATE cache_meta SET created_unix = ?", time.Now().Add(-2*time.Hour).Unix())
	if hit(t, dc, "q", time.Hour) {
		t.Fatal("2h-old entry must miss under -max-age 1h")
	}
	if !hit(t, dc, "q", 3*time.Hour) {
		t.Fatal("2h-old entry must hit under -max-age 3h")
	}
	if !hit(t, dc, "q", 0) {
		t.Fatal("max-age 0 keeps the old any-age behaviour")
	}

	// A row written before created_unix existed counts as infinitely old.
	exec(t, "UPDATE cache_meta SET created_unix = NULL")
	if hit(t, dc, "q", 24*time.Hour) {
		t.Fatal("legacy entry must miss under any positive max-age")
	}
	if !hit(t, dc, "q", 0) {
		t.Fatal("legacy entry must still hit under max-age 0")
	}
}

func TestGetRecordsAccess(t *testing.T) {
	dc := newTestCache(t)
	ctx := context.Background()
	if err := dc.Put(ctx, "c", "q", store(1)); err != nil {
		t.Fatal(err)
	}
	exec(t, "UPDATE cache_meta SET accessed_unix = 1")
	if !hit(t, dc, "q", 0) {
		t.Fatal("expected hit")
	}
	db, err := openRO()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var acc sql.NullInt64
	if err := db.QueryRow("SELECT accessed_unix FROM cache_meta").Scan(&acc); err != nil {
		t.Fatal(err)
	}
	if !acc.Valid || acc.Int64 < time.Now().Add(-time.Minute).Unix() {
		t.Fatalf("Get must record the access before returning; accessed_unix=%v", acc)
	}
}

func TestEvictLRU(t *testing.T) {
	dc := newTestCache(t)
	t.Setenv("JACQUES_CACHE_MAX_ROWS", "7")
	ctx := context.Background()

	// a, b, c: 3 rows each. After c the budget of 7 holds two of them.
	for i, q := range []string{"a", "b"} {
		if err := dc.Put(ctx, "c", q, store(3)); err != nil {
			t.Fatal(err)
		}
		exec(t, "UPDATE cache_meta SET accessed_unix = ? WHERE query = ?", int64(100+i), q)
	}
	// Read "a" so it is more recent than "b".
	if !hit(t, dc, "a", 0) {
		t.Fatal("a should hit before eviction")
	}
	if err := dc.Put(ctx, "c", "c", store(3)); err != nil {
		t.Fatal(err)
	}

	if !hit(t, dc, "c", 0) {
		t.Fatal("the entry just written is never evicted")
	}
	if !hit(t, dc, "a", 0) {
		t.Fatal("recently read a must survive")
	}
	if hit(t, dc, "b", 0) {
		t.Fatal("least recently used b must be evicted")
	}

	// An entry larger than the whole budget is still kept (it is the one in use).
	if err := dc.Put(ctx, "c", "big", store(50)); err != nil {
		t.Fatal(err)
	}
	if !hit(t, dc, "big", 0) {
		t.Fatal("an oversized entry just written is kept")
	}
	if hit(t, dc, "a", 0) || hit(t, dc, "c", 0) {
		t.Fatal("everything else is evicted to make room")
	}
}

func TestEvictDisabled(t *testing.T) {
	dc := newTestCache(t)
	t.Setenv("JACQUES_CACHE_MAX_ROWS", "0")
	ctx := context.Background()
	for _, q := range []string{"a", "b", "c"} {
		if err := dc.Put(ctx, "c", q, store(5)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"a", "b", "c"} {
		if !hit(t, dc, q, 0) {
			t.Fatalf("%s evicted with eviction disabled", q)
		}
	}
}
