/**
 * Many files, or whole folders, into one directory.
 *
 * What arrives from the browser is turned into a PLAN first — every file with
 * its path relative to the target, and every directory that has to exist for
 * those paths to mean anything — and only then is anything sent. Building the
 * plan up front is what lets the page ask about name collisions once, before
 * the first byte, and show a byte total that does not grow as folders are read.
 */

export interface UploadItem {
  /** Relative to the target directory, '/'-separated, never empty. */
  rel: string
  file: File
}

export interface UploadPlan {
  files: UploadItem[]
  /**
   * Every directory to create, parents before children. Includes the ones that
   * only hold files (their files cannot be written until they exist) and empty
   * ones (a dropped folder is copied as the folder it is).
   */
  dirs: string[]
}

/** The names in the target's own listing a plan would write to. */
export function topLevelNames(plan: UploadPlan): Set<string> {
  const out = new Set<string>()
  for (const d of plan.dirs) out.add(d.split('/')[0] ?? d)
  for (const f of plan.files) out.add(f.rel.split('/')[0] ?? f.rel)
  return out
}

function withParents(rels: Iterable<string>, explicitDirs: Iterable<string> = []): string[] {
  const dirs = new Set<string>(explicitDirs)
  for (const rel of rels) {
    const parts = rel.split('/')
    for (let i = 1; i < parts.length; i++) dirs.add(parts.slice(0, i).join('/'))
  }
  for (const d of [...dirs]) {
    const parts = d.split('/')
    for (let i = 1; i < parts.length; i++) dirs.add(parts.slice(0, i).join('/'))
  }
  // Depth first, so a parent is always created before what goes inside it.
  return [...dirs].sort((a, b) => a.split('/').length - b.split('/').length || a.localeCompare(b))
}

/**
 * From an <input type=file>. With `webkitdirectory`, each File carries the
 * path below the folder the person picked — including that folder's name,
 * which is the point: picking "photos" uploads a "photos" folder, not its loose
 * contents. Empty folders are not reported by the browser on this path.
 */
export function planFromInput(list: FileList | File[]): UploadPlan {
  const files = [...list].map((file) => ({ rel: file.webkitRelativePath || file.name, file }))
  return { files, dirs: withParents(files.map((f) => f.rel)) }
}

/**
 * From a drop. A dropped folder is only reachable through the entries API:
 * `dataTransfer.files` lists it as a zero-byte File that cannot be read.
 *
 * The entries have to be taken out of the DataTransfer synchronously, inside
 * the drop handler — the browser empties it as soon as the event returns — so
 * this is split: call takeEntries() in the handler, then await planFromEntries.
 */
export function takeEntries(dt: DataTransfer): FileSystemEntry[] | null {
  const items = [...dt.items].filter((i) => i.kind === 'file')
  if (items.length === 0 || typeof items[0]?.webkitGetAsEntry !== 'function') return null
  const out: FileSystemEntry[] = []
  for (const item of items) {
    const entry = item.webkitGetAsEntry()
    // Not every drop has entries (one built by a script, some file managers).
    // Then the plain file list is the only description there is, and returning
    // a partial or empty list here would upload nothing for those items.
    if (!entry) return null
    out.push(entry)
  }
  return out
}

export async function planFromEntries(entries: FileSystemEntry[]): Promise<UploadPlan> {
  const files: UploadItem[] = []
  const dirs: string[] = []

  async function walk(entry: FileSystemEntry, prefix: string): Promise<void> {
    const rel = prefix ? `${prefix}/${entry.name}` : entry.name
    if (entry.isFile) {
      const file = await new Promise<File>((resolve, reject) =>
        (entry as FileSystemFileEntry).file(resolve, reject),
      )
      files.push({ rel, file })
      return
    }
    if (!entry.isDirectory) return
    dirs.push(rel)
    const reader = (entry as FileSystemDirectoryEntry).createReader()
    // readEntries answers in batches (Chrome: 100 at a time) and signals the end
    // with an empty one. Reading once, as most examples do, silently drops
    // everything after the first hundred.
    for (;;) {
      const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
        reader.readEntries(resolve, reject),
      )
      if (batch.length === 0) break
      for (const child of batch) await walk(child, rel)
    }
  }

  for (const entry of entries) await walk(entry, '')
  return { files, dirs: withParents(files.map((f) => f.rel), dirs) }
}
