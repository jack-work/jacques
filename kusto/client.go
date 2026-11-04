package kusto

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jack-work/jacques/data"
	"github.com/jack-work/jacques/logging"
	"go.opentelemetry.io/otel"
)

type Client struct {
	Cluster  string
	Database string
	Token    string
	// Timeout is the query budget: sent to Kusto as servertimeout, and the
	// HTTP client waits that long plus clientGrace. Zero means DefaultTimeout.
	Timeout    time.Duration
	HTTPClient *http.Client
}

func NewClient(cluster, database, token string) *Client {
	return &Client{
		Cluster:    cluster,
		Database:   database,
		Token:      token,
		HTTPClient: &http.Client{},
	}
}

func (c *Client) timeout() time.Duration {
	if c.Timeout == 0 {
		return DefaultTimeout
	}
	return c.Timeout
}

type queryRequest struct {
	DB         string          `json:"db"`
	CSL        string          `json:"csl"`
	Properties queryProperties `json:"properties"`
}

type queryProperties struct {
	Options queryOptions `json:"Options"`
}

type queryOptions struct {
	ServerTimeout       string `json:"servertimeout"`
	QueryConsistency    string `json:"queryconsistency"`
	QueryLanguage       string `json:"query_language"`
	RequestReadonly     bool   `json:"request_readonly"`
	RequestReadonlyHard bool   `json:"request_readonly_hardline"`
}

// v2 wire types — internal to this package
type v2Frame struct {
	FrameType string     `json:"FrameType"`
	TableName string     `json:"TableName"`
	Columns   []v2Column `json:"Columns"`
	// Rows holds arrays of values, but with ErrorReportingPlacement "InData"
	// Kusto appends an object carrying OneApiErrors after a partial result.
	Rows         []json.RawMessage `json:"Rows"`
	OneApiErrors []oneAPIError     `json:"OneApiErrors"`
}

type oneAPIError struct {
	Error struct {
		Code      string `json:"code"`
		Message   string `json:"message"`
		AtMessage string `json:"@message"`
	} `json:"error"`
}

type v2Column struct {
	ColumnName string `json:"ColumnName"`
	ColumnType string `json:"ColumnType"`
}

var tracer = otel.Tracer("jacques/kusto")

func (c *Client) Query(kql string) (data.RowStore, error) {
	return c.QueryContext(context.Background(), kql)
}

func (c *Client) QueryContext(ctx context.Context, kql string) (data.RowStore, error) {
	ctx, span := tracer.Start(ctx, "kusto.Query")
	defer span.End()

	logging.Info(ctx, "executing kusto query",
		logging.String("cluster", c.Cluster),
		logging.String("database", c.Database),
		logging.String("kql", kql),
	)

	store, err := c.doQuery(ctx, kql)
	if err != nil {
		logging.Error(ctx, "kusto query failed", logging.String("error", err.Error()))
		return nil, err
	}

	logging.Info(ctx, "kusto query completed",
		logging.Int("columns", len(store.Columns())),
		logging.Int("rows", store.RowCount()),
	)
	return store, nil
}

