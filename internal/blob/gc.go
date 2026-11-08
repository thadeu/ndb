package blob

import (
	"iter"
	"time"
)

// DefaultGrace is how old an unreferenced blob must be before GC deletes it.
// It covers a blob that was stored and whose document is not written yet.
const DefaultGrace = 24 * time.Hour

// GCResult is what one GC run removed.
type GCResult struct {
	Deleted int
	Bytes   int64
}

// SetHold makes GC keep an unreferenced blob until it was found
// unreferenced for at least d (the longest PITR window). 0 turns it off.
func (s *Store) SetHold(d time.Duration) {
	s.gcMu.Lock()
	defer s.gcMu.Unlock()

	s.hold = d
}

// GC deletes the blobs that are not in live and are older than grace.
// live yields the hashes that the documents refer to (see Refs). A blob that
// is young, or that was stored again lately, always stays.
func (s *Store) GC(live iter.Seq[string], grace time.Duration) (GCResult, error) {
	keep := map[string]struct{}{}

	for h := range live {
		keep[h] = struct{}{}
	}

	s.gcMu.Lock()
	defer s.gcMu.Unlock()

	var doomed []string

	now := time.Now()
	cutoff := now.Add(-grace)
	orphans := map[string]time.Time{}

	err := s.walk(func(hash string, _ int64, mtime time.Time) error {
		if _, ok := keep[hash]; ok || !mtime.Before(cutoff) {
			return nil
		}

		if s.hold > 0 {
			since, seen := s.orphans[hash]

			if !seen {
				since = now
			}

			if now.Sub(since) < s.hold {
				orphans[hash] = since

				return nil
			}
		}

		doomed = append(doomed, hash)

		return nil
	})
	if err != nil {
		return GCResult{}, err
	}

	s.orphans = orphans

	return s.remove(doomed, cutoff)
}

// remove deletes each blob again under the lock, because Put may have renewed
// it since the scan.
func (s *Store) remove(hashes []string, cutoff time.Time) (GCResult, error) {
	var res GCResult

	s.mu.Lock()
	defer s.mu.Unlock()

	for _, h := range hashes {
		info, err := s.fs.Stat(s.path(h))
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}

		if err := s.fs.Remove(s.path(h)); err != nil {
			return res, err
		}

		res.Deleted++
		res.Bytes += info.Size()

		s.count.Add(-1)
		s.bytes.Add(-info.Size())
	}

	return res, nil
}
