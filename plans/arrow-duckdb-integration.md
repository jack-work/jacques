# Arrow IR + DuckDB Integration

## How This Fits

This plan replaces the row-oriented `[][]interface{}` IR with Apache Arrow
RecordBatches and replaces the planned SQLite cache tier with DuckDB. It
slots into the existing architecture transition at Step 4 and reshapes
everything after it.

### Mapping to existing concepts

| Our current concept | What it becomes |
|---|---|
| `data.RowStore` | `data.PageStream` (Arrow RecordBatch iterator) |
| `data.MemoryStore` | Arrow RecordBatch(es) in memory |
| `data.PagedStore` | Native — Arrow IPC is already paged by RecordBatch |
| `data.Searchable` | DuckDB FTS extension or `LIKE`/`contains` pushdown |
| `[][]interface{}` | `arrow.Record` (columnar, zero-copy sliceable) |
| SQLite cache (Step 4) | DuckDB cache (columnar, Arrow-native, vectorized) |
| SQLite backend | DuckDB backend (strictly better for analytics workloads) |
| Kusto backend | Unchanged — still HTTP, but now returns Arrow RecordBatches |
| CSV backend | Unchanged — reads into Arrow via DuckDB `read_csv_auto()` |
| Renderer | Consumes `PageStream`, formats one RecordBatch at a time |

### What doesn't change

- `config/` — HCL config, connection management, `jacques config` commands
- `logging/` — OTel file logging
- `render/` — still depends only on `data/`, never imports backends
- Dependency rule: render/ → data/ ← backend/

### What changes

- `data/` gains Arrow types: `PageStream`, schema helpers
- `data/` depends on `github.com/apache/arrow/go/v17` (this is the one
  package that was previously stdlib-only — acceptable because Arrow IS
  the IR)
- Cache tier switches from `modernc.org/sqlite` to `github.com/marcboeker/go-duckdb`
  (cgo required — acceptable per the proposal; DuckDB's Arrow integration
  is native and avoids ser/de overhead that SQLite would incur)
- `backend.Backend.Execute` returns `PageStream` instead of `RowStore`

---

## Target Architecture

```
    ┌──────────────────────────────────────────────┐
    │                  TUI / Renderer               │
    │  Consumes one RecordBatch (page) at a time.   │
    │  Formats cells on demand from Arrow columns.  │
    │  Page-local search only.                      │
    └──────────────┬───────────────────────────────┘
                   │ data.PageStream (Arrow RecordBatch iterator)
                   │
    ┌──────────────▼───────────────────────────────┐
    │              Cache (DuckDB)                   │
    │  - Ingests Arrow batches from upstream        │
    │  - Serves pages via SELECT ... LIMIT/OFFSET   │
    │  - FTS index for cross-page string search     │
    │  - Local re-query in SQL                      │
    │  - LRU eviction by table size                 │
    │  Optional — bypassed if cache.enabled=false   │
    └──────────────┬───────────────────────────────┘
                   │ data.PageStream
                   │
         ┌─────────┴──────────┐
         │                    │
    ┌────▼────┐         ┌────▼─────────┐
    │  Kusto  │         │  DuckDB      │
    │ Backend │         │  Backend     │
    │ (HTTP)  │         │  (in-memory) │
    └─────────┘         └──────────────┘
```

Two backends, one cache, one renderer contract. The cache sits in the
middle and speaks Arrow in both directions.

---

## Key Interfaces

### data.PageStream

Replaces `RowStore`. The fundamental unit of data flow is a stream of
Arrow RecordBatches (pages).

```go
type PageStream interface {
    Schema() *arrow.Schema
    Next(ctx context.Context) (arrow.Record, error) // io.EOF when done
    Close() error
}
```

Each `arrow.Record` is one page (~10k rows by default). The consumer
must call `Record.Release()` when done — this is how memory pressure is
managed. The renderer holds at most one Record at a time.

### data.PagedStore

For random-access (TUI scrolling). Wraps a `PageStream` and caches
pages, evicting under memory pressure.

```go
type PagedStore interface {
    Schema() *arrow.Schema
    TotalRows() int                    // -1 if unknown (streaming)
    Page(index int) (arrow.Record, error)
    PageCount() int                    // -1 if unknown
    PageSize() int
    EvictPage(index int) error
    ResidentPages() []int
    Close() error
}
```

The DuckDB cache implements this natively. For non-cached backends,
a `MemoryPagedStore` buffers pages in memory with an LRU cap.

### data.Searchable

Unchanged interface, but now operates on Arrow data:

```go
type Searchable interface {
    Search(query string, opts SearchOpts, cancel <-chan struct{}) ([]SearchMatch, error)
}

type SearchOpts struct {
    Smartcase bool   // auto-detect from query if not explicitly set
    CaseSensitive *bool // override smartcase; nil = use smartcase
    HeadersOnly bool    // /\h mode — search column names only
    Columns []string    // restrict search to these columns; nil = all
}
```

### backend.Backend

