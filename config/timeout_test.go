package config

import (
	"testing"
	"time"

	"github.com/hashicorp/hcl/v2/hclsimple"
)

func TestQueryTimeoutDecodes(t *testing.T) {
	var cfg Config
	src := []byte("query_timeout = \"20m\"\nconnection \"kusto\" \"k\" {\n  cluster = \"https://x\"\n}\n")
	if err := hclsimple.Decode("config.hcl", src, nil, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d, err := cfg.Timeout(); err != nil || d != 20*time.Minute {
		t.Fatalf("Timeout() = %s, %v; want 20m", d, err)
	}
	if d, err := (&Config{}).Timeout(); err != nil || d != 0 {
		t.Fatalf("unset Timeout() = %s, %v; want 0", d, err)
	}
}

func TestDefaultConfigDecodes(t *testing.T) {
	var cfg Config
	if err := hclsimple.Decode("config.hcl", []byte(defaultConfig), nil, &cfg); err != nil {
		t.Fatalf("default config does not decode: %v", err)
	}
}
