import { useCallback, useEffect, useState } from 'react'
import { api, filesUrl } from '../api'
import type { Entry } from '../types'
import { domainRoot, sortEntries, TRASH_DIR } from '../lib/format'
import { FileRow } from './FileRow'

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
          새 폴더
        </button>
        <button type="button" className="ghost" onClick={openTrash}>
          휴지통
        </button>
      </div>

      {dragging && <div className="drop-hint">여기에 놓으면 올라갑니다</div>}

      {upload && (
        <div className="progress">
          <div className="bar" style={{ width: `${(upload.done / upload.total) * 100}%` }} />
          <span className="muted small">
            {upload.current} ({upload.done + 1}/{upload.total})
          </span>
        </div>
      )}

      <main aria-live="polite">
        {loadError && <p className="empty error">{loadError}</p>}
        {!loadError && entries === null && <p className="empty">불러오는 중…</p>}
        {!loadError && entries !== null && entries.length === 0 && (
          <p className="empty">{inTrash ? '휴지통이 비어 있습니다.' : '비어 있습니다.'}</p>
        )}
        {entries !== null &&
          sortEntries(entries).map((entry) => {
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
      </main>
    </div>
  )
}
