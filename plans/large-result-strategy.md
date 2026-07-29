# Large Query Result Strategy

## Problem

Jacques currently loads the entire Kusto response into memory at once, then
duplicates it as formatted strings for rendering. For a query returning 500K
rows × 48 columns of log data, memory usage follows this chain:

```
HTTP response body ([]byte)          ~500 MB  (raw JSON)
  → json.Unmarshal into []v2Frame    ~800 MB  (interface{} boxing, string copies)
    → QueryResult.Rows               ~800 MB  (shared with above, but pinned)
      → cells [][]string             ~400 MB  (formatted copies of every value)
        → colWidths scan             trivial  (but touches every cell)
          → search scan              trivial  (but touches every cell)
```

Peak memory is roughly **2–3× the wire size** because Go's JSON decoder
allocates boxed values, the original `[]byte` stays live until the frame
parse completes, and `buildCells` creates a parallel string matrix.

The TUI only ever displays ~30–50 rows at a time. Materializing all 500K
rows as styled strings up front is pure waste.

## Current Allocation Hotspots

| Location | What it allocates | Why it hurts |
|---|---|---|
| `client.go: io.ReadAll(resp.Body)` | Full response as `[]byte` | Cannot GC until unmarshal finishes |
| `client.go: json.Unmarshal` | Every cell as `interface{}` (boxed float64, string, bool, map, slice) | 2× overhead vs raw bytes for numeric types |
| `tui.go: buildCells()` | `[][]string` — formatted copy of every cell | Doubles string memory; redundant for off-screen rows |
| `tui.go: computeNaturalWidths()` | Scans every cell to find max width | Correct but forces full materialization before first paint |
| `tui.go: runSearch()` | Scans every cell with `strings.ToLower` | Allocates lowered copies; O(rows × cols) per search |
| `table.go: Table()` | Same `[][]string` pattern | Fine for piped output but same issue if rows are huge |

---

## Strategies

### Strategy A — Soft Limit with User Warning (quick win)

Add a `--max-results` flag (default 10,000). If the Kusto response exceeds
this, truncate `QueryResult.Rows` and print a warning:

```
warning: result truncated to 10,000 rows (query returned 523,841).
         Use --max-results to raise the limit, or add a KQL filter.
```

This doesn't solve the architectural problem but immediately prevents OOM
for casual queries that accidentally return millions of rows.

**Cost:** ~10 lines of code. Ship immediately.

### Strategy B — Lazy Cell Formatting

Replace `cells [][]string` (eagerly formatted for every row) with
on-demand formatting. The TUI only needs formatted strings for visible rows
+ a small look-ahead buffer.

```go
type lazyCells struct {
    result *kusto.QueryResult
    opts   TableOptions
    cache  map[int][]string  // row index → formatted cells
    cap    int               // max cached rows (e.g., 500)
}

func (lc *lazyCells) Row(i int) []string {
    if row, ok := lc.cache[i]; ok {
        return row
    }
    row := formatRow(lc.result.Rows[i], lc.result.Columns, lc.opts)
    lc.cache[i] = row
    if len(lc.cache) > lc.cap {
        lc.evictFarthest(i)
    }
    return row
}
```

**Benefit:** Memory for formatted cells drops from O(N × C) to O(cache_cap × C).
Column width computation can sample (first 1000 rows + random sample) instead
of scanning all rows.

**Cost:** Moderate refactor of TUI model; `cells[r][c]` access becomes
`lc.Row(r)[c]`.

### Strategy C — Streaming JSON Decode

Replace `io.ReadAll` + `json.Unmarshal` with a streaming decoder that
processes the v2 frame array incrementally:

```go
dec := json.NewDecoder(resp.Body)
// read opening '['
dec.Token()
for dec.More() {
    var frame v2Frame
    dec.Decode(&frame)
    if frame.TableName == "PrimaryResult" {
        // process rows as they arrive
    }
}
```