func (c *Client) doQuery(ctx context.Context, kql string) (data.RowStore, error) {
	budget := c.timeout()
	if err := CheckTimeout(budget); err != nil {
		return nil, err
	}
	errClientTimeout := errors.New("jacques client timeout")
	ctx, cancel := context.WithTimeoutCause(ctx, budget+clientGrace, errClientTimeout)
	defer cancel()
	// clientDeadline wraps a transport error when our own deadline, not the
	// caller's context, is what ended the request.
	clientDeadline := func(stage string, err error) error {
		if context.Cause(ctx) == errClientTimeout {
			return fmt.Errorf("client timeout: no complete response from Kusto within %s (-timeout %s + %s grace); %s",
				budget+clientGrace, budget, clientGrace, raiseHint)
		}
		return fmt.Errorf("%s: %w", stage, err)
	}

	body := queryRequest{
		DB:  c.Database,
		CSL: kql,
		Properties: queryProperties{
			Options: queryOptions{
				ServerTimeout:       formatTimespan(budget),
				QueryConsistency:    "strongconsistency",
				QueryLanguage:       "kql",
				RequestReadonly:     false,
				RequestReadonlyHard: true,
			},
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := c.Cluster + "/v2/rest/query"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("x-ms-app", "jacques")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, clientDeadline("execute request", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, clientDeadline("read response", err)
	}

	logging.Info(ctx, "kusto HTTP response",
		logging.Int("status", resp.StatusCode),
		logging.Int("body_bytes", len(respBody)),
	)

	if resp.StatusCode != http.StatusOK {
		var e oneAPIError
		if json.Unmarshal(respBody, &e) == nil && e.Error.Code == serverTimeoutCode {
			return nil, serverTimeoutError(budget, []oneAPIError{e})
		}
		return nil, fmt.Errorf("kusto returned %d: %s", resp.StatusCode, truncate(string(respBody), 500))
	}

	var frames []v2Frame
	if err := json.Unmarshal(respBody, &frames); err != nil {
		logging.Error(ctx, "failed to unmarshal response",
			logging.String("error", err.Error()),
			logging.String("body_prefix", truncate(string(respBody), 200)),
		)
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	logging.Info(ctx, "parsed v2 frames", logging.Int("frame_count", len(frames)))
	if errs := frameErrors(frames); len(errs) > 0 {
		for _, e := range errs {
			if e.Error.Code == serverTimeoutCode {
				return nil, serverTimeoutError(budget, errs)
			}
		}
		return nil, fmt.Errorf("kusto query failed: %s", describeErrors(errs))
	}
	for i, f := range frames {
		logging.Debugf(ctx, "frame[%d]: type=%s table=%s cols=%d rows=%d",
			i, f.FrameType, f.TableName, len(f.Columns), len(f.Rows))
	}

	for _, f := range frames {
		if f.FrameType == "DataTable" && f.TableName == "PrimaryResult" {
			return frameToStore(f)
		}
	}

	for _, f := range frames {
		if f.FrameType == "DataTable" && len(f.Rows) > 0 {
			return frameToStore(f)
		}
	}

	logging.Warn(ctx, "no DataTable with rows found in response")
	return data.NewMemoryStore(nil, nil), nil
}

func frameToStore(f v2Frame) (data.RowStore, error) {
	cols := make([]data.Column, len(f.Columns))
	for i, c := range f.Columns {
		cols[i] = data.Column{Name: c.ColumnName, Type: c.ColumnType}
	}
	rows := make([][]interface{}, len(f.Rows))
	for i, raw := range f.Rows {
		if err := json.Unmarshal(raw, &rows[i]); err != nil {
			return nil, fmt.Errorf("unmarshal %s row %d: %w", f.TableName, i, err)
		}
	}
	return data.NewMemoryStore(cols, rows), nil
}

// frameErrors collects every Kusto error in the response: the dataset-level
// OneApiErrors and any in-data error rows. A response carrying any of them
// is partial and must not be returned as a result.
func frameErrors(frames []v2Frame) []oneAPIError {
	var errs []oneAPIError
	for _, f := range frames {
		errs = append(errs, f.OneApiErrors...)
		if f.FrameType != "DataTable" {
			continue
		}
		for _, raw := range f.Rows {
			if len(raw) == 0 || raw[0] != '{' {
				continue
			}
			var row struct {
				OneApiErrors []oneAPIError `json:"OneApiErrors"`
			}
			if json.Unmarshal(raw, &row) == nil {
				errs = append(errs, row.OneApiErrors...)
			}
		}
	}
	return dedupeErrors(errs)
}

func dedupeErrors(errs []oneAPIError) []oneAPIError {
	seen := map[oneAPIError]bool{}
	var out []oneAPIError
	for _, e := range errs {
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out
}

func describeErrors(errs []oneAPIError) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		msg := e.Error.AtMessage
		if msg == "" {
			msg = e.Error.Message
		}
		parts[i] = e.Error.Code + ": " + msg
	}
	return strings.Join(parts, "; ")
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
