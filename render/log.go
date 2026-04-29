package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jokellih/jacques/data"
)

type LogOptions struct {
	TimeColumn    string
	MessageColumn string
	LevelColumn   string
	ExtraColumns  []string
	TimeFormat    string
	ShowAllCols   bool
}

func DefaultLogOptions() LogOptions {
	return LogOptions{
		TimeColumn:    "env_time",
		MessageColumn: "message",
		LevelColumn:   "level",
		TimeFormat:    "15:04:05.000",
		ShowAllCols:   false,
	}
}

func Log(w io.Writer, store data.RowStore, opts LogOptions) {
	cols := store.Columns()
	if store.RowCount() == 0 {
		fmt.Fprintln(w, "(no results)")
		return
	}

	colIdx := make(map[string]int)
	for i, c := range cols {
		colIdx[c.Name] = i
	}

	if opts.ShowAllCols {
		logAllColumns(w, store, cols, colIdx, opts)
		return
	}

	timeIdx, hasTime := colIdx[opts.TimeColumn]
	msgIdx, hasMsg := colIdx[opts.MessageColumn]
	levelIdx, hasLevel := colIdx[opts.LevelColumn]

	extraIdxs := make([]int, 0, len(opts.ExtraColumns))
	for _, name := range opts.ExtraColumns {
		if idx, ok := colIdx[name]; ok {
			extraIdxs = append(extraIdxs, idx)
		}
	}

	for r := 0; r < store.RowCount(); r++ {
		row, _ := store.Row(r)
		var parts []string

		if hasTime {
			parts = append(parts, formatTime(row[timeIdx], opts.TimeFormat))
		}
		if hasLevel {
			parts = append(parts, formatLevel(row[levelIdx]))
		}
		for _, idx := range extraIdxs {
			parts = append(parts, fmt.Sprintf("[%s=%v]", cols[idx].Name, row[idx]))
		}
		if hasMsg {
			parts = append(parts, toString(row[msgIdx]))
		}

		fmt.Fprintln(w, strings.Join(parts, " "))
	}

	fmt.Fprintf(w, "\n(%d log entries)\n", store.RowCount())
}

func logAllColumns(w io.Writer, store data.RowStore, cols []data.Column, colIdx map[string]int, opts LogOptions) {
	for i := 0; i < store.RowCount(); i++ {
		if i > 0 {
			fmt.Fprintln(w, "---")
		}
		row, _ := store.Row(i)
		for j, col := range cols {
			val := ""
			if j < len(row) {
				val = toString(row[j])
			}
			if val == "" || val == "<nil>" {
				continue
			}
			fmt.Fprintf(w, "  %s: %s\n", col.Name, val)
		}
	}
	fmt.Fprintf(w, "\n(%d log entries)\n", store.RowCount())
}

func formatTime(val interface{}, format string) string {
	if val == nil {
		return "??:??:??"
	}
	if s, ok := val.(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t.Format(format)
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Format(format)
		}
	}
	return fmt.Sprintf("%v", val)
}

func formatLevel(val interface{}) string {
	s := strings.ToUpper(toString(val))
	switch s {
	case "ERROR", "ERR":
		return "[ERR]"
	case "WARNING", "WARN":
		return "[WRN]"
	case "INFORMATION", "INFO":
		return "[INF]"
	case "DEBUG", "DBG":
		return "[DBG]"
	case "VERBOSE", "TRACE":
		return "[TRC]"
	default:
		if s == "" {
			return "[---]"
		}
		return "[" + s + "]"
	}
}

func toString(val interface{}) string {
	if val == nil {
		return ""
	}
	return fmt.Sprintf("%v", val)
}
