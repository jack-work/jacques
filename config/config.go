package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

type Config struct {
	CurrentConnection string       `hcl:"current_connection,optional"`
	QueryTimeout      string       `hcl:"query_timeout,optional"` // Go duration, e.g. "20m"; Kusto caps it at 1h
	Connections       []Connection `hcl:"connection,block"`
	Display           *Display     `hcl:"display,block"`
}

type Connection struct {
	Type     string `hcl:"type,label"`
	Name     string `hcl:"name,label"`
	Cluster  string `hcl:"cluster,optional"`
	Database string `hcl:"database,optional"`
	Path     string `hcl:"path,optional"`

	TokenProvider string `hcl:"token_provider,optional"` // "az" or "" (oauth device code)
	TenantID      string `hcl:"tenant_id,optional"`
	ClientID      string `hcl:"client_id,optional"`
	Scopes        string `hcl:"scopes,optional"`

	Token   string        `json:"-"` // runtime only, set by auth package
	Timeout time.Duration `json:"-"` // runtime only, resolved from -timeout / query_timeout
}

type Display struct {
	Format   string `hcl:"format,optional"`
	TimeCol  string `hcl:"time_col,optional"`
	MsgCol   string `hcl:"msg_col,optional"`
	LevelCol string `hcl:"level_col,optional"`
	MaxRows  int    `hcl:"max_rows,optional"`
	Harness  string `hcl:"harness,optional"`
}

func Dir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".jacques"
	}
	return filepath.Join(home, ".jacques")
}

func Path() string {
	return filepath.Join(Dir(), "config.hcl")
}

func Load() (*Config, error) {
	path := Path()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return &Config{}, nil
	}

	var cfg Config
	if err := hclsimple.DecodeFile(path, nil, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &cfg, nil
}

// Timeout parses query_timeout; zero means unset.
func (c *Config) Timeout() (time.Duration, error) {
	if c.QueryTimeout == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(c.QueryTimeout)
	if err != nil {
		return 0, fmt.Errorf("query_timeout %q in %s: %w", c.QueryTimeout, Path(), err)
	}
	return d, nil
}

func (c *Config) FindConnection(name string) *Connection {
	for i := range c.Connections {
		if c.Connections[i].Name == name {
			return &c.Connections[i]
		}
	}
	return nil
}

func (c *Config) DefaultConn() *Connection {
	if c.CurrentConnection != "" {
		return c.FindConnection(c.CurrentConnection)
	}
	if len(c.Connections) > 0 {
		return &c.Connections[0]
	}
	return nil
}

func (c *Config) KustoConnections() []Connection {
	var out []Connection
	for _, conn := range c.Connections {
		if conn.Type == "kusto" {
			out = append(out, conn)
		}
	}
	return out
}

func EnsureDir() error {
	return os.MkdirAll(Dir(), 0o755)
}

func WriteDefault() error {
	if err := EnsureDir(); err != nil {
		return err
	}
	path := Path()
	if _, err := os.Stat(path); err == nil {
		return nil // already exists
	}
	return os.WriteFile(path, []byte(defaultConfig), 0o644)
}

const defaultConfig = `current_connection = "telemetry"

# Kusto query budget (servertimeout and client wait). Default 5m, max 1h.
# query_timeout = "20m"

connection "kusto" "telemetry" {
  cluster        = "https://mycluster.centralus.kusto.windows.net"
  database       = "MyDatabase"
  token_provider = "az"
  scopes         = "https://help.kusto.windows.net/.default"
}

connection "kusto" "logs" {
  cluster        = "https://mycluster-logs.eastus.kusto.windows.net"
  database       = ""
  token_provider = "az"
  scopes         = "https://help.kusto.windows.net/.default"
}

connection "csv" "sample" {
  path = "testdata/sample.csv"
}

display {
  format    = "tui"
  time_col  = "env_time"
  msg_col   = "message"
  level_col = "level"
  max_rows  = 10000
}
`
