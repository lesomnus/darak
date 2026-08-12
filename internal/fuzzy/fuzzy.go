// Package fuzzy scores a filename against a search query.
//
// It is a PORT, not an original. The interface has had this matcher since
// before there was a search route, because filtering the directory you are
// already looking at has to happen without a round trip -- see
// web/src/lib/fuzzy.ts. This copy exists so the recursive search can rank
// server-side and send a few matches instead of twenty thousand names.
//
// Two implementations of one algorithm is a real cost, and the way it is paid
// is testdata/vectors.json: a corpus both test suites read. Neither side can
// change how anything scores without the other side's tests failing. If you are
// editing this file, you are editing that one too.
//
// Everything works in UTF-16 code units rather than runes, because JavaScript
// strings are UTF-16 and the positions returned here are used to highlight
// characters in the browser. Matching runes would make the two agree on every
// name until the first one containing an emoji.
package fuzzy

import (
	"strings"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"
)

// Match is one scored embedding of a query in a name.
type Match struct {
	Score float64 `json:"score"`
	// Positions of the matched characters, as indices into the folded name --
	// which has the same length as the NFC name the browser displays.
	Positions []int `json:"pos"`
}

// Fold puts a name into the shape the matcher compares: NFC, lowercased, as
// UTF-16 code units.
//
// NFC is the part that is not obvious. A file written from a Mac over SMB
// arrives with its Korean name decomposed -- "한" as ㅎ+ㅏ+ㄴ -- and a query
// typed into a browser is composed. Same word, different bytes, no match, and
// nothing on screen to suggest why.
//
// This uses x/text rather than the twenty lines that would compose Hangul
// arithmetically, because the same decomposition happens to `café`. Measured:
// 104 KB on a 9.6 MB binary, for the difference between "Korean works" and
// "Unicode works".
func Fold(s string) []uint16 {
	return utf16.Encode([]rune(strings.ToLower(norm.NFC.String(s))))
}

// FoldString is Fold's input side only, for callers that need the folded text
// rather than the units -- the browser is sent the NFC name so that the
// positions above line up with what it renders.
func NFC(s string) string { return norm.NFC.String(s) }

// separators are the code units after which a new "word" starts:
// space - _ . / ( ) [ ] , '
//
// A match at the beginning of a word is worth far more than one in the middle:
// typing `2026` should find `회의록 2026-08-07.md` ahead of `backup-2026.tar`,
// where the same digits sit inside a longer run.
var separators = map[uint16]bool{
	' ': true, '-': true, '_': true, '.': true, '/': true,
	'(': true, ')': true, '[': true, ']': true, ',': true, '\'': true,
}

func atBoundary(hay []uint16, i int) bool {
	return i == 0 || separators[hay[i-1]]
}

func scoreOf(hay []uint16, positions []int) float64 {
	score := 0
	prev := -2
	for _, at := range positions {
		s := 10
		// A run of adjacent characters is the strongest signal there is: it means
		// the person typed part of the name rather than letters that happen to
		// appear in it.
		if at == prev+1 {
			s += 30
		}
		if atBoundary(hay, at) {
			s += 25
		}
		gap := at
		if prev >= 0 {
			gap = at - prev - 1
		}
		if gap > 20 {
			gap = 20
		}
		s -= gap
		score += s
		prev = at
	}
	// Among equally good matches, the shorter name is the better answer: the
	// query is a larger fraction of it.
	return float64(score) - float64(len(hay))*0.1
}

