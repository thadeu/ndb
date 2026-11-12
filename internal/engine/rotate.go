package engine

import (
	"errors"
	"time"

	"github.com/thadeu/ndb/internal/catalog"
	"github.com/thadeu/ndb/internal/settings"
)

// A key rotation: the box seals with the new key and still opens the old
// one. Each collection with encrypted fields runs its reseal job, which
// seals every value of the old key again. When all of them are done,
// rotationLoop drops the old key (settings.Finish).

// RotationTick is how often the daemon checks a running key rotation.
var RotationTick = 2 * time.Second

// ErrEnvKey is a rotation in the settings of a data dir whose key comes
// from NDB_ENCRYPTION_KEY.
var ErrEnvKey = errors.New("the key comes from NDB_ENCRYPTION_KEY: rotate it with ndb key rotate -env")

// RotateKey makes a new key the current one, now, for a key kept in the
// settings. With env, it makes the env key of the next start instead and
// gives it in hex (shown once, never kept).
func (db *DB) RotateKey(env bool) (string, error) {
	db.keyMu.Lock()
	defer db.keyMu.Unlock()

	if env {
		return settings.PlanEnv(db.settings, db.key)
	}

	if db.key.Source == "env" {
		return "", ErrEnvKey
	}

	next, err := settings.Rotate(db.settings, db.key)
	if err != nil {
		return "", err
	}

	if err := db.box.Rotate(next.ID, next.Derive("ndb:e1"), next.Derive("ndb:h1")); err != nil {
		return "", err
	}

	db.key = next

	db.log.Info("key rotation started", "key_id", next.ID, "from", next.Old.ID)

	db.resealAll()

	return "", nil
}

// Rotation is the state of a key rotation: the old key id and the
// collections that still have values of it. Zero when there is none.
type Rotation struct {
	From    string
	Pending []string
}

// RotationState reads the state of the rotation.
func (db *DB) RotationState() Rotation {
	key := db.Key()
	if key.Old == nil {
		return Rotation{}
	}

	return Rotation{From: key.Old.ID, Pending: db.pendingReseal(false)}
}

// resealAll starts the job of each collection that is not done, and wakes
// a sleeping one for it.
func (db *DB) resealAll() {
	db.pendingReseal(true)
}

// pendingReseal lists the collections whose encrypted values are not all
// of the current key. With start, it also starts their jobs.
func (db *DB) pendingReseal(start bool) []string {
	var out []string

	for _, col := range db.engineList() {
		def, ok := db.cat.Get(col.name)
		if !ok || len(db.encryptedOf(def)) == 0 {
			continue
		}

		if err := col.enter(); err != nil {
			continue
		}

		if !col.eng.Resealed() {
			out = append(out, col.name)

			if start {
				col.eng.StartReseal()
			}
		}

		col.leave()
	}

	return out
}

// encryptedOf is the encrypted fields of def; a partition has the ones of
// its logical collection.
func (db *DB) encryptedOf(def catalog.Def) []string {
	if def.PartitionOf != "" {
		if logical, ok := db.cat.Get(def.PartitionOf); ok {
			return logical.Encrypted
		}
	}

	return def.Encrypted
}

// rotationLoop ends a rotation when every collection is done: the settings
// drop the old key, and the box stops opening it.
func (db *DB) rotationLoop() {
	defer db.gcWait.Done()

	tick := time.NewTicker(RotationTick)
	defer tick.Stop()

	for {
		select {
		case <-db.ctx.Done():
			return
		case <-tick.C:
			db.finishRotation()
		}
	}
}

func (db *DB) finishRotation() {
	if !db.box.Rotating() || len(db.pendingReseal(true)) > 0 {
		return
	}

	db.keyMu.Lock()
	defer db.keyMu.Unlock()

	if db.key.Old == nil {
		return
	}

	if err := settings.Finish(db.settings); err != nil {
		db.log.Error("key rotation: drop the old key", "err", err)

		return
	}

	from := db.key.Old.ID

	db.box.DropOld()
	db.key.Old = nil

	if db.key.Source == "env" {
		db.log.Warn("key rotation done: remove NDB_ENCRYPTION_KEY_OLD from the env", "key_id", db.key.ID, "from", from)

		return
	}

	db.log.Info("key rotation done", "key_id", db.key.ID, "from", from)
}
