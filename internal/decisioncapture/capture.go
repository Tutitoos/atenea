// Package decisioncapture records opt-in, privacy-screened decision requests.
// The records are candidates for human review, not a representative sample.
package decisioncapture

import (
	"bufio"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/Tutitoos/atenea/internal/buildinfo"
)

// EnvDir is the explicit opt-in directory for private capture.
const EnvDir = "ATENEA_DECISION_CAPTURE_DIR"

// Record is an automatically redacted, unreviewed candidate.
type Record struct {
	ID          string    `json:"id"`
	CapturedAt  time.Time `json:"captured_at"`
	Source      string    `json:"source"`
	Repository  string    `json:"repository"`
	Objective   string    `json:"objective_redacted"`
	Intent      string    `json:"intent"`
	Revision    string    `json:"revision,omitempty"`
	ReviewState string    `json:"review_state"`
}

var (
	email      = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
	url        = regexp.MustCompile(`(?i)\bhttps?://[^\s<>()]+`)
	ip         = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	path       = regexp.MustCompile(`(?:/(?:Users|home|private|var|tmp)/|[A-Za-z]:\\Users\\)[^\s"']+`)
	secret     = regexp.MustCompile(`(?i)\b(?:api[_-]?key|token|password|secret|authorization)\s*[:=]\s*[^\s,;]+`)
	bearer     = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/-]{8,}`)
	knownToken = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,})\b`)
)

// Redact removes common private tokens; human screening is still required.
func Redact(s string) string {
	s = secret.ReplaceAllString(s, "[SECRET]")
	s = bearer.ReplaceAllString(s, "[SECRET]")
	s = knownToken.ReplaceAllString(s, "[SECRET]")
	s = email.ReplaceAllString(s, "[EMAIL]")
	s = url.ReplaceAllString(s, "[URL]")
	s = path.ReplaceAllString(s, "[PATH]")
	s = ip.ReplaceAllString(s, "[IP]")
	return strings.TrimSpace(s)
}

// Capture writes a candidate only when EnvDir is explicitly set.
func Capture(source, repository, objective, intent string) error {
	return CaptureIn(os.Getenv(EnvDir), source, repository, objective, intent)
}

// CaptureIn writes a redacted candidate to a private operator-owned directory.
// It never stores caller-provided paths or credentials as separate fields.
func CaptureIn(dir, source, repository, objective, intent string) error {
	if dir == "" {
		return nil
	}
	if source != "mcp.decision.plan" {
		return errors.New("unsupported capture source")
	}
	if len(objective) == 0 || len(objective) > 8192 {
		return errors.New("invalid capture size")
	}
	if !filepath.IsAbs(dir) {
		return errors.New("capture directory must be absolute")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return errors.New("capture directory unavailable")
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("capture directory must be private")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		return errors.New("capture directory owner mismatch")
	}
	lock, err := privateFile(filepath.Join(dir, "capture.lock"))
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return errors.New("capture lock unavailable")
	}
	defer func() { _ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) }()
	key, err := captureKey(dir)
	if err != nil {
		return err
	}
	f, err := privateFile(filepath.Join(dir, "candidates.private.jsonl"))
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	redacted := Redact(objective)
	if redacted == "" {
		return errors.New("empty redacted objective")
	}
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(source + "\x00" + Redact(repository) + "\x00" + redacted))
	id := hex.EncodeToString(hash.Sum(nil))
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var old struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &old); err != nil {
			return errors.New("capture file invalid")
		}
		if old.ID == id {
			return nil
		}
	}
	if scanner.Err() != nil {
		return errors.New("capture file unreadable")
	}
	if _, err := f.Seek(0, 2); err != nil {
		return errors.New("capture file unavailable")
	}
	revision, modified := buildinfo.Source()
	if modified {
		revision = ""
	}
	record := Record{ID: id, CapturedAt: time.Now().UTC(), Source: source, Repository: Redact(repository), Objective: redacted, Intent: intent, Revision: revision, ReviewState: "automatic_unreviewed"}
	line, err := json.Marshal(record)
	if err != nil {
		return errors.New("capture encoding failed")
	}
	if _, err := fmt.Fprintln(f, string(line)); err != nil {
		return errors.New("capture write failed")
	}
	if err := f.Sync(); err != nil {
		return errors.New("capture sync failed")
	}
	return nil
}

func captureKey(dir string) ([]byte, error) {
	f, err := privateFile(filepath.Join(dir, "capture.key"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	key := make([]byte, 32)
	info, err := f.Stat()
	if err != nil {
		return nil, errors.New("capture key unavailable")
	}
	if info.Size() == 0 {
		if _, err := rand.Read(key); err != nil {
			return nil, errors.New("capture key unavailable")
		}
		if _, err := f.Write(key); err != nil {
			return nil, errors.New("capture key unavailable")
		}
		if err := f.Sync(); err != nil {
			return nil, errors.New("capture key unavailable")
		}
	} else if info.Size() == int64(len(key)) {
		if _, err := io.ReadFull(f, key); err != nil {
			return nil, errors.New("capture key unavailable")
		}
	} else {
		return nil, errors.New("capture key invalid")
	}
	return key, nil
}

func privateFile(name string) (*os.File, error) {
	if info, err := os.Lstat(name); err == nil {
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			return nil, errors.New("capture file must be private")
		}
	} else if !os.IsNotExist(err) {
		return nil, errors.New("capture file unavailable")
	}
	fd, err := syscall.Open(name, syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, errors.New("capture file unavailable")
	}
	f := os.NewFile(uintptr(fd), name)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		_ = f.Close()
		return nil, errors.New("capture file must be private")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Getuid() {
		_ = f.Close()
		return nil, errors.New("capture file owner mismatch")
	}
	return f, nil
}
