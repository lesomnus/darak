import type { KeyboardEvent, MouseEvent } from 'react'
import type { Entry } from '../types'
import { formatDate, formatSize, iconFor, TRASH_DIR } from '../lib/format'

export function FileRow({
  entry,
  inTrash,
  onOpen,
  onShare,
  onDelete,
}: {
  entry: Entry
  inTrash: boolean
  onOpen: () => void
  onShare: () => void
  onDelete: () => void
}) {
  function stop(fn: () => void) {
    return (ev: MouseEvent) => {
      ev.stopPropagation()
      fn()
    }
  }

  function onKeyDown(ev: KeyboardEvent) {
    if (ev.key === 'Enter' || ev.key === ' ') {
      ev.preventDefault()
      onOpen()
    }
  }

  const isTrashFolder = entry.name === TRASH_DIR

  return (
    <div className="row" role="button" tabIndex={0} onClick={onOpen} onKeyDown={onKeyDown}>
      <span className="icon" aria-hidden="true">
        {iconFor(entry)}
      </span>
      <span className="name">{entry.name}</span>
      <span className="meta">{entry.dir ? '' : formatSize(entry.size)}</span>
      <span className="meta">{formatDate(entry.mod_time)}</span>

      <span className="actions">
        {!entry.dir && (
          <button type="button" title="공유 링크 만들기" aria-label="공유 링크 만들기" onClick={stop(onShare)}>
            🔗
          </button>
        )}
        {!isTrashFolder && (
          <button
            type="button"
            /* Inside the trash there is nowhere further to move something, so
               the same button really deletes — and says so. */
            title={inTrash ? '완전히 지우기' : '휴지통으로 보내기'}
            aria-label={inTrash ? '완전히 지우기' : '휴지통으로 보내기'}
            onClick={stop(onDelete)}
          >
            🗑️
          </button>
        )}
      </span>
    </div>
  )
}
