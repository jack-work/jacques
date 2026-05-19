package main

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jokellih/jacques/auth"
	"github.com/jokellih/jacques/backend"
	_ "github.com/jokellih/jacques/backend/csv"
	_ "github.com/jokellih/jacques/backend/duckdb"
	_ "github.com/jokellih/jacques/backend/kusto"
	"github.com/jokellih/jacques/cache"
	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/data"
	"github.com/jokellih/jacques/harness"
	"github.com/jokellih/jacques/logging"
	"github.com/jokellih/jacques/render"
	"github.com/jokellih/jacques/server"
)

const (
	ansiDim   = "\x1b[38;5;241m"
	ansiBold  = "\x1b[1;38;5;39m"
	ansiGreen = "\x1b[38;5;35m"
	ansiEnd   = "\x1b[0m"
)

func status(icon, msg string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, ansiDim+icon+" "+msg+ansiEnd+"\n", args...)
}

var buildVersion = "dev"

//go:embed all:webui
var webFS embed.FS

func main() {
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Println("jacques", buildVersion)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "config" {
		handleConfig(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "cache" {
		handleCache(os.Args[2:])
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		handleServe(os.Args[2:])
		return
	}

	queryCmd := flag.NewFlagSet("query", flag.ExitOnError)
	conn := queryCmd.String("c", "", "connection name from ~/.jacques/config.hcl")
	format := queryCmd.String("format", "", "output format: table, log, json, raw, tui")
	cluster := queryCmd.String("cluster", "", "kusto cluster URL (overrides config)")
	database := queryCmd.String("db", "", "database name (overrides config)")
	maxRows := queryCmd.Int("max-rows", 0, "max rows to display (0 = unlimited)")
	showAll := queryCmd.Bool("all-cols", false, "in log mode, show all columns per entry")
	timeCol := queryCmd.String("time-col", "", "column name for timestamps in log mode")
	msgCol := queryCmd.String("msg-col", "", "column name for message in log mode")
	levelCol := queryCmd.String("level-col", "", "column name for log level in log mode")
	extraCols := queryCmd.String("extra-cols", "", "comma-separated extra columns to show in log mode")
	tuiCols := queryCmd.String("cols", "", "comma-separated columns to show in TUI mode")
	queryFile := queryCmd.String("f", "", "read query from file")
	noCache := queryCmd.Bool("no-cache", false, "bypass cache, always query backend")
	refresh := queryCmd.Bool("refresh", false, "ignore cached result, re-query and update cache")
	queryCmd.Parse(os.Args[1:])

	loadEnv(".env")

	shutdown, err := logging.Init("jacques")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to init logging: %v\n", err)
	} else {
		defer shutdown()
	}

	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (using flags/env only)\n", err)
		cfg = &config.Config{}
	}

	// Resolve connection
	var connCfg *config.Connection
	if *conn != "" {
		connCfg = cfg.FindConnection(*conn)
		if connCfg == nil {
			fmt.Fprintf(os.Stderr, "error: connection %q not found in %s\n", *conn, config.Path())
			fmt.Fprintln(os.Stderr, "run: jacques config list")
			os.Exit(1)
		}
	} else {
		connCfg = cfg.DefaultConn()
	}

	// Build resolved connection: flag > config > env > fallback
	resolved := resolveConnection(connCfg, *cluster, *database)

	// Resolve display settings: flag > config > defaults
	fmtStr := resolveValue(*format, configFormat(cfg), "log")
	timColStr := resolveValue(*timeCol, configTimeCol(cfg), "env_time")
	msgColStr := resolveValue(*msgCol, configMsgCol(cfg), "message")
	lvlColStr := resolveValue(*levelCol, configLevelCol(cfg), "level")

	// Read query
	var kql string
	if *queryFile != "" {
		qdata, err := os.ReadFile(*queryFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading query file: %v\n", err)
			os.Exit(1)
		}
		kql = string(qdata)
	} else {
		kql = strings.Join(queryCmd.Args(), " ")
		if strings.HasPrefix(kql, "@") {
			qdata, err := os.ReadFile(kql[1:])
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading query file: %v\n", err)
				os.Exit(1)
			}
			kql = string(qdata)
		}
	}

	if kql == "" {
		fmt.Fprintln(os.Stderr, "usage: jacques [flags] <KQL query>")
		fmt.Fprintln(os.Stderr, "       jacques -f <file.kql> [flags]")
		fmt.Fprintln(os.Stderr, "       jacques -c <connection> <KQL query>")
		fmt.Fprintln(os.Stderr, "       jacques config list")
		fmt.Fprintln(os.Stderr, "       jacques config init")
		os.Exit(1)
	}

	logging.Info(ctx, "jacques starting",
		logging.String("format", fmtStr),
		logging.String("connection", resolved.Name),
		logging.String("type", resolved.Type),
	)

	status("→", "%s/%s", resolved.Name, resolved.Database)

	// Open cache
	useCache := !*noCache
	var dc *cache.DuckCache
	if useCache {
		var err error
		dc, err = cache.NewDuckCache()
		if err != nil {
			logging.Warn(ctx, "failed to open cache, continuing without", logging.String("error", err.Error()))
			useCache = false
		} else {
			defer dc.Close()
		}
	}

	// Check cache
	var store data.RowStore
	if useCache && !*refresh {
		if cached, ok := dc.Get(ctx, resolved.Name, kql); ok {
			store = cached
			status("✓", "cache hit — %s%d rows%s", ansiBold, store.RowCount(), ansiDim)
		}
	}

	// Cache miss — query backend
	if store == nil {
		if *refresh {
			status("↻", "refresh requested, skipping cache")
		} else if useCache {
			status("○", "cache miss")
		}

		token, err := auth.GetToken(ctx, resolved)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		resolved.Token = token

		be, err := backend.New(resolved)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer be.Close()

		sp := render.NewSpinner("querying " + resolved.Name)
		store, err = be.Query(ctx, kql)
		elapsed := sp.Stop()
		if err != nil {
			logging.Error(ctx, "query failed", logging.String("error", err.Error()))
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		status("✓", "%s%d rows%s in %s", ansiBold, store.RowCount(), ansiDim, elapsed.Truncate(time.Millisecond))

		if useCache {
			if err := dc.Put(ctx, resolved.Name, kql, store); err != nil {
				logging.Warn(ctx, "failed to cache result", logging.String("error", err.Error()))
			}
		}
	}
	defer store.Close()

	w := os.Stdout

	switch fmtStr {
	case "table":
		opts := render.DefaultOptions()
		opts.MaxRows = *maxRows
		render.Table(w, store, opts)

	case "log":
		opts := render.DefaultLogOptions()
		opts.TimeColumn = timColStr
		opts.MessageColumn = msgColStr
		opts.LevelColumn = lvlColStr
		opts.ShowAllCols = *showAll
		if *extraCols != "" {
			opts.ExtraColumns = strings.Split(*extraCols, ",")
		}
		render.Log(w, store, opts)

	case "json":
		render.JSON(w, store)

	case "tui":
		tuiOpts := render.DefaultTUIOptions()
		if *tuiCols != "" {
			tuiOpts.Columns = strings.Split(*tuiCols, ",")
		}
		if h := initHarness(cfg); h != nil {
			render.PreviewHook = func(s data.RowStore, row, col int) {
				content := cellToPreview(s, row, col)
				if err := h.Preview(content); err != nil {
					logging.Warn(ctx, "preview failed", logging.String("error", err.Error()))
				}
			}
		}
		render.TUI(store, tuiOpts)

	case "raw":
		opts := render.DefaultOptions()
		opts.MaxColWidth = 0
		opts.MaxRows = *maxRows
		render.Table(w, store, opts)

	default:
		fmt.Fprintf(os.Stderr, "unknown format: %s\n", fmtStr)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// serve subcommand
// ---------------------------------------------------------------------------

func handleServe(args []string) {
	loadEnv(".env")

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v (using defaults)\n", err)
		cfg = &config.Config{}
	}

	port := 8080
	if len(args) > 0 {
		p, err := strconv.Atoi(args[0])
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid port %q: %v\n", args[0], err)
			os.Exit(2)
		}
		port = p
	}

	srv := server.New(cfg, webFS)
	defer srv.Close()
	if err := srv.ListenAndServe(port); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// cache subcommand
// ---------------------------------------------------------------------------

func handleCache(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jacques cache <list|show|clear|query|path>")
		os.Exit(1)
	}

	ctx := context.Background()

	switch args[0] {
	case "path":
		fmt.Println(cache.DuckDBPath())

	case "list":
		dc, err := cache.NewDuckCache()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer dc.Close()

		entries, err := dc.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Fprintln(os.Stderr, "cache is empty")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "#\tCONN\tROWS\tCOLS\tAGE\tTABLE\tQUERY")
		fmt.Fprintln(tw, "-\t----\t----\t----\t---\t-----\t-----")
		for i, e := range entries {
			age := time.Since(e.Timestamp).Truncate(time.Second)
			query := e.Query
			query = strings.ReplaceAll(query, "\n", " ")
			query = strings.ReplaceAll(query, "\r", "")
			if len(query) > 60 {
				query = query[:59] + "…"
			}
			tblName := strings.TrimSuffix(e.Key, ".json.gz")
			if !strings.HasPrefix(tblName, "cache_") {
				tblName = "cache_" + tblName
			}
			fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%s\t%s\t%s\n",
				i+1, e.Conn, e.Rows, e.Cols, age, tblName, query)
		}
		tw.Flush()

	case "show":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: jacques cache show <#>")
			fmt.Fprintln(os.Stderr, "  show full query for a cache entry (use 'jacques cache list' to see #)")
			os.Exit(1)
		}
		var idx int
		if _, err := fmt.Sscanf(args[1], "%d", &idx); err != nil || idx < 1 {
			fmt.Fprintf(os.Stderr, "error: invalid entry number %q\n", args[1])
			os.Exit(1)
		}
		dc, err := cache.NewDuckCache()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer dc.Close()

		entries, err := dc.List(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if idx > len(entries) {
			fmt.Fprintf(os.Stderr, "error: entry #%d not found (have %d entries)\n", idx, len(entries))
			os.Exit(1)
		}
		e := entries[idx-1]
		tblName := strings.TrimSuffix(e.Key, ".json.gz")
		if !strings.HasPrefix(tblName, "cache_") {
			tblName = "cache_" + tblName
		}
		fmt.Fprintf(os.Stderr, "connection: %s\n", e.Conn)
		fmt.Fprintf(os.Stderr, "table:      %s\n", tblName)
		fmt.Fprintf(os.Stderr, "rows:       %d\n", e.Rows)
		fmt.Fprintf(os.Stderr, "cached:     %s ago\n", time.Since(e.Timestamp).Truncate(time.Second))
		fmt.Fprintf(os.Stderr, "---\n")
		fmt.Println(e.Query)

	case "clear":
		dc, err := cache.NewDuckCache()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer dc.Close()

		if err := dc.Clear(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("cache cleared")

	case "query":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: jacques cache query <SQL>")
			fmt.Fprintln(os.Stderr, "  run SQL against cached results")
			fmt.Fprintln(os.Stderr, "  use: jacques cache list  to see available tables")
			os.Exit(1)
		}
		dc, err := cache.NewDuckCache()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer dc.Close()

		sql := strings.Join(args[1:], " ")
		store, err := dc.QueryCache(ctx, sql)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		opts := render.DefaultOptions()
		render.Table(os.Stdout, store, opts)

	default:
		fmt.Fprintf(os.Stderr, "unknown cache command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: jacques cache <list|show|clear|query|path>")
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// config subcommand
// ---------------------------------------------------------------------------

func handleConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jacques config <list|init|use|set|path>")
		os.Exit(1)
	}

	switch args[0] {
	case "init":
		if err := config.WriteDefault(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("config written to %s\n", config.Path())

	case "path":
		fmt.Println(config.Path())

	case "list":
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			fmt.Fprintf(os.Stderr, "run: jacques config init\n")
			os.Exit(1)
		}
		if len(cfg.Connections) == 0 {
			fmt.Fprintln(os.Stderr, "no connections configured")
			fmt.Fprintf(os.Stderr, "run: jacques config init\n")
			os.Exit(1)
		}

		if len(args) > 1 && args[1] == "--json" {
			type connJSON struct {
				Current  bool   `json:"current"`
				Name     string `json:"name"`
				Type     string `json:"type"`
				Cluster  string `json:"cluster,omitempty"`
				Database string `json:"database,omitempty"`
				Path     string `json:"path,omitempty"`
				Auth     string `json:"auth"`
				Scopes   string `json:"scopes,omitempty"`
			}
			var out []connJSON
			for _, c := range cfg.Connections {
				auth := "-"
				if c.Scopes != "" {
					auth = c.TokenProvider
					if auth == "" {
						auth = "az"
					}
				}
				out = append(out, connJSON{
					Current:  c.Name == cfg.CurrentConnection,
					Name:     c.Name,
					Type:     c.Type,
					Cluster:  c.Cluster,
					Database: c.Database,
					Path:     c.Path,
					Auth:     auth,
					Scopes:   c.Scopes,
				})
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
		} else {
			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "DEFAULT\tNAME\tTYPE\tCLUSTER\tDATABASE\tAUTH")
			fmt.Fprintln(tw, "-------\t----\t----\t-------\t--------\t----")
			for _, c := range cfg.Connections {
				def := ""
				if c.Name == cfg.CurrentConnection {
					def = "*"
				}
				authStatus := "-"
				if c.Scopes != "" {
					switch c.TokenProvider {
					case "az", "":
						authStatus = "az"
					default:
						authStatus = c.TokenProvider
					}
				}
				endpoint := c.Cluster
				if c.Path != "" {
					endpoint = c.Path
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
					def, c.Name, c.Type, endpoint, c.Database, authStatus)
			}
			tw.Flush()
		}

	case "set":
		if len(args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: jacques config set <connection-name> <field> <value>")
			fmt.Fprintln(os.Stderr, "  fields: cluster, database, token, path")
			fmt.Fprintln(os.Stderr, "  example: jacques config set cap-analytics cluster https://new.kusto.windows.net")
			os.Exit(1)
		}
		connName := args[1]
		field := args[2]
		value := args[3]

		validFields := map[string]bool{"cluster": true, "database": true, "path": true, "token_provider": true, "tenant_id": true, "client_id": true, "scopes": true}
		if !validFields[field] {
			fmt.Fprintf(os.Stderr, "error: unknown field %q (valid: cluster, database, path, token_provider, tenant_id, client_id, scopes)\n", field)
			os.Exit(1)
		}

		raw, err := os.ReadFile(config.Path())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading config: %v\n", err)
			os.Exit(1)
		}
		updated := replaceFieldInConfig(string(raw), connName, field, value)
		if err := os.WriteFile(config.Path(), []byte(updated), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("%s.%s updated in %s\n", connName, field, config.Path())

	case "use":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: jacques config use <connection-name>")
			os.Exit(1)
		}
		connName := args[1]
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if cfg.FindConnection(connName) == nil {
			fmt.Fprintf(os.Stderr, "error: connection %q not found\n", connName)
			fmt.Fprintln(os.Stderr, "run: jacques config list")
			os.Exit(1)
		}

		raw, err := os.ReadFile(config.Path())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading config: %v\n", err)
			os.Exit(1)
		}
		updated := replaceTopLevelField(string(raw), "current_connection", connName)
		if err := os.WriteFile(config.Path(), []byte(updated), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("switched to %s\n", connName)

	default:
		fmt.Fprintf(os.Stderr, "unknown config command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: jacques config <list|init|use|set|path>")
		os.Exit(1)
	}
}

