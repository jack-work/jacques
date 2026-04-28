package render

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jokellih/jacques/kusto"
)

func JSON(w io.Writer, result *kusto.QueryResult) {
	if result == nil || len(result.Rows) == 0 {
		fmt.Fprintln(w, "[]")
		return
	}

	records := make([]map[string]interface{}, len(result.Rows))
	for i, row := range result.Rows {
		record := make(map[string]interface{}, len(result.Columns))
		for j, col := range result.Columns {
			if j < len(row) {
				record[col.ColumnName] = row[j]
			}
		}
		records[i] = record
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(records)
}
