package vault

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/Thanhbinh1905/secondbrain/internal/frontmatter"
)

// DuplicateIDError reports one id claimed by two files. The tool refuses to
// guess which is canonical, because guessing wrong loses a record silently.
type DuplicateIDError struct {
	ID    string
	Paths []string
}

func (e *DuplicateIDError) Error() string {
	return fmt.Sprintf("id %q is claimed by %d files, and there is no way to tell which is canonical:\n  - %s",
		e.ID, len(e.Paths), strings.Join(e.Paths, "\n  - "))
}

// Walk parses every Markdown record in the vault, in a stable order.
//
// The first malformed file stops the walk and its error is returned verbatim
// (NFR-4). There is no skip-and-continue mode and no scoped mode: a second
// brain a user trusts must not quietly hold a partial view of itself, so
// every command reads every record and corruption anywhere is visible from
// anywhere. Scoping the walk to the one directory a query needs would save
// about twenty milliseconds of the hundred NFR-1 allows, in exchange for
// `today` exiting zero on a vault with a broken file in it. That is not a
// trade worth making.
//
// Files are parsed across all available cores. Results and errors are both
// collected in path order, so a parallel walk answers exactly what a serial
// one would: the same records in the same order, and the same first error.
func (v *Vault) Walk() ([]*Record, error) {
	v.walkMu.Lock()
	defer v.walkMu.Unlock()
	if v.haveWalk {
		return v.walked, v.walkedErr
	}
	records, err := v.walk()
	v.walked, v.walkedErr, v.haveWalk = records, err, true
	return records, err
}

// forgetWalk drops the memoised walk. Every write calls it, so no command can
// read a view of the vault from before its own change.
func (v *Vault) forgetWalk() {
	v.walkMu.Lock()
	defer v.walkMu.Unlock()
	v.walked, v.walkedErr, v.haveWalk = nil, nil, false
}

func (v *Vault) walk() ([]*Record, error) {
	paths, err := v.recordPaths()
	if err != nil {
		return nil, err
	}
	records := make([]*Record, len(paths))
	errs := make([]error, len(paths))

	workers := runtime.GOMAXPROCS(0)
	if workers > len(paths) {
		workers = len(paths)
	}
	if workers < 1 {
		workers = 1
	}
	var wg sync.WaitGroup
	next := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range next {
				rel := paths[i]
				abs := filepath.Join(v.Root, rel)
				raw, err := os.ReadFile(abs)
				if err != nil {
					errs[i] = fmt.Errorf("reading %s: %w", rel, err)
					continue
				}
				rec, err := v.ParseRecord(abs, rel, raw)
				if err != nil {
					errs[i] = err
					continue
				}
				records[i] = rec
			}
		}()
	}
	for i := range paths {
		next <- i
	}
	close(next)
	wg.Wait()

	// The first failure in path order is the one reported, so the same corrupt
	// vault always names the same file.
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	byID := map[string][]string{}
	for _, rec := range records {
		byID[rec.ID] = append(byID[rec.ID], rec.Rel)
	}
	var dupIDs []string
	for id, ps := range byID {
		if len(ps) > 1 {
			dupIDs = append(dupIDs, id)
		}
	}
	if len(dupIDs) > 0 {
		sort.Strings(dupIDs)
		dupPaths := byID[dupIDs[0]]
		sort.Strings(dupPaths)
		return nil, &DuplicateIDError{ID: dupIDs[0], Paths: dupPaths}
	}
	return records, nil
}

