import type { Entry } from '../types'

export function formatSize(bytes: number): string {
  if (!bytes) return ''
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let n = bytes
  let i = 0
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024
    i++
  }
  const shown = i === 0 ? String(n) : n.toFixed(n < 10 ? 1 : 0)
  return `${shown} ${units[i]}`
}

export function formatDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const sameYear = d.getFullYear() === new Date().getFullYear()
  const date = d.toLocaleDateString('ko-KR', {
    year: sameYear ? undefined : 'numeric',
    month: 'numeric',
    day: 'numeric',
  })
  const time = d.toLocaleTimeString('ko-KR', { hour: '2-digit', minute: '2-digit' })
  return `${date} ${time}`
}

const IMAGE = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg', 'heic', 'avif']
const VIDEO = ['mp4', 'mov', 'avi', 'mkv', 'webm']
const AUDIO = ['mp3', 'wav', 'flac', 'm4a', 'ogg']
const ARCHIVE = ['zip', 'tar', 'gz', 'tgz', '7z', 'rar']
const SHEET = ['xlsx', 'xls', 'csv']
const DOC = ['docx', 'doc', 'hwp', 'hwpx', 'txt', 'md', 'rtf']

/** A glyph rather than an icon set: nothing to load, and it survives a font. */
export function iconFor(entry: Entry): string {
  if (entry.dir) return entry.name === '.trash' ? '🗑️' : '📁'
  const ext = entry.name.split('.').pop()?.toLowerCase() ?? ''
  if (IMAGE.includes(ext)) return '🖼️'
  if (VIDEO.includes(ext)) return '🎬'
  if (AUDIO.includes(ext)) return '🎵'
  if (ext === 'pdf') return '📕'
  if (ARCHIVE.includes(ext)) return '🗜️'
  if (SHEET.includes(ext)) return '📊'
  if (DOC.includes(ext)) return '📄'
  return '📄'
}

/**
 * Folders first, then by name in Korean collation — what a file manager is
 * expected to do, and not what the server returns (it sorts by name alone,
 * because that is what the listing's resume cursor needs).
 */
export function sortEntries(entries: Entry[]): Entry[] {
  return [...entries].sort((a, b) =>
    a.dir === b.dir ? a.name.localeCompare(b.name, 'ko') : a.dir ? -1 : 1,
  )
}

export const TRASH_DIR = '.trash'

/**
 * The permission domain a path belongs to: the first two segments, `homes/<user>`
 * or `teams/<team>`. It is where that domain's trash lives, and it mirrors
 * vfs.DomainRoot on the server.
 */
export function domainRoot(path: string): string | null {
  const parts = path.split('/').filter(Boolean)
  if (parts.length < 2) return null
  if (parts[0] !== 'homes' && parts[0] !== 'teams') return null
  return `${parts[0]}/${parts[1]}`
}
