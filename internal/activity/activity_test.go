package activity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Verbatim full_audit output, captured from Samba 4.22 by mounting a share with
// the kernel cifs client and running echo/mkdir/mv/rm through the mountpoint.
// Written from imagination this fixture would not contain the two traps it
// exists to pin: the TMPNAME dance and create_file firing on plain opens.
const capturedSMB = `alice|127.0.0.1|team-a|create_file|ok|0x80|file|open|/srv/data/teams/team-a
alice|127.0.0.1|team-a|create_file|ok|0x40000080|file|overwrite_if|/srv/data/teams/team-a/notes.txt
alice|127.0.0.1|team-a|mkdirat|ok|/srv/data/teams/team-a/.::TMPNAME:D:4735%11415357240929327586:plans
alice|127.0.0.1|team-a|renameat|ok|/srv/data/teams/team-a/.::TMPNAME:D:4735%11415357240929327586:plans|/srv/data/teams/team-a/plans
alice|127.0.0.1|team-a|create_file|ok|0x100|dir|create|/srv/data/teams/team-a/plans
alice|127.0.0.1|team-a|create_file|ok|0x80|file|open|/srv/data/teams/team-a/plans
alice|127.0.0.1|team-a|create_file|ok|0x10000|file|open|/srv/data/teams/team-a/notes.txt
alice|127.0.0.1|team-a|renameat|ok|/srv/data/teams/team-a/notes.txt|/srv/data/teams/team-a/plans/final.txt
alice|127.0.0.1|team-a|unlinkat|ok|/srv/data/teams/team-a/plans/final.txt`

const root = "/srv/data"

func parseAll(t *testing.T, raw string) []Event {
	t.Helper()
	at := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	var out []Event
	for _, line := range splitLines(raw) {
		if e, ok := ParseAuditLine(line, root, at); ok {
			out = append(out, e)
		}
	}
	return out
}

func splitLines(s string) []string { return strings.Split(s, "\n") }

// The whole session above is four things a person did. Anything more is the
// protocol showing through, and a page that shows it is worse than one that
// does not.
func TestCapturedSessionBecomesFourEvents(t *testing.T) {
	got := parseAll(t, capturedSMB)

	want := []Event{
		{Action: Created, Path: "teams/team-a/notes.txt"},
		{Action: Mkdir, Path: "teams/team-a/plans"},
		{Action: Renamed, Path: "teams/team-a/notes.txt", To: "teams/team-a/plans/final.txt"},
		{Action: Deleted, Path: "teams/team-a/plans/final.txt"},
	}
	if len(got) != len(want) {
		for _, e := range got {
			t.Logf("  %s %s %s", e.Action, e.Path, e.To)
		}
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Action != w.Action || got[i].Path != w.Path || got[i].To != w.To {
			t.Errorf("event %d = {%s %s %s}, want {%s %s %s}",
				i, got[i].Action, got[i].Path, got[i].To, w.Action, w.Path, w.To)
		}
		if got[i].User != "alice" || got[i].Source != SourceSMB || got[i].From != "127.0.0.1" {
			t.Errorf("event %d = %+v, want alice over smb from 127.0.0.1", i, got[i])
		}
	}
}

// Samba makes a directory under a temporary name and renames it into place. A
// reader that trusts the mkdirat reports `.::TMPNAME:D:4735%114…:plans`, which
// names nothing anyone will recognise.
func TestTempNameMkdirIsReportedAtItsRealName(t *testing.T) {
	got := parseAll(t, capturedSMB)

	for _, e := range got {
		if containsTmp(e.Path) || containsTmp(e.To) {
			t.Errorf("a temporary name reached the record: %+v", e)
		}
	}
	var mkdirs []Event
	for _, e := range got {
		if e.Action == Mkdir {
			mkdirs = append(mkdirs, e)
		}
	}
	// Exactly one: the mkdirat is dropped, the rename becomes the event, and the
	// create_file with `dir|create` must not add a second.
	if len(mkdirs) != 1 || mkdirs[0].Path != "teams/team-a/plans" {
		t.Errorf("mkdir events = %+v, want exactly one for teams/team-a/plans", mkdirs)
	}
}

func containsTmp(s string) bool { return strings.Contains(s, tmpNamePrefix) }

// create_file fires on plain opens, including of the share root. Reporting
// those would drown the real changes and would claim someone "created" a file
// they only read.
func TestPlainOpensAreNotChanges(t *testing.T) {
	for _, line := range []string{
		`alice|127.0.0.1|team-a|create_file|ok|0x80|file|open|/srv/data/teams/team-a`,
		`alice|127.0.0.1|team-a|create_file|ok|0x10000|file|open|/srv/data/teams/team-a/notes.txt`,
	} {
		if e, ok := ParseAuditLine(line, root, time.Now()); ok {
			t.Errorf("a plain open was recorded as %s on %s", e.Action, e.Path)
		}
	}
}

