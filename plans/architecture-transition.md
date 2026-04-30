# Architecture Transition Plan

## Target Architecture

```
~/.jacques/
├── config.hcl              # all provider config, display prefs
└── scripts/
    └── refresh-kusto.ps1   # token refresh hack (until proper token server)

jacques (binary)
├── data/                   # shared contracts — zero dependencies
│   ├── rowstore.go         # RowStore interface
│   ├── column.go           # Column type
│   └── memstore.go         # In-memory RowStore
├── backend/                # query backends — each implements Backend
│   ├── backend.go          # Backend interface + registry
│   ├── kusto/              # Kusto provider
│   │   └── kusto.go
│   ├── csv/                # CSV provider (for testing / local files)
│   │   └── csv.go
│   └── sqlite/             # SQLite provider (for local analytics)
│       └── sqlite.go
├── cache/                  # cache middleware between backend and renderer
│   └── cache.go            # SQLite-backed result cache with FTS5
├── config/                 # HCL config loading from ~/.jacques/
│   └── config.go
├── render/                 # renderers — depend only on data.RowStore
│   ├── table.go
│   ├── log.go
│   ├── json.go
│   └── tui.go
├── logging/                # OTel logging to %APPDATA%/jacques/
│   ├── logging.go
│   └── logger.go
└── main.go                 # CLI: load config, pick backend, run query, render
```

### Dependency Rules

```
data/       ← depends on nothing (stdlib only)
backend/*   ← depends on data/ and logging/
cache/      ← depends on data/ and logging/ (sits between backend and render)
config/     ← depends on data/ (for type definitions)
render/     ← depends on data/ and logging/
main.go     ← depends on everything, wires it all together
```

**render/ never imports backend/. backend/ never imports render/.**
The only shared contract is `data.RowStore`.

---

## Key Interfaces

### data.RowStore

The central abstraction. Every backend produces one. Every renderer
consumes one.

```go
type RowStore interface {
    Columns() []Column
    RowCount() int
    Row(index int) ([]interface{}, error)
    Close() error
}
```

### data.PagedStore

For backends that support pagination natively (Kusto, SQLite). The
renderer requests pages instead of random rows. The store manages
which pages are resident.

```go
type PagedStore interface {
    RowStore
    PageSize() int
    LoadPage(pageIndex int) error   // fetch page from backend
    EvictPage(pageIndex int) error  // release memory
    ResidentPages() []int           // which pages are in memory
}
```

### data.Searchable

Backend-provided search. The renderer type-asserts for this; if absent,
it falls back to scanning the current page only.

```go
type Searchable interface {
    RowStore
    Search(query string, cancel <-chan struct{}) ([]SearchMatch, error)
}
```

Search semantics:
- **smartcase**: all-lowercase query → case-insensitive; any uppercase →
  case-sensitive. Suffix `\c` forces case-sensitive, `\C` forces
  case-insensitive.
- **`/\hPREFIX`**: searches column headers instead of cell values.
  Returns matches as `SearchMatch{Row: -1, Col: colIndex}`.
- Search beyond the current page is the **backend's** responsibility.
  The renderer searches only its visible page. If the backend implements
  `Searchable`, the TUI delegates to it for cross-page search and shows
  a progress indicator.

### data.Sortable / data.Filterable

Optional backend capabilities for local analytics:

```go
type Sortable interface {
    RowStore
    Sort(column string, ascending bool) error
}

type Filterable interface {
    RowStore
    Filter(column string, op string, value string) (RowStore, error)
}
```

### backend.Backend

Each provider implements this to produce a RowStore from a query:

```go
type Backend interface {
    Name() string
    Query(ctx context.Context, query string) (data.RowStore, error)
    Close() error
}
```

A registry maps provider names to constructors:

```go
var registry = map[string]func(cfg config.Provider) (Backend, error){
    "kusto":  kusto.New,
    "csv":    csv.New,
    "sqlite": sqlite.New,
}
```

---

## Pagination

### The Problem

Kusto can return millions of rows. Holding them all in memory is
impossible. The renderer only needs one page (~50 rows) at a time.
We need the backend to manage pagination.

