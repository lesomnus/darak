package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/lesomnus/darak/internal/fuzzy"
	"github.com/lesomnus/darak/internal/vfs"
	"github.com/lesomnus/darak/internal/wire"
	"golang.org/x/sys/unix"
)

// Search bounds. Every one of them is a promise that a request ends.
const (
	// searchDepth is how far below the starting directory to look. Eight levels
	// is deeper than anybody's filing, and it stops a symlink loop or a build
	// tree from becoming the whole budget.
	searchDepth = 8
	// searchVisit caps entries EXAMINED. It must be examined rather than
	// returned: the query that finds nothing is the one that walks furthest.
	searchVisit = 20000
	// searchMatches caps what is sent. Past a thousand, nobody is reading the
	// list; they are going to type more.
	searchMatches = 1000
	// searchDeadline is the wall clock. The walk is round trips to a helper, so
	// this is what actually holds under a cold cache on a slow disk.
	searchDeadline = 2 * time.Second

	// batchSize and batchWait are how often results are flushed.
	//
	// Not per match, and the reason is compression rather than the interface:
	// every Flush makes gzip emit a sync marker, and doing that for a 60-byte
	// line spends more than the line costs. Batching also keeps the browser from
	// re-rendering a hundred times a second.
	batchSize = 64
	batchWait = 120 * time.Millisecond

	// shortQuery is where fuzzy stops being useful. One or two characters match
	// most of a tree as a subsequence, so below this the query must appear
	// contiguously -- a rule a person can predict, unlike a score threshold,
	// whose numbers are not comparable across queries of different lengths.
	shortQuery = 3
)

// searchHit is one line of the response.
type searchHit struct {
	Path string `json:"path"`
	// Name is the entry's own name in NFC, which is what the browser renders and
	// what Pos indexes.
	Name  string  `json:"name"`
	Dir   bool    `json:"dir"`
	Score float64 `json:"score"`
	// Pos are the matched characters, as UTF-16 code unit offsets into Name.
	Pos []int `json:"pos"`
}

// handleSearch walks below a directory and streams the names that match.
//
//	GET /api/search/<path>?q=<query>
//
// NDJSON: one JSON object per line, ending with a line that says how it
// finished.
//
// Streaming rather than one document because the walk is the half that grows
// with the data. Measured warm on a local disk it is fast -- 20,000 entries
// across 200 directories in about 35ms, against ~1ms of matching -- but that
// number is the best case it will ever have: a cold cache, a spinning disk or
// the shared filesystem this is meant to sit on later all cost round trips per
// directory, and those are seconds. A stream is what makes that a list filling
// in rather than a page that hangs.
//
// Matching happens HERE rather than in the browser, even though the browser has
// the same matcher, because the alternative is shipping every name it examined:
// twenty thousand of them, to show thirty. The cost of that decision is that
// the matcher exists twice; see internal/fuzzy.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	user, p := userOf(r), requestPath(r, "/api/search/")
	if p == "" {
		writeError(w, http.StatusBadRequest, "no path")
		return
	}
	query := r.URL.Query().Get("q")
	needle := fuzzy.NeedleOf(query)
	if len(needle.Text) == 0 {
		writeError(w, http.StatusBadRequest, "no query")
		return
	}
	// Below three characters, only a contiguous hit counts. Enforced by asking
	// the matcher for a score and rejecting the scattered ones, so the rule lives
	// in one place rather than being re-derived per call site.
	exactOnly := len(needle.Text) < shortQuery

	// The starting point has to be a directory, and saying so up front turns
	// "searching a file" into a 400 instead of an empty result nobody can
	// explain. It also surfaces 403 and 404 with the status they deserve, which
	// a stream that has already sent its headers cannot do.
	st, err := s.cfg.FS.Stat(r.Context(), user, p)
	if err != nil {
		writeFSError(w, err)
		return
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR {
		writeError(w, http.StatusBadRequest, "not a directory")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), searchDeadline)
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson; charset=utf-8")
	// A search result is about the state of a directory a second ago. Nothing
	// downstream should keep it.
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	pending := 0
	lastFlush := time.Now()
	flush := func() {
		pending = 0
		lastFlush = time.Now()
		if flusher != nil {
			flusher.Flush()
		}
	}

	sent := 0
	var writeErr error
	res, err := s.cfg.FS.Walk(ctx, user, p, vfs.WalkLimits{Depth: searchDepth, Visit: searchVisit},
		func(rel string, e wire.DirEntry) bool {
			folded := fuzzy.FoldOf(e.Name)
			m, ok := fuzzy.Score(folded, needle)
			if !ok {
				return true
			}
			if exactOnly && !contiguous(m.Positions) {
				return true
			}
			if err := enc.Encode(searchHit{
				Path:  rel,
				Name:  folded.NFC,
				Dir:   e.Type == unix.DT_DIR,
				Score: m.Score,
				Pos:   m.Positions,
			}); err != nil {
				// The client hung up. Stop walking rather than finishing a search
				// nobody is reading.
				writeErr = err
				return false
			}
			sent++
			pending++
			if pending >= batchSize || time.Since(lastFlush) >= batchWait {
				flush()
			}
			return sent < searchMatches
		})
	if writeErr != nil {
		return
	}

	// The last line is the only place "there is nothing there" and "I gave up"
	// can be told apart, and they are opposite answers to the same question.
	done := map[string]any{
		"done":      true,
		"visited":   res.Visited,
		"matches":   sent,
		"truncated": res.Truncated || sent >= searchMatches,
	}
	if err != nil {
		// Mid-stream, with a 200 already sent. The status line cannot say this, so
		// the last line does.
		done["error"] = err.Error()
	}
	_ = enc.Encode(done)
	flush()
}

// contiguous reports whether the matched characters are adjacent, which is what
// a one- or two-character query has to be to count.
func contiguous(pos []int) bool {
	for i := 1; i < len(pos); i++ {
		if pos[i] != pos[i-1]+1 {
			return false
		}
	}
	return true
}
