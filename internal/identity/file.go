//darak:local-state
//
// This file resolves two paths, which the rest of the server must never do. The
// exemption is the same one internal/share/file.go carries and rests on the same
// two facts: the paths come from command-line flags rather than from a request,
// and they name server state rather than anything in the served tree. No user
// owns them, so there is no helper that could open them — which is precisely why
// they live outside the data volume, as nas-design.md §7 requires.
//
// See internal/lint for the check this marker is read by.

package identity

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// NewFileStore loads the approved mappings from path, or starts empty if there
// is no file yet, and persists every change back to it.
//
// A missing file is the normal first run: nobody has been approved. A malformed
// one is an error, because starting empty would tell every approved person that
// their address is unknown, and the fix — restoring the file — is not something
// they can do from the login page.
func NewFileStore(path string) (*Store, error) {
	s := NewStore()

	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := s.Load(data); err != nil {
			return nil, fmt.Errorf("identity: %s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return nil, fmt.Errorf("identity: %s: %w", path, err)
	}

	s.Save = func([]Mapping) error {
		data, err := s.Marshal()
		if err != nil {
			return err
		}
		return replace(path, ".identities-*.json", data)
	}
	return s, nil
}

// NewFileQueue loads the pending requests from path.
//
// Unlike the mapping store, a broken file here is a warning rather than a
// refusal: the queue grants nothing, and the alternative is letting unreviewed
// input decide whether the server starts. The caller gets the error and decides;
// cmd/darak logs it and carries on with an empty queue.
func NewFileQueue(path string) (*Queue, error) {
	q := NewQueue()

	var loadErr error
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		loadErr = q.Load(data)
	case errors.Is(err, os.ErrNotExist):
	default:
		loadErr = err
	}

	q.Save = func([]Request) error {
		data, err := q.Marshal()
		if err != nil {
			return err
		}
		return replace(path, ".pending-*.json", data)
	}
	if loadErr != nil {
		return q, fmt.Errorf("identity: %s: %w", path, loadErr)
	}
	return q, nil
}

// JournalEntry is one change to who may sign in as whom.
type JournalEntry struct {
	At time.Time `json:"at"`
	// By is the administrator who made the change, or empty when the server did
	// it on its own — pinning a subject at first sign-in is the server's doing,
	// and reads wrong attributed to whoever last touched the page.
	By string `json:"by,omitempty"`
	// Action is approve, forget, pin, attach or discard.
	Action  string   `json:"action"`
	Account string   `json:"account,omitempty"`
	Issuer  string   `json:"issuer,omitempty"`
	Subject string   `json:"subject,omitempty"`
	Emails  []string `json:"emails,omitempty"`
}

// Journal is an append-only record of every change to the mappings.
//
// This is what the decision to keep the mapping out of git costs, bought back.
// A roster entry answers "who added this, and when" through `git blame`; a file
// the application rewrites answers it not at all — the previous contents are
// simply gone. Since every line of that file is a statement about who may sign
// in as whom, the history has to exist somewhere, and this is it.
//
// It is JSONL in the same directory as the store, for the reason the activity
// log is: copying the directory is the whole of backing it up.
type Journal struct {
	mu   sync.Mutex
	path string
}

// NewJournal opens (or creates) the journal at path.
func NewJournal(path string) (*Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("identity: %s: %w", filepath.Dir(path), err)
	}
	return &Journal{path: path}, nil
}

// Append records one change.
//
// Failure is returned rather than swallowed, but callers treat it as a warning:
// refusing an approval because the note about it could not be written would
// make the audit trail able to take the feature down.
func (j *Journal) Append(e JournalEntry) error {
	if j == nil {
		return nil
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}

	j.mu.Lock()
	defer j.mu.Unlock()
	f, err := os.OpenFile(j.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("identity: %s: %w", j.path, err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// Tail returns the most recent entries, newest first.
func (j *Journal) Tail(n int) ([]JournalEntry, error) {
	if j == nil || n <= 0 {
		return []JournalEntry{}, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()

	f, err := os.Open(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return []JournalEntry{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("identity: %s: %w", j.path, err)
	}
	defer f.Close()

	all := []JournalEntry{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e JournalEntry
		// A damaged line is skipped rather than failing the read: this is a
		// history, and one bad record must not hide the rest of it.
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		all = append(all, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if len(all) > n {
		all = all[len(all)-n:]
	}
	out := make([]JournalEntry, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		out = append(out, all[i])
	}
	return out, nil
}

// replace writes data to path atomically.
//
// Temp file then rename, for the reason the write protocol uses it: a partial
// write leaves a truncated JSON document, and the next start would refuse to
// load it — turning a crash mid-save into every approval being lost.
func replace(path, pattern string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("identity: %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op once the rename has happened

	// 0600: this file decides who may sign in as whom. It is not a secret in the
	// way a token is, but anything that can write it can grant access.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	// Durability before visibility: the rename must not be able to publish a name
	// whose contents are still in the page cache.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
