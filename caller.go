package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// defaultCaller names the run when neither -caller nor $JACQUES_CALLER is set,
// so usage can be attributed without every caller opting in: the systemd unit
// the process runs under, else the parent process's name.
func defaultCaller() string {
	if v := os.Getenv("JACQUES_CALLER"); v != "" {
		return v
	}
	if b, err := os.ReadFile("/proc/self/cgroup"); err == nil {
		if u := unitFromCgroup(string(b)); u != "" {
			return u
		}
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", os.Getppid())); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return "parent:" + s
		}
	}
	return ""
}

// unitFromCgroup returns the innermost .service or .scope in a cgroup v2 path,
// skipping the per-user manager and login session scopes, which name no caller.
func unitFromCgroup(cg string) string {
	for _, line := range strings.Split(cg, "\n") {
		i := strings.LastIndex(line, ":")
		if i < 0 {
			continue
		}
		parts := strings.Split(line[i+1:], "/")
		for j := len(parts) - 1; j >= 0; j-- {
			p := parts[j]
			if !strings.HasSuffix(p, ".service") && !strings.HasSuffix(p, ".scope") {
				continue
			}
			if strings.HasPrefix(p, "user@") || strings.HasPrefix(p, "session-") || p == "init.scope" {
				continue
			}
			return filepath.Base(p)
		}
	}
	return ""
}
