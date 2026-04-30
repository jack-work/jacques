package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jokellih/jacques/backend"
	_ "github.com/jokellih/jacques/backend/csv"
	_ "github.com/jokellih/jacques/backend/kusto"
	"github.com/jokellih/jacques/cache"
	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/data"
	"github.com/jokellih/jacques/logging"
	"github.com/jokellih/jacques/render"
)

var buildVersion = "dev"

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
		fmt.Fprintln(os.Stderr, "       jacques config set-token <connection-name>")
		os.Exit(1)
	}

	logging.Info(ctx, "jacques starting",
		logging.String("format", fmtStr),
		logging.String("connection", resolved.Name),
		logging.String("type", resolved.Type),
	)

	// Check cache
	useCache := !*noCache
	var store data.RowStore
	if useCache && !*refresh {
		if cached, ok := cache.Get(ctx, resolved.Name, kql); ok {
			store = cached
			logging.Info(ctx, "serving from cache",
				logging.Int("rows", store.RowCount()),
			)
		}
	}

	// Cache miss — query backend
	if store == nil {
		be, err := backend.New(resolved)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		defer be.Close()

		store, err = be.Query(ctx, kql)
		if err != nil {
			logging.Error(ctx, "query failed", logging.String("error", err.Error()))
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}

		if useCache {
			if err := cache.Put(ctx, resolved.Name, kql, store); err != nil {
				logging.Warn(ctx, "failed to cache result", logging.String("error", err.Error()))
			}
		}
	}
	defer store.Close()

	logging.Info(ctx, "query returned",
		logging.Int("columns", len(store.Columns())),
		logging.Int("rows", store.RowCount()),
	)

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
// cache subcommand
// ---------------------------------------------------------------------------

func handleCache(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jacques cache <list|clear|path>")
		os.Exit(1)
	}

	switch args[0] {
	case "path":
		fmt.Println(cache.Dir())

	case "list":
		entries, err := cache.List()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(entries) == 0 {
			fmt.Fprintln(os.Stderr, "cache is empty")
			return
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "CONN\tROWS\tCOLS\tSIZE\tAGE\tQUERY")
		fmt.Fprintln(tw, "----\t----\t----\t----\t---\t-----")
		for _, e := range entries {
			age := time.Since(e.Timestamp).Truncate(time.Second)
			query := e.Query
			if len(query) > 60 {
				query = query[:59] + "…"
			}
			query = strings.ReplaceAll(query, "\n", " ")
			query = strings.ReplaceAll(query, "\r", "")
			fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\t%s\n",
				e.Conn, e.Rows, e.Cols, humanSize(e.SizeBytes), age, query)
		}
		tw.Flush()

	case "clear":
		if err := cache.Clear(); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("cache cleared")

	default:
		fmt.Fprintf(os.Stderr, "unknown cache command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: jacques cache <list|clear|path>")
		os.Exit(1)
	}
}

func humanSize(bytes int64) string {
	switch {
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}

// ---------------------------------------------------------------------------
// config subcommand
// ---------------------------------------------------------------------------

func handleConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jacques config <list|init|set|set-token|path>")
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

		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "DEFAULT\tNAME\tTYPE\tCLUSTER\tDATABASE\tTOKEN")
		fmt.Fprintln(tw, "-------\t----\t----\t-------\t--------\t-----")
		for _, c := range cfg.Connections {
			def := ""
			if c.Name == cfg.DefaultConnection {
				def = "*"
			}
			tokenStatus := "missing"
			if c.Token != "" {
				tokenStatus = "set (" + fmt.Sprintf("%d chars", len(c.Token)) + ")"
			}
			endpoint := c.Cluster
			if c.Path != "" {
				endpoint = c.Path
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				def, c.Name, c.Type, endpoint, c.Database, tokenStatus)
		}
		tw.Flush()

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

		validFields := map[string]bool{"cluster": true, "database": true, "token": true, "path": true}
		if !validFields[field] {
			fmt.Fprintf(os.Stderr, "error: unknown field %q (valid: cluster, database, token, path)\n", field)
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

	case "set-token":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: jacques config set-token <connection-name>")
			fmt.Fprintln(os.Stderr, "  reads token from stdin or KUSTO_TOKEN env var")
			os.Exit(1)
		}
		connName := args[1]
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		c := cfg.FindConnection(connName)
		if c == nil {
			fmt.Fprintf(os.Stderr, "error: connection %q not found\n", connName)
			os.Exit(1)
		}

		token := os.Getenv("KUSTO_TOKEN")
		if token == "" {
			fmt.Fprint(os.Stderr, "paste token: ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				token = strings.TrimSpace(scanner.Text())
			}
		}
		if token == "" {
			fmt.Fprintln(os.Stderr, "error: no token provided")
			os.Exit(1)
		}

		// Rewrite the config file with the updated token
		raw, err := os.ReadFile(config.Path())
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading config: %v\n", err)
			os.Exit(1)
		}
		updated := replaceFieldInConfig(string(raw), connName, "token", token)
		if err := os.WriteFile(config.Path(), []byte(updated), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("token updated for connection %q in %s\n", connName, config.Path())

	default:
		fmt.Fprintf(os.Stderr, "unknown config command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: jacques config <list|init|set|set-token|path>")
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
	if c.Token == "" {
		c.Token = os.Getenv("KUSTO_TOKEN")
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
