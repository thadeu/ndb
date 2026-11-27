package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/thadeu/ndb/internal/engine"
	"github.com/thadeu/ndb/internal/replication"
)

// firstHello is how long ndb serve waits for the first answer of each
// owner, to refuse a start with another key or a lower term. An owner that
// does not answer in time does not stop the start.
const firstHello = 5 * time.Second

// startCopies makes and starts the copy of each source of NDB_REPLICA_OF.
func startCopies(ctx context.Context, root *engine.DB, sources []replication.Source, log *slog.Logger) ([]*replication.Copy, error) {
	var copies []*replication.Copy

	stop := func() {
		for _, c := range copies {
			c.Stop()
		}
	}

	for _, src := range sources {
		db := root

		if src.Local != engine.DefaultDatabase {
			var err error

			if db, err = root.Database(src.Local, true); err != nil {
				stop()

				return nil, err
			}
		}

		c, err := replication.New(src, db, db.Key().Fingerprint(), log, replication.Options{})
		if err != nil {
			stop()

			return nil, err
		}

		c.Start(ctx)

		copies = append(copies, c)

		log.Info("replication: copy started", "database", src.Local, "source", src.Redacted())
	}

	var errs []error

	for _, c := range copies {
		errs = append(errs, c.First(firstHello))
	}

	if err := errors.Join(errs...); err != nil {
		stop()

		return nil, err
	}

	return copies, nil
}