// index is strings.Index for UTF-16 units.
func index(hay, needle []uint16) int {
	if len(needle) == 0 {
		return 0
	}
	if len(needle) > len(hay) {
		return -1
	}
outer:
	for i := 0; i <= len(hay)-len(needle); i++ {
		for j := range needle {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return i
	}
	return -1
}

// Find scores needle against hay, both already folded. ok is false when the
// characters do not appear in order.
func Find(hay, needle []uint16) (m Match, ok bool) {
	if len(needle) == 0 {
		return Match{Positions: []int{}}, true
	}

	// A contiguous hit is not merely a good fuzzy match, it is a different kind
	// of answer, and it outranks every scattered one. Checked first because it is
	// also the cheapest thing here.
	if at := index(hay, needle); at >= 0 {
		positions := make([]int, len(needle))
		for i := range positions {
			positions[i] = at + i
		}
		capped := at
		if capped > 100 {
			capped = 100
		}
		score := float64(1000 - capped*2)
		if atBoundary(hay, at) {
			score += 200
		}
		return Match{Score: score - float64(len(hay))*0.1, Positions: positions}, true
	}

	// Left-to-right, taking the first place each character fits. This finds the
	// EARLIEST end position, which is what we want to fix; where the characters
	// sit before it is decided by the second pass.
	forward := make([]int, len(needle))
	h := 0
	for i, c := range needle {
		for h < len(hay) && hay[h] != c {
			h++
		}
		if h >= len(hay) {
			return Match{}, false
		}
		forward[i] = h
		h++
	}

	// Then right-to-left from that end, taking the LAST place each character
	// fits. Greedy alone spreads a match across the whole name -- "ab" against
	// "a-x-ab" matches the first `a` and the final `b`, scoring as two isolated
	// characters when there is an adjacent pair sitting right there. Walking back
	// pulls the match as tight as it can be for that ending.
	positions := make([]int, len(needle))
	hi := forward[len(forward)-1]
	for i := len(needle) - 1; i >= 0; i-- {
		for hi >= 0 && hay[hi] != needle[i] {
			hi--
		}
		// Cannot happen -- the forward pass proved an embedding exists -- but a
		// silent -1 index would be a very confusing highlight.
		if hi < 0 {
			return Match{Score: scoreOf(hay, forward), Positions: forward}, true
		}
		positions[i] = hi
		hi--
	}
	return Match{Score: scoreOf(hay, positions), Positions: positions}, true
}

// The nineteen lead consonants, in the order the syllable block encodes them.
// These are the COMPATIBILITY jamo (U+3131…), which is what a Korean IME emits
// when you type a consonant on its own -- the conjoining set (U+1100…) would
// never match anything anyone types.
var lead = []uint16{
	0x3131, 0x3132, 0x3134, 0x3137, 0x3138, 0x3139, 0x3141, 0x3142, 0x3143,
	0x3145, 0x3146, 0x3147, 0x3148, 0x3149, 0x314A, 0x314B, 0x314C, 0x314D, 0x314E,
}

// LeadConsonants replaces every Hangul syllable with its lead consonant and
// leaves everything else alone: `회의록 2026.md` becomes `ㅎㅇㄹ 2026.md`.
//
// This is how Korean speakers actually search. `ㅎㅇㄹ` for 회의록 is not a
// trick, it is the normal way to find a file whose name you know.
//
// The output is the SAME LENGTH as the input, so a match position in it is the
// same position in the name and highlighting needs no second mapping.
func LeadConsonants(s []uint16) []uint16 {
	out := make([]uint16, len(s))
	copy(out, s)
	for i, c := range out {
		if c >= 0xAC00 && c <= 0xD7A3 {
			out[i] = lead[(c-0xAC00)/588]
		}
	}
	return out
}

// HasJamo reports whether a query is worth trying against the lead-consonant
// form at all. `ㅎㅇㄹ` is; `report.pdf` is not, and running a second pass over
// every name to discover that is half the cost of a search for nothing.
func HasJamo(s []uint16) bool {
	for _, c := range s {
		if c >= 0x3131 && c <= 0x3163 {
			return true
		}
	}
	return false
}

// Equal reports whether two folded strings are identical, so a caller can skip
// the lead-consonant pass on a name that has no Hangul in it.
func Equal(a, b []uint16) bool {
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

// Folded is a name with everything the matcher needs, computed once.
type Folded struct {
	// NFC is what gets sent to the browser, so positions line up with it.
	NFC  string
	Fold []uint16
	Lead []uint16
}

func FoldOf(name string) Folded {
	nfc := norm.NFC.String(name)
	f := utf16.Encode([]rune(strings.ToLower(nfc)))
	return Folded{NFC: nfc, Fold: f, Lead: LeadConsonants(f)}
}

// Needle is a query, prepared once per search instead of once per name.
type Needle struct {
	Text []uint16
	// TryLead is whether the lead-consonant pass is worth running at all.
	TryLead bool
}

func NeedleOf(query string) Needle {
	t := Fold(strings.TrimSpace(query))
	return Needle{Text: t, TryLead: HasJamo(t)}
}

// Score is the whole decision for one name: match the folded name, and also its
// lead consonants when the query could be lead consonants, keeping the better.
//
// This is the function the golden vectors pin, and it must stay in step with
// scoreFolded() in web/src/lib/fuzzy.ts.
func Score(f Folded, n Needle) (Match, bool) {
	if len(n.Text) == 0 {
		return Match{}, false
	}
	match, ok := Find(f.Fold, n.Text)
	if n.TryLead && !Equal(f.Lead, f.Fold) {
		if byLead, ok2 := Find(f.Lead, n.Text); ok2 && (!ok || byLead.Score > match.Score) {
			match, ok = byLead, true
		}
	}
	return match, ok
}

// ScoreName is Score without the precomputation, for callers that score one
// name -- the tests, mostly.
func ScoreName(name, query string) (Match, bool) {
	return Score(FoldOf(name), NeedleOf(query))
}
