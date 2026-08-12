/**
 * Fuzzy matching for the search box.
 *
 * The scoring is not a general-purpose fuzzy library. It is tuned for one kind
 * of string: filenames, which are short, full of separators, and often Korean.
 *
 * It runs against every entry on every keystroke, in a directory that may hold
 * 50,000 of them, so the inner loops compare CHARACTER CODES rather than
 * one-character strings -- measured at half the time for the same result.
 *
 * THERE IS A SECOND COPY OF THIS, in Go, at internal/fuzzy. The in-folder
 * filter has to run without a round trip, and the recursive search has to rank
 * server-side so it can send a few matches instead of twenty thousand names --
 * so the algorithm exists in both places. What stops them drifting is
 * internal/fuzzy/testdata/vectors.json, a corpus both test suites read.
 * Changing how anything here scores will fail the Go tests, and it should.
 */

export interface Match {
  score: number
  /**
   * Indices of the matched characters, for highlighting.
   *
   * Into the string that was passed in -- see `alignable` at the call site for
   * why that is not always the string being displayed.
   */
  positions: number[]
}

/**
 * Character codes after which a new "word" starts: space - _ . / ( ) [ ] , '
 *
 * A match at the beginning of a word is worth far more than one in the middle:
 * typing `2026` should find `회의록 2026-08-07.md` ahead of `backup-2026.tar`,
 * where the same digits sit inside a longer run.
 */
const SEPARATORS = new Set([32, 45, 95, 46, 47, 40, 41, 91, 93, 44, 39])

function atBoundary(hay: string, i: number): boolean {
  return i === 0 || SEPARATORS.has(hay.charCodeAt(i - 1))
}

/**
 * Scores one embedding of the needle in `hay`.
 *
 * Kept separate from finding the embedding because the search below finds two
 * of them and keeps the better one.
 */
function scoreOf(hay: string, positions: number[]): number {
  let score = 0
  let prev = -2
  for (let k = 0; k < positions.length; k++) {
    const at = positions[k] as number
    let s = 10
    // A run of adjacent characters is the strongest signal there is: it means
    // the person typed part of the name rather than letters that happen to
    // appear in it.
    if (at === prev + 1) s += 30
    if (atBoundary(hay, at)) s += 25
    // How much was skipped to get here. Bounded, or one enormous gap would sink
    // a match that is otherwise good.
    const gap = prev < 0 ? at : at - prev - 1
    s -= gap < 20 ? gap : 20
    score += s
    prev = at
  }
  // Among equally good matches, the shorter name is the better answer: the
  // query is a larger fraction of it.
  return score - hay.length * 0.1
}

/**
 * Matches a needle against `hay`, both already folded by the caller.
 *
 * @param needle the query, for the contiguous check
 * @param codes  the same query as character codes, hoisted out of the loop over
 *               entries because it is the same for every one of them
 *
 * Returns null when the characters do not appear in order.
 */
export function fuzzyMatch(hay: string, needle: string, codes: number[]): Match | null {
  if (codes.length === 0) return { score: 0, positions: [] }

  // A contiguous hit is not merely a good fuzzy match, it is a different kind
  // of answer, and it outranks every scattered one. Checked first because it is
  // also the cheapest thing here.
  const at = hay.indexOf(needle)
  if (at >= 0) {
    const positions: number[] = new Array(needle.length)
    for (let i = 0; i < needle.length; i++) positions[i] = at + i
    let score = 1000 - (at < 100 ? at : 100) * 2
    if (atBoundary(hay, at)) score += 200
    return { score: score - hay.length * 0.1, positions }
  }

  // Left-to-right, taking the first place each character fits. This finds the
  // EARLIEST end position, which is what we want to fix; where the characters
  // sit before it is decided by the second pass.
  const len = hay.length
  const n = codes.length
  const forward: number[] = new Array(n)
  let h = 0
  for (let i = 0; i < n; i++) {
    const c = codes[i] as number
    while (h < len && hay.charCodeAt(h) !== c) h++
    if (h >= len) return null
    forward[i] = h
    h++
  }

  // Then right-to-left from that end, taking the LAST place each character
  // fits. Greedy alone spreads a match across the whole name -- "ab" against
  // "a-x-ab" matches the first `a` and the final `b`, scoring as two isolated
  // characters when there is an adjacent pair sitting right there. Walking back
  // pulls the match as tight as it can be for that ending.
  const positions: number[] = new Array(n)
  let hi = forward[n - 1] as number
  for (let i = n - 1; i >= 0; i--) {
    const c = codes[i] as number
    while (hi >= 0 && hay.charCodeAt(hi) !== c) hi--
    // Cannot happen -- the forward pass proved an embedding exists -- but a
    // silent -1 index would be a very confusing highlight.
    if (hi < 0) return { score: scoreOf(hay, forward), positions: forward }
    positions[i] = hi
    hi--
  }
  return { score: scoreOf(hay, positions), positions }
}

