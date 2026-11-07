package server_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/config"
	"github.com/thadeu/ndb/internal/engine"
)

const benchDocs = 1000

// seedBench fills a collection with docs; find by id is the measured query.
func seedBench(tb testing.TB, db *engine.DB) {
	tb.Helper()

	if _, err := db.Ensure(catalog.Def{Name: "bench"}); err != nil {
		tb.Fatal(err)
	}

	col, err := db.Get("bench")
	if err != nil {
		tb.Fatal(err)
	}

	for i := range benchDocs {
		doc := fmt.Appendf(nil, `{"k":"d%d","n":%d}`, i, i)

		if _, err := col.Insert(context.Background(), doc); err != nil {
			tb.Fatal(err)
		}
	}
}

// BenchmarkFindByIDInProcess is the engine cost alone: parse + query.
func BenchmarkFindByIDInProcess(b *testing.B) {
	db, err := engine.Open(config.Config{DataDir: b.TempDir(), AllowImplicit: true}, slog.Default())
	if err != nil {
		b.Fatal(err)
	}

	defer db.Close()

	seedBench(b, db)

	col, _ := db.Get("bench")
	body := []byte(`{"query":{"k":"d500"}}`)

	b.ResetTimer()

	for range b.N {
		if _, err := col.Find(context.Background(), body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFindByIDUnixSocket adds HTTP framing and JSON over a unix socket
// with keep-alive. The difference to the in-process case is the overhead.
func BenchmarkFindByIDUnixSocket(b *testing.B) {
	client := unixServer(b)

	seedViaHTTP(b, client)

	body := []byte(`{"query":{"k":"d500"}}`)

	b.ResetTimer()

	for range b.N {
		resp, err := client.Post("http://ndb/bench/_search", "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}

		if _, err := io.Copy(io.Discard, resp.Body); err != nil {
			b.Fatal(err)
		}

		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b.Fatalf("status %d", resp.StatusCode)
		}
	}
}

func seedViaHTTP(b *testing.B, client *http.Client) {
	b.Helper()

	req, _ := http.NewRequest("PUT", "http://ndb/bench", bytes.NewReader([]byte(`{}`)))

	if _, err := client.Do(req); err != nil {
		b.Fatal(err)
	}

	for i := range benchDocs {
		body := fmt.Appendf(nil, `{"docs":[{"k":"d%d","n":%d}]}`, i, i)

		resp, err := client.Post("http://ndb/bench/_bulk", "application/json", bytes.NewReader(body))
		if err != nil {
			b.Fatal(err)
		}

		resp.Body.Close()
	}
}
