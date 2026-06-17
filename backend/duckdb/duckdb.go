package duckdb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jack-work/jacques/backend"
	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/data"
	"github.com/jack-work/jacques/logging"

	_ "github.com/duckdb/duckdb-go/v2"
)

func init() {
	backend.Register("duckdb", New)
}

type Backend struct {
	db   *sql.DB
	name string
}

func New(conn config.Connection) (backend.Backend, error) {
	path := conn.Path
	if path == "" {
		path = ":memory:"
	}

	db, err := sql.Open("duckdb", path)
	if err != nil {
		return nil, fmt.Errorf("duckdb open %q: %w", path, err)
	}

	return &Backend{db: db, name: conn.Name}, nil
}

func (b *Backend) Name() string { return b.name }

func (b *Backend) Close() error {
	return b.db.Close()
}

func (b *Backend) Query(ctx context.Context, query string) (data.RowStore, error) {
	logging.Info(ctx, "duckdb query",
		logging.String("backend", b.name),
		logging.String("query", query),
	)

	rows, err := b.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("duckdb query: %w", err)
	}
	defer rows.Close()

	colTypes, err := rows.ColumnTypes()
	if err != nil {
		return nil, fmt.Errorf("duckdb column types: %w", err)
	}

	columns := make([]data.Column, len(colTypes))
	for i, ct := range colTypes {
		columns[i] = data.Column{
			Name: ct.Name(),
			Type: mapDuckDBType(ct.DatabaseTypeName()),
		}
	}

	var resultRows [][]interface{}
	scanDest := make([]interface{}, len(columns))
	for i := range scanDest {
		scanDest[i] = new(interface{})
	}

	for rows.Next() {
		if err := rows.Scan(scanDest...); err != nil {
			return nil, fmt.Errorf("duckdb scan: %w", err)
		}
		row := make([]interface{}, len(columns))
		for i, ptr := range scanDest {
			row[i] = *(ptr.(*interface{}))
		}
		resultRows = append(resultRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("duckdb rows: %w", err)
	}

	logging.Info(ctx, "duckdb query complete",
		logging.Int("columns", len(columns)),
		logging.Int("rows", len(resultRows)),
	)

	return data.NewMemoryStore(columns, resultRows), nil
}

func mapDuckDBType(dbType string) string {
	switch dbType {
	case "BIGINT", "INTEGER", "SMALLINT", "TINYINT", "HUGEINT":
		return "long"
	case "FLOAT", "DOUBLE", "DECIMAL":
		return "real"
	case "BOOLEAN":
		return "bool"
	case "TIMESTAMP", "TIMESTAMP WITH TIME ZONE", "DATE", "TIME":
		return "datetime"
	case "BLOB":
		return "dynamic"
	default:
		return "string"
	}
}
