package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// parts is the disk usage of a data dir, split by what the bytes are for.
type parts struct {
	WAL      int64 `json:"wal"`
	Segments int64 `json:"segments"`
	Meta     int64 `json:"meta"`
	Blobs    int64 `json:"blobs"`
	Cursors  int64 `json:"cursors"`
}

func (p parts) total() int64 {
	return p.WAL + p.Segments + p.Meta + p.Blobs + p.Cursors
}

func (p *parts) add(o parts) {
	p.WAL += o.WAL
	p.Segments += o.Segments
	p.Meta += o.Meta
	p.Blobs += o.Blobs
	p.Cursors += o.Cursors
}

type collectionUsage struct {
	Name         string `json:"name"`
	Bytes        parts  `json:"bytes"`
	Total        int64  `json:"total"`
	WALFiles     int    `json:"wal_files"`
	SegmentFiles int    `json:"segment_files"`
}

type usage struct {
	DataDir     string            `json:"data_dir"`
	Total       int64             `json:"total"`
	Collections []collectionUsage `json:"collections"`
	Shared      parts             `json:"shared"`
}

// scanUsage reads the sizes under a data dir. It only stats files: it takes
// no lock and works while a daemon runs.
func scanUsage(dataDir string) (usage, error) {
	u := usage{DataDir: dataDir}

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return u, err
	}

	for _, e := range entries {
		path := filepath.Join(dataDir, e.Name())

		switch {
		case e.Name() == "collections" && e.IsDir():
			if u.Collections, err = scanCollections(path); err != nil {
				return u, err
			}
		case e.Name() == "blobs":
			u.Shared.Blobs, _ = treeSize(path)
		case e.Name() == "cursors.bolt":
			u.Shared.Cursors, _ = treeSize(path)
		default:
			size, _ := treeSize(path)
			u.Shared.Meta += size
		}
	}

	u.Total = u.Shared.total()

	for _, c := range u.Collections {
		u.Total += c.Total
	}

	return u, nil
}

func scanCollections(dir string) ([]collectionUsage, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var out []collectionUsage

	for _, e := range entries {
		if e.IsDir() {
			out = append(out, scanCollection(filepath.Join(dir, e.Name()), e.Name()))
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	return out, nil
}

// scanCollection sorts the files of one collection dir: *.ndjson are WAL
// files, segments/ holds the segments, the rest (meta.bolt) is meta.
func scanCollection(dir, name string) collectionUsage {
	c := collectionUsage{Name: name}

	entries, _ := os.ReadDir(dir)

	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		size, files := treeSize(path)

		switch {
		case e.IsDir() && e.Name() == "segments":
			c.Bytes.Segments += size
			c.SegmentFiles += files
		case strings.HasSuffix(e.Name(), ".ndjson"):
			c.Bytes.WAL += size
			c.WALFiles += files
		default:
			c.Bytes.Meta += size
		}
	}

	c.Total = c.Bytes.total()

	return c
}

// treeSize is the byte size and file count of a file or a dir tree. A file
// that vanishes during the walk (a compaction) counts as zero.
func treeSize(path string) (size int64, files int) {
	_ = filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}

			return err
		}

		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		size += info.Size()
		files++

		return nil
	})

	return size, files
}
