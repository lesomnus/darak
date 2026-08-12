package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// searchLines runs a search and returns the hits plus the terminal line.
func searchLines(t *testing.T, h *harness, c *http.Cookie, target string) ([]searchHit, map[string]any) {
	t.Helper()
	rec := h.do("GET", target, nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s = %d %s", target, rec.Code, rec.Body)
	}
	var hits []searchHit
	var done map[string]any
	for _, line := range strings.Split(strings.TrimSpace(rec.Body.String()), "\n") {
		if line == "" {
			continue
		}
		var probe map[string]any
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("not JSON: %q: %v", line, err)
		}
		if _, ok := probe["done"]; ok {
			done = probe
			continue
		}
		if done != nil {
			t.Fatal("a hit arrived after the done line")
		}
		var hit searchHit
		if err := json.Unmarshal([]byte(line), &hit); err != nil {
			t.Fatal(err)
		}
		hits = append(hits, hit)
	}
	if done == nil {
		t.Fatal("no done line; a client cannot tell a finished search from a dropped one")
	}
	return hits, done
}

func mustWrite(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFindsBelowTheStartingDirectory(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	home := filepath.Join(h.root, "homes", "alice")
	mustWrite(t, filepath.Join(home, "회의록 2026-08-07.md"))
	mustWrite(t, filepath.Join(home, "기획", "2026 예산.xlsx"))
	mustWrite(t, filepath.Join(home, "기획", "초안", "발표자료.pdf"))
	mustWrite(t, filepath.Join(home, "notes.txt"))

	hits, done := searchLines(t, h, c, "/api/search/homes/alice?q=2026")
	var paths []string
	for _, hit := range hits {
		paths = append(paths, hit.Path)
	}
	want := map[string]bool{"회의록 2026-08-07.md": true, "기획/2026 예산.xlsx": true}
	if len(paths) != len(want) {
		t.Fatalf("paths = %v, want %v", paths, want)
	}
	for _, p := range paths {
		if !want[p] {
			t.Errorf("unexpected hit %q", p)
		}
	}
	// The path is relative to what was searched, because that is what the
	// interface has to append to its own location to navigate there.
	for _, hit := range hits {
		if strings.HasPrefix(hit.Path, "/") || strings.HasPrefix(hit.Path, "homes/") {
			t.Errorf("path %q is not relative to the search root", hit.Path)
		}
	}
	if done["truncated"] != false {
		t.Errorf("truncated = %v, want false: the tree is four files", done["truncated"])
	}
	if v, _ := done["visited"].(float64); v < 4 {
		t.Errorf("visited = %v, want at least 4", done["visited"])
	}

	// Lead consonants reach down the tree too, not only into the folder you are
	// standing in.
	hits, _ = searchLines(t, h, c, "/api/search/homes/alice?q="+urlEscape("ㅂㅍ"))
	if len(hits) != 1 || hits[0].Path != "기획/초안/발표자료.pdf" {
		t.Errorf("ㅂㅍ found %v, want 기획/초안/발표자료.pdf", hits)
	}
}

// The score and the highlight come from the server. If they are absent the
// browser cannot rank or mark anything, and the results look arbitrary.
func TestSearchReportsScoreAndPositions(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	mustWrite(t, filepath.Join(h.root, "homes", "alice", "notes.txt"))

	hits, _ := searchLines(t, h, c, "/api/search/homes/alice?q=notes")
	if len(hits) != 1 {
		t.Fatalf("hits = %v", hits)
	}
	if hits[0].Score <= 0 {
		t.Errorf("score = %v", hits[0].Score)
	}
	if got := hits[0].Pos; len(got) != 5 || got[0] != 0 || got[4] != 4 {
		t.Errorf("pos = %v, want the five characters of a contiguous hit at 0", got)
	}
	if hits[0].Name != "notes.txt" {
		t.Errorf("name = %q", hits[0].Name)
	}
}

// A deleted file that shows up next to live ones, looking identical, is worse
// than not finding it. Same for a crashed upload.
func TestSearchSkipsTrashAndPartialUploads(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	home := filepath.Join(h.root, "homes", "alice")
	mustWrite(t, filepath.Join(home, "report.pdf"))
	mustWrite(t, filepath.Join(home, ".trash", "report.pdf"))
	mustWrite(t, filepath.Join(home, ".upload-abc-report.pdf"))

	hits, _ := searchLines(t, h, c, "/api/search/homes/alice?q=report")
	if len(hits) != 1 || hits[0].Path != "report.pdf" {
		var got []string
		for _, x := range hits {
			got = append(got, x.Path)
		}
		t.Errorf("hits = %v, want only report.pdf", got)
	}
}

// One or two characters match most of a tree as a subsequence. Requiring them
// to be adjacent is a rule someone can predict; a score cutoff is not, because
// the numbers are not comparable between queries of different lengths.
func TestSearchShortQueriesMustBeContiguous(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	home := filepath.Join(h.root, "homes", "alice")
	mustWrite(t, filepath.Join(home, "ab.txt"))    // contiguous "ab"
	mustWrite(t, filepath.Join(home, "a-x-b.txt")) // scattered "a...b"

	hits, _ := searchLines(t, h, c, "/api/search/homes/alice?q=ab")
	for _, hit := range hits {
		if hit.Path == "a-x-b.txt" {
			t.Error("a two-character query matched a scattered subsequence")
		}
	}
	if len(hits) == 0 {
		t.Error("the contiguous one was dropped too")
	}

	// Three characters go back to fuzzy.
	hits, _ = searchLines(t, h, c, "/api/search/homes/alice?q=axb")
	if len(hits) != 1 || hits[0].Path != "a-x-b.txt" {
		t.Errorf("axb found %v", hits)
	}
}

// A budget that is reached has to be announced. A silently shortened list reads
// as "it is not here", which is the opposite answer.
func TestSearchSaysWhenItGaveUp(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	home := filepath.Join(h.root, "homes", "alice")
	// Deeper than searchDepth, with the target at the bottom.
	deep := home
	for i := 0; i < searchDepth+3; i++ {
		deep = filepath.Join(deep, "d")
	}
	mustWrite(t, filepath.Join(deep, "buried.txt"))

	hits, done := searchLines(t, h, c, "/api/search/homes/alice?q=buried")
	if len(hits) != 0 {
		t.Errorf("hits = %v, want none below the depth limit", hits)
	}
	if done["truncated"] != true {
		t.Error("the depth limit stopped the walk and the response did not say so")
	}
}

func TestSearchRefusesWhatItCannotSearch(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	mustWrite(t, filepath.Join(h.root, "homes", "alice", "a.txt"))

	for _, tt := range []struct {
		name, target string
		want         int
	}{
		{"no query", "/api/search/homes/alice", http.StatusBadRequest},
		{"blank query", "/api/search/homes/alice?q=%20%20", http.StatusBadRequest},
		{"no path", "/api/search/?q=x", http.StatusBadRequest},
		{"a file, not a directory", "/api/search/homes/alice/a.txt?q=abc", http.StatusBadRequest},
		{"missing", "/api/search/homes/alice/nope?q=abc", http.StatusNotFound},
	} {
		if rec := h.do("GET", tt.target, nil, c); rec.Code != tt.want {
			t.Errorf("%s: %s = %d, want %d (%s)", tt.name, tt.target, rec.Code, tt.want, rec.Body)
		}
	}
	// And the whole route is behind the session, like everything else.
	if rec := h.do("GET", "/api/search/homes/alice?q=abc", nil, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated = %d, want 401", rec.Code)
	}
}

// A directory that cannot be read is normal, not a failure: `teams/` lists
// every team and you are a member of two. The search has to skip it and carry
// on, which is what walking there by hand would do.
func TestSearchSkipsUnreadableDirectories(t *testing.T) {
	h := newHarness(t, fakeAuth{ok: true})
	c := h.login("alice")
	closed := filepath.Join(h.root, "teams", "closed")
	mustWrite(t, filepath.Join(closed, "secret-plan.txt"))
	mustWrite(t, filepath.Join(h.root, "teams", "design", "open-plan.txt"))
	if err := os.Chmod(closed, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(closed, 0o755) })
	if os.Geteuid() == 0 {
		t.Skip("running as root: the mode would not be enforced")
	}

	hits, done := searchLines(t, h, c, "/api/search/teams?q=plan")
	var paths []string
	for _, hit := range hits {
		paths = append(paths, hit.Path)
	}
	// The closed directory's own NAME is visible -- teams/ is listable -- but
	// nothing inside it is.
	for _, p := range paths {
		if strings.HasPrefix(p, "closed/") {
			t.Errorf("leaked %q from a directory the user cannot read", p)
		}
	}
	if len(paths) == 0 {
		t.Error("one unreadable directory stopped the whole search")
	}
	if done["error"] != nil {
		t.Errorf("reported an error for an unreadable directory: %v", done["error"])
	}
}
