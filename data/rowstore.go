package data

type RowStore interface {
	Columns() []Column
	RowCount() int
	Row(index int) ([]interface{}, error)
	Close() error
}

type SearchMatch struct {
	Row, Col int
}

type Searchable interface {
	RowStore
	Search(query string, cancel <-chan struct{}) ([]SearchMatch, error)
}
