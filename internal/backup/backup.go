// Package backup copies a data dir. Everything it copies is immutable (sealed
// WAL files, segments, blobs) or a consistent snapshot (bbolt Tx.WriteTo),
// so a backup is a plain copy that a restore opens as a data dir (plan §8.5).
//
// Two modes:
//   - Offline: the daemon is stopped. Offline holds the data dir lock and
//     copies every file, the active WAL included.
//   - Online: the daemon runs. engine.DB.Backup copies the WAL up to a mark
//     taken between two commits; segments and meta.bolt are left out and
//     the restore rebuilds them from the WAL.
package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/thadeu/ndb/internal/fs"
	"github.com/thadeu/ndb/internal/lock"
)

var disk = fs.OS{}

// PrepareDest creates dest, or checks that an existing dest is an empty dir.
func PrepareDest(dest string) error {
	entries, err := os.ReadDir(dest)

	if errors.Is(err, os.ErrNotExist) {
		return disk.MkdirAll(dest)
	}

	if err != nil {
		return err
	}

	if len(entries) > 0 {
		return fmt.Errorf("backup: %s is not empty", dest)
	}

	return nil
}

// CopyFile copies the first limit bytes of src to dst (limit < 0 copies all),
// creates the dirs of dst, and fsyncs the copy.
func CopyFile(src, dst string, limit int64) error {
	in, err := disk.Open(src)
	if err != nil {
		return err
	}

	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return err
	}

	size := info.Size()

	if limit >= 0 && limit < size {
		size = limit
	}

	if err := disk.MkdirAll(filepath.Dir(dst)); err != nil {
		return err
	}

	out, err := disk.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, io.NewSectionReader(in, 0, size)); err != nil {
		out.Close()

		return err
	}

	if err := out.Sync(); err != nil {
		out.Close()

		return err
	}

	return out.Close()
}

// CopyTree copies the files under src to dst. skip is asked with the path
// relative to src and tells whether to leave it out.
func CopyTree(src, dst string, skip func(rel string) bool) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}

		if rel != "." && skip != nil && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}

			return nil
		}

		if d.IsDir() {
			return disk.MkdirAll(filepath.Join(dst, rel))
		}

		return CopyFile(p, filepath.Join(dst, rel), -1)
	})
}

// Offline copies a data dir of a stopped daemon. It takes the data dir lock
// for the whole copy, so a daemon cannot start in the middle; a running
// daemon gives lock.ErrLocked and the caller uses the online path.
func Offline(dataDir, dest string) error {
	lk, err := lock.Acquire(dataDir)
	if err != nil {
		return err
	}

	defer lk.Release()

	if err := PrepareDest(dest); err != nil {
		return err
	}

	err = CopyTree(dataDir, dest, func(rel string) bool {
		return rel == "LOCK" || rel == filepath.Join("blobs", "tmp") || strings.HasSuffix(rel, ".tmp")
	})
	if err != nil {
		return err
	}

	return disk.SyncDir(dest)
}

// WriteFile writes data to path and fsyncs it.
func WriteFile(path string, data []byte) error {
	if err := disk.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}

	out, err := disk.Create(path)
	if err != nil {
		return err
	}

	if _, err := out.Write(data); err != nil {
		out.Close()

		return err
	}

	if err := out.Sync(); err != nil {
		out.Close()

		return err
	}

	return out.Close()
}
