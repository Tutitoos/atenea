package core

import (
	"os"
	"path/filepath"
)

func kivgraphMaintenanceDirectory() string {
	directory, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(directory, "atenea", "kivgraph-maintenance")
}
