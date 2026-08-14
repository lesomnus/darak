import { basicSetup, EditorView } from 'codemirror'
import { EditorState, type Extension } from '@codemirror/state'
import { keymap } from '@codemirror/view'
import { languages } from '@codemirror/language-data'
import { oneDark } from '@codemirror/theme-one-dark'

import { api } from '../../api'
import { type RenderCtx, type RendererModule } from '../registry'

// Editing, in CodeMirror 6.
//
// CodeMirror over Monaco for one concrete reason: it bundles as plain ESM with
// no web-worker plumbing, which keeps the "a clean build needs nothing fetched
// at runtime" property the whole project rests on. (Monaco's workers fought the
// bundler and would have needed a CDN or fragile deep-import shims.) Grammars
// come from @codemirror/language-data, each lazily imported the first time a
// file of that language is edited — the same code-split-per-need idea as the
// renderer registry itself.
//
// Save reuses PUT /api/files, which is the write protocol: temp file, fsync,
// the old inode linked into the trash, one rename. So a save is atomic and the
// previous version is recoverable. No locking (ADR-6, last-write-wins).

// languageFor finds a CodeMirror language extension for a file, or null.
async function languageFor(name: string): Promise<Extension | null> {
  const desc =
    languages.find((l) => l.extensions.some((e) => name.toLowerCase().endsWith('.' + e))) ??
    languages.find((l) => l.filename?.test(name))
  if (!desc) return null
  try {
    const support = await desc.load()
    return support.extension
  } catch {
    return null
  }
}

const mod: RendererModule = {
  async mount(ctx: RenderCtx) {
    const bytes = await ctx.fetchBytes() // the whole file — you edit all of it
    const original = await bytes.text()
    const lang = await languageFor(ctx.file.name)

    const host = document.createElement('div')
    host.className = 'preview-editor'
    ctx.el.appendChild(host)

    let saved = original
    const doSave = async () => {
      const text = view.state.doc.toString()
      await api.upload(ctx.file.path, new File([text], ctx.file.name, { type: 'text/plain' }))
      saved = text
      ctx.setDirty?.(false)
    }

    const extensions: Extension[] = [
      basicSetup,
      EditorView.lineWrapping,
      keymap.of([
        {
          key: 'Mod-s',
          run: () => {
            void doSave().catch((e: unknown) =>
              ctx.onError(e instanceof Error ? e.message : '저장하지 못했습니다.'),
            )
            return true
          },
        },
      ]),
      EditorView.updateListener.of((u) => {
        if (u.docChanged) ctx.setDirty?.(u.state.doc.toString() !== saved)
      }),
    ]
    if (lang) extensions.push(lang)
    if (ctx.theme === 'dark') extensions.push(oneDark)

    const view = new EditorView({
      state: EditorState.create({ doc: original, extensions }),
      parent: host,
    })

    ctx.setSaver?.(doSave)

    return () => {
      ctx.setSaver?.(null)
      view.destroy()
      host.remove()
    }
  },
}

export default mod