// recordPaths lists the vault-relative paths of every Markdown file under the
// record directories, sorted, so two runs see the same vault in the same order.
func (v *Vault) recordPaths() ([]string, error) {
	var out []string
	for _, dir := range RecordDirs {
		root := filepath.Join(v.Root, dir)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && path == root {
					return nil // a vault need not use every directory
				}
				return err
			}
			if d.IsDir() {
				if strings.HasPrefix(d.Name(), ".") && path != root {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(d.Name(), ".md") || strings.HasPrefix(d.Name(), ".") {
				return nil
			}
			rel, err := filepath.Rel(v.Root, path)
			if err != nil {
				return err
			}
			out = append(out, rel)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walking %s: %w", dir, err)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Find loads the single record with this id.
func (v *Vault) Find(id string) (*Record, error) {
	records, err := v.Walk()
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		if r.ID == id {
			return r, nil
		}
	}
	return nil, &NotFoundIDError{ID: id}
}

// NotFoundIDError reports that no record claims this id.
type NotFoundIDError struct{ ID string }

func (e *NotFoundIDError) Error() string {
	return fmt.Sprintf("no record with id %q; run `brain-axi search %s` to look for it by text", e.ID, e.ID)
}

// IDTaken reports whether any record already uses this id. Ids are stable and
// never reused, so add consults this before writing.
func (v *Vault) IDTaken(id string) (string, bool, error) {
	records, err := v.Walk()
	if err != nil {
		return "", false, err
	}
	for _, r := range records {
		if r.ID == id {
			return r.Rel, true, nil
		}
	}
	return "", false, nil
}

// FreeID returns want, or want with the smallest numeric suffix that is free.
// It never reuses an id and never overwrites the record that holds one.
func (v *Vault) FreeID(want string) (string, error) {
	records, err := v.Walk()
	if err != nil {
		return "", err
	}
	taken := map[string]bool{}
	for _, r := range records {
		taken[r.ID] = true
	}
	if !taken[want] {
		return want, nil
	}
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d", want, n)
		if !taken[candidate] {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot find a free id near %q after 999 attempts", want)
}

// WriteFile writes data to a vault-relative path atomically: a temporary file
// in the destination directory, fsynced, then renamed over the target (NFR-3).
// An interrupted write leaves the previous file standing and is never observed
// as a partial file.
func (v *Vault) WriteFile(rel string, data []byte) error {
	v.forgetWalk()
	abs := filepath.Join(v.Root, rel)
	dir := filepath.Dir(abs)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(rel), err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(abs)+".tmp-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file next to %s: %w", rel, err)
	}
	tmpName := tmp.Name()
	// Any failure from here on removes the temporary file, but never touches
	// the file being replaced.
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("writing %s: %w", rel, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return fmt.Errorf("flushing %s to disk: %w", rel, err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("closing %s: %w", rel, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		cleanup()
		return fmt.Errorf("setting permissions on %s: %w", rel, err)
	}
	if err := os.Rename(tmpName, abs); err != nil {
		cleanup()
		return fmt.Errorf("replacing %s: %w", rel, err)
	}
	// Fsync the directory so the rename itself survives a power loss.
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("opening %s to flush the rename: %w", filepath.Dir(rel), err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("flushing the rename of %s: %w", rel, err)
	}
	return nil
}

// Save writes a document back to the file it came from.
func (v *Vault) Save(rel string, doc *frontmatter.Doc) error {
	data, err := doc.Bytes()
	if err != nil {
		return err
	}
	return v.WriteFile(rel, data)
}

// Remove deletes a record's file. rm requires an explicit confirmation flag at
// the CLI layer (FR-8); this function is the mechanism, not the policy.
func (v *Vault) Remove(rel string) error {
	v.forgetWalk()
	if err := os.Remove(filepath.Join(v.Root, rel)); err != nil {
		return fmt.Errorf("removing %s: %w", rel, err)
	}
	return nil
}

// Exists reports whether a vault-relative path is already taken.
func (v *Vault) Exists(rel string) bool {
	_, err := os.Stat(filepath.Join(v.Root, rel))
	return err == nil
}

// FreePath returns rel, or rel with the smallest numeric suffix that is free.
// A capture never silently overwrites a file that is already there.
func (v *Vault) FreePath(rel string) (string, error) {
	if !v.Exists(rel) {
		return rel, nil
	}
	ext := filepath.Ext(rel)
	stem := strings.TrimSuffix(rel, ext)
	for n := 2; n < 1000; n++ {
		candidate := fmt.Sprintf("%s-%d%s", stem, n, ext)
		if !v.Exists(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot find a free filename near %q after 999 attempts", rel)
}
