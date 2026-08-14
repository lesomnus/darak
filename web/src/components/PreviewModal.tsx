import { useEffect, useMemo, useRef, useState } from 'react'
import { filesUrl } from '../api'
import { fetchBytes, resolveTheme } from '../preview/bytes'
import { renderersFor, toPreviewFile, type Cleanup } from '../preview/registry'
import type { Entry } from '../types'

/**
 * Previewing a file in place, without downloading it.
 *
 * A file has one or more views (highlighted code, plain text, image, edit,
 * sandboxed HTML). The one with the highest priority is shown first; the rest
 * are a button group. Each view is code-split, so its renderer — and heavy
 * things like Monaco or Shiki — loads only when picked.
 *
 * The React side owns only the chrome (dialog, switcher, save/download). The
 * renderers are imperative: they draw into a container element and return a
 * cleanup. That boundary is deliberate — Monaco and Shiki are imperative DOM
 * libraries, and wrapping each in React would fight them for no gain.
 */
export function PreviewModal({
  path,
  entry,
  onClose,
  onError,
}: {
  path: string
  entry: Entry
  onClose: () => void
  onError: (message: string) => void
}) {
  const ref = useRef<HTMLDialogElement>(null)
  const hostRef = useRef<HTMLDivElement>(null)

  const file = useMemo(() => toPreviewFile(path, entry), [path, entry])
  const renderers = useMemo(() => renderersFor(file), [file])
  const theme = useMemo(() => resolveTheme(), [])

  const [activeId, setActiveId] = useState(renderers[0]?.id ?? '')
  const [loading, setLoading] = useState(true)
  const [saver, setSaver] = useState<(() => Promise<void>) | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    ref.current?.showModal()
  }, [])

  // Mount the active renderer; clean it up when the view changes or the modal
  // closes. A load that is superseded mid-flight is discarded (cancelled).
  useEffect(() => {
    const renderer = renderers.find((r) => r.id === activeId)
    const host = hostRef.current
    if (!renderer || !host) return

    let cleanup: Cleanup | undefined
    let cancelled = false
    setLoading(true)
    setSaver(null)
    setDirty(false)
    host.replaceChildren()

    renderer
      .load()
      .then((mod) =>
        mod.mount({
          file,
          el: host,
          theme,
          fetchBytes: (max) => fetchBytes(path, max),
          onError,
          setSaver: (fn) => !cancelled && setSaver(() => fn),
          setDirty: (d) => !cancelled && setDirty(d),
        }),
      )
      .then((c) => {
        if (cancelled) {
          c?.()
          return
        }
        cleanup = c
        setLoading(false)
      })
      .catch((e: unknown) => {
        if (cancelled) return
        setLoading(false)
        onError(e instanceof Error ? e.message : '미리보기를 열 수 없습니다.')
      })

    return () => {
      cancelled = true
      cleanup?.()
      host.replaceChildren()
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeId, file])

  async function save() {
    if (!saver) return
    setSaving(true)
    try {
      await saver()
    } catch (e) {
      onError(e instanceof Error ? e.message : '저장하지 못했습니다.')
    } finally {
      setSaving(false)
    }
  }

  // A dirty editor should not vanish on a stray Escape/backdrop click.
  function requestClose() {
    if (dirty && !window.confirm('저장하지 않은 변경이 있습니다. 닫을까요?')) return
    onClose()
  }

  return (
    <dialog
      ref={ref}
      className="preview-dialog"
      onClose={onClose}
      onCancel={(e) => {
        e.preventDefault()
        requestClose()
      }}
    >
      <header className="preview-head">
        <span className="preview-name" title={file.name}>
          {file.name}
        </span>

        {renderers.length > 1 && (
          <div className="preview-views" role="tablist">
            {renderers.map((r) => (
              <button
                key={r.id}
                type="button"
                className={r.id === activeId ? '' : 'ghost'}
                aria-selected={r.id === activeId}
                onClick={() => setActiveId(r.id)}
              >
                {r.label}
              </button>
            ))}
          </div>
        )}

        <span className="preview-actions">
          {saver && (
            <button type="button" disabled={!dirty || saving} onClick={() => void save()}>
              {saving ? '저장 중…' : '저장'}
            </button>
          )}
          <a className="button ghost" href={filesUrl(path)}>
            다운로드
          </a>
          <button type="button" className="icon" aria-label="닫기" onClick={requestClose}>
            ✕
          </button>
        </span>
      </header>

      <div className="preview-body">
        {loading && <p className="muted preview-loading">불러오는 중…</p>}
        <div ref={hostRef} className="preview-host" />
      </div>
    </dialog>
  )
}
