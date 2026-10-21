import { filesUrl } from '../../api'
import type { RenderCtx, RendererModule } from '../registry'

// Video, played straight from the file URL.
//
// Not through fetchBytes() like an image: a video is streamed, and the browser
// asks for it in byte ranges as it plays and wherever it is scrubbed to. The
// server answers those with http.ServeContent over the helper's descriptor, so
// a two-gigabyte recording starts in a moment and seeks without downloading
// what was skipped. The session cookie goes along on a same-origin <video>, so
// the kernel still decides, as the user, whether it may be read at all.
const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const video = document.createElement('video')
    video.className = 'preview-video'
    video.controls = true
    // Metadata only until play is pressed: opening the preview should not start
    // pulling a large file for someone who only wanted to see what it was.
    video.preload = 'metadata'
    video.playsInline = true
    video.src = filesUrl(ctx.file.path)

    // The browser plays what its decoders handle, whatever the extension says:
    // an .mp4 in H.265, or a .mov in ProRes, loads and then fails here.
    video.onerror = () =>
      ctx.onError('이 브라우저에서 재생할 수 없는 형식입니다. 다운로드해서 여세요.')
    ctx.el.appendChild(video)

    return () => {
      // Without clearing the source the element keeps its connection open after
      // it leaves the document, still buffering a video nobody is watching.
      video.pause()
      video.removeAttribute('src')
      video.load()
      video.remove()
    }
  },
}

export default mod
