# DuckDB + Arrow on Windows: Research Notes

## Current State of Our Machine

- No msys2/mingw installed (`/c/msys64` doesn't exist)
- `CGO_ENABLED=0` by default
- `go env CC` = `gcc` (not found on PATH)

---

## Driver Options

### Option 1: `github.com/duckdb/duckdb-go/v2` (official, cgo)

The official driver. Formerly `marcboeker/go-duckdb`, now maintained by
the DuckDB team directly. Latest: `v2.10502.0` (DuckDB 1.5.2).

**Arrow support:** Opt-in via `-tags=duckdb_arrow`. Uses
`github.com/apache/arrow-go/v18`. The Arrow interface is zero-copy
through DuckDB's C Arrow data interface (`ArrowSchema`/`ArrowArray`
structs in shared memory). No serialization.

**Windows build requires:**

1. Install msys2: https://www.msys2.org/
2. In msys2 terminal: `pacman -S mingw-w64-ucrt-x86_64-gcc`
3. Add to PATH: `$env:PATH = "C:\msys64\ucrt64\bin;$env:PATH"`
4. Build: `CGO_ENABLED=1 go build -tags=duckdb_arrow`

The driver ships pre-built DuckDB static libraries via
`github.com/duckdb/duckdb-go-bindings` — so you don't need to compile
DuckDB from source. The mingw gcc just compiles the cgo bridge.

**Pros:** Full Arrow integration (zero-copy query results as
`arrow.Record` batches), all DuckDB extensions (FTS, JSON, Parquet),
actively maintained by DuckDB team, `database/sql` compatible.

**Cons:** cgo required. Mingw on Windows adds a build dep.
Cross-compilation becomes harder. Binary size increases (~30–50MB from
DuckDB static lib).

### Option 2: `github.com/fpt/go-pduckdb` (purego, no cgo)

Brand new (April 2025). Uses `ebitengine/purego` to call DuckDB's C API
via `syscall.NewLazyDLL` / `dlopen` at runtime. No cgo at compile time.

**Windows setup:**
1. Download `duckdb.dll` from DuckDB releases
2. Put it on PATH or set `DUCKDB_LIBRARY_PATH`
3. Build: `go build` (no cgo flags needed)

**Pros:** No cgo, no mingw, trivial cross-compilation. The DLL is a
runtime dep, not a compile-time dep.

**Cons:** No Arrow interface (yet). Standard `database/sql` only —
results come back as Go types via `rows.Scan()`, not as Arrow
RecordBatches. This means we'd need to build Arrow RecordBatches
ourselves from scanned rows — losing the zero-copy advantage.
Very new, single maintainer, not battle-tested.

### Option 3: `github.com/scottlepp/go-duck` (CLI wrapper)

Shells out to `duckdb` CLI. Not a real library — passes SQL as
command-line args, parses stdout. Not viable for us.

---

## Arrow Go Library

**Import:** `github.com/apache/arrow-go/v18`

Note: the module path changed from `github.com/apache/arrow/go/v17` to
`github.com/apache/arrow-go/v18` in Oct 2024. The external proposal
pinned v17 (old path). We should use **v18** with the new path.

**Key types:**
- `arrow.Schema` — column names + types
- `arrow.Record` (= RecordBatch) — columnar data, ref-counted
- `arrow.Field` — one column definition
- `memory.GoAllocator` — production allocator
- `memory.CheckedAllocator` — test allocator (panics on leak)
- `array.RecordBuilder` — builds a RecordBatch row-by-row
- `array.RecordReader` — interface DuckDB's Arrow API returns
- `ipc.NewWriter` / `ipc.NewReader` — Arrow IPC streaming format

**Idiomatic usage pattern:**

