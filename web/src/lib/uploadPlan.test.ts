import { test } from 'node:test'
import assert from 'node:assert/strict'
import { planFromEntries, planFromInput, topLevelNames } from './uploadPlan.ts'

// A stand-in for the entries API: a directory reads out in batches of `batch`
// and then an empty one, the way Chrome does it with 100.
type Tree = { [name: string]: Tree | string }

function entryOf(name: string, node: Tree | string, batch: number): FileSystemEntry {
  if (typeof node === 'string') {
    return {
      name,
      isFile: true,
      isDirectory: false,
      file: (ok: (f: File) => void) => ok(new File([node], name)),
    } as unknown as FileSystemEntry
  }
  return {
    name,
    isFile: false,
    isDirectory: true,
    createReader: () => {
      const children = Object.entries(node).map(([n, c]) => entryOf(n, c, batch))
      let at = 0
      return {
        readEntries: (ok: (es: FileSystemEntry[]) => void) => {
          const out = children.slice(at, at + batch)
          at += out.length
          ok(out)
        },
      }
    },
  } as unknown as FileSystemEntry
}

test('a dropped folder keeps its name, its tree and its empty folders', async () => {
  const plan = await planFromEntries([
    entryOf('photos', { 'a.jpg': 'A', trip: { 'b.jpg': 'BB' }, empty: {} }, 100),
    entryOf('loose.txt', 'x', 100),
  ])
  assert.deepEqual(plan.files.map((f) => f.rel).sort(), ['loose.txt', 'photos/a.jpg', 'photos/trip/b.jpg'])
  assert.deepEqual(plan.dirs, ['photos', 'photos/empty', 'photos/trip'])
  assert.equal(plan.files.find((f) => f.rel === 'photos/trip/b.jpg')?.file.size, 2)
})

// The bug this exists for: reading a directory once returns only the first
// batch, and everything past it is silently left behind.
test('every batch of a large directory is read', async () => {
  const big: Tree = {}
  for (let i = 0; i < 250; i++) big[`f${i}.txt`] = 'x'
  const plan = await planFromEntries([entryOf('big', big, 100)])
  assert.equal(plan.files.length, 250)
})

test('parents come before children', async () => {
  const plan = await planFromEntries([entryOf('a', { b: { c: { 'd.txt': 'x' } } }, 1)])
  assert.deepEqual(plan.dirs, ['a', 'a/b', 'a/b/c'])
})

test('a folder picked with the file dialog keeps its path', () => {
  const f = (rel: string) => {
    const file = new File(['x'], rel.split('/').pop() as string)
    Object.defineProperty(file, 'webkitRelativePath', { value: rel })
    return file
  }
  const plan = planFromInput([f('photos/a.jpg'), f('photos/trip/b.jpg')])
  assert.deepEqual(plan.files.map((x) => x.rel), ['photos/a.jpg', 'photos/trip/b.jpg'])
  assert.deepEqual(plan.dirs, ['photos', 'photos/trip'])
})

test('plain files make no folders', () => {
  const plan = planFromInput([new File(['1'], 'one.txt'), new File(['2'], 'two.txt')])
  assert.deepEqual(plan.files.map((x) => x.rel), ['one.txt', 'two.txt'])
  assert.deepEqual(plan.dirs, [])
})

test('collisions are asked about by the names in this folder', async () => {
  const plan = await planFromEntries([entryOf('photos', { 'a.jpg': 'A' }, 100), entryOf('x.txt', 'x', 100)])
  assert.deepEqual([...topLevelNames(plan)].sort(), ['photos', 'x.txt'])
})
