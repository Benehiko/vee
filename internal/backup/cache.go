package backup

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

const cacheTTL = time.Hour

// CacheGet returns cached dir entries for vmName if the cache is fresher than
// cacheTTL. Returns nil, nil when the cache is missing or expired.
func CacheGet(db *sql.DB, vmName string) ([]*DirEntry, error) {
	var pathsJSON string
	var cachedAt time.Time
	err := db.QueryRowContext(context.Background(),
		`SELECT paths_json, cached_at FROM dir_cache WHERE vm_name = ?`, vmName,
	).Scan(&pathsJSON, &cachedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache get: %w", err)
	}
	if time.Since(cachedAt) > cacheTTL {
		return nil, nil
	}
	var paths []string
	if err := json.Unmarshal([]byte(pathsJSON), &paths); err != nil {
		return nil, fmt.Errorf("cache decode: %w", err)
	}
	return pathsToEntries(paths), nil
}

// CacheSet stores the dir entries for vmName, overwriting any previous entry.
func CacheSet(db *sql.DB, vmName string, entries []*DirEntry) error {
	paths := make([]string, len(entries))
	for i, e := range entries {
		paths[i] = e.Path
	}
	j, err := json.Marshal(paths)
	if err != nil {
		return fmt.Errorf("cache encode: %w", err)
	}
	_, err = db.ExecContext(context.Background(),
		`INSERT OR REPLACE INTO dir_cache (vm_name, paths_json, cached_at) VALUES (?, ?, ?)`,
		vmName, string(j), time.Now(),
	)
	return err
}

// CacheAge returns how old the cache entry is. Returns -1 if no entry exists.
func CacheAge(db *sql.DB, vmName string) (time.Duration, error) {
	var cachedAt time.Time
	err := db.QueryRowContext(context.Background(), `SELECT cached_at FROM dir_cache WHERE vm_name = ?`, vmName).Scan(&cachedAt)
	if err == sql.ErrNoRows {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	return time.Since(cachedAt), nil
}

func pathsToEntries(paths []string) []*DirEntry {
	entries := make([]*DirEntry, len(paths))
	for i, p := range paths {
		entries[i] = pathToEntry(p)
	}
	return entries
}