This alone cuts peak memory by ~1× (no `[]byte` buffer). Combined with
other strategies it enables true streaming into a backing store.

**Cost:** Rewrite `doQuery`. Medium effort, but the v2 frame format is
well-structured for this.

### Strategy D — Memory-Mapped Row Store

For truly large results (>100K rows), write decoded rows to a temporary
file and memory-map it for random access. Each row is stored as a
length-prefixed msgpack or flatbuffers record.

```
┌─────────────────────────────────────────┐
│ Temp file: %LOCALAPPDATA%/jacques/cache │
│                                         │
│  [len][row0_msgpack]                    │
│  [len][row1_msgpack]                    │
│  ...                                    │
│  [len][rowN_msgpack]                    │
│                                         │
│  Index: []int64 (file offsets)          │
└─────────────────────────────────────────┘
```

The TUI reads rows by seeking to the offset and decoding on demand.
The index array is ~8 bytes per row (8 MB for 1M rows — trivial).

**Benefit:** Resident memory stays bounded regardless of result size.
The OS page cache handles hot/cold row access naturally.

**Cost:** Significant new code. Adds a temp-file lifecycle (cleanup on
exit, crash recovery). Needs a serialization format — msgpack is a good
fit (small, fast, schema-free). Worth it only if users regularly query
>100K rows.

### Strategy E — Local Database (SQLite)

Stream decoded rows into a local SQLite database (via `modernc.org/sqlite`
for a pure-Go driver, or `mattn/go-sqlite3` for CGo). Create a table
matching the Kusto schema on the fly:

```sql
CREATE TABLE results (
    _rowid INTEGER PRIMARY KEY,
    env_time TEXT,
    message TEXT,
    traceLevel TEXT,
    ...
);
CREATE INDEX idx_rowid ON results(_rowid);
```

The TUI reads pages with `SELECT * FROM results LIMIT 50 OFFSET ?` and
search becomes `SELECT _rowid FROM results WHERE message LIKE '%term%'`.

#### Where SQLite Wins

- **Search for free.** `LIKE`, `GLOB`, and with FTS5, full-text search
  across any column — no custom inverted index needed. This is a massive
  win over every other strategy where search is a hand-rolled O(N) scan.
- **Secondary queries.** The user could filter, sort, or aggregate in-place
  without re-querying Kusto. Imagine `:sort env_time desc` or
  `:where traceLevel = 'Error'` working locally.
- **Persistence across sessions.** Results survive process exit. A user
  could re-open a previous query result without re-running it. Enables a
  "query history" feature trivially.
- **Pagination is native.** `LIMIT/OFFSET` is exactly the access pattern
  the TUI needs. No custom page cache or LRU eviction logic.
- **Battle-tested.** SQLite handles terabyte databases. Its page cache,
  B-tree, and query planner are far more robust than anything we'd build.

#### Where SQLite Loses

- **Ingest latency.** Inserting 500K rows into SQLite takes 2–5 seconds
  even with `PRAGMA journal_mode=WAL` and batched transactions. That's
  2–5 seconds before the TUI can show the first row. The mmap approach
  (Strategy D) can show the first row as soon as the first page of the
  HTTP response arrives.
  - *Mitigation:* Insert in a background goroutine while the TUI shows a
    progress bar. Display rows as they become available (streaming ingest).
    Use `PRAGMA synchronous=OFF` and `PRAGMA journal_mode=MEMORY` for
    temp databases we don't need to survive crashes.
- **Dependency weight.** `mattn/go-sqlite3` requires CGo (complicates
  cross-compilation). `modernc.org/sqlite` is pure Go but adds ~30MB to
  the binary and is slower. Either way, it's a heavy dependency for a CLI
  tool.
- **Type mapping friction.** Kusto's `dynamic` type (arbitrary JSON) doesn't
  map cleanly to SQL columns. We'd store it as TEXT and lose structured
  query ability on nested fields, or use SQLite's JSON functions which
  are powerful but add complexity.
