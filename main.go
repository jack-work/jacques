package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jokellih/jacques/backend"
	_ "github.com/jokellih/jacques/backend/csv"
	_ "github.com/jokellih/jacques/backend/kusto"
	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/logging"
	"github.com/jokellih/jacques/render"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "config" {
		handleConfig(os.Args[2:])
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

	be, err := backend.New(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	defer be.Close()

	store, err := be.Query(ctx, kql)
	if err != nil {
		logging.Error(ctx, "query failed", logging.String("error", err.Error()))
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
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
// config subcommand
// ---------------------------------------------------------------------------

func handleConfig(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: jacques config <list|init|set-token|path>")
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
		updated := replaceTokenInConfig(string(raw), connName, token)
		if err := os.WriteFile(config.Path(), []byte(updated), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing config: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("token updated for connection %q in %s\n", connName, config.Path())

	default:
		fmt.Fprintf(os.Stderr, "unknown config command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, "usage: jacques config <list|init|set-token|path>")
		os.Exit(1)
	}
}

// replaceTokenInConfig finds the connection block by name and replaces its
// token value. This is a simple text replacement — not a full HCL rewrite.
func replaceTokenInConfig(content, connName, newToken string) string {
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

			if strings.HasPrefix(trimmed, "token") && strings.Contains(trimmed, "=") {
				indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
				lines[i] = fmt.Sprintf("%stoken    = %q", indent, newToken)
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
