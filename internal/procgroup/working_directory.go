package procgroup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// BindWorkingDirectory applies an explicit cwd before spawn. Empty preserves
// inheritance; an alias or unavailable directory is never treated as a binding.
func BindWorkingDirectory(cmd *exec.Cmd, directory string) error {
	if directory == "" {
		return nil
	}
	canonical, err := filepath.EvalSymlinks(directory)
	info, statErr := os.Stat(directory)
	if err != nil || statErr != nil || !info.IsDir() || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || canonical != directory {
		return fmt.Errorf("working_directory must be an existing canonical absolute directory")
	}
	cmd.Dir = directory
	return nil
}