- **Overhead for small results.** For the common case of <1K rows, creating
  a SQLite database, inserting rows, and querying them back is strictly
  slower than just keeping them in a Go slice. The break-even point is
  probably around 50–100K rows.
- **Disk I/O.** On spinning disks (rare but possible), random-access reads
  during scroll are noticeably slower than in-memory. On SSDs this is
  negligible.

#### Verdict on SQLite

SQLite is the right answer **if jacques evolves toward being a local
analytics workbench** — where users want to filter, sort, search, and
cross-reference results locally. It's overkill if the tool stays a
read-only log viewer. The search and secondary-query capabilities are
compelling enough to keep it on the table.

If adopted, it should be behind the same `RowStore` interface as the
in-memory and mmap strategies, activated automatically above a row
threshold (e.g., >10K rows use SQLite, ≤10K stay in memory).

### Strategy F — Compressed In-Memory Column Store

Instead of row-oriented storage, store each column as a compressed array.
Log data is extremely compressible — timestamps are monotonic (delta +
varint), log levels have ~5 unique values (dictionary encoding), UUIDs
are 16 bytes (store as `[16]byte` not 36-char strings).

```go
type columnStore struct {
    columns []compressedColumn
    rowCount int
}

type compressedColumn struct {
    name     string
    colType  string
    // one of these is populated:
    dict     *dictColumn    // for low-cardinality strings
    raw      []byte         // for compressed arbitrary strings
    ints     []int64        // for numeric columns
    times    []int64        // for datetime as unix nanos
}
```

**Benefit:** 5–20× compression vs `[]interface{}` for typical log data.
A 500K row × 48 column result that takes ~800MB as `interface{}` values
might take 50–100MB in a column store. Still in memory, but now it fits.

**Drawback:** Significant implementation effort. Row access requires
assembling across columns. Not a standard pattern in Go — no off-the-shelf
library does this well.

**Verdict:** Interesting for a v2 rewrite but too much architecture
astronautics for the current codebase. The wins are real but the same
memory reduction is achieved more simply with Strategy D or E.

### Strategy G — Server-Side Pagination

Use Kusto's client-side cursor patterns to page through results:

```kql
OriginalQuery
| serialize _rownum = row_number()
| where _rownum between (pageStart .. pageEnd)
```

The client issues multiple queries, each returning a page of N rows.
The TUI fetches pages as the user scrolls, similar to how web UIs work.

**Benefit:** Memory is O(page_size) regardless of total result size.
Works even for results that exceed Kusto's 64MB response limit.

**Risk:** Multiple round-trips add latency. Results may be inconsistent
if underlying data changes between pages (mitigate with `query_consistency`).
The user experience during fast scrolling needs a loading indicator.

**Cost:** High. Requires rethinking the data model — `QueryResult` becomes
an interface with sync/async page fetching, the TUI needs a loading state,
and scrolling math gets more complex.

### Strategy H — Hybrid: Stream to Disk, Promote to SQLite

Combine C + D + E adaptively:

1. **<1K rows:** Keep everything in memory as today. No changes.
2. **1K–100K rows:** Stream JSON decode (C) into an append-only temp file
   (D). TUI reads via seek + decode. Search is a sequential scan of the
   file (fast enough at this scale — 100K rows scans in <100ms).
3. **>100K rows:** After the temp file is written, a background goroutine
   bulk-loads it into SQLite (E). Search and sort switch to SQL queries
   once ingest completes. The TUI shows a "indexing..." indicator and
   falls back to sequential scan until SQLite is ready.

