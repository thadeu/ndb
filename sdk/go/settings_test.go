package ndb

import (
	"context"
	"strings"
	"testing"
)

func TestSettings(t *testing.T) {
	ctx := context.Background()
	db := openTest(t, daemon.unix)

	got, err := db.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}

	enc, _ := got["encryption"].(map[string]any)

	if key, _ := enc["key"].(string); !strings.Contains(key, "****") || enc["key_id"] != "k1" {
		t.Fatalf("encryption %v", enc)
	}

	put, err := db.PutSettings(ctx, map[string]any{"log": map[string]any{"slow_ms": 900}})
	if err != nil {
		t.Fatal(err)
	}

	slow, _ := put["log"].(map[string]any)["slow_ms"].(map[string]any)

	if slow["value"] != float64(900) || slow["source"] != "stored" {
		t.Fatalf("slow_ms %v", slow)
	}

	if _, err := db.PutSettings(ctx, map[string]any{"encryption": map[string]any{"key": "x"}}); err == nil {
		t.Fatal("the key could be changed")
	}
}
