package render

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jack-work/jacques/data"
)

func JSON(w io.Writer, store data.RowStore) {
	cols := store.Columns()
	if store.RowCount() == 0 {
		fmt.Fprintln(w, "[]")
		return
	}

	records := make([]map[string]interface{}, store.RowCount())
	for i := 0; i < store.RowCount(); i++ {
		row, _ := store.Row(i)
		record := make(map[string]interface{}, len(cols))
		for j, col := range cols {
			if j < len(row) {
				record[col.Name] = row[j]
			}
		}
		records[i] = record
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(records)
}
