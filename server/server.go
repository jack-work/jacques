package server

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jack-work/jacques/auth"
	"github.com/jack-work/jacques/backend"
	"github.com/jack-work/jacques/cache"
	"github.com/jack-work/jacques/config"
	"github.com/jack-work/jacques/data"
	"github.com/jack-work/jacques/render"
)

type Server struct {
	cfg   *config.Config
	webFS embed.FS
	cache *cache.DuckCache
	mu    sync.Mutex
}

type queryResult struct {
	Columns []data.Column   `json:"columns"`
	Rows    [][]interface{} `json:"rows"`
	Cells   [][]string      `json:"cells"`
	Conn    string          `json:"connection"`
	Query   string          `json:"query"`
	Elapsed string          `json:"elapsed"`
	Cached  bool            `json:"cached"`
	Error   string          `json:"error,omitempty"`
}

func New(cfg *config.Config, webFS embed.FS) *Server {
	dc, err := cache.NewDuckCache()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[jacques-web] cache unavailable: %v\n", err)
	}
	return &Server{cfg: cfg, webFS: webFS, cache: dc}
}

func (s *Server) ListenAndServe(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/connections", s.handleConnections)
	mux.HandleFunc("POST /api/query", s.handleQuery)
	stat, _ := fs.Sub(s.webFS, "webui/static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(stat))))
	mux.HandleFunc("GET /", s.handleIndex)

	fmt.Fprintf(os.Stderr, "jacques-web listening on http://localhost:%d\n", port)
	return http.ListenAndServe(fmt.Sprintf(":%d", port), mux)
}

func (s *Server) Close() {
	if s.cache != nil {
		s.cache.Close()
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	f, err := s.webFS.Open("webui/templates/index.html")
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.Copy(w, f)
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request) {
	type connInfo struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		Cluster  string `json:"cluster,omitempty"`
		Database string `json:"database,omitempty"`
		Current  bool   `json:"current"`
	}
	var out []connInfo
	for _, c := range s.cfg.Connections {
		out = append(out, connInfo{
			Name:     c.Name,
			Type:     c.Type,
			Cluster:  c.Cluster,
			Database: c.Database,
			Current:  c.Name == s.cfg.CurrentConnection,
		})
	}
	writeJSON(w, out)
}

func (s *Server) handleQuery(w http.ResponseWriter, r *http.Request) {
	defer func() {
		if rec := recover(); rec != nil {
			fmt.Fprintf(os.Stderr, "[jacques-web] panic in query handler: %v\n", rec)
			writeJSON(w, queryResult{Error: fmt.Sprintf("internal error: %v", rec)})
		}
	}()

	var req struct {
		Connection string `json:"connection"`
		Query      string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, queryResult{Error: "invalid request: " + err.Error()})
		return
	}
	if req.Query == "" {
		writeJSON(w, queryResult{Error: "query is required"})
		return
	}

	fmt.Fprintf(os.Stderr, "[jacques-web] query on %q: %s\n", req.Connection, truncateLog(req.Query, 100))

	connCfg := s.cfg.FindConnection(req.Connection)
	if connCfg == nil {
		connCfg = s.cfg.DefaultConn()
	}
	if connCfg == nil {
		writeJSON(w, queryResult{Error: "no connection configured"})
		return
	}
	resolved := *connCfg
	ctx := context.Background()

	// check cache
	if s.cache != nil {
		if store, ok := s.cache.Get(ctx, resolved.Name, req.Query); ok {
			fmt.Fprintf(os.Stderr, "[jacques-web] cache hit: %d rows\n", store.RowCount())
			result := buildResult(store, resolved.Name, req.Query, 0)
			result.Cached = true
			store.Close()
			writeJSON(w, result)
			return
		}
	}

	token, err := auth.GetToken(ctx, resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[jacques-web] auth error: %v\n", err)
		writeJSON(w, queryResult{Error: "auth failed: " + err.Error()})
		return
	}
	resolved.Token = token

	be, err := backend.New(resolved)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[jacques-web] backend error: %v\n", err)
		writeJSON(w, queryResult{Error: "backend error: " + err.Error()})
		return
	}
	defer be.Close()

	start := time.Now()
	store, err := be.Query(ctx, req.Query)
	elapsed := time.Since(start)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[jacques-web] query error (%s): %v\n", elapsed.Truncate(time.Millisecond), err)
		writeJSON(w, queryResult{Error: "query failed: " + err.Error()})
		return
	}

	result := buildResult(store, resolved.Name, req.Query, elapsed)
	fmt.Fprintf(os.Stderr, "[jacques-web] OK: %d rows in %s\n", len(result.Rows), elapsed.Truncate(time.Millisecond))

	// write to cache
	if s.cache != nil {
		if err := s.cache.Put(ctx, resolved.Name, req.Query, store); err != nil {
			fmt.Fprintf(os.Stderr, "[jacques-web] cache put error: %v\n", err)
		}
	}
	store.Close()

	writeJSON(w, result)
}

func buildResult(store data.RowStore, conn, query string, elapsed time.Duration) *queryResult {
	cols := store.Columns()
	opts := render.DefaultOptions()

	rows := make([][]interface{}, store.RowCount())
	cells := make([][]string, store.RowCount())
	for r := 0; r < store.RowCount(); r++ {
		row, _ := store.Row(r)
		rows[r] = row
		cells[r] = make([]string, len(cols))
		for c, val := range row {
			colType := ""
			if c < len(cols) {
				colType = cols[c].Type
			}
			cells[r][c] = render.FormatValue(val, colType, opts)
		}
	}

	return &queryResult{
		Columns: cols,
		Rows:    rows,
		Cells:   cells,
		Conn:    conn,
		Query:   query,
		Elapsed: elapsed.Truncate(time.Millisecond).String(),
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "[jacques-web] json encode error: %v\n", err)
	}
}

func truncateLog(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > max {
		return s[:max-1] + "…"
	}
	return s
}
