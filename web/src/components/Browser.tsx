import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, filesUrl } from '../api'
import type { Entry } from '../types'
import { domainRoot, sortEntries, TRASH_DIR } from '../lib/format'
import { useWindowVirtual } from '../lib/useVirtual'
import { FileRow } from './FileRow'
import { Icon } from './Icon'

interface UploadState {
  current: string
  done: number
  total: number
}

export function Browser({
  path,
  onNavigate,
  onError,
  onShare,
}: {
  path: string
  onNavigate: (path: string) => void
  onError: (message: string) => void
  onShare: (path: string) => void
}) {
  const [entries, setEntries] = useState<Entry[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [upload, setUpload] = useState<UploadState | null>(null)
  const [dragging, setDragging] = useState(false)

  // Sorted once per listing rather than once per render: at 50,000 entries a
  // Korean-collation sort is not something to redo because a drag started.
  const sorted = useMemo(() => (entries === null ? null : sortEntries(entries)), [entries])
  const virtual = useWindowVirtual({ count: sorted?.length ?? 0, resetKey: path })

  // Windowing unmounts rows as they leave the viewport, and one of them may be
  // the row the keyboard is on. When that happens the browser drops focus to
  // <body> and the next Tab starts over from the top of the document -- so
  // scrolling with the keyboard loses your place entirely. Catching it here
  // puts focus on the list itself, which keeps Tab where the user was.
  const focusWasInList = useRef(false)
  useEffect(() => {
    const el = virtual.ref.current
    if (!el || !focusWasInList.current) return
    if (document.activeElement === document.body) {
      el.focus({ preventScroll: true })
      focusWasInList.current = false
    }
  })

  const inTrash = path.endsWith('/' + TRASH_DIR)
  // A file can only be written inside a permission domain; the two top levels
  // are navigation, not places to put things.
  const canWrite = domainRoot(path) !== null

  const reload = useCallback(async () => {
    setLoadError('')
    try {
      setEntries((await api.list(path)).entries)
    } catch (e) {
      setEntries(null)
      setLoadError(e instanceof Error ? e.message : '불러오지 못했습니다.')
    }
  }, [path])

  useEffect(() => {
    setEntries(null)
    // Opening a directory puts you at ITS top. Without this the window keeps
    // whatever offset the previous listing was scrolled to, which with
    // windowing means arriving in the middle of a list you have not seen the
    // start of -- and the virtualiser reads the window's scroll, so it would
    // faithfully render row 4,000 of a directory holding nine.
    window.scrollTo(0, 0)
    void reload()
  }, [reload])

  const uploadFiles = useCallback(
    async (files: File[]) => {
      if (files.length === 0) return
      if (!canWrite) {
        onError('파일은 내 폴더나 팀 폴더 안에만 올릴 수 있습니다.')
        return
      }
      for (const [i, file] of files.entries()) {
        setUpload({ current: file.name, done: i, total: files.length })
        try {
          await api.upload(path + '/' + file.name, file)
        } catch (e) {
          onError(`${file.name}: ${e instanceof Error ? e.message : '올리지 못했습니다.'}`)
        }
      }
      setUpload(null)
      await reload()
    },
    [canWrite, onError, path, reload],
  )

  async function remove(entry: Entry) {
    const message = inTrash
      ? `"${entry.name}"을(를) 완전히 지웁니다. 되돌릴 수 없습니다.`
      : `"${entry.name}"을(를) 휴지통으로 보냅니다.`
    if (!confirm(message)) return
    try {
      await api.remove(path + '/' + entry.name)
      await reload()
    } catch (e) {
      onError(e instanceof Error ? e.message : '지우지 못했습니다.')
    }
  }

  async function mkdir() {
    const name = prompt('새 폴더 이름')
    if (!name) return
    try {
      await api.mkdir(path + '/' + name)
      await reload()
    } catch (e) {
      onError(e instanceof Error ? e.message : '만들지 못했습니다.')
    }
  }

  function openTrash() {
    const domain = domainRoot(path)
    if (!domain) {
      onError('휴지통은 내 폴더나 팀 폴더 안에서 볼 수 있습니다.')
      return
    }
    onNavigate(`${domain}/${TRASH_DIR}`)
  }

  return (
    <div
      className={dragging ? 'browser dragging' : 'browser'}
      onDragOver={(e) => {
        if (!canWrite) return
        e.preventDefault()
        setDragging(true)
      }}
      onDragLeave={(e) => {
        // Only when the pointer has actually left this element, or moving over a
        // child would flicker the hint off and on.
        if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setDragging(false)
      }}
      onDrop={(e) => {
        e.preventDefault()
        setDragging(false)
        void uploadFiles([...e.dataTransfer.files])
      }}
    >
      <div className="toolbar">
        <label className="button">
          <Icon name="upload" size={17} />
          파일 올리기
          <input
            type="file"
            multiple
            hidden
            onChange={(e) => {
              void uploadFiles([...(e.target.files ?? [])])
              e.target.value = ''
            }}
          />
        </label>
        <button type="button" className="ghost" onClick={() => void mkdir()} disabled={!canWrite}>
          <Icon name="folder-plus" size={17} />새 폴더
        </button>
        <button type="button" className="ghost" onClick={openTrash}>
          <Icon name="trash" size={17} />
          휴지통
        </button>
      </div>

      {dragging && <div className="drop-hint">여기에 놓으면 올라갑니다</div>}

      {upload && (
        <div className="progress">
          {/* The bar needs a track behind it, or a 10%-done upload reads as a
              stray blue dash rather than as progress through something. */}
          <div
            className="track"
            role="progressbar"
            aria-valuenow={upload.done}
            aria-valuemax={upload.total}
          >
            <div className="bar" style={{ width: `${(upload.done / upload.total) * 100}%` }} />
          </div>
          <span className="muted small">
            {upload.current} ({upload.done + 1}/{upload.total})
          </span>
        </div>
      )}

      <main>
        {/* aria-live sits on the STATUS only. It used to wrap the rows, which
            with windowing would make a screen reader announce the list again
            on every scroll -- the DOM changes constantly by design. */}
        <div aria-live="polite">
          {loadError && <p className="empty error">{loadError}</p>}
          {!loadError && entries === null && <Skeleton />}
          {!loadError && entries !== null && entries.length === 0 && (
            <p className="empty">{inTrash ? '휴지통이 비어 있습니다.' : '비어 있습니다.'}</p>
          )}
          {/* A directory that loaded WITH files used to say nothing at all: the
              live region wrapped the rows before, and taking it off them (the
              windowed list rewrites itself on every scroll, which a screen
              reader would read out) removed the only announcement there was.
              The count is the useful sentence anyway -- the rows themselves are
              navigable once you know they arrived. */}
          {!loadError && sorted !== null && sorted.length > 0 && (
            <p className="sr-only">{sorted.length.toLocaleString('ko-KR')}개 항목</p>
          )}
        </div>

        {/* Above the list, not below it. Below, it sits after the spacer that
            stands in for every row not rendered -- which on the listings this
            notice exists for is several hundred thousand pixels down. */}
        {virtual.active && sorted && (
          <p className="muted small count">
            {sorted.length.toLocaleString('ko-KR')}개 — 화면에 보이는 만큼만 그립니다. 브라우저의
            페이지 내 찾기(Ctrl+F)는 보이는 범위에서만 동작합니다.
          </p>
        )}

        {sorted !== null && (
          <div
            ref={virtual.ref}
            // Focusable so there is somewhere to put focus when the row that
            // held it is scrolled out and unmounted. Without this, focus falls
            // to <body> and the next Tab restarts from the top of the page.
            tabIndex={-1}
            onFocusCapture={() => {
              focusWasInList.current = true
            }}
          >
            {/* Spacers stand in for the rows that are not rendered, so the
                scrollbar describes the whole directory rather than the part
                currently in the document. */}
            {virtual.padTop > 0 && <div style={{ height: virtual.padTop }} aria-hidden="true" />}
            {sorted.slice(virtual.start, virtual.end).map((entry) => {
              const child = path + '/' + entry.name
              return (
                <FileRow
                  key={entry.name}
                  entry={entry}
                  inTrash={inTrash}
                  onOpen={() => {
                    if (entry.dir) onNavigate(child)
                    // A download, not a fetch: the browser streams it, and Range
                    // and resume come from the server for free.
                    else window.location.href = filesUrl(child)
                  }}
                  onShare={() => onShare(child)}
                  onDelete={() => void remove(entry)}
                />
              )
            })}
            {virtual.padBottom > 0 && (
              <div style={{ height: virtual.padBottom }} aria-hidden="true" />
            )}
          </div>
        )}
      </main>
    </div>
  )
}

/**
 * Placeholder rows while the listing loads.
 *
 * The text "불러오는 중…" was one line in the middle of an empty page, so the
 * whole list jumped into place underneath it when the response landed. Rows of
 * the right height mean the only thing that changes is their contents.
 *
 * Six of them: enough to fill the fold on a phone, few enough that a directory
 * with two files does not flash a wall of grey.
 */
function Skeleton() {
  return (
    <div aria-hidden="true">
      {[0, 1, 2, 3, 4, 5].map((i) => (
        <div className="row skeleton-row" key={i}>
          <span className="icon">
            <span className="skeleton" />
          </span>
          <span className="name">
            {/* Varied widths -- six identical bars read as a loading graphic,
                varied ones read as filenames that have not arrived. */}
            <span className="skeleton" style={{ width: `${[58, 34, 71, 45, 62, 39][i]}%` }} />
          </span>
        </div>
      ))}
    </div>
  )
}
