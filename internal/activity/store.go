//darak:local-state — this file opens two OPERATOR-CONFIGURED paths: the
// activity directory it owns, and smbd's log, which it only ever reads. Neither
// is derived from a request and neither is in the served tree, so the rule the
// lint enforces (the server never resolves a path a REQUEST supplied) is not
// what is happening here. Every other file in this package is pure.

package activity

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store keeps a rolling window of events as one JSONL file per day.
//
// Per-day files rather than one growing file, and JSONL rather than a database,
// because of what retention is here: the window is short and permanent
// retention is a backup's job. Copying a directory of dated text files is
// something an operator can do with the tools they already have, and can read
// years later without this program.
type Store struct {
	dir    string
	keep   time.Duration
	now    func() time.Time
	mu     sync.Mutex
	day    string
	file   *os.File
	writer *bufio.Writer
}

// DefaultKeep is the rolling window.
const DefaultKeep = 30 * 24 * time.Hour

// NewStore opens (and creates) the activity directory.
func NewStore(dir string, keep time.Duration) (*Store, error) {
	if dir == "" {
		return nil, errors.New("activity: a directory is required")
	}
	if keep <= 0 {
		keep = DefaultKeep
	}
	// 0700: an audit record names who did what, and the people it names have no
	// business reading it.
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("activity: create %s: %w", dir, err)
	}
	return &Store{dir: dir, keep: keep, now: time.Now}, nil
}

// Record appends an event.
//
// Failures are returned, not swallowed, but every caller treats them as
// non-fatal: a file operation that succeeded must not be reported as failed
// because the note about it could not be written. The caller logs instead.
func (s *Store) Record(e Event) error {
	if e.At.IsZero() {
		e.At = s.now()
	}
	line, err := e.MarshalLine()
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.rollLocked(e.At); err != nil {
		return err
	}
	if _, err := s.writer.Write(line); err != nil {
		return err
	}
	// Flushed per event rather than buffered: the reason to look at this file is
	// usually that something went wrong, and events still sitting in a buffer
	// when the process died are the ones that would have explained it.
	return s.writer.Flush()
}

// rollLocked switches to the file for the event's day, pruning old ones.
func (s *Store) rollLocked(at time.Time) error {
	day := at.UTC().Format("2006-01-02")
	if s.file != nil && s.day == day {
		return nil
	}
	if s.file != nil {
		s.writer.Flush()
		s.file.Close()
		s.file, s.writer = nil, nil
	}
	f, err := os.OpenFile(filepath.Join(s.dir, day+".jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("activity: open the day's file: %w", err)
	}
	s.file, s.writer, s.day = f, bufio.NewWriter(f), day
	s.pruneLocked(at)
	return nil
}

// pruneLocked deletes files outside the window.
//
// By the DATE IN THE NAME, not by mtime: an operator who copies the directory
// for permanent retention would otherwise find their backup deciding what to
// delete based on when it was copied.
func (s *Store) pruneLocked(now time.Time) {
	cutoff := now.UTC().Add(-s.keep).Format("2006-01-02")
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		if day := strings.TrimSuffix(name, ".jsonl"); day < cutoff {
			_ = os.Remove(filepath.Join(s.dir, name))
		}
	}
}

// Close flushes and closes the current file.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	s.writer.Flush()
	err := s.file.Close()
	s.file, s.writer = nil, nil
	return err
}

// Query returns matching events, newest first.
//
// Read newest day first and stopped as soon as the limit is reached, so the
// common case — "what happened recently" — does not read a month of files.
func (s *Store) Query(q Query) ([]Event, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}

	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("activity: read %s: %w", s.dir, err)
	}
	days := []string{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			days = append(days, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))

	// Flush first: an event recorded a moment ago should be visible to the page
	// that is asking about it.
	s.mu.Lock()
	if s.writer != nil {
		s.writer.Flush()
	}
	s.mu.Unlock()

	out := []Event{}
	for _, name := range days {
		if !q.Since.IsZero() {
			day := strings.TrimSuffix(name, ".jsonl")
			if day < q.Since.UTC().Format("2006-01-02") {
				break
			}
		}
		es, err := readDay(filepath.Join(s.dir, name), q)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
		if len(out) >= limit {
			break
		}
	}
	sortNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func readDay(path string, q Query) ([]Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil // pruned between the listing and here
		}
		return nil, err
	}
	defer f.Close()

	out := []Event{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		e, err := ParseLine(line)
		if err != nil {
			// A torn last line after a crash is not a reason to refuse the
			// whole day. Everything before it is still true.
			continue
		}
		if q.matches(e) {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// --- smbd log tailer ---

// Tail follows smbd's log and records the audit events in it.
//
// It runs until ctx ends. A missing log is not an error: smbd creates it when
// the first client connects, which on a quiet server can be much later than
// startup.
//
// Reading from the END on the first open, so a restart does not re-record
// however many days of history the log happens to hold. The cost is that events
// during a darak restart are missed, which is the right trade for a rolling
// window whose purpose is "who touched my file recently".
func (s *Store) Tail(done <-chan struct{}, logPath, root string, onErr func(error)) {
	var (
		f      *os.File
		reader *bufio.Reader
		size   int64
	)
	defer func() {
		if f != nil {
			f.Close()
		}
	}()

	openLog := func() bool {
		fi, err := os.Stat(logPath)
		if err != nil {
			return false
		}
		nf, err := os.Open(logPath)
		if err != nil {
			return false
		}
		if f != nil {
			f.Close()
		}
		f, size = nf, fi.Size()
		// Skip what is already there.
		if _, err := f.Seek(size, io.SeekStart); err != nil {
			f.Close()
			f = nil
			return false
		}
		reader = bufio.NewReader(f)
		return true
	}

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-done:
			return
		case <-tick.C:
		}

		if f == nil && !openLog() {
			continue
		}
		// Rotation: the file shrank or was replaced, so what we hold is the old
		// one. Reopen from the start of the new file — unlike the first open,
		// here everything in it is genuinely new.
		if fi, err := os.Stat(logPath); err != nil || fi.Size() < size {
			f.Close()
			f = nil
			size = 0
			continue
		}

		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				break
			}
			size += int64(len(line))
			payload, ok := ExtractPayload(line)
			if !ok {
				continue
			}
			e, ok := ParseAuditLine(payload, root, time.Now())
			if !ok {
				continue
			}
			if err := s.Record(e); err != nil && onErr != nil {
				onErr(err)
			}
		}
	}
}