func replaceFieldInConfig(content, connName, field, value string) string {
	lines := strings.Split(content, "\n")
	inTargetBlock := false
	braceDepth := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.Contains(trimmed, fmt.Sprintf("%q", connName)) && strings.HasPrefix(trimmed, "connection ") {
			inTargetBlock = true
			if strings.Contains(trimmed, "{") {
				braceDepth = 1
			}
			continue
		}

		if inTargetBlock {
			if strings.Contains(trimmed, "{") {
				braceDepth++
			}
			if strings.Contains(trimmed, "}") {
				braceDepth--
				if braceDepth <= 0 {
					inTargetBlock = false
				}
				continue
			}

			if strings.HasPrefix(trimmed, field) && strings.Contains(trimmed, "=") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = fmt.Sprintf("%s%-8s = %q", indent, field, value)
			}
		}
	}

	return strings.Join(lines, "\n")
}

func replaceTopLevelField(content, field, value string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, field) && strings.Contains(trimmed, "=") {
			lines[i] = fmt.Sprintf("%s = %q", field, value)
			return strings.Join(lines, "\n")
		}
	}
	return fmt.Sprintf("%s = %q\n", field, value) + content
}

// ---------------------------------------------------------------------------
// resolution helpers
// ---------------------------------------------------------------------------

