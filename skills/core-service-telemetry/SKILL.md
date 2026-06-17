---
name: core-service-telemetry
description: Query CAPAnalytics and Core Services telemetry lakes using jacques, the local Kusto TUI client. Use when investigating service health, errors, performance, or operational telemetry on island clusters (OperationEvents, TraceEvents). Covers query patterns, performance rules, auth troubleshooting, and Orchard-specific schema knowledge.
disable-model-invocation: true
---

# Core Service Telemetry

Query CAPAnalytics (and related Core Services telemetry lakes) efficiently
using `jacques`, the local Kusto TUI client.

## Jacques basics

```bash
# Run a .kql file against a named connection
jacques.exe -c cap-analytics -f path/to/query.kql -format json -no-cache

# Inline queries via stdin do NOT work from the agent shell.
# Always write to a .kql file and use -f. Clean up temp files after use.
```

Config lives at `~/.jacques/config.hcl`. Each `connection "kusto" "<name>"`
block defines a cluster, database, tenant, and scopes.

Output formats: `json`, `table`, `raw`, `tui`. Use `json` when piping to
`jq` or when running from an agent. Use `tui` for interactive exploration
with column search, nvim preview, etc.

### Flags worth knowing

| Flag | Purpose |
|---|---|
| `-no-cache` | Bypass DuckDB result cache, always hit the cluster |
| `-refresh` | Re-query and overwrite the cached result |
| `-format <fmt>` | Output format: `json`, `table`, `raw`, `tui` |
| `-cols <list>` | Comma-separated columns to show in TUI |
| `-f <path>` | Read query from a `.kql` file |

### Auth flow

Jacques acquires tokens via `az cli` and caches them. When the token
expires, it shells out to `az account get-access-token`.

**Gotcha:** When jacques returns empty stdout and exits quickly, the most
likely cause is an expired `az login` session. Check `stderr` — the error
message `az account get-access-token failed: exit status 1` confirms it.
Run `az login` and retry.

## The CAPAnalytics data model

Two primary tables, both in `CAPAnalytics` on island clusters
(e.g., `fdislandsus.centralus.kusto.windows.net`). **OperationEvents** is
structured activity telemetry with durations, result types, and exception
names. **TraceEvents** is unstructured log lines with a message field and
trace level. They share most indexed columns but have different payloads.

### OperationEvents

Structured activity telemetry. Each operation emits a `Start` and `End`
event pair.

**Indexed / cheap columns** (filter on these first):

- `env_time` — event timestamp (always filter on this)
- `applicationName` — service fabric app name (e.g.,
  `"fabric:/PowerApps.Authoring.Orchard"`)
- `activityName` — the operation name (e.g.,
  `"PowerApps.Authoring.Orchard.Model.Api.History"`)
- `eventType` — `"Start"` or `"End"`
- `resultType` — `"Success"`, `"Failure"`, `"RemoteError"`,
  `"SuccessDespiteError"`
- `correlationId`, `serviceRequestId`, `clientSessionId` — correlation keys
- `exceptionTypeName` — exception class name on failure rows
- `env_cloud_name` — island + region (e.g., `prdil108wus`)

**Expensive columns** (scan-forcing, use only after narrowing):

- `customDimensions` — JSON bag with activity-specific context. Querying
  with `has` or `has_any` forces a full scan over the time window. Always
  combine with indexed filters to keep the scan small.

**Columns that do NOT exist on OperationEvents:**

- `message` — exists only on TraceEvents. Projecting it from
  OperationEvents causes a `SEM0100` semantic error. Use `customDimensions`
  for context on OperationEvents, or query TraceEvents for free-text
  messages.
- `traceLevel` — TraceEvents only (`Info`, `Warning`, `Error`).

In `union` queries, use `column_ifexists("message", "")` and
`column_ifexists("traceLevel", "")` to avoid projection errors.

**Note on `eventType`:** Filtering `eventType == "End"` is useful when
querying OperationEvents alone, but does not work when unioning with
TraceEvents (which lack `eventType`). Many OperationEvents rows also
have an empty `eventType` value, so this filter can hide results even
on OperationEvents alone. Drop it when doing broad entity searches via
`customDimensions has`.

### TraceEvents

