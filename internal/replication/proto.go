// Package replication is the copy side of ndb replication, and the lines
// that the owner and a copy share (docs/dev/replication.md).
//
// One owner per database takes the writes. A copy pulls the change stream
// of the owner (GET /_replication/stream), writes each record with its own
// normal write path, and keeps the position of each collection on disk. A
// write that comes to a copy goes to the owner (Forward), and the copy
// answers it after it has applied the position of that write, so the next
// read of the client sees it.
package replication

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
)

// The headers of replication.
const (
	// PositionHeader is the answer of the owner to a forwarded write: the
	// last tx of each collection that the write touched, and the catalog
	// version (CatalogKey) after a change of a definition.
	PositionHeader = "Ndb-Position"

	// ForwardedHeader marks a request that a copy forwards to the owner.
	ForwardedHeader = "Ndb-Forwarded"

	// StaleHeader is set by a copy on the answer of a forwarded write whose
	// position it did not apply in time.
	StaleHeader = "Ndb-Stale"

	// GoneHeader lists the collections of a 410 position_gone answer.
	GoneHeader = "Ndb-Gone"
)

// CatalogKey is the name of the catalog version in a position.
const CatalogKey = "_catalog"

// Line is one line of the change stream. Exactly one field is set.
type Line struct {
	Hello     *Hello     `json:"hello,omitempty"`
	Catalog   *Catalog   `json:"catalog,omitempty"`
	Record    *Record    `json:"record,omitempty"`
	Heartbeat *Heartbeat `json:"heartbeat,omitempty"`

	// Gone names the collections whose history the owner can no longer
	// send (a restore, or a rename onto a truncated WAL). The copy takes a
	// snapshot of each one, then connects again.
	Gone []string `json:"gone,omitempty"`

	// End is the last line of a snapshot: the tx of the snapshot.
	End *End `json:"end,omitempty"`
}

// Hello is the first line of a stream.
type Hello struct {
	Database string `json:"database"`

	// Term goes up by one at each promote. A copy refuses an owner with a
	// lower term than the one it knows.
	Term uint64 `json:"term"`

	// Key is the fingerprint of the encryption key of the owner.
	Key string `json:"key"`

	// Promoted is set on an owner that was a copy: for each collection, the
	// position of the old owner at the promote (From) and its own last tx
	// then (At). An old owner that comes back as a copy goes on from At if
	// its last tx is From.
	Promoted map[string]Promoted `json:"promoted,omitempty"`
}

// Promoted is the position of one collection at a promote.
type Promoted struct {
	From uint64 `json:"from"`
	At   uint64 `json:"at"`
}

// Catalog is the definitions of the engine-mode collections of the
// database, and the catalog version of the owner.
type Catalog struct {
	Gen  uint64        `json:"gen"`
	Defs []catalog.Def `json:"defs"`
}

// Record is one WAL record of a collection. No ops: every op of the
// record was left out (the TTL sweep), the copy only moves its position.
type Record struct {
	C   string    `json:"c"`
	Tx  uint64    `json:"tx"`
	TS  time.Time `json:"ts"`
	Ops []Op      `json:"ops,omitempty"`
}

// Op is one change of a record.
type Op struct {
	Op  string          `json:"op"`
	ID  string          `json:"_id,omitempty"`
	V   uint64          `json:"_v,omitempty"`
	Doc json.RawMessage `json:"doc,omitempty"`
}

// Heartbeat is sent when there is no write: the time of the owner and the
// last tx of each collection, for the lag.
type Heartbeat struct {
	TS   time.Time         `json:"ts"`
	Last map[string]uint64 `json:"last"`
}

// End closes a snapshot.
type End struct {
	Tx   uint64 `json:"tx"`
	Docs int    `json:"docs"`
}

// FormatPositions writes positions as "a=1,b=2", sorted by name.
func FormatPositions(pos map[string]uint64) string {
	names := make([]string, 0, len(pos))

	for name := range pos {
		names = append(names, name)
	}

	sort.Strings(names)

	parts := make([]string, 0, len(names))

	for _, name := range names {
		parts = append(parts, name+"="+strconv.FormatUint(pos[name], 10))
	}

	return strings.Join(parts, ",")
}

// ParsePositions reads "a=1,b=2". An empty string is no position.
func ParsePositions(s string) (map[string]uint64, error) {
	out := map[string]uint64{}

	if strings.TrimSpace(s) == "" {
		return out, nil
	}

	for part := range strings.SplitSeq(s, ",") {
		name, n, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("bad position %q: use name=tx", part)
		}

		tx, err := strconv.ParseUint(n, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad position %q: %w", part, err)
		}

		out[name] = tx
	}

	return out, nil
}
