package kusto

import (
	"context"
	"fmt"

	"github.com/jokellih/jacques/backend"
	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/data"
	kustohttp "github.com/jokellih/jacques/kusto"
)

func init() {
	backend.Register("kusto", New)
}

type Backend struct {
	client *kustohttp.Client
	name   string
}

func New(conn config.Connection) (backend.Backend, error) {
	if conn.Token == "" {
		return nil, fmt.Errorf("kusto connection %q: no token available (configure tenant_id, client_id, scopes in config)", conn.Name)
	}
	if conn.Cluster == "" {
		return nil, fmt.Errorf("kusto connection %q: no cluster URL configured", conn.Name)
	}
	client := kustohttp.NewClient(conn.Cluster, conn.Database, conn.Token)
	return &Backend{client: client, name: conn.Name}, nil
}

func (b *Backend) Name() string { return b.name }

func (b *Backend) Query(ctx context.Context, query string) (data.RowStore, error) {
	return b.client.QueryContext(ctx, query)
}

func (b *Backend) Close() error { return nil }