### Kusto Pagination

Kusto doesn't have native cursors, but we can synthesize pagination
with `row_number()`:

```kql
let _page_size = 50;
let _page = 3;
OriginalQuery
| serialize _rownum = row_number()
| where _rownum between (_page * _page_size + 1 .. (_page + 1) * _page_size)
```

**Caveats:**
- Requires deterministic ordering. If the source query doesn't have an
  `| order by`, results may shift between pages. The backend should
  detect this and warn, or auto-append `| order by env_time asc`.
- Each page is a separate HTTP request with full query re-execution.
  Kusto may optimize via query caching, but no guarantee.
- This is a **configurable feature** of the Kusto provider. Users can
  disable it if their query doesn't produce stable ordering:

  ```hcl
  provider "kusto" "work-kusto" {
    cluster    = "https://..."
    database   = "CAPAnalytics"
    pagination = true         # default true; set false to disable
    page_size  = 100          # default 100
  }
  ```

### SQLite Pagination

Native — `SELECT * FROM results LIMIT ? OFFSET ?`. The SQLite backend
implements `PagedStore` directly. FTS5 handles cross-page search.

### CSV Pagination

CSV files are small enough to fit in memory. No pagination needed.
The CSV backend returns a plain `MemoryStore`.

---

## Cache Middleware

The cache sits between the backend and the renderer. It intercepts
`RowStore` access and provides:

1. **Result persistence**: Query results are stored in a local SQLite
   database keyed by `(provider, query_hash)`. Re-running the same
   query can hit cache instead of the backend.

2. **Local re-query**: Given a cached result, the user can run secondary
   queries in SQL against the cache without re-querying the backend:
   ```
   :cache SELECT * FROM results WHERE message LIKE '%error%' ORDER BY env_time
   ```
   These cache queries only work against a constant source query — we
   don't translate between query languages.

3. **FTS5 search**: The cache builds an FTS5 index on ingest. When the
   renderer calls `Search()`, the cache uses FTS5 instead of scanning.
   This is the primary search acceleration path.

4. **Page management**: The cache implements `PagedStore`. It loads pages
   from the backend on demand and evicts cold pages when memory gets hot.

```
Backend (Kusto/CSV/SQLite)
    │
    ▼
Cache (SQLite + FTS5)     ← implements RowStore, PagedStore, Searchable
    │
    ▼
Renderer (TUI/table/log/json)
```

The cache is optional. Without it, the renderer talks directly to the
backend's `RowStore`. With it, the renderer gets pagination, search
acceleration, and local re-query for free.

---

## Search Architecture

### Renderer Responsibility

The renderer (TUI) is responsible for:
- Visual highlighting of matches
- Navigating between matches (n/N)
- Searching column headers (`/\h`)
- Searching the **current visible page** as a fast path

The renderer does **not** scan all rows. If the backend/cache implements
`Searchable`, the renderer delegates cross-page search to it.

### Backend Responsibility

The backend (or cache) is responsible for:
- Full-result search across all pages
- Leveraging indexes (FTS5, Kusto `contains` operator)
- Returning `[]SearchMatch` with row/col positions
- Supporting cancellation via `<-chan struct{}`

### Smartcase

Applied at the point of search (renderer for page-local, backend for
cross-page):

```go
func isSmartCaseSensitive(query string) bool {
    if strings.HasSuffix(query, `\c`) {
        return true
    }
    if strings.HasSuffix(query, `\C`) {
        return false
    }
    return query != strings.ToLower(query)
}
```

### Column Header Search

`/\hprefix` searches column names, not cell values. This is a
renderer-local operation (columns are always in memory). Returns
matches with `Row: -1` to distinguish from cell matches. The TUI
uses these to jump the cursor to matching columns.

### Kusto Server-Side Search

When the Kusto backend has pagination enabled, a search like `/error`
can be pushed to the server:

```kql
OriginalQuery
| where * contains "error"
| serialize _rownum = row_number()
| take 100
```

This returns only matching rows, avoiding the need to page through
the entire result. The backend advertises this capability; the renderer
uses it when available.

---

## Config: ~/.jacques/config.hcl

