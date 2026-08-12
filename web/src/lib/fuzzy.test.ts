import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { scoreName, foldOf, needleOf, scoreFolded } from './fuzzy.ts'

/**
 * The other half of the contract.
 *
 * internal/fuzzy is a port of this file, and internal/fuzzy/testdata/
 * vectors.json is what stops the two drifting. That file is generated FROM here
 * (web/scripts/gen-fuzzy-vectors.ts), so on its own it would only prove this
 * code equals itself -- which is why the Go suite reads the same file. What
 * this side is for is catching a change to fuzzy.ts that was never regenerated:
 * edit the scoring, and these fail before anyone gets as far as Go.
 */
const vectors = JSON.parse(
  readFileSync(new URL('../../../internal/fuzzy/testdata/vectors.json', import.meta.url), 'utf8'),
) as {
  cases: { name: string; query: string; match: boolean; score?: number; pos?: number[] }[]
}

test('scores match the checked-in vectors', () => {
  assert.ok(vectors.cases.length > 0, 'no vectors')
  for (const v of vectors.cases) {
    const m = scoreName(v.name, v.query)
    assert.equal(m !== null, v.match, `${v.name} / ${v.query}: matched`)
    if (!v.match || m === null) continue
    assert.equal(m.score, v.score, `${v.name} / ${v.query}: score`)
    assert.deepEqual(m.positions, v.pos, `${v.name} / ${v.query}: positions`)
  }
})

// The listing does not call scoreName -- it precomputes the fold once per entry
// and calls scoreFolded per keystroke. If those two ever stop agreeing, the
// tested path and the shipped path are different code.
test('the precomputed path agrees with the convenience one', () => {
  for (const v of vectors.cases.slice(0, 120)) {
    const a = scoreName(v.name, v.query)
    const b = scoreFolded(foldOf(v.name), needleOf(v.query))
    assert.deepEqual(b, a, `${v.name} / ${v.query}`)
  }
})

test('a word boundary outranks the middle of a run', () => {
  const a = scoreName('회의록 2026-08-07.md', '2026')
  const b = scoreName('backup-2026-08.tar.gz', '2026')
  assert.ok(a && b && a.score > b.score)
})

test('contiguous outranks scattered', () => {
  const a = scoreName('notes.txt', 'notes')
  const b = scoreName('no-one-tells-esther.txt', 'notes')
  assert.ok(a && b && a.score > b.score)
})

test('lead consonants find Korean names', () => {
  assert.ok(scoreName('회의록 2026-08-07.md', 'ㅎㅇㄹ'))
  assert.ok(scoreName('발표자료.pdf', 'ㅂㅍ'))
  assert.ok(scoreName('예산안.xlsx', 'ㅇㅅㅇ'))
  // Not a wildcard: a consonant that is not there is still not there.
  assert.equal(scoreName('예산안.xlsx', 'ㅎㅎㅎ'), null)
})

test('a decomposed name matches a composed query', () => {
  const nfd = '회의록 2026.md'.normalize('NFD')
  const a = scoreName(nfd, '회의록')
  const b = scoreName('회의록 2026.md', '회의록')
  assert.ok(a && b)
  assert.equal(a.score, b.score)
})

// Positions index UTF-16 code units, which is what a JavaScript string is and
// what FileRow slices to draw the highlight. An emoji is two of them.
test('positions are UTF-16 code units', () => {
  const m = scoreName('🎉 파티.txt', '파티')
  assert.ok(m)
  assert.deepEqual(m.positions, [3, 4])
})

// The highlight only renders when folding preserved length; this is the case
// that makes it not.
test('a name whose case folding changes length is marked unalignable', () => {
  assert.equal(foldOf('İstanbul.txt').alignable, false)
  assert.equal(foldOf('회의록.md').alignable, true)
})