```go
type Query struct {
    Text       string            // KQL, SQL, or file path depending on backend
    Language   string            // "kql", "sql", "csv"
    Params     map[string]any
    CachePolicy string           // "bypass", "read_through", "refresh"
}

type ExecOpts struct {
    PageRows         int         // page size hint (default 10000)
    MaxPagesInFlight int         // client memory constraint
    PrefetchAhead    int         // how many pages ahead to pre-fetch
    EvictionHint     string      // "lru" | "fifo" | "none"
}

type Backend interface {
    Name() string
    Execute(ctx context.Context, q Query, opts ExecOpts) (data.PageStream, error)
    Close() error
}
```

### Renderer contract

The renderer never sees `Backend`. It receives a `PagedStore` (for TUI)
or a `PageStream` (for streaming outputs like log/json/table):

```go
// TUI
func TUI(store data.PagedStore, opts TUIOptions)

// Streaming renderers
func Table(w io.Writer, stream data.PageStream, opts TableOptions)
func Log(w io.Writer, stream data.PageStream, opts LogOptions)
func JSON(w io.Writer, stream data.PageStream)
```

---

## Memory Model

### Current (broken at scale)

```
Kusto JSON → [][]interface{} (entire result) → [][]string (entire result)
Peak: ~3× wire size
```

### Target

```
Kusto JSON → Arrow RecordBatch (one page) → DuckDB table (on disk)
                                           → Renderer formats one page
Peak: ~2 pages × page_size × row_width
     = 2 × 10,000 × ~1KB = ~20MB regardless of total result size
```

The key insight: Arrow RecordBatches are the **unit of memory management**.
When the renderer is done with a page, it calls `Record.Release()` and
the memory is freed. DuckDB holds the full result on disk (or in its own
managed memory with its own eviction). The Go heap never holds more than
a couple of pages.

### Allocator discipline

- Production: `memory.GoAllocator` (delegates to Go GC)
- Tests: `memory.CheckedAllocator` (panics on leak)
- One allocator created at startup, threaded through all Arrow operations
- Every `Record` returned by `PageStream.Next()` or `PagedStore.Page()`
  must be `Release()`d by the consumer

---

## DuckDB Specifics

### As cache (`cache/duckdb.go`)

- Opens a persistent DuckDB database at `~/.jacques/cache.ddb`
- Cache key: `sha256(provider + normalized_query + schema_version)`
- Table name: `cache_<key_prefix>`
- On miss: stream Arrow batches from upstream, tee into DuckDB via
  Arrow appender API, serve pages to renderer simultaneously
- On hit: `SELECT * FROM cache_<key> LIMIT ? OFFSET ?`
- Metadata table tracks `(key, created_at, last_accessed, row_count,
  byte_estimate)` for eviction
- Eviction: LRU by `last_accessed`, triggered when total exceeds
  `cache.max_size` from config
