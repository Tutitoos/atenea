// Package readscope bounds reads made by shipped agents. It is not a process sandbox.
package readscope

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrDirectory identifies a directory reached through the confined handle.
var ErrDirectory = errors.New("assigned path is a directory")

// ReadFile reads at most 8 MiB through a traversal-resistant directory handle.
// Without repository context, only explicitly assigned absolute paths are allowed.
func ReadFile(root, name string, allowed []string) ([]byte, error) {
	if root == "" {
		found := false
		for _, a := range allowed {
			if filepath.Clean(a) == filepath.Clean(name) {
				found = true
			}
		}
		if !found || !filepath.IsAbs(name) {
			return nil, fmt.Errorf("read outside assigned files")
		}
		root = filepath.Dir(name)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	absolute := name
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(absoluteRoot, absolute)
	}
	rel, err := filepath.Rel(absoluteRoot, absolute)
	if err != nil {
		return nil, err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("read outside repository")
	}
	if len(allowed) > 0 {
		found := false
		for _, a := range allowed {
			if !filepath.IsAbs(a) {
				a = filepath.Join(absoluteRoot, a)
			}
			if filepath.Clean(a) == filepath.Clean(absolute) {
				found = true
			}
		}
		if !found {
			return nil, fmt.Errorf("read outside assigned files")
		}
	}
	directory, err := os.OpenRoot(absoluteRoot)
	if err != nil {
		return nil, err
	}
	defer func() { _ = directory.Close() }()
	file, err := openNoFollow(directory, rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, ErrDirectory
	}
	body, err := io.ReadAll(io.LimitReader(file, (8<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 8<<20 {
		return nil, fmt.Errorf("file exceeds 8 MiB read limit")
	}
	return body, nil
}

// openNoFollow opens rel one component at a time and verifies each opened
// handle against its preceding lstat. This rejects symlinks without a gap in
// which a renamed path can redirect the eventual read.
func openNoFollow(directory *os.Root, rel string) (*os.File, error) {
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	current := directory
	for _, part := range parts[:len(parts)-1] {
		info, err := current.Lstat(part)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("read outside assigned files")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(info, opened) {
			_ = next.Close()
			if err != nil {
				return nil, err
			}
			return nil, fmt.Errorf("assigned path changed while opening")
		}
		if current != directory {
			_ = current.Close()
		}
		current = next
	}
	if current != directory {
		defer func() { _ = current.Close() }()
	}
	name := parts[len(parts)-1]
	info, err := current.Lstat(name)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("read outside assigned files")
	}
	file, err := current.Open(name)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		_ = file.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("assigned path changed while opening")
	}
	return file, nil
}
