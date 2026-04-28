package kusto

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"github.com/jokellih/jacques/logging"
)

type Client struct {
	Cluster    string
	Database   string
	Token      string
	HTTPClient *http.Client
}

func NewClient(cluster, database, token string) *Client {
	return &Client{
		Cluster:  cluster,
		Database: database,
		Token:    token,
		HTTPClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
	}
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
	ServerTimeout        string `json:"servertimeout"`
	QueryConsistency     string `json:"queryconsistency"`
	QueryLanguage        string `json:"query_language"`
	RequestReadonly      bool   `json:"request_readonly"`
	RequestReadonlyHard  bool   `json:"request_readonly_hardline"`
}

// V2 response is an array of frames. We care about DataTable frames.
type v2Frame struct {
	FrameType string          `json:"FrameType"`
	TableName string          `json:"TableName"`
	Columns   []Column        `json:"Columns"`
	Rows      [][]interface{} `json:"Rows"`
}

type Column struct {
	ColumnName string `json:"ColumnName"`
	ColumnType string `json:"ColumnType"`
}

type QueryResult struct {
	Columns []Column
	Rows    [][]interface{}
}

var tracer = otel.Tracer("jacques/kusto")

func (c *Client) Query(kql string) (*QueryResult, error) {
	return c.QueryContext(context.Background(), kql)
}

func (c *Client) QueryContext(ctx context.Context, kql string) (*QueryResult, error) {
	ctx, span := tracer.Start(ctx, "kusto.Query")
	defer span.End()

	logging.Info(ctx, "executing kusto query",
		logging.String("cluster", c.Cluster),
		logging.String("database", c.Database),
		logging.String("kql", kql),
	)

	result, err := c.doQuery(ctx, kql)
	if err != nil {
		logging.Error(ctx, "kusto query failed", logging.String("error", err.Error()))
		return nil, err
	}

	logging.Info(ctx, "kusto query completed",
		logging.Int("columns", len(result.Columns)),
		logging.Int("rows", len(result.Rows)),
	)
	return result, nil
}

func (c *Client) doQuery(ctx context.Context, kql string) (*QueryResult, error) {
	body := queryRequest{
		DB:  c.Database,
		CSL: kql,
		Properties: queryProperties{
			Options: queryOptions{
				ServerTimeout:       "01:00:00",
				QueryConsistency:    "strongconsistency",
				QueryLanguage:       "kql",
				RequestReadonly:     false,
				RequestReadonlyHard: false,
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
		return nil, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	logging.Info(ctx, "kusto HTTP response",
		logging.Int("status", resp.StatusCode),
		logging.Int("body_bytes", len(respBody)),
	)

	if resp.StatusCode != http.StatusOK {
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
	for i, f := range frames {
		logging.Debugf(ctx, "frame[%d]: type=%s table=%s cols=%d rows=%d",
			i, f.FrameType, f.TableName, len(f.Columns), len(f.Rows))
	}

	for _, f := range frames {
		if f.FrameType == "DataTable" && f.TableName == "PrimaryResult" {
			return &QueryResult{
				Columns: f.Columns,
				Rows:    f.Rows,
			}, nil
		}
	}

	for _, f := range frames {
		if f.FrameType == "DataTable" && len(f.Rows) > 0 {
			return &QueryResult{
				Columns: f.Columns,
				Rows:    f.Rows,
			}, nil
		}
	}

	logging.Warn(ctx, "no DataTable with rows found in response")
	return &QueryResult{}, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
