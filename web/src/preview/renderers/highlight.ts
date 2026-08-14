import { binaryNotice } from '../binary'
import { decodeText } from '../bytes'
import type { RenderCtx, RendererModule } from '../registry'
import { language } from '../registry'

// Syntax-highlighted read-only view, via Shiki (the VS Code highlighter, real
// TextMate grammars). Shiki ESCAPES the code and emits only span markup, so
// setting its output as innerHTML carries no execution path — the file text
// cannot become script. This is the ordinary, safe highlight pattern, distinct
// from actually rendering an HTML file (see html.ts).
//
// Loaded only when this view is chosen, so the (large) grammar set never touches
// the base bundle.
const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const bytes = await ctx.fetchBytes(512 * 1024)
    const { text: code, binary } = await decodeText(bytes)
    const lang = language(ctx.file.ext, ctx.file.name)
    const theme = ctx.theme === 'dark' ? 'github-dark' : 'github-light'

    const wrap = document.createElement('div')
    wrap.className = 'preview-code'
    if (binary) {
      binaryNotice(wrap, '표시할')
      ctx.el.appendChild(wrap)
      return () => wrap.remove()
    }
    if (bytes.truncated) {
      const note = document.createElement('p')
      note.className = 'muted small'
      note.textContent = '큰 파일이라 앞부분만 하이라이트합니다. 전체는 다운로드하세요.'
      wrap.appendChild(note)
    }

    const { createHighlighter } = await import('shiki')
    try {
      const hl = await createHighlighter({ themes: [theme], langs: [lang] })
      const holder = document.createElement('div')
      holder.innerHTML = hl.codeToHtml(code, { lang, theme }) // escaped by Shiki
      hl.dispose()
      wrap.appendChild(holder)
    } catch {
      // Unknown grammar (or a load failure): fall back to plain escaped text
      // rather than failing the view.
      const pre = document.createElement('pre')
      pre.textContent = code
      wrap.appendChild(pre)
    }

    ctx.el.appendChild(wrap)
    return () => wrap.remove()
  },
}

export default mod
