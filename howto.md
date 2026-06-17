
 ### DuckDB Backend (backend/duckdb/)

 A full SQL backend — any DuckDB-supported query:

 ```powershell
   # Direct SQL
   jacques -c duck "SELECT 42 AS answer"

   # Query CSV/Parquet files with SQL
   jacques -c duck "SELECT * FROM read_csv_auto('data.csv') WHERE level = 'Error'"
   jacques -c duck "SELECT * FROM read_parquet('logs/*.parquet') LIMIT 100"
 ```

 ### DuckDB Cache (replaces JSON file cache)

 All query results are now cached in a persistent DuckDB database at ~/.jacques/cache.duckdb:

 ```powershell
   # First run — hits Kusto, caches in DuckDB
   jacques -c nsp-logs -format tui -f scratch.kql

   # Second run — instant from DuckDB cache
   jacques -c nsp-logs -format tui -f scratch.kql

   # Run SQL against cached results
   jacques cache list                    # see cached queries + table names
   jacques cache query "SELECT level, count(*) FROM cache_xxx GROUP BY level"
   jacques cache query "SELECT * FROM cache_xxx WHERE message LIKE '%error%'"
   jacques cache clear                   # wipe cache
 ```

 ### What this unlocks

 - Results survive restarts (was: gzipped JSON, now: DuckDB)
 - SQL re-query against any cached result — GROUP BY, JOIN, WHERE, aggregates
 - DuckDB as a first-class backend for local analytics on CSV/Parquet
 - Foundation for FTS search acceleration (DuckDB's FTS extension) in Step 7
