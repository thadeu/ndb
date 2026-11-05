package fs

import (
	"errors"
	"sync"
)

var ErrInjected = errors.New("fs: injected fault")

type Op int

const (
	OpWrite Op = iota
	OpSync
	OpRename
	OpSyncDir
)

// Fault fails the At-th call (1-based) of Op. For OpWrite, Partial > 0
// writes that many bytes before the error.
type Fault struct {
	Op      Op
	At      int
	Partial int
}

type FaultFS struct {
	FS

	mu     sync.Mutex
	faults []Fault
	calls  map[Op]int
}

func NewFault(base FS, faults ...Fault) *FaultFS {
	return &FaultFS{FS: base, faults: faults, calls: map[Op]int{}}
}

// hit counts one call of op and returns the fault for it, if any.
func (f *FaultFS) hit(op Op) (Fault, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls[op]++
	n := f.calls[op]

	for _, flt := range f.faults {
		if flt.Op == op && flt.At == n {
			return flt, true
		}
	}

	return Fault{}, false
}

// Calls returns how many times op was called.
func (f *FaultFS) Calls(op Op) int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls[op]
}

func (f *FaultFS) Create(path string) (File, error) {
	file, err := f.FS.Create(path)
	if err != nil {
		return nil, err
	}

	return &faultFile{File: file, fs: f}, nil
}

func (f *FaultFS) OpenAppend(path string) (File, error) {
	file, err := f.FS.OpenAppend(path)
	if err != nil {
		return nil, err
	}

	return &faultFile{File: file, fs: f}, nil
}

func (f *FaultFS) Rename(oldPath, newPath string) error {
	if _, ok := f.hit(OpRename); ok {
		return ErrInjected
	}

	return f.FS.Rename(oldPath, newPath)
}

func (f *FaultFS) SyncDir(path string) error {
	if _, ok := f.hit(OpSyncDir); ok {
		return ErrInjected
	}

	return f.FS.SyncDir(path)
}

type faultFile struct {
	File

	fs *FaultFS
}

func (f *faultFile) Write(p []byte) (int, error) {
	flt, ok := f.fs.hit(OpWrite)
	if !ok {
		return f.File.Write(p)
	}

	if flt.Partial <= 0 {
		return 0, ErrInjected
	}

	n, err := f.File.Write(p[:min(flt.Partial, len(p))])
	if err != nil {
		return n, err
	}

	return n, ErrInjected
}

func (f *faultFile) Sync() error {
	if _, ok := f.fs.hit(OpSync); ok {
		return ErrInjected
	}

	return f.File.Sync()
}
