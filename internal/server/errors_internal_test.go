package server

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/thadeu/ndb/internal/feed"
)

func TestHistoryTruncatedIsGone(t *testing.T) {
	err := fmt.Errorf("%w: the oldest kept tx is 9", feed.ErrHistoryTruncated)

	if code := codeOf(err); code != "history_truncated" || statusOf(code) != http.StatusGone {
		t.Fatalf("code %q status %d", code, statusOf(code))
	}
}
