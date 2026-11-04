package kusto

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheckTimeout(t *testing.T) {
	for _, d := range []time.Duration{time.Second, 20 * time.Minute, time.Hour} {
		if err := CheckTimeout(d); err != nil {
			t.Errorf("CheckTimeout(%s) = %v, want nil", d, err)
		}
	}
	for _, d := range []time.Duration{0, -time.Second, time.Hour + time.Second, 2 * time.Hour} {
		err := CheckTimeout(d)
		if err == nil {
			t.Errorf("CheckTimeout(%s) = nil, want refusal", d)
			continue
		}
		if d > MaxTimeout && !strings.Contains(err.Error(), "maximum servertimeout of 1h") {
			t.Errorf("CheckTimeout(%s) = %q, want it to name the 1h maximum", d, err)
		}
	}
}

func TestFormatTimespan(t *testing.T) {
	cases := map[time.Duration]string{
		5 * time.Minute:         "00:05:00",
		time.Hour:               "01:00:00",
		90 * time.Second:        "00:01:30",
		1500 * time.Millisecond: "00:00:01.500",
		20 * time.Minute:        "00:20:00",
	}
	for d, want := range cases {
		if got := formatTimespan(d); got != want {
			t.Errorf("formatTimespan(%s) = %q, want %q", d, got, want)
		}
	}
}

// serve answers every query with body after delay, and records servertimeout.
func serve(t *testing.T, status int, body string, delay time.Duration, gotTimeout *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req queryRequest
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &req)
		if gotTimeout != nil {
			*gotTimeout = req.Properties.Options.ServerTimeout
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestTimeoutSentAsServerTimeout(t *testing.T) {
	var got string
	srv := serve(t, 200, primaryResultBody, 0, &got)
	c := NewClient(srv.URL, "db", fakeToken)
	c.Timeout = 20 * time.Minute
	if _, err := c.Query("print 1"); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got != "00:20:00" {
		t.Errorf("servertimeout = %q, want 00:20:00", got)
	}
}

func TestOverMaxRefusedBeforeSending(t *testing.T) {
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent = true }))
	defer srv.Close()
	c := NewClient(srv.URL, "db", fakeToken)
	c.Timeout = 2 * time.Hour
	_, err := c.Query("print 1")
	if err == nil || !strings.Contains(err.Error(), "maximum servertimeout of 1h") {
		t.Fatalf("err = %v, want refusal naming the 1h maximum", err)
	}
	if sent {
		t.Error("request was sent despite an over-limit timeout")
	}
}

// TestServerTimeoutInData replays a real Kusto response (captured 2026-11-04
// with servertimeout 5s): HTTP 200, a partial PrimaryResult row, then an
// in-data RequestExecutionTimeout. The partial row must not be returned.
func TestServerTimeoutInData(t *testing.T) {
	body, err := os.ReadFile("testdata/servertimeout_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	srv := serve(t, 200, string(body), 0, nil)
	c := NewClient(srv.URL, "db", fakeToken)
	c.Timeout = 5 * time.Second
	store, err := c.Query("slow")
	if err == nil {
		t.Fatalf("got %d rows from a timed-out query, want an error", store.RowCount())
	}
	for _, want := range []string{"Kusto servertimeout of 5s exceeded", "RequestExecutionTimeout", "-timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}
}

func TestServerTimeoutNon200(t *testing.T) {
	body := `{"error":{"code":"RequestExecutionTimeout","message":"Request is invalid and cannot be executed.","@message":"Query timed out"}}`
	srv := serve(t, 400, body, 0, nil)
	c := NewClient(srv.URL, "db", fakeToken)
	_, err := c.Query("slow")
	if err == nil || !strings.Contains(err.Error(), "Kusto servertimeout of 5m0s exceeded") {
		t.Fatalf("err = %v, want the servertimeout message with the 5m default", err)
	}
}

func TestOtherInDataErrorIsNotATimeout(t *testing.T) {
	body := `[{"FrameType":"DataSetHeader"},
{"FrameType":"DataTable","TableName":"PrimaryResult","Columns":[{"ColumnName":"x","ColumnType":"long"}],
 "Rows":[[1],{"OneApiErrors":[{"error":{"code":"LimitsExceeded","@message":"Query result set has exceeded the internal record count limit"}}]}]},
{"FrameType":"DataSetCompletion","HasErrors":true}]`
	srv := serve(t, 200, body, 0, nil)
	_, err := NewClient(srv.URL, "db", fakeToken).Query("big")
	if err == nil {
		t.Fatal("partial result returned without error")
	}
	if !strings.Contains(err.Error(), "LimitsExceeded") || strings.Contains(err.Error(), "servertimeout") {
		t.Errorf("err = %q, want LimitsExceeded and no timeout blame", err)
	}
}

// TestClientTimeout: a server that never answers trips the client deadline,
// and the same server answering promptly succeeds (the control).
func TestClientTimeout(t *testing.T) {
	saved := clientGrace
	clientGrace = 0
	defer func() { clientGrace = saved }()

	slow := serve(t, 200, primaryResultBody, 5*time.Second, nil)
	c := NewClient(slow.URL, "db", fakeToken)
	c.Timeout = 100 * time.Millisecond
	start := time.Now()
	_, err := c.Query("slow")
	if err == nil {
		t.Fatal("slow query succeeded, want client timeout")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("client waited %s, want ~100ms", elapsed)
	}
	for _, want := range []string{"client timeout", "-timeout 100ms", "query_timeout"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q, want it to contain %q", err, want)
		}
	}

	fast := serve(t, 200, primaryResultBody, 0, nil)
	c = NewClient(fast.URL, "db", fakeToken)
	c.Timeout = 100 * time.Millisecond
	if _, err := c.Query("fast"); err != nil {
		t.Fatalf("control: prompt answer failed: %v", err)
	}
}

// A caller cancelling its own context is not our timeout.
func TestCallerCancelNotBlamedOnTimeout(t *testing.T) {
	slow := serve(t, 200, primaryResultBody, 5*time.Second, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := NewClient(slow.URL, "db", fakeToken).QueryContext(ctx, "slow")
	if err == nil || strings.Contains(err.Error(), "client timeout") {
		t.Fatalf("err = %v, want a plain transport error", err)
	}
}
