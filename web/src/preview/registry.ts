import type { Bytes } from './bytes'
import type { Entry } from '../types'

/** The file a preview is asked to render. */
export interface PreviewFile {
  /** Full path from the served root, e.g. "teams/design/notes.md". */
  path: string
  name: string
  /** Lowercased extension without the dot, "" if none. */
  ext: string
  size: number
  mode: string
}

/** What a renderer is handed when it mounts. */
export interface RenderCtx {
  file: PreviewFile
  /** The element to draw into. Emptied for you before mount. */
  el: HTMLElement
  fetchBytes: (maxBytes?: number) => Promise<Bytes>
  theme: 'light' | 'dark'
  onError: (message: string) => void

  /**
   * An editor calls setSaver with a function that persists the current buffer,
   * or null to hide the Save button. setDirty toggles whether Save is enabled.
   * A read-only renderer touches neither.
   */
  setSaver?: (save: (() => Promise<void>) | null) => void
  setDirty?: (dirty: boolean) => void
}

export type Cleanup = () => void

/** A renderer module: mount draws into ctx.el and returns a cleanup. */
export interface RendererModule {
  mount(ctx: RenderCtx): Promise<Cleanup>
}

/**
 * A way of viewing a file. A file usually has several (view / edit / raw), and
 * the one with the highest priority is the default; the rest are offered as
 * buttons. Each is code-split behind load(), so opening a PDF or the Monaco
 * editor pulls that code only then — the base bundle stays small.
 */
export interface Renderer {
  id: string
  label: string
  priority: number
  match: (f: PreviewFile) => boolean
  load: () => Promise<RendererModule>
}

// Size ceilings. Preview is a convenience, not a reason to pull a huge file into
// a browser tab; above these the option is simply not offered and the file is
// downloaded instead.
const TEXT_MAX = 8 << 20 // 8 MiB — text/code/editor
const MEDIA_MAX = 512 << 20 // images/video: the browser streams these fine

// Extension groups. Deliberately explicit rather than sniffed: the server's
// Content-Type is a hint, but which VIEWS to offer is a UI decision, and an
// operator reading this file should see exactly what gets what.
const IMAGE = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'ico', 'avif', 'apng'])
const CODE = new Set([
  'ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs', 'json', 'jsonc', 'go', 'py', 'rs', 'c', 'h', 'cc',
  'cpp', 'hpp', 'java', 'kt', 'rb', 'php', 'cs', 'swift', 'sh', 'bash', 'zsh', 'fish', 'sql',
  'html', 'htm', 'css', 'scss', 'less', 'xml', 'yaml', 'yml', 'toml', 'ini', 'conf', 'dockerfile',
  'makefile', 'md', 'markdown', 'proto', 'graphql', 'vue', 'svelte', 'lua', 'r', 'jl', 'dart',
  'tf', 'hcl', 'diff', 'patch', 'csv', 'tsv', 'env', 'gitignore',
])
const EDITABLE_EXT = new Set([...CODE, 'txt', 'text', 'log', 'me', 'cfg', 'properties'])

// language() maps an extension to a Shiki/Monaco language id.
export function language(ext: string, name: string): string {
  const low = name.toLowerCase()
  if (low === 'dockerfile') return 'dockerfile'
  if (low === 'makefile') return 'makefile'
  switch (ext) {
    case 'md':
    case 'markdown':
      return 'markdown'
    case 'yml':
      return 'yaml'
    case 'htm':
      return 'html'
    case 'sh':
    case 'bash':
    case 'zsh':
      return 'bash'
    case 'tsx':
      return 'tsx'
    case 'jsx':
      return 'jsx'
    case 'py':
      return 'python'
    case 'rs':
      return 'rust'
    case 'rb':
      return 'ruby'
    case 'kt':
      return 'kotlin'
    case 'cs':
      return 'csharp'
    case 'hcl':
    case 'tf':
      return 'hcl'
    case '':
      return 'text'
    default:
      return ext
  }
}

// The registry. Priorities: image wins for images; for code the highlighted
// view is the default, with plain text, an editor, and (for HTML) a sandboxed
// render offered alongside. HTML render sits LOW so a page is shown as source
// first and only rendered when the person asks.
const RENDERERS: Renderer[] = [
  {
    id: 'image',
    label: '이미지',
    priority: 100,
    match: (f) => IMAGE.has(f.ext) && f.size <= MEDIA_MAX,
    load: () => import('./renderers/image').then((m) => m.default),
  },
  {
    id: 'highlight',
    label: '코드',
    priority: 60,
    match: (f) => CODE.has(f.ext) && f.size <= TEXT_MAX,
    load: () => import('./renderers/highlight').then((m) => m.default),
  },
  {
    id: 'text',
    label: '텍스트',
    priority: 50,
    // The universal small-file fallback: any file under the text ceiling can be
    // shown as escaped text, so there is almost always at least one view.
    match: (f) => f.size <= TEXT_MAX,
    load: () => import('./renderers/text').then((m) => m.default),
  },
  {
    id: 'edit',
    label: '편집',
    priority: 40,
    match: (f) =>
      f.size <= TEXT_MAX && (EDITABLE_EXT.has(f.ext) || f.ext === '') && !IMAGE.has(f.ext),
    load: () => import('./renderers/edit').then((m) => m.default),
  },
  {
    id: 'html',
    label: 'HTML로 보기',
    priority: 30,
    match: (f) => (f.ext === 'html' || f.ext === 'htm') && f.size <= TEXT_MAX,
    load: () => import('./renderers/html').then((m) => m.default),
  },
]

/** toPreviewFile builds the value a renderer sees from a listing Entry. */
export function toPreviewFile(path: string, e: Entry): PreviewFile {
  const dot = e.name.lastIndexOf('.')
  const ext = dot > 0 ? e.name.slice(dot + 1).toLowerCase() : ''
  return { path, name: e.name, ext, size: e.size, mode: e.mode }
}

/** renderersFor returns the applicable renderers, best (default) first. */
export function renderersFor(f: PreviewFile): Renderer[] {
  return RENDERERS.filter((r) => r.match(f)).sort((a, b) => b.priority - a.priority)
}

/** previewable reports whether a file has any renderer at all. */
export function previewable(f: PreviewFile): boolean {
  return RENDERERS.some((r) => r.match(f))
}
