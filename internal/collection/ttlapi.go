package collection

import (
	"context"
	"encoding/json"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/wal"
)

// Per-document expiry, like EXPIRE / PERSIST / TTL in Redis (plan §11.5).
//
// The daemon sets the expires_field of a document to now + duration, in the
// same transaction as the write, so the change has a version, a WAL record
// and a watch event like any other update. A collection with only ttl + time
// has a derived expiry: there is no field to write, so these calls fail.
//
// Rule: an expire_in given with a call wins over the expires_field value that
// the document or the change carries.

// Expiry is when a document expires: In from now, or At. In wins when both
// are set.
type Expiry struct {
	In time.Duration
	At time.Time
}

// TTLInfo is the remaining life of a visible document. Never is true when it
// has no expiry. Remaining stops at the time.Duration limit (292 years);
// Seconds is the whole seconds left (rounded up), with no such limit.
type TTLInfo struct {
	Never     bool
	At        time.Time
	Remaining time.Duration
	Seconds   int64
}

type setExpiryReq struct {
	id      string
	at      time.Time
	persist bool
}

func formatExpiry(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// expiryField is the field that holds the expiry, or the reason there is none.
func (c *Collection) expiryField() (string, error) {
	if c.def.ExpiresField != "" {
		return c.def.ExpiresField, nil
	}

	if c.def.TTL != "" && c.def.Time != "" {
		return "", errf("ttl_derived", "the expiry of %s comes from ttl and %s; set expires_field to expire documents one by one", c.def.Name, c.def.Time)
	}

	return "", errf("ttl_not_enabled", "%s has no expires_field; add it to the collection definition", c.def.Name)
}

// InsertIn is Insert, with every document expiring in d from now.
func (c *Collection) InsertIn(ctx context.Context, d time.Duration, docs ...[]byte) ([]Result, error) {
	if _, err := c.expiryField(); err != nil {
		return nil, err
	}

	return c.insert(ctx, insertReq{docs: docs, expireIn: d})
}

// stampDoc sets the expiry field of a new document.
func (c *Collection) stampDoc(doc json.RawMessage, at time.Time) (json.RawMessage, error) {
	return applyChange(doc, Change{Set: map[string]any{c.def.ExpiresField: formatExpiry(at)}})
}

// stampChange adds the expiry to an update. It wins over the same field in
// Set, and over an Unset of it.
func (c *Collection) stampChange(ch Change, at time.Time) Change {
	set := make(map[string]any, len(ch.Set)+1)

	for k, v := range ch.Set {
		set[k] = v
	}

	set[c.def.ExpiresField] = formatExpiry(at)

	var unset []string

	for _, p := range ch.Unset {
		if p != c.def.ExpiresField {
			unset = append(unset, p)
		}
	}

	return Change{Set: set, Unset: unset}
}

// Expire sets the expiry of a visible document. It returns the new version
// and the expiry time. A missing or expired document is not_found.
func (c *Collection) Expire(ctx context.Context, id string, e Expiry) (uint64, time.Time, error) {
	if _, err := c.expiryField(); err != nil {
		return 0, time.Time{}, err
	}

	at := e.At

	if e.In > 0 {
		at = c.clock().Add(e.In)
	}

	if at.IsZero() {
		return 0, time.Time{}, errf("invalid_request", "expire needs a positive duration or a time")
	}

	_, ops, err := c.commit(ctx, setExpiryReq{id: id, at: at})
	if err != nil {
		return 0, time.Time{}, err
	}

	return ops[0].V, at, nil
}

// Persist removes the expiry of a visible document.
func (c *Collection) Persist(ctx context.Context, id string) (uint64, error) {
	if _, err := c.expiryField(); err != nil {
		return 0, err
	}

	_, ops, err := c.commit(ctx, setExpiryReq{id: id, persist: true})
	if err != nil {
		return 0, err
	}

	return ops[0].V, nil
}

func (c *Collection) prepareSetExpiry(v *view, r setExpiryReq) ([]wal.Op, error) {
	cur, err := current(v, r.id)
	if err != nil {
		return nil, err
	}

	raw, err := cur.raw()
	if err != nil {
		return nil, err
	}

	if !c.visible(raw) {
		return nil, errf("not_found", "%s", r.id)
	}

	ch := Change{Unset: []string{c.def.ExpiresField}}

	if !r.persist {
		ch = c.stampChange(Change{}, r.at)
	}

	doc, err := applyChange(raw, ch)
	if err != nil {
		return nil, err
	}

	doc, err = c.externalize(doc)
	if err != nil {
		return nil, err
	}

	return []wal.Op{{Kind: wal.Put, ID: r.id, V: cur.v + 1, Doc: doc}}, nil
}

// TTL is the remaining life of a visible document. With both ttl + time and
// expires_field, the earlier expiry counts. A missing or expired document is
// not_found.
func (c *Collection) TTL(id string) (TTLInfo, error) {
	d, ok := c.Find(id)
	if !ok {
		return TTLInfo{}, errf("not_found", "%s", id)
	}

	var m map[string]any

	if err := json.Unmarshal(d.Doc, &m); err != nil {
		return TTLInfo{}, err
	}

	var (
		at    time.Time
		found bool
	)

	pick := func(t time.Time, ok bool) {
		if ok && (!found || t.Before(at)) {
			at, found = t, true
		}
	}

	if c.def.ExpiresField != "" {
		pick(timeAt(m, c.def.ExpiresField, 0))
	}

	if c.def.TTL != "" && c.def.Time != "" {
		if ttl, err := catalog.ParseDuration(c.def.TTL); err == nil {
			pick(timeAt(m, c.def.Time, ttl))
		}
	}

	if !found {
		return TTLInfo{Never: true}, nil
	}

	now := c.clock()

	return TTLInfo{At: at, Remaining: at.Sub(now), Seconds: secondsUntil(now, at)}, nil
}

// secondsUntil is the whole seconds from now to at, rounded up. It does not
// use time.Duration, so a far time does not overflow.
func secondsUntil(now, at time.Time) int64 {
	secs := at.Unix() - now.Unix()
	nanos := int64(at.Nanosecond()) - int64(now.Nanosecond())

	if nanos < 0 {
		secs--
		nanos += int64(time.Second)
	}

	if nanos > 0 {
		secs++
	}

	return secs
}

// timeAt reads an RFC 3339 string or unix seconds at path, plus add.
func timeAt(m map[string]any, path string, add time.Duration) (time.Time, bool) {
	val, ok := getPath(m, path)
	if !ok {
		return time.Time{}, false
	}

	switch x := val.(type) {
	case string:
		t, err := time.Parse(time.RFC3339Nano, x)

		return t.Add(add), err == nil
	case float64:
		return time.Unix(int64(x), 0).Add(add), true
	}

	return time.Time{}, false
}
