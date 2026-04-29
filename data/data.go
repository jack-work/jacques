package data

type Column struct {
	Name string
	Type string
}

type Result struct {
	Columns []Column
	Rows    [][]interface{}
}