```
                     ┌──────────────┐
   Kusto HTTP ──────►│ Stream JSON  │
                     │   Decoder    │
                     └──────┬───────┘
                            │
              ┌─────────────┼─────────────┐
              │ <1K rows    │ 1K–100K     │ >100K
              ▼             ▼             ▼
         ┌────────┐   ┌─────────┐   ┌─────────┐
         │ Go     │   │ Temp    │   │ Temp    │
         │ slice  │   │ file +  │   │ file    │──► SQLite
         │        │   │ seek    │   │ (stage) │    (async)
         └────────┘   └─────────┘   └─────────┘
              │             │             │
              └─────────────┼─────────────┘
                            ▼
                     ┌──────────────┐
                     │  RowStore    │
                     │  interface   │
                     └──────────────┘
```

This avoids paying the SQLite cost for small queries while getting its
benefits for large ones. The `RowStore` interface unifies access:

```go
type RowStore interface {
    RowCount() int
    Row(i int) []interface{}
    Columns() []Column
    Search(query string) []SearchMatch
    Close() error
}
```

**Verdict:** Most flexible but also most complex. Worth targeting as the
end state, but build toward it incrementally — start with the in-memory
implementation of `RowStore`, then add file-backed, then SQLite.

### Strategy I — Arrow/Parquet In-Memory via Apache Arrow Go

Use the Apache Arrow columnar format in memory. The Go implementation
(`apache/arrow-go`) is mature and provides:

- Zero-copy slicing (viewing a subset of rows costs nothing)
- Dictionary encoding for low-cardinality columns
- Efficient memory layout (no interface{} boxing)
- Parquet serialization for spilling to disk

```go
import "github.com/apache/arrow-go/v18/arrow"

// Build a record batch from Kusto results
schema := arrow.NewSchema(fields, nil)
builder := array.NewRecordBuilder(memory.DefaultAllocator, schema)
// ... populate from JSON stream ...
record := builder.NewRecord()
```

**Benefit:** Industry-standard columnar format. 3–10× more memory-efficient
than `[]interface{}`. Parquet spill-to-disk is built in. Ecosystem tooling
(DuckDB, Pandas) can read the same files.

**Drawback:** Arrow Go has a learning curve and the API is verbose.
Adds a significant dependency (~15MB). Converting Kusto's loosely-typed
JSON rows into Arrow's strongly-typed columns requires schema inference.

**Verdict:** Overkill for a log viewer, but if jacques ever wants to
support local analytical queries (joins, aggregates, pivots), Arrow is the
right foundation. It's the "if we were starting over" answer.

### Strategy J — Progressive Rendering (Virtual Scroll)

Don't buffer the full result at all. Show rows as they arrive from the
HTTP stream, and let the user scroll within what's been received so far.

The TUI shows a progress indicator at the bottom:

```
 ━━━━━━━━━━━━━━━━━━━━━━━━━━━━░░░░░░░░░░  247,831 rows loaded (streaming...)
```

Rows are appended to whatever backing store is active (memory, file,
SQLite). The user can navigate and search within the loaded portion
immediately. When they scroll past the loaded boundary, the TUI shows a
"loading..." row.

**Benefit:** Time-to-first-paint is near-zero. The user doesn't wait
for the full query to complete before browsing.

**Drawback:** UX complexity — search results are incomplete until loading
finishes, "row N/M" in the status bar is a moving target, and sort order
can't be guaranteed until all rows are in.

**Verdict:** Extremely valuable UX improvement regardless of which backing
store strategy is chosen. Orthogonal to the storage question — it's about
*when* we show data, not *where* we put it.

---

## Comparison Matrix