```hcl
default_provider = "work-kusto"

provider "kusto" "work-kusto" {
  cluster    = "https://fdislandsus.centralus.kusto.windows.net"
  database   = "CAPAnalytics"
  token      = "eyJ0eX..."   // plaintext for now, token server later
  pagination = true
  page_size  = 100
}

provider "csv" "local-logs" {
  path = "C:/logs/export.csv"
}

provider "sqlite" "analytics-db" {
  path = "C:/data/results.db"
  table = "logs"
}

display {
  format     = "tui"
  time_col   = "env_time"
  msg_col    = "message"
  level_col  = "level"
  max_rows   = 10000
}

cache {
  enabled  = true
  path     = "~/.jacques/cache.db"
  max_size = "500MB"
}
```

The token lives in the config file in plaintext. The refresh script
lives at `~/.jacques/scripts/refresh-kusto.ps1` and rewrites the token
field. This is an acknowledged hack — a proper token server (or hush
integration) replaces it later.

---

## Implementation Steps

### Step 1 — RowStore interface + in-memory implementation ✅

- [x] Define `data.RowStore` interface
- [x] Create `data.MemoryStore` with `Searchable` implementation
- [x] Update kusto client to return `data.RowStore`
- [x] Update all renderers to accept `data.RowStore`
- [x] Delete `data.Result`

### Step 1.5 — Quick UX wins ✅

- [x] `-f` flag for query from file
- [x] `y` key to yank cell to clipboard, `Y` for whole row
- [x] Yank confirmation in status bar

### Step 2 — Backend interface + HCL config

- Create `backend/backend.go` with `Backend` interface
- Create `config/config.go` with HCL parsing from `~/.jacques/config.hcl`
- Wrap existing kusto client as `backend/kusto/kusto.go`
- Move token + cluster + database config from .env / flags into HCL
- Move `refresh-token.ps1` to `~/.jacques/scripts/`
- main.go reads config, picks backend by name, runs query
- Flags continue to work as overrides

### Step 3 — CSV backend

- Create `backend/csv/csv.go` — reads a CSV file into a `MemoryStore`
- Good for testing renderers without a Kusto cluster
- Validates the backend abstraction works
- Can also serve as an export target (query kusto → save as CSV →
  re-open later with CSV backend)

### Step 4 — SQLite cache + backend

- Create `cache/cache.go` — SQLite-backed cache with FTS5
- Create `backend/sqlite/sqlite.go` — direct SQLite backend
- Cache wraps any backend's `RowStore` and provides:
  - FTS5 search (implements `Searchable`)
  - Page management (implements `PagedStore`)
  - Result persistence
- Use `modernc.org/sqlite` (pure Go, no cgo)

### Step 5 — Kusto pagination

- Add `row_number()` pagination to Kusto backend
- Implement `PagedStore` interface
- Configurable via HCL (`pagination = true/false`, `page_size`)
- Auto-detect non-deterministic queries and warn
- Server-side search push (`| where * contains "term"`)

### Step 6 — Page-constrained renderer

- TUI holds only one page of formatted cells at a time
- Scrolling past page boundary triggers page load from backend/cache
- Loading indicator while pages are fetched
- Memory ceiling: renderer + 1 page of formatted cells

### Step 7 — Search overhaul

- Smartcase search (case-sensitive if query has uppercase)
- `\c` / `\C` suffixes for explicit case control
- `/\h` prefix for column header search (renderer-local)
- Backend-delegated cross-page search with progress indicator
- Search cancellation with Esc

### Future

- Cache re-query (`:cache SELECT ...` in TUI command mode)
- Token server integration
- Progressive rendering (show rows as they stream in)
- Hybrid store (auto-promote memory → file → SQLite by size)
- Export commands (`:export csv`, `:export json`)

---

## Migration Path

- Step 1–1.5: Invisible. Same CLI, same flags, same behavior.
- Step 2: Introduces `~/.jacques/config.hcl`. Falls back to flags / env
  vars if no config exists. `.env` continues to work.
- Step 3+: Additive capabilities. Nothing breaks.

*Each step ships independently. No big-bang rewrites.*
