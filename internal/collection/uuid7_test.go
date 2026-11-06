package collection_test

import (
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/thadeu/ndb/internal/collection"
)

var uuid7Re = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestNewIDIsUUIDv7(t *testing.T) {
	for range 100 {
		id := collection.NewID()

		if !uuid7Re.MatchString(id) {
			t.Fatalf("not a lowercase hyphenated UUIDv7: %q", id)
		}
	}
}

func TestNewIDTextOrderIsCreationOrder(t *testing.T) {
	prev := collection.NewID()

	for i := range 10000 {
		next := collection.NewID()

		if strings.Compare(prev, next) >= 0 {
			t.Fatalf("id %d is not greater than the previous one: %s then %s", i, prev, next)
		}

		prev = next
	}
}

func TestNewIDIsUniqueAcrossGoroutines(t *testing.T) {
	var (
		mu   sync.Mutex
		seen = map[string]bool{}
		wg   sync.WaitGroup
	)

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 2000 {
				id := collection.NewID()

				mu.Lock()

				if seen[id] {
					t.Errorf("duplicate id %s", id)
				}

				seen[id] = true

				mu.Unlock()
			}
		}()
	}

	wg.Wait()
}
