// Package sourceidentity creates safe, read-only identities for repository
// contents.  The identity contains no source text: only a physical root and
// digests of the revision, dirty state and untracked files are retained.
package sourceidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Identity is the complete cache subject identity.  Fingerprint changes when
// HEAD, tracked dirty content, staged content, or untracked content changes.
type Identity struct {
	Root        string `json:"root"`
	Head        string `json:"head,omitempty"`
	Fingerprint string `json:"fingerprint"`
	Dirty       bool   `json:"dirty"`
	Untracked   bool   `json:"untracked"`
	Git         bool   `json:"git"`
}

// Discover reads repository state without writing to it.  A non-git root is
// still usable: its regular files are hashed in a deterministic walk.
func Discover(ctx context.Context, root string) (Identity, error) {
	physical, err := physicalRoot(root)
	if err != nil {
		return Identity{}, err
	}
	if physical == "" {
		return Identity{}, nil
	}
	ctx, cancel := boundedContext(ctx)
	defer cancel()

	head, headErr := gitOutput(ctx, physical, "rev-parse", "HEAD")
	status, statusErr := gitOutput(ctx, physical, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	identity := Identity{Root: physical, Git: headErr == nil && statusErr == nil}
	h := sha256.New()
	part(h, "root", physical)
	if identity.Git {
		identity.Head = strings.TrimSpace(head)
		part(h, "head", identity.Head)
		part(h, "status", status)
		paths := dirtyPaths([]byte(status))
		sort.Strings(paths)
		for _, rel := range paths {
			if strings.HasPrefix(rel, "../") || filepath.IsAbs(rel) {
				continue
			}
			if strings.HasPrefix(statusPathStatus([]byte(status), rel), "??") {
				identity.Untracked = true
			}
			if err := hashFile(h, physical, rel); err != nil {
				return Identity{}, err
			}
		}
		identity.Dirty = len(strings.TrimSpace(strings.ReplaceAll(status, "\x00", ""))) > 0
		identity.Fingerprint = hex.EncodeToString(h.Sum(nil))
		return identity, nil
	}

	var paths []string
	err = filepath.Walk(physical, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(physical, path)
		if relErr != nil {
			return relErr
		}
		if rel == "." {
			return nil
		}
		if info.IsDir() && rel == ".git" {
			return filepath.SkipDir
		}
		if info.Mode().IsRegular() {
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return Identity{}, err
	}
	sort.Strings(paths)
	for _, rel := range paths {
		if err := hashFile(h, physical, filepath.FromSlash(rel)); err != nil {
			return Identity{}, err
		}
	}
	identity.Fingerprint = hex.EncodeToString(h.Sum(nil))
	return identity, nil
}

func physicalRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		// Workflows that intentionally have no repository retain the historical
		// empty fingerprint. They cannot participate in a repository result
		// cache, but creating them must remain compatible.
		return "", nil
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("source identity: repository root is not a directory")
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func boundedContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, 15*time.Second)
}

func gitOutput(ctx context.Context, root string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

func dirtyPaths(status []byte) []string {
	seen := map[string]bool{}
	for offset := 0; offset < len(status); {
		rest := status[offset:]
		n := bytes.IndexByte(rest, 0)
		if n < 0 {
			n = len(rest)
		}
		entry := string(rest[:n])
		offset += n + 1
		if len(entry) < 4 {
			continue
		}
		addPath(seen, entry[3:])
		if entry[0] == 'R' || entry[0] == 'C' {
			if offset < len(status) {
				rest = status[offset:]
				n = bytes.IndexByte(rest, 0)
				if n < 0 {
					n = len(rest)
				}
				addPath(seen, string(rest[:n]))
				offset += n + 1
			}
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	return out
}

func addPath(seen map[string]bool, raw string) {
	path := strings.TrimSpace(raw)
	if path == "" || filepath.IsAbs(path) || path == "." || strings.HasPrefix(path, "../") {
		return
	}
	seen[filepath.Clean(path)] = true
}

func statusPathStatus(status []byte, rel string) string {
	for offset := 0; offset < len(status); {
		rest := status[offset:]
		n := bytes.IndexByte(rest, 0)
		if n < 0 {
			n = len(rest)
		}
		entry := string(rest[:n])
		offset += n + 1
		if len(entry) >= 4 && strings.TrimSpace(entry[3:]) == rel {
			return entry[:2]
		}
	}
	return ""
}

func hashFile(h io.Writer, root, rel string) error {
	clean := filepath.Clean(rel)
	full := filepath.Join(root, clean)
	info, err := os.Lstat(full)
	if err != nil {
		if os.IsNotExist(err) {
			part(h, "file", filepath.ToSlash(clean))
			part(h, "missing", "true")
			return nil
		}
		return err
	}
	part(h, "file", filepath.ToSlash(clean))
	part(h, "mode", info.Mode().String())
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return err
		}
		part(h, "symlink", target)
		return nil
	}
	if !info.Mode().IsRegular() {
		part(h, "special", info.Mode().String())
		return nil
	}
	f, err := os.Open(full)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	digest := sha256.New()
	if _, err := io.Copy(digest, f); err != nil {
		return err
	}
	part(h, "content", hex.EncodeToString(digest.Sum(nil)))
	return nil
}

func part(w io.Writer, key, value string) { _, _ = fmt.Fprintf(w, "%s\x00%s\x00", key, value) }
