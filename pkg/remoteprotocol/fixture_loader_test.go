package remoteprotocol

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"
	"testing"
	"testing/fstest"

	protocolv1 "github.com/Tutitoos/atenea/protocol/atenea.remote.v1"
)

type discoveredFixture struct {
	name string
	raw  []byte
}

type discoveredInvalidFixture struct {
	category ErrorCategory
	caseID   string
	name     string
	raw      []byte
}

// discoverFixtureCorpus reads a complete fixture directory from an injected
// filesystem. Keeping the directory as an fs.FS name makes the loader
// deterministic and portable across embedded and in-memory filesystems.
func discoverFixtureCorpus(fsys fs.FS, corpusDir string) ([]discoveredFixture, error) {
	entries, err := fs.ReadDir(fsys, corpusDir)
	if err != nil {
		return nil, fmt.Errorf("read fixture root %q: %w", corpusDir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("fixture root is empty: %s", corpusDir)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

	fixtures := make([]discoveredFixture, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".json") {
			return nil, fmt.Errorf("non-JSON entry in fixture root: %s", name)
		}
		info, err := entry.Info()
		if err != nil {
			return nil, fmt.Errorf("fixture %s info: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("fixture %s is not a regular file", name)
		}
		if info.Mode().Perm()&0444 == 0 {
			return nil, fmt.Errorf("fixture %s is unreadable", name)
		}

		raw, err := fs.ReadFile(fsys, path.Join(corpusDir, name))
		if err != nil {
			return nil, fmt.Errorf("fixture %s: %w", name, err)
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("fixture %s is empty", name)
		}
		fixtures = append(fixtures, discoveredFixture{name: name, raw: raw})
	}
	return fixtures, nil
}

// discoverInvalidFixtureCorpus treats the category directory as the expected
// error contract. The allowlist and required directory set are deliberately
// supplied by the test inventory so a new validator category cannot silently
// become an undocumented fixture classification.
func discoverInvalidFixtureCorpus(fsys fs.FS, corpusDir string, required []ErrorCategory) ([]discoveredInvalidFixture, error) {
	entries, err := fs.ReadDir(fsys, corpusDir)
	if err != nil {
		return nil, fmt.Errorf("read invalid fixture root %q: %w", corpusDir, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("invalid fixture root is empty: %s", corpusDir)
	}

	allowed := make(map[ErrorCategory]struct{}, len(required))
	for _, category := range required {
		if category == "" {
			return nil, fmt.Errorf("invalid fixture allowlist contains empty category")
		}
		if _, duplicate := allowed[category]; duplicate {
			return nil, fmt.Errorf("invalid fixture allowlist duplicates category %q", category)
		}
		allowed[category] = struct{}{}
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	seenCategories := make(map[ErrorCategory]struct{}, len(entries))
	seenIDs := make(map[string]string)
	var fixtures []discoveredInvalidFixture
	for _, categoryEntry := range entries {
		category := ErrorCategory(categoryEntry.Name())
		if !categoryEntry.IsDir() {
			return nil, fmt.Errorf("invalid fixture root entry is not a category directory: %s", categoryEntry.Name())
		}
		if _, ok := allowed[category]; !ok {
			return nil, fmt.Errorf("unknown invalid fixture category: %s", categoryEntry.Name())
		}
		if _, duplicate := seenCategories[category]; duplicate {
			return nil, fmt.Errorf("duplicate invalid fixture category: %s", category)
		}
		seenCategories[category] = struct{}{}

		files, err := fs.ReadDir(fsys, path.Join(corpusDir, categoryEntry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read invalid fixture category %q: %w", category, err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("invalid fixture category is empty: %s", category)
		}
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		for _, fileEntry := range files {
			name := fileEntry.Name()
			if path.Ext(name) != ".json" || strings.TrimSuffix(name, ".json") == "" {
				return nil, fmt.Errorf("non-JSON invalid fixture entry in %s: %s", category, name)
			}
			info, err := fileEntry.Info()
			if err != nil {
				return nil, fmt.Errorf("invalid fixture %s/%s info: %w", category, name, err)
			}
			if !info.Mode().IsRegular() {
				return nil, fmt.Errorf("invalid fixture %s/%s is not a regular file", category, name)
			}
			if info.Mode().Perm()&0444 == 0 {
				return nil, fmt.Errorf("invalid fixture %s/%s is unreadable", category, name)
			}
			raw, err := fs.ReadFile(fsys, path.Join(corpusDir, categoryEntry.Name(), name))
			if err != nil {
				return nil, fmt.Errorf("invalid fixture %s/%s: %w", category, name, err)
			}
			if len(raw) == 0 {
				return nil, fmt.Errorf("invalid fixture %s/%s is empty", category, name)
			}
			caseID := strings.TrimSuffix(name, ".json")
			if previous, duplicate := seenIDs[caseID]; duplicate {
				return nil, fmt.Errorf("duplicate invalid fixture case ID %q in %s and %s/%s", caseID, previous, category, name)
			}
			seenIDs[caseID] = path.Join(string(category), name)
			fixtures = append(fixtures, discoveredInvalidFixture{category: category, caseID: caseID, name: name, raw: raw})
		}
	}
	for _, category := range required {
		if _, ok := seenCategories[category]; !ok {
			return nil, fmt.Errorf("missing invalid fixture category directory: %s", category)
		}
	}
	return fixtures, nil
}

func TestDiscoverFixtureCorpus(t *testing.T) {
	tests := []struct {
		name     string
		fsys     fs.FS
		wantText string
	}{
		{name: "successful control", fsys: reversedReadDirFS{ReadDirFS: fstest.MapFS{
			"fixtures/valid/b.json": {Data: []byte(`{"b":true}`), Mode: 0444},
			"fixtures/valid/a.json": {Data: []byte(`{"a":true}`), Mode: 0444},
		}}},
		{name: "missing", fsys: fstest.MapFS{}, wantText: "read fixture root"},
		{name: "empty", fsys: fstest.MapFS{
			"fixtures/valid": {Mode: fs.ModeDir | 0755},
		}, wantText: "fixture root is empty: fixtures/valid"},
		{name: "non-JSON", fsys: fstest.MapFS{
			"fixtures/valid/readme.txt": {Data: []byte("not a fixture"), Mode: 0444},
		}, wantText: "non-JSON entry in fixture root: readme.txt"},
		{name: "directory", fsys: fstest.MapFS{
			"fixtures/valid/directory.json": {Mode: fs.ModeDir | 0755},
		}, wantText: "fixture directory.json is not a regular file"},
		{name: "nonregular", fsys: fstest.MapFS{
			"fixtures/valid/device.json": {Data: []byte("not a regular file"), Mode: fs.ModeNamedPipe | 0666},
		}, wantText: "fixture device.json is not a regular file"},
		{name: "unreadable", fsys: fstest.MapFS{
			"fixtures/valid/unreadable.json": {Data: []byte(`{"secret":true}`), Mode: 0000},
		}, wantText: "fixture unreadable.json is unreadable"},
		{name: "empty file", fsys: fstest.MapFS{
			"fixtures/valid/empty.json": {Mode: 0444},
		}, wantText: "fixture empty.json is empty"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixtures, err := discoverFixtureCorpus(test.fsys, "fixtures/valid")
			if test.name == "successful control" {
				if err != nil {
					t.Fatalf("discoverFixtureCorpus failed: %v", err)
				}
				if got := []string{fixtures[0].name, fixtures[1].name}; got[0] != "a.json" || got[1] != "b.json" {
					t.Fatalf("fixture order = %v, want [a.json b.json]", got)
				}
				return
			}
			if err == nil {
				t.Fatalf("discoverFixtureCorpus accepted %s corpus: %#v", test.name, fixtures)
			}
			if !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("discoverFixtureCorpus error = %q, want stable text fragment %q", err, test.wantText)
			}
		})
	}
}

func TestDiscoverEmbeddedFixtureCorporaAreOrderIndependent(t *testing.T) {
	orderedValid, err := discoverFixtureCorpus(protocolv1.FS, "fixtures/valid")
	if err != nil {
		t.Fatalf("discover ordered valid corpus: %v", err)
	}
	reversed := reversedReadDirFS{ReadDirFS: protocolv1.FS}
	reversedValid, err := discoverFixtureCorpus(reversed, "fixtures/valid")
	if err != nil {
		t.Fatalf("discover reversed valid corpus: %v", err)
	}
	if !reflect.DeepEqual(orderedValid, reversedValid) {
		t.Fatal("valid fixture discovery depends on directory enumeration order")
	}

	orderedInvalid, err := discoverInvalidFixtureCorpus(protocolv1.FS, "fixtures/invalid", invalidFixtureFileCategories)
	if err != nil {
		t.Fatalf("discover ordered invalid corpus: %v", err)
	}
	reversedInvalid, err := discoverInvalidFixtureCorpus(reversed, "fixtures/invalid", invalidFixtureFileCategories)
	if err != nil {
		t.Fatalf("discover reversed invalid corpus: %v", err)
	}
	if !reflect.DeepEqual(orderedInvalid, reversedInvalid) {
		t.Fatal("invalid fixture discovery depends on directory enumeration order")
	}
}

func TestEmbeddedFixtureTreeIncludesNonJSONCanary(t *testing.T) {
	const (
		canaryPath = "fixtures/embed-probe/non-json.txt"
		want       = "recursive fixture embed probe\n"
	)

	got, err := fs.ReadFile(protocolv1.FS, canaryPath)
	if err != nil {
		t.Fatalf("read embedded canary %q: %v", canaryPath, err)
	}
	if string(got) != want {
		t.Fatalf("embedded canary = %q, want exact content %q", got, want)
	}
}

type failingReadFS struct {
	fs.FS
	name string
	err  error
}

func (failing failingReadFS) Open(name string) (fs.File, error) {
	file, err := failing.FS.Open(name)
	if err != nil || name != failing.name {
		return file, err
	}
	return failingReadFile{File: file, err: failing.err}, nil
}

type failingReadFile struct {
	fs.File
	err error
}

func (f failingReadFile) Read([]byte) (int, error) { return 0, f.err }

type failingEntryInfoFS struct {
	fs.FS
	directory string
	entry     string
	err       error
}

func (f failingEntryInfoFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.directory {
		return file, err
	}
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return file, nil
	}
	return failingEntryInfoDir{ReadDirFile: directory, entry: f.entry, err: f.err}, nil
}

type failingEntryInfoDir struct {
	fs.ReadDirFile
	entry string
	err   error
}

func (f failingEntryInfoDir) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := f.ReadDirFile.ReadDir(n)
	if err != nil {
		return nil, err
	}
	for index, entry := range entries {
		if entry.Name() == f.entry {
			entries[index] = failingEntryInfo{DirEntry: entry, err: f.err}
		}
	}
	return entries, nil
}

type failingEntryInfo struct {
	fs.DirEntry
	err error
}

func (f failingEntryInfo) Info() (fs.FileInfo, error) { return nil, f.err }

type failingReadDirFS struct {
	fs.FS
	directory string
	err       error
}

func (f failingReadDirFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.directory {
		return file, err
	}
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return file, nil
	}
	return failingReadDirFile{ReadDirFile: directory, err: f.err}, nil
}

