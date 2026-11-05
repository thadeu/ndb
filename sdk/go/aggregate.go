package ndb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Stage is one stage of an aggregate pipeline, such as
// {"$group": {...}} (plan §10.2). Its value is any JSON value.
type Stage = map[string]any

// Pipeline is the list of stages of an aggregate. The daemon needs exactly
// one $group; see the stages and accumulators of plan §10.2.
type Pipeline []Stage

// SortStage is a $sort stage. It keeps the order of the keys, which a Go
// map would lose.
func SortStage(keys ...SortKey) Stage {
	return Stage{"$sort": sortOrder(keys)}
}

// AggOption changes one aggregate call.
type AggOption func(*aggBody)

// AggLimit sets the most rows of the page. Zero means no limit.
func AggLimit(n int) AggOption {
	return func(b *aggBody) { b.Limit = n }
}

// AggSkip drops the first n rows of the output.
func AggSkip(n int) AggOption {
	return func(b *aggBody) { b.Skip = n }
}

// AggAfter continues from the Next token of the previous page of the same
// pipeline. Another pipeline gets ErrCursorMismatch.
func AggAfter(token string) AggOption {
	return func(b *aggBody) { b.After = token }
}

// AggCount asks for the number of output rows before limit and skip. It
// fills AggResult.Total.
func AggCount() AggOption {
	return func(b *aggBody) { b.Count = true }
}

// aggBody is the body of POST /v1/c/{coll}/aggregate.
type aggBody struct {
	Pipeline Pipeline `json:"pipeline"`
	Limit    int      `json:"limit,omitempty"`
	Skip     int      `json:"skip,omitempty"`
	After    string   `json:"after,omitempty"`
	Count    bool     `json:"count,omitempty"`
}

// AggResult is one page of an aggregate. Next is empty on the last page.
// Total is set only with AggCount.
type AggResult[R any] struct {
	Rows  []R
	Next  string
	Total int
}

// Aggregate runs a pipeline and returns the rows as maps. Numbers are
// float64. Group rows look like {"_id": key, "<accumulator>": value}.
func (c *Coll[T]) Aggregate(ctx context.Context, p Pipeline, opts ...AggOption) (*AggResult[map[string]any], error) {
	return AggregateAs[map[string]any](ctx, c, p, opts...)
}

// AggregateAs runs a pipeline and decodes each row into R, with
// encoding/json.
func AggregateAs[R, T any](ctx context.Context, c *Coll[T], p Pipeline, opts ...AggOption) (*AggResult[R], error) {
	body := aggBody{Pipeline: p}

	for _, o := range opts {
		o(&body)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	resp, err := c.db.aggregate(ctx, c.name, raw)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	return readAgg[R](resp)
}

func readAgg[R any](resp *http.Response) (*AggResult[R], error) {
	res := &AggResult[R]{Rows: []R{}, Next: resp.Header.Get("X-Ndb-Next")}

	if total := resp.Header.Get("X-Ndb-Total"); total != "" {
		n, err := strconv.Atoi(total)
		if err != nil {
			return nil, fmt.Errorf("ndb: bad X-Ndb-Total %q", total)
		}

		res.Total = n
	}

	for row, err := range ndjsonRows(resp.Body) {
		if err != nil {
			return nil, err
		}

		var v R

		if err := json.Unmarshal(row, &v); err != nil {
			return nil, fmt.Errorf("ndb: aggregate row: %w", err)
		}

		res.Rows = append(res.Rows, v)
	}

	return res, nil
}