| Strategy | Peak Memory | Time to First Row | Search | Sort/Filter | Complexity | Dependencies |
|----------|------------|-------------------|--------|-------------|------------|-------------|
| **A** Soft limit | Capped by limit | Immediate | O(N) scan | No | Trivial | None |
| **B** Lazy formatting | -40% (cells only) | Immediate | O(N) scan | No | Low | None |
| **C** Streaming decode | -50% (no []byte) | Immediate* | O(N) scan | No | Medium | None |
| **D** Mmap row store | O(index) resident | Near-immediate | O(N) scan | No | Medium-High | None |
| **E** SQLite | O(page cache) | 2–5s ingest delay | FTS5 / LIKE | Yes, full SQL | High | sqlite driver |
| **F** Column store | -80% vs interface{} | After ingest | Custom index | Custom | Very High | None |
| **G** Server pagination | O(page_size) | 1 RTT | Server-side | Server-side | High | None |
| **H** Hybrid | Adaptive | Near-immediate | Adaptive | After SQLite ready | Very High | sqlite driver |
| **I** Arrow/Parquet | -70% vs interface{} | After ingest | Custom | Via DuckDB/etc | High | arrow-go |
| **J** Progressive render | Same as backing store | Near-zero | Partial until done | After load | Medium | None |

## Recommended Implementation Path

```
Phase 1 (now)
  A  --max-results soft cap
  B  Lazy cell formatting
  J  Progressive row count in status bar (cosmetic — just show total)

Phase 2 (next)
  C  Streaming JSON decode
  Define the RowStore interface (in-memory impl only)
  All renderers and TUI code against RowStore, not [][]interface{}

Phase 3 (when needed)
  D  File-backed RowStore implementation
  Search in background goroutine with cancellation

Phase 4 (if jacques becomes an analytics tool)
  E  SQLite-backed RowStore with FTS5 search
  H  Automatic tier promotion (memory → file → SQLite)
  Local :sort, :where, :count commands in TUI

Parking lot (revisit later)
  F  Column store — only if we hit memory walls that D/E don't solve
  G  Server-side pagination — only if results exceed Kusto's 64MB limit
  I  Arrow — only if we add cross-query joins or export features
```

## The RowStore Interface

Every strategy converges on the same abstraction. Defining this interface
early decouples the TUI from the storage backend:

```go
type RowStore interface {
    // Schema
    Columns() []kusto.Column
    RowCount() int

    // Random access
    Row(index int) ([]interface{}, error)
    FormattedRow(index int) ([]string, error)

    // Bulk operations
    Search(query string, cancel <-chan struct{}) ([]SearchMatch, error)
    NaturalWidths(sampleSize int) []int

    // Lifecycle
    Close() error
}
```

The in-memory implementation wraps `QueryResult` directly. The file-backed
implementation uses seek + decode. The SQLite implementation uses SQL
queries. The TUI doesn't know or care which one it's talking to.

---

## Search at Scale

Search (`/`) currently scans every cell. At scale:

- **In-memory / file-backed:** Search runs in a goroutine with a progress
  indicator. Searches the raw `QueryResult.Rows` or file contents (avoids
  formatting cost). Cancel with Esc.
- **SQLite:** `SELECT _rowid, col FROM results WHERE col LIKE '%term%'`
  across relevant columns. With FTS5: `SELECT rowid FROM results_fts
  WHERE results_fts MATCH 'term'`. Near-instant for any result size.
- **Server-side:** Push the search to Kusto (`| where * contains "term"`)
  and re-query. Simplest, most correct, but adds latency.

## Column Width Computation at Scale

`computeNaturalWidths` currently scans all rows. At scale:

- Sample the first 1,000 rows + 100 random rows for width estimation.
  The visual difference is negligible — worst case a column is slightly
  too narrow, but the expand (Space) and detail (Enter) views handle
  overflow already.

## Impact on Non-TUI Renderers

The `table`, `log`, and `json` output formats stream to stdout and don't
buffer the full result in memory (except `json` which uses `json.Encoder`).
These are naturally streaming-friendly. The main concern is the
`QueryResult` itself — Strategies C–E benefit all renderers by reducing
the in-memory footprint of the raw data.

For `json` format with huge results, switch from `enc.Encode(records)` to
streaming each record individually so we never build the full
`[]map[string]interface{}` array.
