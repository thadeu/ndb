package streams

import (
	"encoding/json"
	"errors"
	"strconv"
	"time"

	bolt "go.etcd.io/bbolt"
)

// The group store of one stream, streams.bolt in the collection dir:
//
//	meta                      last, max_deleted, cp_last, cp_added
//	groups/<g>                last, read (entries_read, -1 unknown)
//	groups/<g>/pel            entry key -> pending
//	groups/<g>/consumers      name -> consumer
//
// Every change is one bbolt transaction, fsync'd before it returns (unless
// the stream is relaxed).

// StoreFile is the group store of a stream, in its collection dir.
const StoreFile = "streams.bolt"

var (
	bMeta      = []byte("meta")
	bGroups    = []byte("groups")
	bPEL       = []byte("pel")
	bConsumers = []byte("consumers")
)

// pending is one entry of a PEL.
type pending struct {
	Consumer   string `json:"c"`
	Delivered  int64  `json:"t"` // unix ms of the last delivery
	Deliveries int64  `json:"n"`
}

// consumer is one consumer of a group.
type consumer struct {
	Seen   int64 `json:"s"` // unix ms of the last read or claim
	Active int64 `json:"a"` // unix ms of the last read that gave entries; 0 never
}

// group is the head of a group.
type group struct {
	Last ID
	Read int64
}

func openStore(path string, relaxed bool) (*bolt.DB, error) {
	db, err := bolt.Open(path, 0o644, &bolt.Options{Timeout: time.Second, PageSize: 4096})
	if err != nil {
		return nil, err
	}

	db.NoSync = relaxed

	err = db.Update(func(tx *bolt.Tx) error {
		for _, b := range [][]byte{bMeta, bGroups} {
			if _, err := tx.CreateBucketIfNotExists(b); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		db.Close()

		return nil, err
	}

	return db, nil
}

func getID(b *bolt.Bucket, key string) ID {
	id, _ := FromKey(string(b.Get([]byte(key))))

	return id
}

func putID(b *bolt.Bucket, key string, id ID) error {
	return b.Put([]byte(key), []byte(id.Key()))
}

func getInt(b *bolt.Bucket, key string, def int64) int64 {
	v := b.Get([]byte(key))

	if v == nil {
		return def
	}

	n, err := strconv.ParseInt(string(v), 10, 64)
	if err != nil {
		return def
	}

	return n
}

func putInt(b *bolt.Bucket, key string, n int64) error {
	return b.Put([]byte(key), []byte(strconv.FormatInt(n, 10)))
}

// groupBucket is the bucket of group g; the caller turns a missing one
// into unknown_group.
func groupBucket(tx *bolt.Tx, g string) (*bolt.Bucket, error) {
	b := tx.Bucket(bGroups).Bucket([]byte(g))

	if b == nil {
		return nil, errors.New("unknown_group")
	}

	return b, nil
}

func readGroup(b *bolt.Bucket) group {
	return group{Last: getID(b, "last"), Read: getInt(b, "read", -1)}
}

func getPending(b *bolt.Bucket, key []byte) (pending, bool) {
	v := b.Get(key)

	if v == nil {
		return pending{}, false
	}

	var p pending

	return p, json.Unmarshal(v, &p) == nil
}

func putPending(b *bolt.Bucket, key string, p pending) error {
	v, err := json.Marshal(p)
	if err != nil {
		return err
	}

	return b.Put([]byte(key), v)
}

func getConsumer(b *bolt.Bucket, name string) (consumer, bool) {
	v := b.Get([]byte(name))

	if v == nil {
		return consumer{}, false
	}

	var c consumer

	return c, json.Unmarshal(v, &c) == nil
}

func putConsumer(b *bolt.Bucket, name string, c consumer) error {
	v, err := json.Marshal(c)
	if err != nil {
		return err
	}

	return b.Put([]byte(name), v)
}