type failingReadDirFile struct {
	fs.ReadDirFile
	err error
}

func (f failingReadDirFile) ReadDir(int) ([]fs.DirEntry, error) { return nil, f.err }

type reversedReadDirFS struct {
	fs.ReadDirFS
}

func (f reversedReadDirFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := f.ReadDirFS.ReadDir(name)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	return entries, nil
}

type duplicateEntryFS struct {
	fs.FS
	directory string
	entry     string
}

func (f duplicateEntryFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open(name)
	if err != nil || name != f.directory {
		return file, err
	}
	directory, ok := file.(fs.ReadDirFile)
	if !ok {
		return file, nil
	}
	return duplicateEntryDir{ReadDirFile: directory, entry: f.entry}, nil
}

type duplicateEntryDir struct {
	fs.ReadDirFile
	entry string
}

func (f duplicateEntryDir) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := f.ReadDirFile.ReadDir(n)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name() == f.entry {
			return append(entries, entry), nil
		}
	}
	return entries, nil
}

func TestDiscoverFixtureCorpusRejectsReadFailure(t *testing.T) {
	base := fstest.MapFS{
		"fixtures/valid/failing.json": {Data: []byte(`{"failure":true}`), Mode: 0444},
	}
	wantErr := errors.New("injected read failure")
	_, err := discoverFixtureCorpus(failingReadFS{FS: base, name: "fixtures/valid/failing.json", err: wantErr}, "fixtures/valid")
	if err == nil {
		t.Fatal("discoverFixtureCorpus accepted injected read failure")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("discoverFixtureCorpus error = %v, want injected read failure", err)
	}
	if !strings.Contains(err.Error(), "fixture failing.json") {
		t.Fatalf("discoverFixtureCorpus error = %q, want fixture context", err)
	}
}

func TestDiscoverFixtureCorpusPreservesEntryInfoFailure(t *testing.T) {
	base := fstest.MapFS{
		"fixtures/valid/info-failure.json": {Data: []byte(`{"info":true}`), Mode: 0444},
	}
	wantErr := errors.New("injected Entry.Info failure")
	_, err := discoverFixtureCorpus(failingEntryInfoFS{
		FS: base, directory: "fixtures/valid", entry: "info-failure.json", err: wantErr,
	}, "fixtures/valid")
	if err == nil {
		t.Fatal("discoverFixtureCorpus accepted injected Entry.Info failure")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("discoverFixtureCorpus error = %v, want injected Entry.Info failure", err)
	}
	if !strings.Contains(err.Error(), "fixture info-failure.json info") {
		t.Fatalf("discoverFixtureCorpus error = %q, want fixture info context", err)
	}
}
