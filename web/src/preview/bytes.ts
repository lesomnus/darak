import { filesUrl } from '../api'

/** A fetched file body, plus whether it was cut short by a Range request. */
export interface Bytes {
  blob: Blob
  contentType: string
  /** True when only a prefix was fetched and the file is larger. */
  truncated: boolean
  size: number
  text: () => Promise<string>
  arrayBuffer: () => Promise<ArrayBuffer>
}

/**
 * Fetches a file's bytes, optionally only a prefix.
 *
 * When maxBytes is given it asks for `Range: bytes=0-…`, which the server honours
 * through http.ServeContent — so a two-gigabyte log or a binary is not pulled
 * whole just to show the top of it. Same-origin, so the session cookie rides
 * along automatically.
 *
 * The bytes are never handed to an <iframe src=file> or innerHTML; a renderer
 * only ever draws them into a safe sink (an <img>/<video> from a blob URL, a
 * <canvas>, escaped text, or a fully sandboxed iframe). That is what keeps a
 * malicious file from running as this origin — the reason files are served
 * `Content-Disposition: attachment` in the first place.
 */
export async function fetchBytes(path: string, maxBytes?: number): Promise<Bytes> {
  const headers: Record<string, string> = {}
  if (maxBytes && maxBytes > 0) headers.Range = `bytes=0-${maxBytes - 1}`

  const res = await fetch(filesUrl(path), { headers })
  if (!res.ok && res.status !== 206) {
    let msg = `열 수 없습니다 (${res.status})`
    try {
      const j = (await res.json()) as { error?: string }
      if (j.error) msg = j.error
    } catch {
      // not JSON
    }
    throw new Error(msg)
  }

  const blob = await res.blob()
  const contentType = (res.headers.get('Content-Type') ?? 'application/octet-stream').split(';', 1)[0] ?? 'application/octet-stream'

  // Content-Range is "bytes 0-N/TOTAL"; a total larger than what we got means
  // there is more of the file than this preview loaded.
  let total = blob.size
  const cr = res.headers.get('Content-Range')
  if (cr) {
    const m = /\/(\d+)\s*$/.exec(cr)
    if (m) total = Number(m[1])
  }

  return {
    blob,
    contentType,
    truncated: total > blob.size,
    size: total,
    text: () => blob.text(),
    arrayBuffer: () => blob.arrayBuffer(),
  }
}

/**
 * Reports whether a byte prefix looks like a binary file rather than text.
 *
 * A NUL byte is the signal git itself uses — real text never contains one — and
 * it catches the common cases (compiled binaries, images with a text-ish
 * extension, archives). A high proportion of other control characters is the
 * backstop for encodings with no NUL. This is what keeps the text and code
 * views from dumping garbage into a <pre>, and — more importantly — keeps the
 * editor from opening a binary as UTF-8 and CORRUPTING it on save, the way VS
 * Code refuses a binary with "the file is not displayed…".
 */
export function looksBinary(bytes: Uint8Array): boolean {
  const n = Math.min(bytes.length, 8000)
  if (n === 0) return false
  let suspicious = 0
  for (let i = 0; i < n; i++) {
    const b = bytes[i]
    if (b === 0) return true // a NUL byte — decisive
    // Control chars other than tab, LF, CR, form-feed and ESC.
    if (b !== undefined && (b < 0x08 || (b > 0x0e && b < 0x20 && b !== 0x1b))) suspicious++
  }
  return suspicious / n > 0.1
}

/** decodeText reads a blob's prefix and reports whether it was binary, so a
 *  renderer can decide before showing anything. */
export async function decodeText(bytes: Bytes): Promise<{ text: string; binary: boolean }> {
  const buf = new Uint8Array(await bytes.arrayBuffer())
  if (looksBinary(buf)) return { text: '', binary: true }
  return { text: new TextDecoder().decode(buf), binary: false }
}

/** The theme a renderer (Shiki, Monaco) should draw in, resolved from the same
 *  three-state signal the rest of the app uses (data-theme, else the OS). */
export function resolveTheme(): 'light' | 'dark' {
  const chosen = document.documentElement.dataset.theme
  if (chosen === 'light' || chosen === 'dark') return chosen
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}
