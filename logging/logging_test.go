package logging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"JACQUES_LOG_DIR", "APPDATA", "XDG_STATE_HOME"} {
		t.Setenv(k, "")
	}

	if got, want := Dir(), filepath.Join(home, ".local", "state", "jacques"); got != want {
		t.Fatalf("fallback: got %q want %q", got, want)
	}
	t.Setenv("XDG_STATE_HOME", "relative/state") // not absolute: ignored per the XDG spec
	if got := Dir(); !filepath.IsAbs(got) {
		t.Fatalf("a relative XDG_STATE_HOME must not yield a cwd-relative log dir, got %q", got)
	}
	t.Setenv("XDG_STATE_HOME", "/xdg/state")
	if got, want := Dir(), "/xdg/state/jacques"; got != want {
		t.Fatalf("xdg: got %q want %q", got, want)
	}
	t.Setenv("APPDATA", "/appdata")
	if got, want := Dir(), "/appdata/jacques"; got != want {
		t.Fatalf("appdata: got %q want %q", got, want)
	}
	t.Setenv("JACQUES_LOG_DIR", "/explicit")
	if got := Dir(); got != "/explicit" {
		t.Fatalf("explicit: got %q", got)
	}
}

func TestInitNeverWritesCwd(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"JACQUES_LOG_DIR", "APPDATA", "XDG_STATE_HOME", "JACQUES_LOG_LEVEL"} {
		t.Setenv(k, "")
	}
	t.Chdir(cwd)

	shutdown, err := Init("test")
	if err != nil {
		t.Fatal(err)
	}
	Info(context.Background(), "kept")
	Debug(context.Background(), "dropped-at-info")
	shutdown()

	if _, err := os.Stat(filepath.Join(cwd, "jacques")); !os.IsNotExist(err) {
		t.Fatalf("log directory created in the working directory (err=%v)", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".local", "state", "jacques", "jacques.log"))
	if err != nil {
		t.Fatal(err)
	}
	// Positive control: the Info record is there, so absence of the Debug
	// record is the filter and not a log that was never written.
	if !strings.Contains(string(data), "kept") {
		t.Fatal("info record missing")
	}
	if strings.Contains(string(data), "dropped-at-info") {
		t.Fatal("debug record written at the default level")
	}

	t.Setenv("JACQUES_LOG_LEVEL", "debug")
	shutdown, err = Init("test")
	if err != nil {
		t.Fatal(err)
	}
	Debug(context.Background(), "kept-at-debug")
	shutdown()
	data, _ = os.ReadFile(filepath.Join(home, ".local", "state", "jacques", "jacques.log"))
	if !strings.Contains(string(data), "kept-at-debug") {
		t.Fatal("JACQUES_LOG_LEVEL=debug must keep debug records")
	}
}
