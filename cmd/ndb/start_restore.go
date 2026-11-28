package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// restoreMark is the file in the data dir with the NDB_RESTORE_ID that was
// restored last.
const restoreMark = ".restore-id"

// startRestore restores the backup of NDB_RESTORE_URL into the data dir
// before the daemon opens it, when NDB_RESTORE_ID is not the id of the last
// restore. It replaces what the data dir holds. The same id again does
// nothing, so a restart never restores twice. A failed download or a bad
// file leaves the data dir as it was, and the daemon does not start.
//
// For a host that starts the daemon from its env only (Ndb Cloud): a
// restore into the same server, or a new server from a backup.
func startRestore(dataDir string, lookup func(string) (string, bool), log *slog.Logger) error {
	url, _ := lookup("NDB_RESTORE_URL")

	if url == "" {
		return nil
	}

	id, _ := lookup("NDB_RESTORE_ID")

	if id == "" {
		return errors.New("NDB_RESTORE_URL needs NDB_RESTORE_ID, the name of this restore")
	}

	mark := filepath.Join(dataDir, restoreMark)

	if last, err := os.ReadFile(mark); err == nil && strings.TrimSpace(string(last)) == id {
		log.Info("restore skipped: done before", "restore_id", id)

		return nil
	}

	log.Info("restore started", "restore_id", id)

	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("restore %s: %w", id, err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("restore %s: the backup URL answered %s", id, resp.Status)
	}

	summary, err := restoreInto(dataDir, resp.Body, true)
	if err != nil {
		return fmt.Errorf("restore %s: %w", id, err)
	}

	if err := os.WriteFile(mark, []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("restore %s: %w", id, err)
	}

	log.Info("restore done", "restore_id", id, "restored", summary)

	return nil
}
