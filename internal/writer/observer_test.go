package writer_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/thadeu/ndb/internal/writer"
)

type obsSpy struct {
	records atomic.Int64
	commits atomic.Int64
	fsyncs  atomic.Int64
}

func (o *obsSpy) Committed(records int, _ time.Duration) {
	o.records.Add(int64(records))
	o.commits.Add(1)
}

func (o *obsSpy) Fsynced(time.Duration) {
	o.fsyncs.Add(1)
}

func TestObserverSeesCommitsAndFsyncs(t *testing.T) {
	spy := &obsSpy{}
	w, _ := open(t, writer.Config{Observer: spy})

	for _, id := range []string{"a", "b", "c"} {
		if _, _, err := w.Commit(context.Background(), put(id)); err != nil {
			t.Fatal(err)
		}
	}

	if spy.records.Load() != 3 || spy.commits.Load() < 1 || spy.fsyncs.Load() < 1 {
		t.Fatalf("records %d commits %d fsyncs %d", spy.records.Load(), spy.commits.Load(), spy.fsyncs.Load())
	}
}
