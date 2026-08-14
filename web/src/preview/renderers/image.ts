import type { RenderCtx, RendererModule } from '../registry'

// Images, drawn from a blob: URL into an <img>.
//
// An <img> never executes its source — even an SVG loaded this way renders as a
// picture, not as a document, so its scripts do not run. That is why an SVG's
// default view is "이미지" and viewing it as a document is not offered.
const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const bytes = await ctx.fetchBytes()
    const url = URL.createObjectURL(bytes.blob)

    const img = document.createElement('img')
    img.className = 'preview-image'
    img.alt = ctx.file.name
    img.src = url
    img.onerror = () => ctx.onError('이미지를 표시할 수 없습니다.')
    ctx.el.appendChild(img)

    return () => {
      img.remove()
      URL.revokeObjectURL(url)
    }
  },
}

export default mod
