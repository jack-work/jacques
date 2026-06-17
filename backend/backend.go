package backend

import (
	"context"
	"fmt"

	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/data"
)

type Backend interface {
	Name() string
	Query(ctx context.Context, query string) (data.RowStore, error)
	Close() error
}

type Constructor func(conn config.Connection) (Backend, error)

var registry = map[string]Constructor{}

func Register(connType string, ctor Constructor) {
	registry[connType] = ctor
}

func New(conn config.Connection) (Backend, error) {
	ctor, ok := registry[conn.Type]
	if !ok {
		return nil, fmt.Errorf("unknown backend type %q (available: %s)", conn.Type, available())
	}
	return ctor(conn)
}

func available() string {
	var names []string
	for k := range registry {
		names = append(names, k)
	}
	if len(names) == 0 {
		return "none"
	}
	return fmt.Sprintf("%v", names)
}
