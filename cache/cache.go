package cache

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jokellih/jacques/config"
	"github.com/jokellih/jacques/data"
	"github.com/jokellih/jacques/logging"
)

type entry struct {
	Columns   []data.Column     `json:"columns"`
	Rows      [][]interface{}   `json:"rows"`
	Query     string            `json:"query"`
	Conn      string            `json:"connection"`
	Timestamp time.Time         `json:"timestamp"`
}

func Dir() string {
	return filepath.Join(config.Dir(), "cache")
}

func cacheKey(connName, query string) string {
	h := sha256.Sum256([]byte(connName + "\x00" + query))
	return fmt.Sprintf("%x", h[:12])
}

func cachePath(key string) string {
	return filepath.Join(Dir(), key+".json.gz")
}

func Get(ctx context.Context, connName, query string) (data.RowStore, bool) {
	key := cacheKey(connName, query)
	path := cachePath(key)

	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, false
	}
	defer gz.Close()

	var e entry
	if err := json.NewDecoder(gz).Decode(&e); err != nil {
		return nil, false
	}

	logging.Info(ctx, "cache hit",
		logging.String("key", key),
		logging.String("connection", connName),
		logging.Int("rows", len(e.Rows)),
	)

	return data.NewMemoryStore(e.Columns, e.Rows), true
}

func Put(ctx context.Context, connName, query string, store data.RowStore) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}

	cols := store.Columns()
	rows := make([][]interface{}, store.RowCount())
	for i := 0; i < store.RowCount(); i++ {
		row, err := store.Row(i)
		if err != nil {
			return err
		}
		rows[i] = row
	}

	key := cacheKey(connName, query)
	path := cachePath(key)

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)

	e := entry{
		Columns:   cols,
		Rows:      rows,
		Query:     query,
		Conn:      connName,
		Timestamp: time.Now(),
	}

	if err := json.NewEncoder(gz).Encode(&e); err != nil {
		gz.Close()
		return err
	}

	if err := gz.Close(); err != nil {
		return err
	}

	logging.Info(ctx, "cache put",
		logging.String("key", key),
		logging.String("connection", connName),
		logging.Int("rows", len(rows)),
	)

	return nil
}

func Clear() error {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		os.Remove(filepath.Join(dir, e.Name()))
	}
	return nil
}

func List() ([]CacheEntry, error) {
	dir := Dir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var result []CacheEntry
	for _, de := range entries {
		if !de.Type().IsRegular() {
			continue
		}
		path := filepath.Join(dir, de.Name())
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			f.Close()
			continue
		}
		var e entry
		err = json.NewDecoder(gz).Decode(&e)
		gz.Close()
		f.Close()
		if err != nil {
			continue
		}

		info, _ := de.Info()
		size := int64(0)
		if info != nil {
			size = info.Size()
		}

		result = append(result, CacheEntry{
			Key:       de.Name(),
			Conn:      e.Conn,
			Query:     e.Query,
			Rows:      len(e.Rows),
			Cols:      len(e.Columns),
			Timestamp: e.Timestamp,
			SizeBytes: size,
		})
	}
	return result, nil
}

type CacheEntry struct {
	Key       string
	Conn      string
	Query     string
	Rows      int
	Cols      int
	Timestamp time.Time
	SizeBytes int64
}
