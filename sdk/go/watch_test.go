package ndb

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type Job struct {
	JobID  string `ndb:"job_id,id"`
	Status string `ndb:"status"`
}

func watchColl(t *testing.T) (*Coll[Job], context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)

	db, err := Open(daemon.unix)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(db.Close)

	c, err := Collection[Job](ctx, db, newColl())
	if err != nil {
		t.Fatal(err)
	}

	return c, ctx
}

func addJobs(t *testing.T, ctx context.Context, c *Coll[Job], from, to int) {
	t.Helper()

	for i := from; i <= to; i++ {
		if _, err := c.Insert(ctx, Job{JobID: fmt.Sprintf("j%d", i), Status: "pending"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWatchYieldsTypedEventsAndAckResumes(t *testing.T) {
	c, ctx := watchColl(t)

	addJobs(t, ctx, c, 1, 3)

	var last Event[Job]

	n := 0

	for ev, err := range c.Watch(ctx, "relay", Limit(2)) {
		if err != nil {
			t.Fatal(err)
		}

		n++

		if ev.Op != OpPut || ev.ID != fmt.Sprintf("j%d", n) || ev.Doc.JobID != ev.ID || ev.Doc.Status != "pending" || ev.V != 1 {
			t.Fatalf("event %d = %+v", n, ev)
		}

		last = ev
	}

	if n != 2 {
		t.Fatalf("got %d events, want 2 (limit)", n)
	}

	if err := c.Ack(ctx, last); err != nil {
		t.Fatal(err)
	}

	for ev, err := range c.Watch(ctx, "relay", Limit(1)) {
		if err != nil || ev.ID != "j3" {
			t.Fatalf("after ack got %+v err=%v, want j3", ev, err)
		}
	}
}

func TestWatchDeleteEventHasZeroDocAndFromNow(t *testing.T) {
	c, ctx := watchColl(t)

	addJobs(t, ctx, c, 1, 1)

	got := make(chan Event[Job], 1)

	started := make(chan struct{})

	go func() {
		close(started)

		for ev, err := range c.Watch(ctx, "late", FromNow(), Limit(1)) {
			if err == nil {
				got <- ev
			}
		}
	}()

	<-started

	time.Sleep(200 * time.Millisecond)

	if err := c.Delete(ctx, "j1"); err != nil {
		t.Fatal(err)
	}

	select {
	case ev := <-got:
		if ev.Op != OpDel || ev.ID != "j1" || ev.Doc != (Job{}) {
			t.Fatalf("event = %+v, want a del of j1", ev)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no event")
	}
}

func TestWatchStopsWhenTheCallerBreaks(t *testing.T) {
	c, ctx := watchColl(t)

	addJobs(t, ctx, c, 1, 3)

	n := 0

	for _, err := range c.Watch(ctx, "br") {
		if err != nil {
			t.Fatal(err)
		}

		if n++; n == 2 {
			break
		}
	}

	if n != 2 {
		t.Fatalf("n = %d", n)
	}
}

func TestWatchErrorsAreTyped(t *testing.T) {
	c, ctx := watchColl(t)

	for _, err := range c.Watch(ctx, "") {
		var apiErr *Error

		if !errors.As(err, &apiErr) || apiErr.Code != "invalid_request" {
			t.Fatalf("err = %v, want invalid_request", err)
		}
	}

	err := c.Ack(ctx, Event[Job]{Consumer: "x", Pos: "garbage"})

	if !errors.Is(err, ErrInvalidPosition) {
		t.Fatalf("err = %v, want ErrInvalidPosition", err)
	}
}