func resolveConnection(connCfg *config.Connection, clusterFlag, dbFlag string) config.Connection {
	var c config.Connection
	if connCfg != nil {
		c = *connCfg
	}
	if c.Type == "" {
		c.Type = "kusto"
	}
	if c.Name == "" {
		c.Name = "(cli)"
	}
	if clusterFlag != "" {
		c.Cluster = clusterFlag
	}
	if c.Cluster == "" {
		c.Cluster = os.Getenv("KUSTO_CLUSTER")
	}
	if dbFlag != "" {
		c.Database = dbFlag
	}
	if c.Database == "" {
		c.Database = os.Getenv("KUSTO_DATABASE")
	}
	return c
}

func resolveValue(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func configFormat(cfg *config.Config) string {
	if cfg.Display != nil && cfg.Display.Format != "" {
		return cfg.Display.Format
	}
	return ""
}
func configTimeCol(cfg *config.Config) string {
	if cfg.Display != nil && cfg.Display.TimeCol != "" {
		return cfg.Display.TimeCol
	}
	return ""
}
func configMsgCol(cfg *config.Config) string {
	if cfg.Display != nil && cfg.Display.MsgCol != "" {
		return cfg.Display.MsgCol
	}
	return ""
}
func configLevelCol(cfg *config.Config) string {
	if cfg.Display != nil && cfg.Display.LevelCol != "" {
		return cfg.Display.LevelCol
	}
	return ""
}

// ---------------------------------------------------------------------------
// preview harness
// ---------------------------------------------------------------------------

func initHarness(cfg *config.Config) harness.PreviewHarness {
	name := ""
	if cfg.Display != nil {
		name = cfg.Display.Harness
	}
	switch name {
	case "nvim":
		h, err := harness.NewNvim()
		if err != nil {
			status("!", "nvim harness: %v", err)
			return nil
		}
		return h
	case "":
		return nil
	default:
		status("!", "unknown harness %q", name)
		return nil
	}
}

func cellToPreview(store data.RowStore, rowIdx, colIdx int) string {
	row, err := store.Row(rowIdx)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	if colIdx >= len(row) {
		return ""
	}
	val := row[colIdx]
	b, err := json.MarshalIndent(val, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", val)
	}
	// Unwrap plain strings so they don't show with quotes
	var s string
	if json.Unmarshal(b, &s) == nil && string(b[0]) == `"` {
		return s
	}
	return string(b)
}

func loadEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			k = strings.TrimSpace(k)
			v = strings.TrimSpace(v)
			if os.Getenv(k) == "" {
				os.Setenv(k, v)
			}
		}
	}
}
