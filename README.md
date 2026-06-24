# jacques

A local Kusto TUI client and query tool. Queries Azure Data Explorer (Kusto)
clusters, caches results in DuckDB, and renders them as interactive terminal
tables, log streams, JSON, or a web UI. Also works as a standalone DuckDB and
CSV query tool.

## Quickstart

### 1. Install

**One-liner** (PowerShell -- works on Windows, macOS, Linux):

```powershell
irm https://raw.githubusercontent.com/jack-work/jacques/master/install.ps1 | iex
```

This downloads the latest release binary to `~/.jacques/bin` and adds it to
your PATH.

To install a specific version:

```powershell
& ([scriptblock]::Create((irm https://raw.githubusercontent.com/jack-work/jacques/master/install.ps1))) -Version v0.1.0
```

**From source** (requires Go 1.25+):

```powershell
go install github.com/jack-work/jacques@latest
```

### 2. Prerequisites

**Azure CLI** -- jacques acquires Kusto tokens via `az account get-access-token`.
Install it and log in before first use:

```powershell
az login
```

### 3. Create a config

```powershell
jacques config init
```

This writes `~/.jacques/config.hcl` with a default configuration. Edit it to
point at your cluster:

```hcl
current_connection = "my-cluster"

connection "kusto" "my-cluster" {
  cluster        = "https://mycluster.region.kusto.windows.net"
  database       = "MyDatabase"
  token_provider = "az"
  scopes         = "https://help.kusto.windows.net/.default"
}

display {
  format    = "tui"
  time_col  = "env_time"
  msg_col   = "message"
  level_col = "level"
  max_rows  = 10000
}
```

### 4. Run a query

```powershell
# From a .kql file (recommended, especially from agents)
jacques -c my-cluster -f query.kql

# Inline
jacques -c my-cluster "StormEvents | take 10"
```

## Configuration

Config lives at `~/.jacques/config.hcl` (HCL format). Manage it with the
`config` subcommand or edit the file directly.

### Connection types

**Kusto** -- query Azure Data Explorer clusters:

```hcl
connection "kusto" "my-kusto" {
  cluster        = "https://mycluster.region.kusto.windows.net"
  database       = "MyDatabase"
  token_provider = "az"
  scopes         = "https://help.kusto.windows.net/.default"
}
```

**DuckDB** -- run SQL against local files or an in-memory database:

```hcl
connection "duckdb" "duck" {
  path = ""  # empty = in-memory, or path to a .duckdb file
}
```

```powershell
jacques -c duck "SELECT * FROM read_csv_auto('data.csv') WHERE level = 'Error'"
jacques -c duck "SELECT * FROM read_parquet('logs/*.parquet') LIMIT 100"
```

**CSV** -- read a CSV file directly (no SQL):

```hcl
connection "csv" "sample" {
  path = "testdata/sample.csv"
}
```

### Display settings

```hcl
display {
  format    = "tui"       # default output format: tui, table, log, json, raw
  time_col  = "env_time"  # column name for timestamps (log mode)
  msg_col   = "message"   # column name for log message (log mode)
  level_col = "level"     # column name for log level (log mode)
  max_rows  = 10000       # max rows to display (0 = unlimited)
  harness   = "nvim"      # optional: "nvim" for neovim preview on Enter in TUI
}
```

### Config subcommands

```
jacques config init                              # create default config
jacques config list                              # show all connections
jacques config list --json                       # show as JSON
jacques config use <name>                        # set default connection
jacques config set <name> <field> <value>        # update a connection field
jacques config path                              # print config file path
```

## Usage

```
jacques [flags] <KQL or SQL query>
jacques -f <file.kql> [flags]
jacques -c <connection> [flags] <query>
```

### Query flags

| Flag | Default | Description |
|---|---|---|
| `-c <name>` | `current_connection` | Connection name from config |
| `-f <path>` | | Read query from a `.kql` file |
| `-format <fmt>` | `log` | Output format: `tui`, `table`, `log`, `json`, `raw` |
| `-cluster <url>` | | Override cluster URL |
| `-db <name>` | | Override database name |
| `-max-rows <n>` | `0` | Max rows to display (0 = unlimited) |
| `-no-cache` | `false` | Skip DuckDB cache, always hit the cluster |
| `-refresh` | `false` | Re-query and overwrite cached result |
| `-cols <list>` | | Comma-separated columns to show (TUI mode) |
| `-all-cols` | `false` | Show all columns per entry (log mode) |
| `-time-col <name>` | `env_time` | Timestamp column (log mode) |
| `-msg-col <name>` | `message` | Message column (log mode) |
| `-level-col <name>` | `level` | Level column (log mode) |
| `-extra-cols <list>` | | Extra columns to show (log mode) |

