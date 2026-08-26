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

**Nix** (x86_64/aarch64, linux and darwin):

```bash
nix build github:jack-work/jacques --override-input msauth path:/abs/path/to/msauth
```

The override is not optional. `go.mod` carries
`replace github.com/jack-work/msauth => ../msauth` and that repository is not
public, so the flake declares the input as an empty placeholder rather than
committing a path out of somebody's home directory; `nix/package.nix` builds a
store workspace holding both repos as siblings. From a consuming flake, use
`inputs.jacques.inputs.msauth.follows = "<your msauth source input>"`.

The derivation sets `allowGoReference = true` on purpose. `-trimpath` makes Go
drop the `-ldflags` build setting, which is the only place the foundation stamp
below can be read from -- so a trimmed build is permanently `VerdictUnstamped`.
See the comment on that line for the measurement.

### 2. Prerequisites

**Azure CLI** -- jacques acquires Kusto tokens through the shared `msauth`
foundation using its `azure-cli-only` policy, which uses the current Azure CLI
login. Install the Azure CLI and log in before first use:

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

Jacques does not implement token acquisition. It delegates to the shared
`msauth` foundation (`github.com/jack-work/msauth`), which owns acquisition,
the current-user DPAPI-protected token cache, the two-minute refresh skew,
sanitized diagnostics, and stable error codes.

jacques has no broker (WAM) path, so every request uses the `azure-cli-only`
policy: the identity is the current `az login`. The connection's `scopes` value
in `config.hcl` is the single source of truth for the selector and is handed to
`msauth` unmodified:

- a value with a path segment (`https://help.kusto.windows.net/.default`,
  `.../user_impersonation`) is passed as the scope, byte for byte;
- a bare resource (`https://help.kusto.windows.net`) is passed as the audience,
  which `msauth` normalizes to that resource's `/.default` scope.

jacques never derives a resource, splits scopes, or parses expiry itself.

`token_provider` accepts `az` or an empty value; both mean "acquire through
`msauth` with the `azure-cli-only` policy". `tenant_id` selects the tenant.
`client_id` is config metadata only: under `azure-cli-only` the Entra client is
the Azure CLI public client. A connection with no `scopes` needs no token.

**Which `msauth` is in this binary:**

`msauth` is linked as a library, so a fix to the foundation reaches you only
after jacques is rebuilt and reinstalled. `jacques version` reports the linked
module version so an installed binary can be audited without disassembling it.
It authenticates nothing and reads no cache:

```
$ jacques version
jacques v0.2.0
msauth  v0.3.1

$ jacques version --json
{
  "jacques": "v0.2.0",
  "msauth": "v0.3.1"
}
```

A build that resolves `msauth` through a local `replace` directive has no module
version to report, so stamp the foundation commit at link time. **Stamp the
dirty state too:**

```bash
STAMP=$(git -C ../msauth rev-parse --short HEAD)
[ -z "$(git -C ../msauth status --porcelain)" ] || STAMP="$STAMP+dirty"
go build -ldflags "-X github.com/jack-work/msauth.Stamp=$STAMP"
```

Such a build reports `replaced+<commit>`. Unstamped it reports
`replaced=../msauth`, which names the directory and admits the artifact is
reproducible on one machine only. A real module version always wins, so the
stamp retires itself once `msauth` is published with tags.

Write it as two statements. The tidier one-liner
`STAMP=$(...)$(git status --porcelain | grep -q . && echo +dirty)` **exits 1 on a
clean tree**, because the assignment takes the status of its last substitution
and `grep -q` found nothing, so under `set -e` it aborts the build on exactly the
healthy case.

Do not shorten this to `rev-parse --short HEAD` alone. `rev-parse` reports HEAD
whatever the worktree contains, so a jacques built against a *modified* `msauth`
would be stamped with a clean revision whose content it does not carry, and
every freshness check that reads the stamp would pass on it. That is strictly
worse than a stale artifact, because a stale one is detectable.

**Is the installed binary current:**

`.msauth-floor` names the `msauth` revision this source requires, and
`TestInstalledArtifactCarriesRequiredFoundation` judges the installed binary
against it, so `go test .` fails when the jacques a user actually runs carries an
older auth foundation than the source was written against. It reads the
artifact's build metadata and never executes it. The judgement itself lives in
`msauth`, so every client answers this question the same way:

```bash
go test -run TestInstalledArtifactCarriesRequiredFoundation .
msauth audit --gate . "$(command -v jacques)"   # the same verdict, from the same floor
```

An absent artifact skips; a stale one fails and prints the rebuild recipe.
`JACQUES_INSTALLED_ARTIFACT` overrides which binary is judged.

**Troubleshooting:**

Acquisition failures print the stable `msauth` error code plus one line per
credential source, so they cannot be mistaken for a configuration problem:

```
error: token acquisition failed [acquisition_failed]: no configured credential source could satisfy the token request
  azure-cli [azure_cli_failed]: Azure CLI command failed: exit status 1: Please run 'az login'
```

Codes are `invalid_request`, `acquisition_failed`, `wam_unavailable`,
`wam_failed`, `azure_cli_failed`, and `internal_error`.

- `azure_cli_failed` mentioning `az login` = expired Azure CLI session.
  Run `az login`.
- `invalid_request` = a bad `token_provider` or a `scopes` value that names more
  than one resource.

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

- **`skills/core-service-telemetry/SKILL.md`** -- general MyDatabase query
  patterns, performance rules, `customDimensions` cost model, time window
  sizing, and correlation-pull techniques.
- **`skills/orchard-telemetry/SKILL.md`** -- MyService-specific activity names,
  `customDimensions` key layouts, session tracing across multi-node topology,
  and log-structured storage diagnostics.

## Project structure

```
main.go                  CLI entrypoint, flag parsing, subcommands
config/                  HCL config loading (~/.jacques/config.hcl)
auth/                    Connection-to-msauth adapter (azure-cli-only policy)
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
