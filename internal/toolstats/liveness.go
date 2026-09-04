package toolstats

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
)

// deadOwners holds read-only shared locks before the statistics snapshot starts.
// This ensures a writer's final commits precede the snapshot if it closed normally.
// Missing lock files are unknown, never evidence that an event failed.
func deadOwners(ctx context.Context, db *sql.DB, path string) (string, func(), error) {
	locks := []*os.File{}
	release := func() {
		for _, f := range locks {
			_ = f.Close()
		}
	}
	var exists int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='event_owners'`).Scan(&exists); err != nil {
		return "", release, err
	}
	if exists == 0 {
		return "[]", release, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT DISTINCT owner FROM event_owners JOIN events ON events.id=event_owners.event WHERE ended IS NULL`)
	if err != nil {
		return "", release, err
	}
	var owners []string
	for rows.Next() {
		var owner string
		if err = rows.Scan(&owner); err != nil {
			_ = rows.Close()
			return "", release, err
		}
		owners = append(owners, owner)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return "", release, err
	}
	dead := []string{}
	for _, owner := range owners {
		if err = ctx.Err(); err != nil {
			return "", release, err
		}
		if !validOwner(owner) {
			return "", release, fmt.Errorf("invalid stats writer identifier")
		}
		lock, e := inspectOwner(path + "." + owner + ".lock")
		if e != nil {
			if ownerBusy(e) || os.IsNotExist(e) {
				continue
			}
			return "", release, e
		}
		locks = append(locks, lock)
		dead = append(dead, owner)
	}
	encoded, err := json.Marshal(dead)
	return string(encoded), release, err
}
