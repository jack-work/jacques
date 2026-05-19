package render

import (
	"encoding/json"
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

func Table(w io.Writer, store data.RowStore, opts TableOptions) {
	cols := store.Columns()
	if len(cols) == 0 {
		fmt.Fprintln(w, "(no results)")
		return
	}

	rowCount := store.RowCount()
	if opts.MaxRows > 0 && rowCount > opts.MaxRows {
		rowCount = opts.MaxRows
	}

	headers := make([]string, len(cols))
	colTypes := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Name
		colTypes[i] = c.Type
	}

	cells := make([][]string, rowCount)
	for r := 0; r < rowCount; r++ {
		row, _ := store.Row(r)
		cells[r] = make([]string, len(cols))
		for c, val := range row {
			cells[r][c] = FormatValue(val, colTypes[c], opts)
		}
	}

	widths := computeWidths(headers, cells, opts.MaxColWidth)

	writeRow(w, headers, widths)
	writeSep(w, widths)
	for _, row := range cells {
		writeRow(w, row, widths)
	}

	fmt.Fprintf(w, "\n(%d rows)\n", rowCount)
}

func FormatValue(val interface{}, colType string, opts TableOptions) string {
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

	b, err := json.Marshal(val)
	if err != nil {
		return fmt.Sprintf("%v", val)
	}
	s := string(b)
	if s != "" && s[0] == '"' {
		var unq string
		if json.Unmarshal(b, &unq) == nil {
			return unq
		}
	}
	return s
}

func formatJSON(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

func formatJSONPretty(v interface{}) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
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