// A failed operation changed nothing.
func TestFailedOperationsAreNotRecorded(t *testing.T) {
	line := `bob|10.0.0.5|team-a|unlinkat|fail|/srv/data/teams/team-a/notes.txt`
	if _, ok := ParseAuditLine(line, root, time.Now()); ok {
		t.Error("a failed unlink was recorded as a deletion")
	}
}

// Paths are reported the way the interface addresses them. An absolute
// container path is meaningless to a reader and discloses the layout.
func TestPathsOutsideTheRootAreDropped(t *testing.T) {
	for _, line := range []string{
		`alice|127.0.0.1|x|unlinkat|ok|/etc/shadow`,
		`alice|127.0.0.1|x|unlinkat|ok|/srv/dataother/f.txt`,
		`alice|127.0.0.1|x|unlinkat|ok|/srv/data`,
	} {
		if e, ok := ParseAuditLine(line, root, time.Now()); ok {
			t.Errorf("%q was recorded as %s on %q", line, e.Action, e.Path)
		}
	}
}

// --- store ---

func TestStoreRoundTripsAndFilters(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir, DefaultKeep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	for i, e := range []Event{
		{At: base, User: "alice", Action: Created, Path: "homes/alice/a.txt", Source: SourceWeb},
		{At: base.Add(time.Minute), User: "bob", Action: Deleted, Path: "teams/team-a/b.txt", Source: SourceSMB},
		{At: base.Add(2 * time.Minute), User: "alice", Action: Deleted, Path: "teams/team-a/c.txt", Source: SourceSMB},
	} {
		if err := s.Record(e); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}

	all, err := s.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d events, want 3", len(all))
	}
	// Newest first: the question is almost always about what just happened.
	if all[0].Path != "teams/team-a/c.txt" {
		t.Errorf("first = %s, want the newest", all[0].Path)
	}

	byUser, _ := s.Query(Query{User: "alice"})
	if len(byUser) != 2 {
		t.Errorf("alice's events = %d, want 2", len(byUser))
	}
	byAction, _ := s.Query(Query{Action: Deleted})
	if len(byAction) != 2 {
		t.Errorf("deletions = %d, want 2", len(byAction))
	}
	byPath, _ := s.Query(Query{Path: "team-a"})
	if len(byPath) != 2 {
		t.Errorf("team-a events = %d, want 2", len(byPath))
	}
}

// The window is enforced by the DATE IN THE NAME, not by mtime -- otherwise an
// operator who copies the directory for permanent retention would find their
// backup pruning itself based on when it was copied.
func TestPruneUsesTheDateInTheName(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "2020-01-01.jsonl")
	if err := os.WriteFile(old, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Freshly written, so an mtime-based prune would keep it.
	recent := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(recent, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := NewStore(dir, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Record(Event{User: "alice", Action: Created, Path: "x"}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("a file outside the window survived")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("a file inside the window was pruned: %v", err)
	}
}

// A torn last line after a crash must not hide the events before it.
func TestATruncatedLineDoesNotLoseTheDay(t *testing.T) {
	dir := t.TempDir()
	day := time.Now().UTC().Format("2006-01-02")
	body := `{"at":"2026-08-08T10:00:00Z","user":"alice","action":"delete","path":"a.txt","source":"smb"}
{"at":"2026-08-08T10:01:00Z","user":"bob","action":"delete","pa`
	if err := os.WriteFile(filepath.Join(dir, day+".jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(dir, DefaultKeep)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	got, err := s.Query(Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].User != "alice" {
		t.Errorf("got %+v, want the one intact event", got)
	}
}

// The log framing is Samba's, not ours: a header line then the indented
// payload. Neither half should be mistaken for the other.
func TestExtractPayloadSkipsTheHeader(t *testing.T) {
	header := `[2026/08/08 12:21:13.698792,  1] ../../source3/modules/vfs_full_audit.c:637(do_log)`
	payload := `  alice|127.0.0.1|team-a|unlinkat|ok|/srv/data/teams/team-a/final.txt`

	if _, ok := ExtractPayload(header); ok {
		t.Error("the header was taken for a payload")
	}
	got, ok := ExtractPayload(payload)
	if !ok {
		t.Fatal("the payload was not recognised")
	}
	if got != "alice|127.0.0.1|team-a|unlinkat|ok|/srv/data/teams/team-a/final.txt" {
		t.Errorf("payload = %q", got)
	}
}
