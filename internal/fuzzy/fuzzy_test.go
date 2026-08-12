package fuzzy

import (
	"encoding/json"
	"os"
	"testing"
)

// The whole point of this file.
//
// This matcher exists twice -- here and in web/src/lib/fuzzy.ts -- because the
// in-folder filter has to run without a round trip and the recursive search has
// to rank before it sends. Two implementations of one algorithm drift, and when
// this one drifts the symptom is that the same query ranks differently
// depending on whether you searched a folder or searched below it, which is the
// kind of thing that gets reported as "search is weird" and never diagnosed.
//
// The corpus is generated FROM the TypeScript side (web/scripts/
// gen-fuzzy-vectors.ts), so this asserts that Go agrees with what already
// ships. Regenerating it to make this pass would be exactly backwards.
type vector struct {
	Name  string  `json:"name"`
	Query string  `json:"query"`
	Match bool    `json:"match"`
	Score float64 `json:"score"`
	Pos   []int   `json:"pos"`
}

func loadVectors(t *testing.T) []vector {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Cases []vector `json:"cases"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Cases) == 0 {
		t.Fatal("no vectors; regenerate with web/scripts/gen-fuzzy-vectors.ts")
	}
	return doc.Cases
}

func TestAgreesWithTheBrowser(t *testing.T) {
	for _, v := range loadVectors(t) {
		m, ok := ScoreName(v.Name, v.Query)
		if ok != v.Match {
			t.Errorf("%q / %q: matched=%v, the browser says %v", v.Name, v.Query, ok, v.Match)
			continue
		}
		if !ok {
			continue
		}
		// Exact, not approximate. Both sides accumulate integers and subtract
		// len*0.1 in float64, in the same order, so the doubles are identical --
		// and a tolerance here would hide precisely the small scoring changes
		// this test exists to catch.
		if m.Score != v.Score {
			t.Errorf("%q / %q: score %v, the browser says %v", v.Name, v.Query, m.Score, v.Score)
		}
		if !equalInts(m.Positions, v.Pos) {
			t.Errorf("%q / %q: positions %v, the browser says %v", v.Name, v.Query, m.Positions, v.Pos)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// The properties the vectors encode, stated once in words so a failure above
// can be read as something other than "a number changed".
func TestRanking(t *testing.T) {
	better := func(query, win, lose string) {
		t.Helper()
		a, ok1 := ScoreName(win, query)
		b, ok2 := ScoreName(lose, query)
		if !ok1 || !ok2 {
			t.Fatalf("%q: expected both to match (%v, %v)", query, ok1, ok2)
		}
		if a.Score <= b.Score {
			t.Errorf("%q: %q scored %v, not above %q at %v", query, win, a.Score, lose, b.Score)
		}
	}
	// A word boundary beats the middle of a run.
	better("2026", "회의록 2026-08-07.md", "backup-2026-08.tar.gz")
	// Contiguous beats scattered.
	better("notes", "notes.txt", "no-one-tells-esther.txt")
	// The shorter name wins a tie.
	better("report", "report.pdf", "report-draft-final.pdf")
}

func TestLeadConsonantSearch(t *testing.T) {
	for _, tt := range []struct{ name, query string }{
		{"회의록 2026-08-07.md", "ㅎㅇㄹ"},
		{"발표자료.pdf", "ㅂㅍ"},
		{"예산안.xlsx", "ㅇㅅㅇ"},
	} {
		if _, ok := ScoreName(tt.name, tt.query); !ok {
			t.Errorf("%q did not find %q", tt.query, tt.name)
		}
	}
	// And it must not turn into a wildcard: a consonant that is not there is
	// still not there.
	if _, ok := ScoreName("예산안.xlsx", "ㅎㅎㅎ"); ok {
		t.Error("ㅎㅎㅎ matched 예산안.xlsx")
	}
}

// A name written by a Mac over SMB arrives decomposed. If that does not fold to
// the same thing a browser sends, half a mixed-platform share is unsearchable
// and nothing on screen says why.
func TestDecomposedNamesMatch(t *testing.T) {
	nfd := "회의록 2026.md" // 회의록, decomposed
	m, ok := ScoreName(nfd, "회의록")
	if !ok {
		t.Fatal("a decomposed Korean name did not match its composed query")
	}
	if want, _ := ScoreName("회의록 2026.md", "회의록"); m.Score != want.Score {
		t.Errorf("decomposed scored %v, composed %v", m.Score, want.Score)
	}
}

// Positions index UTF-16 code units, because that is what a JavaScript string
// is and what the browser slices to draw the highlight. An emoji is two of
// them; counting runes here would silently mark the wrong characters.
func TestPositionsAreUTF16(t *testing.T) {
	m, ok := ScoreName("🎉 파티.txt", "파티")
	if !ok {
		t.Fatal("no match")
	}
	if len(m.Positions) != 2 || m.Positions[0] != 3 {
		t.Errorf("positions %v, want the match to start at code unit 3", m.Positions)
	}
}
