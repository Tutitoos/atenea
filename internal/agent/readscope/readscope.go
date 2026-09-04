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
	file, err := directory.Open(rel)
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
