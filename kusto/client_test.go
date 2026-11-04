package kusto

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/exporters/stdout/stdoutlog"
	"go.opentelemetry.io/otel/log/global"
	sdklog "go.opentelemetry.io/otel/sdk/log"
)

// fakeToken looks like a bearer credential so the leak assertions are meaningful.
const fakeToken = "eyJhbGciOiJub25lIn0.eyJhdWQiOiJodHRwczovL2hlbHAua3VzdG8ud2luZG93cy5uZXQifQ.c2lnbmF0dXJlLXBsYWNlaG9sZGVy"

const primaryResultBody = `[
  {"FrameType":"DataSetHeader","Version":"v2.0"},
  {"FrameType":"DataTable","TableName":"PrimaryResult","Columns":[{"ColumnName":"health","ColumnType":"long"}],"Rows":[[1]]},
  {"FrameType":"DataSetCompletion"}
]`

// captureOutput redirects os.Stderr and the OTel logger to buffers for the
// duration of fn, then returns everything both of them received.
func captureOutput(t *testing.T, fn func()) string {
	t.Helper()

	var logBuf bytes.Buffer
	exporter, err := stdoutlog.New(stdoutlog.WithWriter(&logBuf))
	if err != nil {
		t.Fatalf("stdoutlog.New: %v", err)
	}
	provider := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(exporter)))
	previous := global.GetLoggerProvider()
	global.SetLoggerProvider(provider)
	defer func() {
		_ = provider.Shutdown(context.Background())
		global.SetLoggerProvider(previous)
	}()

	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	originalStderr := os.Stderr
	os.Stderr = write

	done := make(chan string, 1)
	go func() {
		var stderrBuf bytes.Buffer
		_, _ = io.Copy(&stderrBuf, read)
		done <- stderrBuf.String()
	}()

	fn()

	os.Stderr = originalStderr
	_ = write.Close()
	stderr := <-done
	_ = read.Close()

	return stderr + logBuf.String()
}

func TestQueryRequestContract(t *testing.T) {
	var (
		gotMethod string
		gotPath   string
		gotHeader http.Header
		gotBody   map[string]any
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotHeader = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &gotBody); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, primaryResultBody)
	}))
	defer server.Close()

	client := NewClient(server.URL, "MyDatabase", fakeToken)
	store, err := client.QueryContext(context.Background(), "print health=1")
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	defer store.Close()

	if store.RowCount() != 1 || len(store.Columns()) != 1 {
		t.Errorf("store rows=%d cols=%d, want 1/1", store.RowCount(), len(store.Columns()))
	}
	if gotMethod != http.MethodPost {
		t.Errorf("method = %s, want POST", gotMethod)
	}
	if gotPath != "/v2/rest/query" {
		t.Errorf("path = %s, want /v2/rest/query", gotPath)
	}
	if got := gotHeader.Get("x-ms-app"); got != "jacques" {
		t.Errorf("x-ms-app = %q, want jacques", got)
	}
	if got := gotHeader.Get("Authorization"); got != "Bearer "+fakeToken {
		t.Errorf("Authorization header missing or malformed (redacted comparison failed)")
	}
	if got := gotHeader.Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", got)
	}

	if got := gotBody["db"]; got != "MyDatabase" {
		t.Errorf("db = %v, want MyDatabase", got)
	}
	if got := gotBody["csl"]; got != "print health=1" {
		t.Errorf("csl = %v, want the submitted query", got)
	}
	properties, ok := gotBody["properties"].(map[string]any)
	if !ok {
		t.Fatalf("properties missing: %#v", gotBody)
	}
	options, ok := properties["Options"].(map[string]any)
	if !ok {
		t.Fatalf("Options missing: %#v", properties)
	}
	if got := options["request_readonly_hardline"]; got != true {
		t.Errorf("request_readonly_hardline = %v, want true", got)
	}
	if got := options["queryconsistency"]; got != "strongconsistency" {
		t.Errorf("queryconsistency = %v, want strongconsistency", got)
	}
	if got := options["servertimeout"]; got != "00:05:00" {
		t.Errorf("servertimeout = %v, want the 5m default 00:05:00", got)
	}
	if got := options["query_language"]; got != "kql" {
		t.Errorf("query_language = %v, want kql", got)
	}
}

func TestNonOKResponseIsBoundedAndSanitized(t *testing.T) {
	longBody := "kusto error detail " + strings.Repeat("x", 4000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, longBody)
	}))
	defer server.Close()

	client := NewClient(server.URL, "MyDatabase", fakeToken)

	var err error
	output := captureOutput(t, func() {
		_, err = client.QueryContext(context.Background(), "print health=1")
	})
	if err == nil {
		t.Fatal("expected an error for a non-200 response")
	}
	message := err.Error()
	if !strings.Contains(message, "400") {
		t.Errorf("error %q does not report the status code", message)
	}
	if len(message) > 600 {
		t.Errorf("error length = %d, want a bounded excerpt", len(message))
	}
	if strings.Contains(message, longBody) {
		t.Error("error contains the full response body instead of an excerpt")
	}
	assertNoBearerMaterial(t, message)
	assertNoBearerMaterial(t, output)
}

func TestSuccessfulQueryLeaksNoBearerMaterial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, primaryResultBody)
	}))
	defer server.Close()

	client := NewClient(server.URL, "MyDatabase", fakeToken)

	var queryErr error
	output := captureOutput(t, func() {
		store, err := client.QueryContext(context.Background(), "print health=1")
		queryErr = err
		if err == nil {
			store.Close()
		}
	})
	if queryErr != nil {
		t.Fatalf("QueryContext: %v", queryErr)
	}
	assertNoBearerMaterial(t, output)
}

func assertNoBearerMaterial(t *testing.T, text string) {
	t.Helper()
	if strings.Contains(text, fakeToken) {
		t.Error("bearer token leaked into stderr or the log")
	}
	if strings.Contains(strings.ToLower(text), "bearer ") {
		t.Error("an Authorization value reached stderr or the log")
	}
}
