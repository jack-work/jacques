package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jokellih/jacques/kusto"
	"github.com/jokellih/jacques/logging"
	"github.com/jokellih/jacques/render"
)

func main() {
	format := flag.String("format", "log", "output format: table, log, json, raw, tui")
	cluster := flag.String("cluster", "", "kusto cluster URL (overrides KUSTO_CLUSTER)")
	database := flag.String("db", "", "database name (overrides KUSTO_DATABASE)")
	maxRows := flag.Int("max-rows", 0, "max rows to display (0 = unlimited)")
	showAll := flag.Bool("all-cols", false, "in log mode, show all columns per entry")
	timeCol := flag.String("time-col", "env_time", "column name for timestamps in log mode")
	msgCol := flag.String("msg-col", "message", "column name for message in log mode")
	levelCol := flag.String("level-col", "level", "column name for log level in log mode")
	extraCols := flag.String("extra-cols", "", "comma-separated extra columns to show in log mode")
	tuiCols := flag.String("cols", "", "comma-separated columns to show in TUI mode (default: all)")
	queryFile := flag.String("f", "", "read query from file")
	flag.Parse()

	loadEnv(".env")

	shutdown, err := logging.Init("jacques")
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to init logging: %v\n", err)
	} else {
		defer shutdown()
	}

	ctx := context.Background()
	logging.Info(ctx, "jacques starting",
		logging.String("format", *format),
	)

	token := os.Getenv("KUSTO_TOKEN")
	if token == "" {
		fmt.Fprintln(os.Stderr, "error: KUSTO_TOKEN not set (add it to .env or export it)")
		os.Exit(1)
	}

	clusterURL := envOrFlag(*cluster, "KUSTO_CLUSTER")
	if clusterURL == "" {
		clusterURL = "https://fdislandsus.centralus.kusto.windows.net"
	}

	db := envOrFlag(*database, "KUSTO_DATABASE")
	if db == "" {
		db = "CAPAnalytics"
	}

	var kql string
	if *queryFile != "" {
		qdata, err := os.ReadFile(*queryFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading query file: %v\n", err)
			os.Exit(1)
		}
		kql = string(qdata)
	} else {
		kql = strings.Join(flag.Args(), " ")
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
		os.Exit(1)
	}

	logging.Info(ctx, "query details",
		logging.String("cluster", clusterURL),
		logging.String("database", db),
		logging.String("kql", kql),
	)

	client := kusto.NewClient(clusterURL, db, token)
	store, err := client.QueryContext(ctx, kql)
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

	switch *format {
	case "table":
		opts := render.DefaultOptions()
		opts.MaxRows = *maxRows
		render.Table(w, store, opts)

	case "log":
		opts := render.DefaultLogOptions()
		opts.TimeColumn = *timeCol
		opts.MessageColumn = *msgCol
		opts.LevelColumn = *levelCol
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
		fmt.Fprintf(os.Stderr, "unknown format: %s\n", *format)
		os.Exit(1)
	}
}

func envOrFlag(flagVal, envKey string) string {
	if flagVal != "" {
		return flagVal
	}
	return os.Getenv(envKey)
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