Unstructured trace lines. Same schema minus `resultType` / `durationMs`.
Add `traceLevel` (`Info`, `Warning`, `Error`).

**Same cost rules apply** — `message has "..."` and
`customDimensions has "..."` are scan operations.

### Union pattern

When you need both tables:

```kusto
union OperationEvents, TraceEvents
| where env_time between (_start .. _end)
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  ...
```

**Schema differences to watch for:** `message` and `traceLevel` exist only
on TraceEvents; `resultType` and `durationMs` exist only on
OperationEvents. In a `union`, missing columns become null. Use
`column_ifexists()` for table-specific columns:

```kusto
union OperationEvents, TraceEvents
| where ...
| extend
    msg = column_ifexists("message", ""),
    lvl = column_ifexists("traceLevel", "")
| project env_time, activityName, resultType, exceptionTypeName,
    durationMs, lvl, msg, customDimensions
```

## Performance rules of thumb

### 1. Never lead with `customDimensions has` on a wide window

This is the single most expensive mistake. `customDimensions` is not
indexed; `has` forces a row-by-row scan.

**Bad** (times out on 7d+ windows):
```kusto
OperationEvents
| where env_time >= ago(7d)
  and customDimensions has "15491111-0df3-ed14-a86e-65fb6971ca13"
```

**Good** (narrows first, then scans a small result set):
```kusto
OperationEvents
| where env_time between (datetime(2026-05-06) .. datetime(2026-05-08))
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  and activityName == "PowerApps.Authoring.Orchard.Model.Api.History"
  and customDimensions has "15491111-0df3-ed14-a86e-65fb6971ca13"
```

**Rule:** Every query must have `env_time` + at least one indexed column
filter before touching `customDimensions` or `message`.

### 2. Time window sizing

| Window | Behavior |
|---|---|
| ≤ 2 days | Fast for most queries, even with `customDimensions has` if `applicationName` is filtered |
| 2–7 days | Needs `activityName` filter alongside `applicationName` |
| 7–30 days | Must narrow to specific `activityName` values; `customDimensions has` only viable with low-volume activities (see below) |
| > 30 days | Data retention limit on most island clusters |

**`activityName in ()` vs `activityName has_any ()`:** On 7d+ windows,
`has_any` with partial strings is scan-forcing and will time out.
Use `in ()` with exact fully-qualified activity names instead — these
hit the index. Activity names are always fully qualified with the
`PowerApps.Authoring.Orchard.` prefix (see activity name section below).

**Activity volume tiers** (matters for `customDimensions has` scans):

| Tier | Activities | 30d + `customDimensions has` |
|---|---|---|
| Low volume | `CreateContainerSession`, `Model.Api.History`, `Model.Api.Create` | ✅ Works |
| Medium volume | `PersistentModelGrain.*`, `Model.Lifecycle.List` | ⚠️ May need ≤7d window |
| High volume | `AuthorizationService.CheckAccess` | ❌ Times out even at 7d; limit to ≤2d |

### 3. `customDimensions` key extraction

Keys are not uniform across activities. Always probe the key space first:

```kusto
OperationEvents
| where env_time >= ago(15m)
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  and activityName == "<specific-activity>"
  and isnotempty(customDimensions)
| extend keys = bag_keys(parse_json(customDimensions))
| mv-expand k = keys to typeof(string)
| summarize n = count() by k
| top 30 by n
```

**Orchard-specific key inconsistencies:**

- Model ID appears as `ModelContext.ModelId`, `modelId` (lowercase), or
  `ResourceId` depending on the activity. Use `coalesce()` across all three.
- `environmentId` sometimes appears as `ModelContext.EnvironmentId`.
- `httpStatusCode` is inside `customDimensions`, not a top-level column.

Extract with `extract_json`:
```kusto
| extend httpStatusCode = extract_json("$httpStatusCode", customDimensions)
```

Or parse once and project:
```kusto
| extend json = parse_json(customDimensions)
| extend envId = tostring(json["environmentId"])
```

### 4. Correlation-pull pattern (the bread and butter)

Find interesting rows, extract their correlation keys, then `rightsemi`
join to pull all related rows:

