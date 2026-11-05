package feed_test

import (
	"testing"

	"github.com/thadeu/ndb/internal/feed"
)

func TestHubDeliversInOrder(t *testing.T) {
	var h feed.Hub[int]

	sub := h.Subscribe(4, nil)

	for i := range 3 {
		h.Publish(i, nil)
	}

	for i := range 3 {
		if got := <-sub.C; got != i {
			t.Fatalf("got %d, want %d", got, i)
		}
	}
}

func TestHubOverflowLagsOnlyTheSlowSubscriber(t *testing.T) {
	var h feed.Hub[int]

	slow := h.Subscribe(2, nil)
	fast := h.Subscribe(16, nil)

	for i := range 5 {
		h.Publish(i, nil)
	}

	n := 0

	for range slow.C {
		n++
	}

	if n != 2 || !slow.Lagged() {
		t.Fatalf("slow got %d events, lagged=%v", n, slow.Lagged())
	}

	if fast.Lagged() || len(fast.C) != 5 {
		t.Fatalf("fast lagged=%v len=%d", fast.Lagged(), len(fast.C))
	}
}

func TestHubCallbacksRunUnderTheLock(t *testing.T) {
	var h feed.Hub[int]

	last := 0

	h.Publish(7, func() { last = 7 })

	got := 0

	h.Subscribe(1, func() { got = last })

	if got != 7 {
		t.Fatalf("snapshot = %d", got)
	}
}

func TestSubCloseIsIdempotent(t *testing.T) {
	var h feed.Hub[int]

	sub := h.Subscribe(1, nil)

	sub.Close()
	sub.Close()
	h.Publish(1, nil)

	if _, ok := <-sub.C; ok || sub.Lagged() {
		t.Fatal("closed sub must be closed and not lagged")
	}
}