// The nineteen lead consonants, in the order the syllable block encodes them.
// These are the COMPATIBILITY jamo (U+3131…), which is what a Korean IME emits
// when you type a consonant on its own -- the conjoining set (U+1100…) would
// never match anything anyone types.
const LEAD = 'ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ'

/**
 * Replaces every Hangul syllable with its lead consonant, leaving the rest
 * alone: `회의록 2026.md` becomes `ㅎㅇㄹ 2026.md`.
 *
 * This is how Korean speakers actually search. `ㅎㅇㄹ` for 회의록 is not a
 * trick, it is the normal way to find a file whose name you know, and it is the
 * single biggest thing a fuzzy matcher can do for a Korean file list.
 *
 * The output is the SAME LENGTH as the input -- one syllable in, one jamo out,
 * everything else passed through -- so a match position in this string is the
 * same position in the name, and highlighting works without a second mapping.
 */
export function leadConsonants(s: string): string {
  let out = ''
  for (const ch of s) {
    const c = ch.codePointAt(0) as number
    if (c >= 0xac00 && c <= 0xd7a3) out += LEAD[Math.floor((c - 0xac00) / 588)]
    else out += ch
  }
  return out
}

/**
 * Whether a query is worth trying against the lead-consonant form at all.
 *
 * `ㅎㅇㄹ` is; `report.pdf` is not, and running a second full pass over 50,000
 * names to discover that is half the cost of a keystroke for nothing. True when
 * the query contains any compatibility jamo (U+3131–U+3163).
 */
export function hasJamo(s: string): boolean {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i)
    if (c >= 0x3131 && c <= 0x3163) return true
  }
  return false
}

/**
 * Puts a name and a typed query into the same shape.
 *
 * NFC is the part that is not obvious. A file written from a Mac over SMB
 * arrives with its Korean name decomposed -- "한" as ㅎ+ㅏ+ㄴ -- and a name
 * typed into the search box is composed. The two are the same word and
 * different strings, so without this, half the files on a mixed-platform share
 * are unfindable by typing their names, with nothing on screen to suggest why.
 */
export function fold(s: string): string {
  return s.normalize('NFC').toLocaleLowerCase('ko')
}

/** A name with everything the matcher needs, computed once per listing. */
export interface Folded {
  /** NFC, which is what gets DISPLAYED so positions line up with it. */
  nfc: string
  fold: string
  lead: string
  /**
   * Whether folding preserved length, and so whether positions can be trusted.
   * Case folding is 1:1 for everything in a Korean or English filename, but not
   * universally -- 'İ' lowercases to two characters.
   */
  alignable: boolean
}

export function foldOf(name: string): Folded {
  const nfc = name.normalize('NFC')
  const f = fold(name)
  return { nfc, fold: f, lead: leadConsonants(f), alignable: f.length === nfc.length }
}

/** A query, prepared once per keystroke instead of once per entry. */
export interface Needle {
  text: string
  codes: number[]
  /** Whether the lead-consonant pass is worth running at all. */
  tryLead: boolean
}

export function needleOf(query: string): Needle {
  const text = fold(query.trim())
  const codes: number[] = []
  for (let i = 0; i < text.length; i++) codes.push(text.charCodeAt(i))
  return { text, codes, tryLead: hasJamo(text) }
}

/**
 * The whole decision for one name: match the folded name, and also its lead
 * consonants when the query could be lead consonants, keeping the better.
 *
 * This is the function the golden vectors pin, and the one the listing calls in
 * its inner loop -- deliberately the same one, so a fast path and a tested path
 * cannot be two different behaviours.
 */
export function scoreFolded(f: Folded, n: Needle): Match | null {
  if (n.text === '') return null
  let match = fuzzyMatch(f.fold, n.text, n.codes)
  if (n.tryLead && f.lead !== f.fold) {
    const byLead = fuzzyMatch(f.lead, n.text, n.codes)
    if (byLead && (!match || byLead.score > match.score)) match = byLead
  }
  return match
}

/** scoreFolded without the precomputation, for callers that score one name. */
export function scoreName(name: string, query: string): Match | null {
  return scoreFolded(foldOf(name), needleOf(query))
}
