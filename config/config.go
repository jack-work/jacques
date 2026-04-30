package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

type Config struct {
	DefaultConnection string       `hcl:"default_connection,optional"`
	Connections       []Connection `hcl:"connection,block"`
	Display           *Display     `hcl:"display,block"`
}

type Connection struct {
	Type     string `hcl:"type,label"`
	Name     string `hcl:"name,label"`
	Cluster  string `hcl:"cluster,optional"`
	Database string `hcl:"database,optional"`
	Token    string `hcl:"token,optional"`
	Path     string `hcl:"path,optional"`
}

type Display struct {
	Format   string `hcl:"format,optional"`
	TimeCol  string `hcl:"time_col,optional"`
	MsgCol   string `hcl:"msg_col,optional"`
	LevelCol string `hcl:"level_col,optional"`
	MaxRows  int    `hcl:"max_rows,optional"`
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

func (c *Config) FindConnection(name string) *Connection {
	for i := range c.Connections {
		if c.Connections[i].Name == name {
			return &c.Connections[i]
		}
	}
	return nil
}

func (c *Config) DefaultConn() *Connection {
	if c.DefaultConnection != "" {
		return c.FindConnection(c.DefaultConnection)
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

const defaultConfig = `default_connection = "cap-analytics"

connection "kusto" "cap-analytics" {
  cluster  = "https://fdislandsus.centralus.kusto.windows.net"
  database = "CAPAnalytics"
  token    = ""
}

connection "kusto" "nsp-logs" {
  cluster  = "https://nsp-logs-summary.eastus.kusto.windows.net"
  database = ""
  token    = ""
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
