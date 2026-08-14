import * as monaco from 'monaco-editor'
// The worker paths deliberately DROP the classic `esm/vs/` prefix.
//
// monaco-editor 0.56 added an exports map with `"./*.js": "./esm/vs/*.js"`, so
// the specifier every Monaco+Vite guide still shows —
// `monaco-editor/esm/vs/editor/editor.worker.js` — now resolves to a DOUBLED
// `./esm/vs/esm/vs/...` that does not exist. rolldown honours the exports map
// strictly (old esbuild-Vite resolved the file directly and hid the bug), so
// the correct specifier is `monaco-editor/editor/editor.worker.js`, which the
// map rewrites back to `./esm/vs/editor/editor.worker.js`.
//
// `?worker` makes Vite bundle each worker LOCALLY — no CDN. That matters more
// here than usual: the whole project is arranged so a clean build needs nothing
// fetched at runtime, and a CDN worker would quietly break that (and fail on a
// VPN-only deployment).
import editorWorker from 'monaco-editor/editor/editor.worker.js?worker'
import jsonWorker from 'monaco-editor/language/json/json.worker.js?worker'
import cssWorker from 'monaco-editor/language/css/css.worker.js?worker'
import htmlWorker from 'monaco-editor/language/html/html.worker.js?worker'
import tsWorker from 'monaco-editor/language/typescript/ts.worker.js?worker'

import { api } from '../../api'
import { language, type RenderCtx, type RendererModule } from '../registry'

// Save reuses PUT /api/files, the write protocol: temp file, fsync, the old
// inode linked into the trash, one rename. So a save is atomic and the previous
// version is recoverable. No locking (ADR-6, last-write-wins).
;(self as unknown as { MonacoEnvironment: unknown }).MonacoEnvironment = {
  getWorker(_workerId: string, label: string) {
    switch (label) {
      case 'json':
        return new jsonWorker()
      case 'css':
      case 'scss':
      case 'less':
        return new cssWorker()
      case 'html':
      case 'handlebars':
      case 'razor':
        return new htmlWorker()
      case 'typescript':
      case 'javascript':
        return new tsWorker()
      default:
        return new editorWorker()
    }
  },
}

// Monaco's language ids overlap Shiki's but not exactly; an unknown id just
// renders as plain text, so only the ones that differ need mapping.
function monacoLang(lang: string): string {
  switch (lang) {
    case 'tsx':
    case 'jsx':
      return 'typescript'
    case 'bash':
      return 'shell'
    case 'hcl':
    case 'text':
      return 'plaintext'
    default:
      return lang
  }
}

const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const bytes = await ctx.fetchBytes() // the whole file — you edit all of it
    const original = await bytes.text()

    const host = document.createElement('div')
    host.className = 'preview-editor'
    ctx.el.appendChild(host)

    const editor = monaco.editor.create(host, {
      value: original,
      language: monacoLang(language(ctx.file.ext, ctx.file.name)),
      theme: ctx.theme === 'dark' ? 'vs-dark' : 'vs',
      automaticLayout: true,
      minimap: { enabled: false },
      scrollBeyondLastLine: false,
      fontSize: 13,
    })

    let saved = original
    const doSave = async () => {
      const text = editor.getValue()
      await api.upload(ctx.file.path, new File([text], ctx.file.name, { type: 'text/plain' }))
      saved = text
      ctx.setDirty?.(false)
    }
    // Ctrl/Cmd-S saves without leaving the keyboard.
    editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
      void doSave().catch((e: unknown) =>
        ctx.onError(e instanceof Error ? e.message : '저장하지 못했습니다.'),
      )
    })

    const sub = editor.onDidChangeModelContent(() => {
      ctx.setDirty?.(editor.getValue() !== saved)
    })
    ctx.setSaver?.(doSave)

    return () => {
      ctx.setSaver?.(null)
      sub.dispose()
      editor.dispose()
      host.remove()
    }
  },
}

export default mod