- FTS: `PRAGMA create_fts_index('cache_<key>', '*')` on ingest
  (DuckDB's FTS extension)

### As direct backend (`backend/duckdb/duckdb.go`)

- Opens `:memory:` DuckDB
- Accepts SQL queries directly
- Returns Arrow RecordBatches via DuckDB's native Arrow result API
  (zero-copy when possible)
- Useful for: local analytics on imported data, joining across cached
  results, ad-hoc SQL against CSV/Parquet files

### Connection config

```hcl
connection "duckdb" "local-analytics" {
  path = ":memory:"   # or a file path for persistent
}

connection "duckdb" "parquet-logs" {
  path = ":memory:"
  init = "CREATE VIEW logs AS SELECT * FROM read_parquet('C:/logs/*.parquet')"
}

cache {
  enabled   = true
  path      = "~/.jacques/cache.ddb"
  max_size  = "500MB"
  fts       = true
}
```

---

## Kusto Backend Changes

The Kusto backend currently:
1. POSTs JSON to `/v2/rest/query`
2. Reads entire response into `[]byte`
3. Unmarshals into `[]v2Frame`
4. Returns `*MemoryStore`

It will change to:
1. POSTs JSON to `/v2/rest/query` (unchanged)
2. Streaming JSON decode of the response
3. Converts each batch of rows into an Arrow RecordBatch
4. Yields RecordBatches via `PageStream`

The Kusto v2 response is already framed (DataSetHeader → DataTable →
DataSetCompletion). We decode the DataTable rows in chunks of
`page_size` and emit each chunk as a RecordBatch. This means the first
page is available to the renderer before the full response is read.

### Kusto pagination via row_number()

For interactive browsing of very large results:

```kql
OriginalQuery
| serialize _rownum = row_number()
| where _rownum between (_page * _page_size + 1 .. (_page + 1) * _page_size)
```

This is a **configurable feature** per connection:

```hcl
connection "kusto" "cap-analytics" {
  cluster    = "https://fdislandsus.centralus.kusto.windows.net"
  database   = "CAPAnalytics"
  token      = "..."
  pagination = true    # default true; false disables row_number() wrapping
  page_size  = 10000
}
```

**Caveat:** Requires deterministic ordering. The backend should detect
missing `| order by` and either warn or auto-append
`| order by env_time asc`. This is configurable:

```hcl
  auto_order_by = "env_time asc"  # appended if no order by detected
```

### Kusto server-side search

When the user searches (`/error`), the Kusto backend can push the filter
to the server instead of scanning locally:

```kql
OriginalQuery
| where * contains "error"
| serialize _rownum = row_number()
| take 100
```

This returns only matching rows. The backend advertises this capability
via the `Searchable` interface. The renderer uses it for cross-page
search when available.

---

## Revised Implementation Steps

### Steps 1–1.6: ✅ Done

Current state: `RowStore` interface, `MemoryStore`, HCL config,
connection management, TUI with cell navigation/search/yank.

### Step 2 — Backend interface + provider abstraction

- Create `backend/backend.go` with `Backend` interface + registry
- Wrap existing kusto client as `backend/kusto/kusto.go`
- main.go resolves connection type → backend constructor

### Step 3 — CSV backend

- Create `backend/csv/csv.go`
- Reads CSV into `MemoryStore` (for now — will switch to Arrow later)
- Validates the backend abstraction

### Step 4 — Arrow IR migration

- Pin `github.com/apache/arrow/go/v17`
- Create `data/arrow.go`: schema derivation from `[]Column`, helpers to
  build `arrow.Record` from `[][]interface{}`
- Define `PageStream` interface
- Create `ArrowMemoryStore` — wraps `[]arrow.Record` and implements
  `PagedStore`
- Create an adapter: `PagedStoreToRowStore` — so existing renderers
  continue to work during the transition (reads one page, serves rows
  from it, loads next page on boundary crossing)
- Update Kusto backend to return `PageStream` (streaming JSON decode →
  Arrow RecordBatch emission)
- **Renderers unchanged at this step** — adapter layer bridges the gap

### Step 5 — DuckDB cache

- Pin `github.com/marcboeker/go-duckdb`
- Create `cache/duckdb.go`
- Implements read-through caching: miss → stream from backend → tee
  into DuckDB table → serve to renderer
- Implements `PagedStore` and `Searchable` (via FTS)
- LRU eviction by table size
- Config: `cache { enabled, path, max_size, fts }`

### Step 6 — DuckDB as direct backend

- Create `backend/duckdb/duckdb.go`
- `:memory:` or file-backed
- SQL queries, Arrow-native result streaming
- Connection config in HCL
- Can load CSV/Parquet via DuckDB's `read_csv_auto()` / `read_parquet()`

### Step 7 — Arrow-native renderer

- Update TUI to consume `PagedStore` directly
- Format cells from Arrow columns (type-switch on `arrow.DataType`)
  instead of `interface{}`
- One `arrow.Record` in memory at a time + look-ahead buffer
- `Record.Release()` on page eviction
- Remove the `RowStore` → `[][]string` cell matrix entirely

### Step 8 — Page-constrained TUI

- TUI requests pages via `PagedStore.Page(n)`
- Scrolling past page boundary → load next page, evict oldest
- Status bar shows page info: `page 3/47 (rows 201-300)`
- Loading indicator while page is fetched
- Memory ceiling: renderer holds ≤ `MaxPagesInFlight` Records

### Step 9 — Search overhaul

- Smartcase, `\c`/`\C`, `/\h` header search
- Page-local search: renderer scans current Arrow Record directly
- Cross-page search: delegate to `Searchable` (DuckDB FTS or Kusto
  server-side push)
- Progress indicator for cross-page search
- Cancellation with Esc

### Step 10 — Control messages + cache policy

- `ExecOpts` sent with every query: page_rows, max_pages_in_flight,
  prefetch_ahead, eviction_hint
- `CachePolicy` per query: bypass, read_through, refresh
- TUI can switch cache policy at runtime (`:cache refresh`)

### Cleanup

- Delete `data.MemoryStore` (replaced by Arrow stores)
- Delete `RowStore` interface (replaced by `PageStream` + `PagedStore`)
- Delete `[][]interface{}` row representation
- All tests use `memory.CheckedAllocator` with zero-leak assertion

---

## Dependencies

```
github.com/apache/arrow/go/v17       # IR + IPC streaming
github.com/marcboeker/go-duckdb      # Cache + direct backend
github.com/hashicorp/hcl/v2          # Config (already present)
charm.land/bubbletea/v2              # TUI (already present)
go.opentelemetry.io/otel             # Logging (already present)
```

cgo is required for DuckDB. This is acceptable — DuckDB's Arrow
integration is native C++ and avoids the serialization overhead that
a pure-Go database (modernc.org/sqlite) would incur when moving
columnar data across the boundary.

---

## What We're NOT Doing

- **Translating between query languages.** KQL stays KQL, SQL stays SQL.
  The cache speaks SQL; the Kusto backend speaks KQL. No transpilation.
- **Network transport.** Jacques is a single binary. If we ever split into
  client/server, Arrow IPC streaming is the wire format — but we're not
  building that transport now.
- **Constraining query surface languages.** KQL, SQL, CSV paths, Parquet
  paths, and potentially others are all valid. The `Query.Language` field
  routes to the right backend. No language is out of scope.