### Output formats

**tui** -- interactive terminal table (bubbletea). Vim-style navigation, cell
expansion, search, yank-to-clipboard, and optional neovim preview harness.

| Key | Action |
|---|---|
| `h` `j` `k` `l` / arrows | Navigate |
| `g` / `G` | Jump to first / last row |
| `Ctrl+d` / `Ctrl+u` | Half-page down / up |
| `0` / `$` | First / last column |
| `Space` | Expand/collapse cell (wraps text, pretty-prints JSON) |
| `Enter` | Preview in nvim harness (if configured) |
| `y` | Yank cell to clipboard |
| `Y` | Yank entire row (tab-separated) |
| `/` | Search (then `n`/`N` for next/prev match) |
| `Esc` | Clear search, collapse, or quit |
| `q` | Quit |

Search modifiers (prefix with `\`): `\j` search column names only, `\C`
case-sensitive, `\c` case-insensitive (default).

**table** -- plain-text aligned table. Good for piping.

**log** -- structured log view with timestamp, level, and message columns.
Designed for telemetry data with `env_time`, `message`, and `level` columns.

**json** -- array of JSON objects, one per row. Use when piping to `jq` or
consuming from a script.

**raw** -- same as table but no column width limit.

## Cache

All query results are cached in a DuckDB database at `~/.jacques/cache.duckdb`.
Subsequent runs of the same query against the same connection return instantly
from cache.

```
jacques cache list                                        # list cached queries
jacques cache show <#>                                    # show full query text
jacques cache query "SELECT * FROM cache_xxx WHERE ..."   # SQL against cache
jacques cache clear                                       # wipe all cached results
jacques cache path                                        # print cache db path
```

Use `-no-cache` to bypass the cache entirely, or `-refresh` to re-query and
update the cached result.

## Web UI

```powershell
jacques serve        # http://localhost:8080
jacques serve 3000   # custom port
```

Serves a browser-based query editor with connection switching, result table,
and the same caching layer as the CLI.

## Auth

Jacques acquires tokens by shelling out to `az account get-access-token` on
each run. Az cli manages its own token cache and refresh flow internally.

**Troubleshooting:**

- Empty stdout and immediate exit = expired `az login` session. Run `az login`.

## Environment variables

| Variable | Purpose |
|---|---|
| `KUSTO_CLUSTER` | Fallback cluster URL when no config/flag is set |
| `KUSTO_DATABASE` | Fallback database name |

A `.env` file in the working directory is loaded automatically.

## Agent usage

When using jacques from a coding agent (pi, Claude Code, etc.):

- **Always write queries to a `.kql` file and use `-f`**. Piping via stdin
  does not work reliably from non-interactive shells.
- Use `-format json` for machine-readable output, `-format table` for readable
  output.
- Use `-no-cache` when you need fresh results.
- If jacques hangs or returns empty output, check for expired auth
  (`az login`) or orphaned processes.
- Clean up temp `.kql` files after use.

See `skills/` for domain-specific telemetry query guidance (activity names,
`customDimensions` schemas, performance rules).

## Skills

The `skills/` directory contains agent skill files with domain knowledge for
specific telemetry workloads:

- **`skills/core-service-telemetry/SKILL.md`** -- general CAPAnalytics query
  patterns, performance rules, `customDimensions` cost model, time window
  sizing, and correlation-pull techniques.
- **`skills/orchard-telemetry/SKILL.md`** -- Orchard-specific activity names,
  `customDimensions` key layouts, session tracing across multi-node topology,
  and log-structured storage diagnostics.

## Project structure

```
main.go                  CLI entrypoint, flag parsing, subcommands
config/                  HCL config loading (~/.jacques/config.hcl)
auth/                    Token acquisition via az cli
backend/                 Backend interface + registry
  backend/kusto/         Azure Data Explorer HTTP backend
  backend/duckdb/        DuckDB SQL backend (local files, in-memory)
  backend/csv/           CSV file backend
cache/                   DuckDB-backed query result cache
data/                    RowStore interface + implementations
kusto/                   Raw Kusto HTTP client
render/                  Output renderers (tui, table, log, json)
harness/                 External preview harness (nvim)
logging/                 OpenTelemetry-based structured logging
server/                  Web UI HTTP server
webui/                   Embedded web frontend (HTML/CSS/JS)
skills/                  Agent skill files for telemetry domains
```
