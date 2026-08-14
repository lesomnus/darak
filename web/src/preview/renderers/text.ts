import type { RenderCtx, RendererModule } from '../registry'

// Plain text — the safe universal view.
//
// The bytes go into a <pre> via textContent, never innerHTML, so nothing in the
// file can become markup or script. A prefix is enough to look at the top of a
// big log, and the header says so when it was cut short.
const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const bytes = await ctx.fetchBytes(1 << 20) // first 1 MiB is plenty to read
    const content = await bytes.text()

    const wrap = document.createElement('div')
    wrap.className = 'preview-text'
    if (bytes.truncated) {
      const note = document.createElement('p')
      note.className = 'muted small'
      note.textContent = '큰 파일이라 앞부분만 보여줍니다. 전체는 다운로드하세요.'
      wrap.appendChild(note)
    }
    const pre = document.createElement('pre')
    pre.textContent = content // textContent, not innerHTML — no execution path
    wrap.appendChild(pre)
    ctx.el.appendChild(wrap)

    return () => wrap.remove()
  },
}

export default mod
