package replication

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// StateFile is the file of the replication state, in the dir of a
// database. A database that never was a copy has none.
const StateFile = "replication.json"

// The roles of a database.
const (
	RoleOwner = "owner"
	RoleCopy  = "copy"
)

// State is what a copy keeps on disk: the owner position of each
// collection it applied, the term of the owner, and whether it caught up
// once. A promoted copy keeps role owner, its term, and the positions of
// the promote.
type State struct {
	Role   string `json:"role"`
	Term   uint64 `json:"term"`
	Source string `json:"source,omitempty"`

	// Synced: the copy caught up with the owner once, so it reads local.
	Synced bool `json:"synced,omitempty"`

	Cursors  map[string]uint64   `json:"cursors,omitempty"`
	Promoted map[string]Promoted `json:"promoted,omitempty"`
}

// LoadState reads the state of the database in dir; ok is false when it
// has none.
func LoadState(dir string) (State, bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, StateFile))

	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}

	if err != nil {
		return State{}, false, err
	}

	var st State

	if err := json.Unmarshal(data, &st); err != nil {
		return State{}, false, fmt.Errorf("%s: %w", StateFile, err)
	}

	return st, true, nil
}

// SaveState writes the state of the database in dir: a temp file, fsync,
// rename, fsync of the dir.
func SaveState(dir string, st State) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}

	tmp := filepath.Join(dir, StateFile+".tmp")

	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	_, err = f.Write(data)

	if err == nil {
		err = f.Sync()
	}

	if cerr := f.Close(); err == nil {
		err = cerr
	}

	if err != nil {
		return err
	}

	if err := os.Rename(tmp, filepath.Join(dir, StateFile)); err != nil {
		return err
	}

	d, err := os.Open(dir)
	if err != nil {
		return err
	}

	defer d.Close()

	return d.Sync()
}

// RemoveState deletes the state of the database in dir.
func RemoveState(dir string) error {
	err := os.Remove(filepath.Join(dir, StateFile))

	if errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}
