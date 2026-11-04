package main

import (
	"strings"
	"testing"
	"time"

	"github.com/jack-work/jacques/config"
)

func TestResolveTimeout(t *testing.T) {
	cases := []struct {
		name    string
		flag    time.Duration
		cfg     string
		want    time.Duration
		wantErr string
	}{
		{name: "unset keeps client default", want: 0},
		{name: "config", cfg: "20m", want: 20 * time.Minute},
		{name: "flag beats config", flag: 30 * time.Minute, cfg: "20m", want: 30 * time.Minute},
		{name: "flag above 1h refused", flag: 2 * time.Hour, wantErr: "maximum servertimeout of 1h"},
		{name: "config above 1h refused", cfg: "90m", wantErr: "maximum servertimeout of 1h"},
		{name: "bad config", cfg: "twenty", wantErr: "query_timeout"},
		{name: "negative flag refused", flag: -time.Minute, wantErr: "must be positive"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveTimeout(tc.flag, &config.Config{QueryTimeout: tc.cfg})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %s, %v; want %s", got, err, tc.want)
			}
		})
	}
}
