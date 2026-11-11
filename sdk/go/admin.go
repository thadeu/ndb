package ndb

import (
	"context"
	"encoding/json"
	"net/http"
)

// Health returns nil when the daemon is ready (GET /_health). It does not
// wait: a daemon that is recovering is ErrNotReady.
func (db *DB) Health(ctx context.Context) error {
	resp, err := db.doOnce(ctx, http.MethodGet, "/_health", nil)
	if err != nil {
		return err
	}

	discard(resp)

	return nil
}

// Collections lists the catalog: one definition per collection, sorted by
// name (GET /_collections).
func (db *DB) Collections(ctx context.Context) ([]map[string]any, error) {
	var out struct {
		Collections []map[string]any `json:"collections"`
	}

	return out.Collections, db.get(ctx, "/_collections", &out)
}

// Stats is the state of every collection, the totals, the version and the
// uptime of the daemon (GET /_stats).
func (db *DB) Stats(ctx context.Context) (map[string]any, error) {
	var out map[string]any

	return out, db.get(ctx, "/_stats", &out)
}

// Settings are the settings of the daemon (GET /_cluster/settings): the
// encryption key, masked, and the log settings, each with where its value
// comes from.
func (db *DB) Settings(ctx context.Context) (map[string]any, error) {
	var out map[string]any

	return out, db.get(ctx, "/_cluster/settings", &out)
}

// PutSettings changes settings and stores them in the data dir (PUT
// /_cluster/settings), such as {"log": {"level": "debug"}}.
func (db *DB) PutSettings(ctx context.Context, settings map[string]any) (map[string]any, error) {
	body, err := json.Marshal(settings)
	if err != nil {
		return nil, err
	}

	resp, err := db.do(ctx, http.MethodPut, "/_cluster/settings", body)
	if err != nil {
		return nil, err
	}

	var out map[string]any

	return out, decodeBody(resp, &out)
}

// Size is the disk use per collection and per part (GET /_size).
func (db *DB) Size(ctx context.Context) (map[string]any, error) {
	var out map[string]any

	return out, db.get(ctx, "/_size", &out)
}

func (db *DB) get(ctx context.Context, path string, out any) error {
	resp, err := db.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}

	return decodeBody(resp, out)
}
