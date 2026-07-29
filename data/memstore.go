package data

import (
	"fmt"
	"strings"
)

type MemoryStore struct {
	columns []Column
	rows    [][]interface{}
}

func NewMemoryStore(columns []Column, rows [][]interface{}) *MemoryStore {
	return &MemoryStore{columns: columns, rows: rows}
}

func (m *MemoryStore) Columns() []Column   { return m.columns }
func (m *MemoryStore) RowCount() int        { return len(m.rows) }
func (m *MemoryStore) Close() error         { return nil }

func (m *MemoryStore) Row(index int) ([]interface{}, error) {
	if index < 0 || index >= len(m.rows) {
		return nil, fmt.Errorf("row index %d out of range [0, %d)", index, len(m.rows))
	}
	return m.rows[index], nil
}

func (m *MemoryStore) Search(query string, cancel <-chan struct{}) ([]SearchMatch, error) {
	q := strings.ToLower(query)
	var matches []SearchMatch
	for r, row := range m.rows {
		select {
		case <-cancel:
			return matches, nil
		default:
		}
		for c, val := range row {
			if val == nil {
				continue
			}
			if strings.Contains(strings.ToLower(fmt.Sprintf("%v", val)), q) {
				matches = append(matches, SearchMatch{Row: r, Col: c})
			}
		}
	}
	return matches, nil
}
