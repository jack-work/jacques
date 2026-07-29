package csv

import (
	"context"
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jack-work/jacques/backend"
	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/data"
)

func init() {
	backend.Register("csv", New)
}

type Backend struct {
	path string
	name string
}

func New(conn config.Connection) (backend.Backend, error) {
	if conn.Path == "" {
		return nil, fmt.Errorf("csv connection %q: no path configured", conn.Name)
	}
	return &Backend{path: conn.Path, name: conn.Name}, nil
}

func (b *Backend) Name() string { return b.name }
func (b *Backend) Close() error { return nil }

func (b *Backend) Query(_ context.Context, query string) (data.RowStore, error) {
	path := b.path
	if query != "" && query != "*" {
		if _, err := os.Stat(query); err == nil {
			path = query
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open csv: %w", err)
	}
	defer f.Close()

	reader := csv.NewReader(f)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read csv: %w", err)
	}

	if len(records) == 0 {
		return data.NewMemoryStore(nil, nil), nil
	}

	headers := records[0]
	dataRows := records[1:]

	columns := make([]data.Column, len(headers))
	colTypes := inferTypes(headers, dataRows)
	for i, h := range headers {
		columns[i] = data.Column{Name: strings.TrimSpace(h), Type: colTypes[i]}
	}

	rows := make([][]interface{}, len(dataRows))
	for r, record := range dataRows {
		row := make([]interface{}, len(columns))
		for c := 0; c < len(columns) && c < len(record); c++ {
			row[c] = coerce(record[c], colTypes[c])
		}
		rows[r] = row
	}

	return data.NewMemoryStore(columns, rows), nil
}

func inferTypes(headers []string, rows [][]string) []string {
	types := make([]string, len(headers))
	for c := range headers {
		types[c] = inferColumn(rows, c)
	}
	return types
}

func inferColumn(rows [][]string, col int) string {
	allInt, allFloat := true, true
	for _, row := range rows {
		if col >= len(row) || row[col] == "" {
			continue
		}
		v := row[col]
		if _, err := strconv.ParseInt(v, 10, 64); err != nil {
			allInt = false
		}
		if _, err := strconv.ParseFloat(v, 64); err != nil {
			allFloat = false
		}
	}
	if allInt {
		return "long"
	}
	if allFloat {
		return "real"
	}
	return "string"
}

func coerce(val, colType string) interface{} {
	if val == "" {
		return nil
	}
	switch colType {
	case "long":
		if v, err := strconv.ParseInt(val, 10, 64); err == nil {
			return float64(v)
		}
	case "real":
		if v, err := strconv.ParseFloat(val, 64); err == nil {
			return v
		}
	}
	return val
}