```kusto
let _qualifying =
    OperationEvents
    | where env_time between (_start .. _end)
      and applicationName == "fabric:/PowerApps.Authoring.Orchard"
      and <your condition>
    | distinct correlationId;
_qualifying
| join kind=rightsemi hint.strategy=broadcast (
    OperationEvents
    | where env_time between (_start .. _end)
      and applicationName == "fabric:/PowerApps.Authoring.Orchard"
) on correlationId
```

This is cheap because both sides filter on indexed columns. The qualifying
set is small; the join is broadcast.

**For single-correlation deep dives**, skip the join and just filter
directly. Pin to a 2-3 minute window around the event for sub-second
results:

```kusto
union OperationEvents, TraceEvents
| where env_time between (datetime(...) .. datetime(...))
  and applicationName == "fabric:/PowerApps.Authoring.Orchard"
  and correlationId == "<id>"
| order by env_time asc
```

### 5. `format_datetime` pitfalls

KQL's `format_datetime` chokes on literal characters in the format string.
`T` and `Z` are not valid format specifiers:

```kusto
// BROKEN — "failed to parse format string"
format_datetime(ts, "yyyy-MM-ddTHH:mm:ssZ")

// WORKS — append literals via strcat
strcat(format_datetime(ts, "yyyy-MM-dd"), "T",
       format_datetime(ts, "HH:mm:ss"), "Z")

// SIMPLEST — tostring() already emits ISO 8601
tostring(ts)
```

### 6. Identifying the island from `env_cloud_name`

```kusto
| extend IslandNumber = extract(@"prdil(\d+)", 1, env_cloud_name)
```

`env_cloud_name` encodes island + region: `prdil108wus` = island 108,
West US. Storage accounts follow the pattern
`prdil{island}{region}0orchard0cc`.

Known island regions for island 301 (first release): `prdil301wus` (West
US, ~80% of traffic), `prdil301eus` (East US, ~20%).

Use `startswith` for island-wide queries:
```kusto
| where env_cloud_name startswith "prdil301"
```

## Orchard-specific reference

For detailed Orchard activity names, `customDimensions` key maps,
`requestUri` extraction patterns, and log-structured storage diagnostics,
see the `orchard-telemetry` skill. For stranded model (island-move)
triage, see the `orchard-island-hopping-triage` skill.

## Jacques timeout patterns

When jacques hangs or times out from the agent:

1. Check if `jacques.exe` is still running (`tasklist | rg jacques`)
2. Empty stdout + process alive = query is still executing on the cluster
3. Empty stdout + process dead = token failure (check stderr for
   `az account get-access-token failed`)
4. Use `-no-cache` if you suspect stale cached results
5. Kill orphaned processes before retrying (`taskkill /F /IM jacques.exe`)

**Piping queries via stdin does not work** with jacques from the agent
shell. Always write queries to a `.kql` file and use `-f`. Clean up temp
files after use.

## Available connections

| Connection | Cluster | Database | Use for |
|---|---|---|---|
| `cap-analytics` | `fdislandsus.centralus.kusto.windows.net` | `CAPAnalytics` | Production island telemetry |
| `cap-analyticstest` | `fdislandsnonprod.kusto.windows.net` | `CAPAnalyticsTest` | Test/nonprod island telemetry |

Island 301 is the **first release island** and its data lives on the
`cap-analytics` (prod) connection.

## Creating Data Explorer deep links

To share a query as a clickable link (e.g., for Teams), gzip-compress,
base64-encode, and URL-encode the query text into the `query` parameter.

Do everything in **one bash invocation** (variables don't persist across
separate agent tool calls):

```bash
QUERY='OperationEvents | where env_time >= ago(1d) | take 10'

ENCODED=$(printf '%s' "$QUERY" | gzip -c | base64 -w0 \
  | python3 -c "import sys,urllib.parse; print(urllib.parse.quote(sys.stdin.read(), safe=''))")
LINK="https://dataexplorer.azure.com/clusters/<cluster>/databases/<database>?query=${ENCODED}"

echo "$LINK"
```

Replace `<cluster>` and `<database>` with the target (e.g.,
`fdislandsus.centralus` / `CAPAnalytics`). Combine with the `teams-fmt`
skill to put a clickable link + formatted query block on the clipboard.

## Template library

Verified query templates live at
`~/dev/skills/orchard-triage/templates/*.kql`. Each file documents its
pattern, knobs, and output shape in comments.
