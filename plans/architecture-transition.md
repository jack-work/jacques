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
│   └── column.go           # Column type
├── backend/                # query backends — each implements RowStore
│   ├── backend.go          # Backend interface + registry
│   ├── kusto/              # Kusto provider
│   │   └── kusto.go
│   ├── csv/                # CSV provider (for testing / local files)
│   │   └── csv.go
│   └── sqlite/             # SQLite provider (for local analytics)
│       └── sqlite.go
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
config/     ← depends on data/ (for type definitions)
render/     ← depends on data/ and logging/
main.go     ← depends on everything, wires it all together
```

**render/ never imports backend/. backend/ never imports render/.**
The only shared contract is `data.RowStore`.

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

This is the minimal interface. Backends can optionally implement richer
interfaces that renderers can type-assert for optimization:

```go
type Searchable interface {
    RowStore
    Search(query string, cancel <-chan struct{}) ([]SearchMatch, error)
}

type Sortable interface {
    RowStore
    Sort(column string, ascending bool) error
}

type Filterable interface {
    RowStore
    Filter(column string, op string, value string) (RowStore, error)
}
```

If a backend doesn't implement Searchable, the renderer falls back to
a brute-force scan. This lets SQLite use FTS5 while CSV just scans.

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

## Config: ~/.jacques/config.hcl

```hcl
default_provider = "work-kusto"

provider "kusto" "work-kusto" {
  cluster  = "https://fdislandsus.centralus.kusto.windows.net"
  database = "CAPAnalytics"
  token    = "eyJ0eX..."   // plaintext for now, token server later
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
```

The token lives in the config file in plaintext. The refresh script
rewrites it:

```
~/.jacques/scripts/refresh-kusto.ps1
```

This is an acknowledged hack. A proper token server (or hush integration)
replaces it later.

## Implementation Steps

### Step 1 — RowStore interface + in-memory implementation

- Define `data.RowStore` interface in `data/rowstore.go`
- Create `data.MemoryStore` that wraps `[]Column` + `[][]interface{}`
  and implements `RowStore`
- Update `kusto/client.go` to return `data.RowStore` (a `*MemoryStore`)
- Update all renderers to accept `data.RowStore` instead of `*data.Result`
- Delete `data.Result` — replaced by the interface
- Everything still works, no behavior change

### Step 2 — Backend interface + config

- Create `backend/backend.go` with `Backend` interface
- Create `config/config.go` with HCL parsing from `~/.jacques/config.hcl`
- Wrap existing kusto client as `backend/kusto/kusto.go`
- Move token + cluster + database config from .env / flags into HCL
- Move `refresh-token.ps1` to `~/.jacques/scripts/`
- main.go reads config, picks backend by name, runs query

### Step 3 — CSV backend

- Create `backend/csv/csv.go` — reads a CSV file into a `MemoryStore`
- Good for testing renderers without a Kusto cluster
- Validates the backend abstraction works

### Step 4 — SQLite backend

- Create `backend/sqlite/sqlite.go` — queries a local SQLite database
- Returns a `RowStore` that reads rows via `SELECT ... LIMIT/OFFSET`
- Implements `Searchable` using `LIKE` / FTS5
- Create a small test database in `~/.jacques/` for validation

### Step 5 — Lazy RowStore for TUI

- Create `data.LazyStore` that wraps a `RowStore` with a formatting
  cache (LRU, ~500 rows)
- TUI uses `LazyStore` — only formats visible + nearby rows
- Search becomes a method on RowStore, not a cell scan

### Future

- Hybrid store (Strategy H from large-result-strategy.md)
- Token server integration replacing plaintext config
- Progressive rendering (Strategy J)

## Migration Path for Existing Users

Step 1 is invisible — same CLI, same flags, same behavior.

Step 2 introduces `~/.jacques/config.hcl` but falls back to flags / env
vars if no config file exists. The `.env` file continues to work during
the transition.

Step 3–4 add new capabilities without changing existing behavior.
```

---

*This plan is incremental. Each step ships independently and leaves the
tool fully functional. No big-bang rewrites.*
