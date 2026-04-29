package render

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jokellih/jacques/data"
)

type TableOptions struct {
	MaxColWidth  int
	MaxRows      int
	TimeFormat   string
	ShowRowNums  bool
}

func DefaultOptions() TableOptions {
	return TableOptions{
		MaxColWidth: 80,
		MaxRows:     0,
		TimeFormat:  "2006-01-02 15:04:05",
		ShowRowNums: false,
	}
}

func Table(w io.Writer, result *data.Result, opts TableOptions) {
	if result == nil || len(result.Columns) == 0 {
		fmt.Fprintln(w, "(no results)")
		return
	}

	rows := result.Rows
	if opts.MaxRows > 0 && len(rows) > opts.MaxRows {
		rows = rows[:opts.MaxRows]
	}

	headers := make([]string, len(result.Columns))
	colTypes := make([]string, len(result.Columns))
	for i, c := range result.Columns {
		headers[i] = c.Name
		colTypes[i] = c.Type
	}

	cells := make([][]string, len(rows))
	for r, row := range rows {
		cells[r] = make([]string, len(result.Columns))
		for c, val := range row {
			cells[r][c] = formatValue(val, colTypes[c], opts)
		}
	}

	widths := computeWidths(headers, cells, opts.MaxColWidth)

	writeRow(w, headers, widths)
	writeSep(w, widths)
	for _, row := range cells {
		writeRow(w, row, widths)
	}

	fmt.Fprintf(w, "\n(%d rows)\n", len(rows))
}

func formatValue(val interface{}, colType string, opts TableOptions) string {
	if val == nil {
		return ""
	}

	switch colType {
	case "datetime":
		if s, ok := val.(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
				return t.Format(opts.TimeFormat)
			}
			if t, err := time.Parse(time.RFC3339, s); err == nil {
				return t.Format(opts.TimeFormat)
			}
		}
	case "long", "int":
		if f, ok := val.(float64); ok {
			return fmt.Sprintf("%d", int64(f))
		}
	case "real", "decimal":
		if f, ok := val.(float64); ok {
			return fmt.Sprintf("%.4f", f)
		}
	case "bool":
		if b, ok := val.(bool); ok {
			if b {
				return "true"
			}
			return "false"
		}
	case "dynamic":
		if m, ok := val.(map[string]interface{}); ok {
			return formatJSON(m)
		}
		if a, ok := val.([]interface{}); ok {
			return formatJSON(a)
		}
	}

	return fmt.Sprintf("%v", val)
}

func formatJSON(v interface{}) string {
	switch t := v.(type) {
	case map[string]interface{}:
		parts := make([]string, 0, len(t))
		for k, val := range t {
			parts = append(parts, fmt.Sprintf("%s=%v", k, val))
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case []interface{}:
		parts := make([]string, len(t))
		for i, val := range t {
			parts[i] = fmt.Sprintf("%v", val)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	default:
		return fmt.Sprintf("%v", v)
	}
}

func computeWidths(headers []string, cells [][]string, maxWidth int) []int {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = len(h)
	}
	for _, row := range cells {
		for i, cell := range row {
			if len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for i := range widths {
		if maxWidth > 0 && widths[i] > maxWidth {
			widths[i] = maxWidth
		}
	}
	return widths
}

func writeRow(w io.Writer, cells []string, widths []int) {
	for i, cell := range cells {
		if i > 0 {
			fmt.Fprint(w, " | ")
		}
		display := cell
		if len(display) > widths[i] {
			display = display[:widths[i]-1] + "…"
		}
		fmt.Fprintf(w, "%-*s", widths[i], display)
	}
	fmt.Fprintln(w)
}

func writeSep(w io.Writer, widths []int) {
	for i, width := range widths {
		if i > 0 {
			fmt.Fprint(w, "-+-")
		}
		fmt.Fprint(w, strings.Repeat("-", width))
	}
	fmt.Fprintln(w)
}
