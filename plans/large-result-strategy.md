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

## Strategy: Tiered Approach

### Tier 0 — Soft Limit with User Warning (quick win)

Add a `--max-results` flag (default 10,000). If the Kusto response exceeds
this, truncate `QueryResult.Rows` and print a warning:

```
warning: result truncated to 10,000 rows (query returned 523,841).
         Use --max-results to raise the limit, or add a KQL filter.
```

This doesn't solve the architectural problem but immediately prevents OOM
for casual queries that accidentally return millions of rows.

**Cost:** ~10 lines of code. Ship immediately.

### Tier 1 — Lazy Cell Formatting

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

### Tier 2 — Streaming JSON Decode

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
Tier 3 it enables true streaming.

**Cost:** Rewrite `doQuery`. Medium effort, but the v2 frame format is
well-structured for this.

### Tier 3 — Memory-Mapped Row Store

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

### Tier 4 — Server-Side Pagination

Use Kusto's `request_external_table_artifacts` or client-side cursor
patterns to page through results:

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

## Recommended Sequence

```
Now         Tier 0  --max-results soft cap
Week 1      Tier 1  lazy cell formatting + width sampling
Week 2      Tier 2  streaming JSON decode
Later       Tier 3  mmap row store (only if >100K row queries are common)
Maybe       Tier 4  server-side pagination (only if >64MB results needed)
```

## Search at Scale

Search (`/`) currently scans every cell. At scale:

- **Tier 0–1:** Search runs in a goroutine with a progress indicator.
  Searches the raw `QueryResult.Rows` (avoids formatting cost). Cancel
  with Esc.
- **Tier 2–3:** Build a simple inverted index on ingest — for each unique
  token (whitespace-split), store the set of `(row, col)` positions.
  Search becomes an index lookup + verify pass. Index is ~20% of data size
  but makes search instant.
- **Tier 4:** Push the search to Kusto (`| where * contains "term"`) and
  re-query. Simplest, most correct, but adds latency.

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
`QueryResult` itself — Tiers 2–3 benefit all renderers by reducing the
in-memory footprint of the raw data.

For `json` format with huge results, switch from `enc.Encode(records)` to
streaming each record individually so we never build the full
`[]map[string]interface{}` array.
