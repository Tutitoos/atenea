package toolstats

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// privateStorage rejects shared directories and symlinks before creating SQLite files.
func privateStorage(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("stats directory must be private (0700): %s", dir)
	}
	for _, name := range []string{path, path + "-wal", path + "-shm"} {
		info, err := os.Lstat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("stats storage must be a regular file: %s", name)
		}
		if err := os.Chmod(name, 0600); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}

// recoverOwners closes only events whose writer lock can be acquired exclusively.
// Ownerless events from earlier versions become unknown after the retention window.
func (s *Store) recoverOwners(tx *sql.Tx, now time.Time, cut int64) error {
	rows, err := tx.Query(`SELECT id FROM owners`)
	if err != nil {
		return err
	}
	var owners []string
	for rows.Next() {
		var owner string
		if err = rows.Scan(&owner); err != nil {
			_ = rows.Close()
			return err
		}
		owners = append(owners, owner)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	recovered := int64(0)
	for _, owner := range owners {
		// Never turn persisted data into an arbitrary filesystem path.
		if !validOwner(owner) {
			return fmt.Errorf("invalid stats writer identifier")
		}
		path := s.Path + "." + owner + ".lock"
		lock, err := lockOwner(path)
		if err != nil {
			if ownerBusy(err) {
				continue
			}
			return err
		}
		result, err := tx.Exec(`UPDATE events SET ended=?, duration=-1, outcome='fail', code='recording_interrupted', reason='Writer ended without a recorded result; execution outcome and duration are unknown.' WHERE ended IS NULL AND id IN (SELECT event FROM event_owners WHERE owner=?)`, now.UnixMicro(), owner)
		if err == nil {
			var n int64
			n, err = result.RowsAffected()
			recovered += n
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM owners WHERE id=?`, owner)
		}
		// Maintenance transactions serialize recovery. A rolled-back owner can
		// safely recreate its unlocked file on the next recovery attempt.
		_ = lock.Close()
		if err == nil {
			err = os.Remove(path)
		}

		if err != nil {
			return err
		}
	}
	result, err := tx.Exec(`UPDATE events SET ended=?, duration=-1, outcome='fail', code='recording_interrupted', reason='Legacy event has no writer identity or recorded result; outcome and duration are unknown.' WHERE ended IS NULL AND at<? AND id NOT IN (SELECT event FROM event_owners)`, now.UnixMicro(), cut)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	recovered += n
	if recovered > 0 {
		_, err = tx.Exec(`INSERT INTO meta VALUES('recovered',?) ON CONFLICT(key) DO UPDATE SET value=value+excluded.value`, recovered)
	}
	return err
}

// validOwner accepts only generated UUID identifiers for writer lock filenames.
func validOwner(owner string) bool {
	if len(owner) != 36 {
		return false
	}
	for i, c := range owner {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
			continue
		}
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
