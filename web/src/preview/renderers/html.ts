import type { RenderCtx, RendererModule } from '../registry'

// Rendering an HTML file — the one genuinely dangerous view, so it is opt-in
// (never the default) and locked down hard.
//
// The file goes into an <iframe sandbox> with NO allow-scripts and NO
// allow-same-origin: the content lands in a unique opaque origin where scripts
// cannot run and cannot reach this page's DOM or cookies. A <meta> CSP of
// default-src 'none' inside it blocks any outbound load (trackers, beacons), so
// what shows is an inert visual of the markup and inline CSS.
//
// Small files go in via srcdoc; larger ones via a blob: URL src, because
// stuffing hundreds of KB into a DOM attribute is what makes the browser
// struggle. Either way the sandbox is what provides the isolation, not the
// delivery mechanism.
const SRCDOC_MAX = 256 * 1024

const CSP_META =
  '<meta http-equiv="Content-Security-Policy" ' +
  'content="default-src \'none\'; img-src data:; style-src \'unsafe-inline\'; font-src data:">'

// injectCSP puts the meta CSP right after <head> (or at the top) so it governs
// the document. A file with no <head> still gets it prepended.
function injectCSP(html: string): string {
  const head = /<head[^>]*>/i.exec(html)
  if (head) {
    const at = head.index + head[0].length
    return html.slice(0, at) + CSP_META + html.slice(at)
  }
  return CSP_META + html
}

const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const bytes = await ctx.fetchBytes()
    const html = injectCSP(await bytes.text())

    const note = document.createElement('p')
    note.className = 'muted small'
    note.textContent =
      '샌드박스에서 렌더합니다 — 스크립트와 외부 요청은 차단됩니다(레이아웃·인라인 CSS만).'

    const frame = document.createElement('iframe')
    frame.className = 'preview-frame'
    // Empty sandbox: no scripts, no same-origin, no top navigation, no forms.
    frame.setAttribute('sandbox', '')
    frame.setAttribute('referrerpolicy', 'no-referrer')

    let blobUrl: string | null = null
    if (html.length <= SRCDOC_MAX) {
      frame.srcdoc = html
    } else {
      blobUrl = URL.createObjectURL(new Blob([html], { type: 'text/html' }))
      frame.src = blobUrl
    }

    ctx.el.appendChild(note)
    ctx.el.appendChild(frame)

    return () => {
      frame.remove()
      note.remove()
      if (blobUrl) URL.revokeObjectURL(blobUrl)
    }
  },
}

export default mod