```go
pool := memory.NewGoAllocator()
schema := arrow.NewSchema([]arrow.Field{
    {Name: "ts", Type: arrow.FixedWidthTypes.Timestamp_us},
    {Name: "msg", Type: arrow.BinaryTypes.String},
    {Name: "level", Type: arrow.BinaryTypes.String},
}, nil)

b := array.NewRecordBuilder(pool, schema)
defer b.Release()

// Append values
b.Field(0).(*array.TimestampBuilder).Append(...)
b.Field(1).(*array.StringBuilder).Append("hello")
b.Field(2).(*array.StringBuilder).Append("info")

rec := b.NewRecord()  // creates the RecordBatch
defer rec.Release()    // MUST release — ref-counted

// Access columns
col := rec.Column(1).(*array.String)
for i := 0; i < col.Len(); i++ {
    fmt.Println(col.Value(i))
}
```

**Memory discipline:** Every `Record`, `Array`, and `Builder` is
ref-counted. Call `.Release()` when done. In tests, use
`memory.CheckedAllocator` which panics if anything leaks.

---

## DuckDB Arrow Integration (via official driver)

```go
// Open DuckDB
c := duckdb.NewConnector("", nil)
conn := c.Connect(ctx)
defer conn.Close()

// Get Arrow interface
ar, _ := duckdb.NewArrowFromConn(conn)

// Query — returns array.RecordReader (streams RecordBatches)
reader, _ := ar.QueryContext(ctx, "SELECT * FROM read_parquet('logs.parquet')")
defer reader.Release()

for reader.Next() {
    rec := reader.Record()      // arrow.Record — one batch
    fmt.Println(rec.NumRows())
    // Process columns directly — zero copy from DuckDB
    col := rec.Column(0)
    // ...
}

// Ingest Arrow data INTO DuckDB
pool := memory.NewGoAllocator()
// ... build records ...
tbl := array.NewTableFromRecords(schema, records)
tr := array.NewTableReader(tbl, 10000) // chunk size
release, _ := ar.RegisterView(tr, "my_arrow_data")
defer release()
db.Exec("CREATE TABLE cached AS SELECT * FROM my_arrow_data")
```

**Key point:** `ar.QueryContext()` returns an `array.RecordReader` that
streams `arrow.Record` batches directly from DuckDB's columnar engine.
No row-by-row scanning, no `interface{}` boxing. This is exactly our
`PageStream` interface.

And `ar.RegisterView()` lets us feed Arrow RecordBatches INTO DuckDB
for caching/indexing — also zero-copy through the C data interface.

---

## Recommendation for Jacques

### Build strategy

Use the **official `duckdb/duckdb-go/v2`** driver with `-tags=duckdb_arrow`.
The zero-copy Arrow integration is the whole point — without it, DuckDB
is just a slower SQLite for our use case.

**Windows toolchain setup (one-time):**

```powershell
# Install msys2 (if not present)
winget install MSYS2.MSYS2

# In msys2 UCRT64 terminal:
pacman -S mingw-w64-ucrt-x86_64-gcc

# Add to PATH permanently (or in profile)
$env:PATH = "C:\msys64\ucrt64\bin;$env:PATH"

# Verify
gcc --version
go env -w CGO_ENABLED=1
```

Then builds work normally:
```powershell
go build -tags=duckdb_arrow .
```

### Dependency pins

```
github.com/apache/arrow-go/v18        # Arrow IR (NOT v17, NOT old path)
github.com/duckdb/duckdb-go/v2        # DuckDB driver (NOT marcboeker)
```

The external proposal pinned `apache/arrow/go/v17` and
`marcboeker/go-duckdb`. Both are outdated. The arrow module moved to
`apache/arrow-go` and the duckdb driver moved to `duckdb/duckdb-go`.

### What about the purego driver?

Keep an eye on `fpt/go-pduckdb`. If it adds Arrow support, it becomes
a compelling alternative that eliminates the mingw dependency. For now,
it's too young and lacks the Arrow interface we need.

### Binary distribution

For users who don't want to set up mingw, we can ship pre-built binaries
via GitHub releases. `goreleaser` with cgo cross-compilation (via
`zig cc` as the cross-compiler, or GitHub Actions with msys2) handles
this well.
